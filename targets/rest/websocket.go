package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/xavidop/tamesu/target"
)

// DefaultIdle is how long a websocket turn waits for the next message, after
// the first one, before taking the turn as complete.
const DefaultIdle = 2 * time.Second

// wsMessage is one message read from the connection, or the error that ended
// the reading.
type wsMessage struct {
	data []byte
	err  error
}

// wsState is the websocket half of a session: one connection for the whole
// session, read by one goroutine into a channel so an idle timeout never has
// to cancel a read (which would close the connection).
type wsState struct {
	conn     *websocket.Conn
	sio      *sioConn
	status   int
	msgs     chan wsMessage
	cancel   context.CancelFunc
	received int
	sent     int
	// backlog are messages read during the handshake that did not belong
	// to it; readWS consumes them first.
	backlog []wsMessage
}

// write sends one message: a text frame, or a socket.io event when the
// payload is {"event": name, "data": payload}.
func (ws *wsState) write(ctx context.Context, payload []byte) error {
	ws.sent++
	if ws.sio != nil {
		var m struct {
			Event string `json:"event"`
			Data  any    `json:"data"`
		}
		if err := json.Unmarshal(payload, &m); err != nil || m.Event == "" {
			return errors.New("a socket.io message is {event: name, data: payload}")
		}
		return ws.sio.emit(ctx, m.Event, m.Data)
	}
	return ws.conn.Write(ctx, websocket.MessageText, payload)
}

// stream is the channel readWS selects on: the handshake backlog is
// served first, through a ready channel, then the connection's messages.
func (ws *wsState) stream() <-chan wsMessage {
	if len(ws.backlog) > 0 {
		ch := make(chan wsMessage, 1)
		ch <- ws.backlog[0]
		ws.backlog = ws.backlog[1:]
		return ch
	}
	return ws.msgs
}

// close ends the connection.
func (ws *wsState) close() {
	if ws.sio != nil {
		ws.sio.close()
		return
	}
	_ = ws.conn.Close(websocket.StatusNormalClosure, "done")
	ws.cancel()
}

// dialWS opens the session's connection: the URL and headers are rendered
// with the run's scope, then request.open is sent when configured.
func (s *session) dialWS(ctx context.Context) error {
	url, err := s.run.Render(s.t.cfg.URL)
	if err != nil {
		return fmt.Errorf("rest %s: url: %w", s.t.name, err)
	}
	hdr := http.Header{}
	for k, v := range s.t.cfg.Headers {
		rv, err := s.run.Render(v)
		if err != nil {
			return fmt.Errorf("rest %s: header %s: %w", s.t.name, k, err)
		}
		hdr.Set(k, rv)
	}
	if s.t.cfg.Protocol == ProtocolSocketIO {
		return s.dialSocketIO(ctx, url, hdr)
	}
	type dialed struct {
		conn   *websocket.Conn
		status int
	}
	d, err := target.Connect(ctx, s.t.cfg.Connect, s.run.Logger, "rest "+s.t.name+" websocket", func(ctx context.Context) (dialed, error) {
		dctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
		defer cancel()
		conn, resp, err := websocket.Dial(dctx, url, &websocket.DialOptions{HTTPClient: s.t.cfg.Client, HTTPHeader: hdr})
		out := dialed{conn: conn}
		if resp != nil {
			out.status = resp.StatusCode
		}
		return out, target.HandshakeError(err, resp)
	})
	if err != nil {
		return fmt.Errorf("rest %s: websocket %s: %w", s.t.name, url, err)
	}
	conn := d.conn
	conn.SetReadLimit(16 << 20)
	ws := &wsState{conn: conn, msgs: make(chan wsMessage), status: d.status}
	dctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	readCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	ws.cancel = stop
	go func() {
		defer close(ws.msgs)
		for {
			_, data, err := conn.Read(readCtx)
			if err != nil {
				select {
				case ws.msgs <- wsMessage{err: err}:
				case <-readCtx.Done():
				}
				return
			}
			select {
			case ws.msgs <- wsMessage{data: data}:
			case <-readCtx.Done():
				return
			}
		}
	}()
	s.ws = ws
	return s.openWS(dctx)
}

// dialSocketIO connects to a socket.io server and routes its events into
// the session's message channel as {"event": name, "data": payload}.
func (s *session) dialSocketIO(ctx context.Context, url string, hdr http.Header) error {
	sio, err := target.Connect(ctx, s.t.cfg.Connect, s.run.Logger, "rest "+s.t.name+" socket.io", func(ctx context.Context) (*sioConn, error) {
		return sioDial(ctx, s.t.cfg.Client, url, hdr, s.t.cfg.Timeout)
	})
	if err != nil {
		return fmt.Errorf("rest %s: socket.io %s: %w", s.t.name, url, err)
	}
	ws := &wsState{sio: sio, msgs: make(chan wsMessage), status: http.StatusSwitchingProtocols}
	readCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	ws.cancel = stop
	go func() {
		defer close(ws.msgs)
		for {
			select {
			case <-readCtx.Done():
				return
			case ev, ok := <-sio.events:
				if !ok {
					select {
					case ws.msgs <- wsMessage{err: errors.New("connection closed")}:
					case <-readCtx.Done():
					}
					return
				}
				if ev.Err != nil {
					select {
					case ws.msgs <- wsMessage{err: ev.Err}:
					case <-readCtx.Done():
					}
					return
				}
				var data any
				if len(ev.Data) > 0 {
					_ = json.Unmarshal(ev.Data, &data)
				}
				b, _ := json.Marshal(map[string]any{"event": ev.Name, "data": data})
				select {
				case ws.msgs <- wsMessage{data: b}:
				case <-readCtx.Done():
					return
				}
			}
		}
	}()
	s.ws = ws
	dctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	return s.openWS(dctx)
}

// openWS sends request.open and plays the handshake steps.
func (s *session) openWS(ctx context.Context) error {
	if s.t.cfg.Open != nil {
		payload, err := s.payload(s.t.cfg.Open, target.UserTurn{})
		if err != nil {
			return fmt.Errorf("rest %s: request.open: %w", s.t.name, err)
		}
		if err := s.ws.write(ctx, payload); err != nil {
			return fmt.Errorf("rest %s: websocket open message: %w", s.t.name, err)
		}
	}
	for i, step := range s.t.cfg.Handshake {
		if step.Send != nil {
			payload, err := s.payload(step.Send, target.UserTurn{})
			if err != nil {
				return fmt.Errorf("rest %s: handshake[%d].send: %w", s.t.name, i, err)
			}
			if err := s.ws.write(ctx, payload); err != nil {
				return fmt.Errorf("rest %s: handshake[%d].send: %w", s.t.name, i, err)
			}
		}
		if step.Wait == "" {
			continue
		}
		wait, err := jp.ParseString(step.Wait)
		if err != nil {
			return fmt.Errorf("rest %s: handshake[%d].wait %q: %w", s.t.name, i, step.Wait, err)
		}
		captures, err := compilePaths(fmt.Sprintf("handshake[%d].capture", i), step.Capture)
		if err != nil {
			return fmt.Errorf("rest %s: %w", s.t.name, err)
		}
		timeout := step.Timeout
		if timeout <= 0 {
			timeout = s.t.cfg.Timeout
		}
		wctx, cancel := context.WithTimeout(ctx, timeout)
		matched, err := s.awaitWS(wctx, wait, step.Wait)
		cancel()
		if err != nil {
			return fmt.Errorf("rest %s: handshake[%d]: waiting for %s: %w", s.t.name, i, step.Wait, err)
		}
		for name, x := range captures {
			if got := evalPath(x, step.Capture[name], matched); len(got) > 0 {
				s.values["session."+name] = asString(got[0])
			}
		}
	}
	return nil
}

// awaitWS reads messages until one satisfies the path; the others are kept
// for the first turn.
func (s *session) awaitWS(ctx context.Context, x jp.Expr, path string) (any, error) {
	var kept []wsMessage
	defer func() { s.ws.backlog = append(s.ws.backlog, kept...) }()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("no such message within the timeout: %w", ctx.Err())
		case m, ok := <-s.ws.msgs:
			if !ok || m.err != nil {
				return nil, fmt.Errorf("connection closed: %w", errOr(m.err))
			}
			s.ws.received++
			ev, err := oj.Parse(m.data)
			if err != nil {
				kept = append(kept, m)
				continue
			}
			if matchesPath(x, path, ev) {
				return ev, nil
			}
			kept = append(kept, m)
		}
	}
}

func errOr(err error) error {
	if err == nil {
		return errors.New("closed")
	}
	return err
}

// sendWS writes the user turn as one text message and reads the agent's turn:
// messages until one marks the turn (response.turn_done) or the session
// (response.done) as done, or until nothing arrives for Idle after the first
// one. Messages that arrived between turns (a greeting, a keepalive) are read
// with the next turn.
func (s *session) sendWS(ctx context.Context, turn target.UserTurn) error {
	if s.ws == nil {
		return errors.New("rest: websocket is not connected")
	}
	payload, err := s.payload(s.body(), turn)
	if err != nil {
		return fmt.Errorf("rest: body: %w", err)
	}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	started := time.Now()
	if err := s.ws.write(tctx, payload); err != nil {
		return fmt.Errorf("rest: websocket write: %w", err)
	}
	agent, err := s.readWS(tctx)
	if err != nil {
		return fmt.Errorf("rest: websocket: %w", err)
	}
	agent.StartedAt = started
	if agent.EndedAt.IsZero() {
		agent.EndedAt = time.Now()
	}
	if agent.FirstTokenAt.IsZero() {
		agent.FirstTokenAt = agent.EndedAt
	}
	agent.Status = s.ws.status
	s.turns++
	s.statuses = append(s.statuses, float64(agent.Status))
	s.pending = &agent
	return nil
}

// readWS assembles one agent turn from the messages of the connection.
func (s *session) readWS(ctx context.Context) (target.AgentTurn, error) {
	agent := target.AgentTurn{}
	var text strings.Builder
	var events []any
	idle := time.NewTimer(s.t.cfg.Idle)
	idle.Stop()
	defer idle.Stop()
	got := false
	for {
		var idleC <-chan time.Time
		if got {
			idleC = idle.C
		}
		select {
		case <-ctx.Done():
			if got {
				return agent, fmt.Errorf("the turn did not complete within %s: %w", s.t.cfg.Timeout, ctx.Err())
			}
			return agent, fmt.Errorf("no message within %s: %w", s.t.cfg.Timeout, ctx.Err())
		case <-idleC:
			agent.Text = text.String()
			agent.Raw = events
			return agent, nil
		case m, ok := <-s.ws.stream():
			if !ok || m.err != nil {
				err := m.err
				if err == nil {
					err = errors.New("connection closed")
				}
				return agent, fmt.Errorf("connection closed by the agent: %w", err)
			}
			s.ws.received++
			if !got {
				got = true
				agent.FirstTokenAt = time.Now()
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(s.t.cfg.Idle)
			ev, err := oj.Parse(m.data)
			if err != nil {
				text.Write(m.data)
				continue
			}
			events = append(events, ev)
			s.extract(ev, &agent, &text)
			turnDone := s.turnDonePath != nil && matchesPath(s.turnDonePath, s.t.cfg.TurnDonePath, ev)
			if turnDone || agent.Done {
				if turnDone && !agent.Done && s.donePath != nil {
					// The end of the session often follows the end of the
					// turn by a few milliseconds: give it a moment, outside
					// the turn's latency.
					agent.EndedAt = time.Now()
					if last := s.doneSoon(); last != nil {
						events = append(events, last)
						s.extract(last, &agent, &text)
					}
				}
				agent.Text = text.String()
				agent.Raw = events
				return agent, nil
			}
		}
	}
}

// doneGrace is how long readWS waits, after a turn ended, for a message
// that ends the session.
const doneGrace = 50 * time.Millisecond

// doneSoon waits briefly for a message matching response.done after the
// turn ended; a message that is not it is kept for the next turn.
func (s *session) doneSoon() any {
	timer := time.NewTimer(doneGrace)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case m, ok := <-s.ws.stream():
		if !ok || m.err != nil {
			return nil
		}
		s.ws.received++
		ev, err := oj.Parse(m.data)
		if err == nil && matchesPath(s.donePath, s.t.cfg.DonePath, ev) {
			return ev
		}
		s.ws.backlog = append([]wsMessage{m}, s.ws.backlog...)
		return nil
	}
}

// closeWS ends the connection and reports the message counts.
func (s *session) closeWS(metrics map[string]float64) {
	if s.ws == nil {
		return
	}
	s.ws.close()
	metrics["ws_messages_sent"] = float64(s.ws.sent)
	metrics["ws_messages_received"] = float64(s.ws.received)
	s.ws = nil
}
