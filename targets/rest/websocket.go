package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
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
	status   int
	msgs     chan wsMessage
	cancel   context.CancelFunc
	received int
	sent     int
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
	if s.t.cfg.Open != nil {
		payload, err := s.payload(s.t.cfg.Open, target.UserTurn{})
		if err != nil {
			return fmt.Errorf("rest %s: request.open: %w", s.t.name, err)
		}
		if err := conn.Write(dctx, websocket.MessageText, payload); err != nil {
			return fmt.Errorf("rest %s: websocket open message: %w", s.t.name, err)
		}
		ws.sent++
	}
	return nil
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
	if err := s.ws.conn.Write(tctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("rest: websocket write: %w", err)
	}
	s.ws.sent++
	agent, err := s.readWS(tctx)
	if err != nil {
		return fmt.Errorf("rest: websocket: %w", err)
	}
	agent.StartedAt = started
	agent.EndedAt = time.Now()
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
		case m, ok := <-s.ws.msgs:
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
			turnDone := s.turnDonePath != nil && truthy(s.turnDonePath.First(ev))
			if turnDone || agent.Done {
				agent.Text = text.String()
				agent.Raw = events
				return agent, nil
			}
		}
	}
}

// closeWS ends the connection and reports the message counts.
func (s *session) closeWS(metrics map[string]float64) {
	if s.ws == nil {
		return
	}
	_ = s.ws.conn.Close(websocket.StatusNormalClosure, "done")
	s.ws.cancel()
	metrics["ws_messages_sent"] = float64(s.ws.sent)
	metrics["ws_messages_received"] = float64(s.ws.received)
	s.ws = nil
}
