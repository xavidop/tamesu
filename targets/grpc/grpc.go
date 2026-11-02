// Package grpc is the grpc target: an agent behind the generic turn API of
// proto/tamesu/turn/v1 (TurnService). tamesu opens a Converse stream per
// session and sends one UserTurn per user turn, reading the agent's text
// deltas and tool calls up to TurnEnd; an agent that only implements the
// unary Turn is used that way.
package grpc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/grpc/turnv1"
)

// DefaultTimeout bounds one turn, and the connection.
const DefaultTimeout = 60 * time.Second

// Config is the grpc target's configuration.
type Config struct {
	// URL is grpc://host:port (plaintext) or grpcs://host:port (TLS).
	URL string
	// Headers go on every call as gRPC metadata; values are templates
	// ({{secrets.NAME}}).
	Headers map[string]string
	// SessionID is the session id template; empty means {{uuid}}.
	SessionID string
	// Timeout bounds one turn; zero means DefaultTimeout.
	Timeout time.Duration
	// DialOptions are added to the connection (tests).
	DialOptions []grpc.DialOption
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "{{uuid}}"
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the grpc target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// serverEvent is one message from the agent, or the error that ended the
// stream.
type serverEvent struct {
	ev  *turnv1.ServerEvent
	err error
}

type session struct {
	t         *Target
	run       *target.RunContext
	conn      *grpc.ClientConn
	client    turnv1.TurnServiceClient
	ctx       context.Context
	cancel    context.CancelFunc
	stream    grpc.BidiStreamingClient[turnv1.ClientEvent, turnv1.ServerEvent]
	events    chan serverEvent
	sessionID string
	unary     bool

	mu       sync.Mutex
	pending  *target.AgentTurn
	closed   bool
	turns    int
	received int
	evs      []target.Event
}

// Start connects and opens the Converse stream with a SessionStart.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	addr, creds, err := dialTarget(t.cfg.URL, run)
	if err != nil {
		return nil, fmt.Errorf("grpc %s: %w", t.name, err)
	}
	md := metadata.MD{}
	for k, v := range t.cfg.Headers {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("grpc %s: header %s: %w", t.name, k, err)
		}
		md.Append(strings.ToLower(k), rv)
	}
	sessionID, err := run.Render(t.cfg.SessionID)
	if err != nil {
		return nil, fmt.Errorf("grpc %s: session id: %w", t.name, err)
	}
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(creds)}, t.cfg.DialOptions...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpc %s: connect %s: %w", t.name, addr, err)
	}
	sctx, cancel := context.WithCancel(metadata.NewOutgoingContext(context.WithoutCancel(ctx), md))
	s := &session{t: t, run: run, conn: conn, client: turnv1.NewTurnServiceClient(conn), ctx: sctx, cancel: cancel, sessionID: sessionID}
	start := &turnv1.SessionStart{SessionId: sessionID, Metadata: map[string]string{"test": run.TestName}}
	octx, ocancel := context.WithTimeout(sctx, t.cfg.Timeout)
	defer ocancel()
	stream, err := s.client.Converse(sctx)
	if err == nil {
		err = stream.Send(&turnv1.ClientEvent{Event: &turnv1.ClientEvent_SessionStart{SessionStart: start}})
	}
	switch {
	case err == nil:
		s.stream = stream
		s.events = make(chan serverEvent)
		go s.read()
		// A server without Converse fails the first Recv with Unimplemented;
		// probe it now so the fallback is decided before the first turn.
		select {
		case ev := <-s.events:
			if ev.err != nil && status.Code(ev.err) == codes.Unimplemented {
				s.unary = true
				s.stream = nil
			} else if ev.err != nil {
				_ = conn.Close()
				cancel()
				return nil, fmt.Errorf("grpc %s: converse: %w", t.name, ev.err)
			} else {
				s.unsolicited(ev.ev)
			}
		case <-time.After(200 * time.Millisecond):
		case <-octx.Done():
		}
	case status.Code(err) == codes.Unimplemented:
		s.unary = true
	default:
		_ = conn.Close()
		cancel()
		return nil, fmt.Errorf("grpc %s: connect %s: %w", t.name, addr, err)
	}
	s.event("opened", addr)
	if s.unary {
		s.event("unary", "the agent has no Converse; using Turn")
	}
	return s, nil
}

// dialTarget turns grpc://host:port or grpcs://host:port into an address and
// credentials.
func dialTarget(raw string, run *target.RunContext) (string, credentials.TransportCredentials, error) {
	rendered, err := run.Render(raw)
	if err != nil {
		return "", nil, fmt.Errorf("url: %w", err)
	}
	u, err := url.Parse(rendered)
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("url %q must be grpc://host:port or grpcs://host:port", rendered)
	}
	switch u.Scheme {
	case "grpc":
		return u.Host, insecure.NewCredentials(), nil
	case "grpcs":
		return u.Host, credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}), nil
	}
	return "", nil, fmt.Errorf("url %q: scheme must be grpc or grpcs", rendered)
}

// read feeds the events channel until the stream ends.
func (s *session) read() {
	defer close(s.events)
	for {
		ev, err := s.stream.Recv()
		select {
		case s.events <- serverEvent{ev: ev, err: err}:
		case <-s.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// Send sends the user's turn and reads the agent's reply up to TurnEnd.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("grpc: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	started := time.Now()
	var agent target.AgentTurn
	var err error
	if s.unary {
		agent, err = s.unaryTurn(tctx, turn)
	} else {
		agent, err = s.streamTurn(tctx, turn)
	}
	if err != nil {
		return fmt.Errorf("grpc: %w", err)
	}
	agent.StartedAt = started
	agent.EndedAt = time.Now()
	if agent.FirstTokenAt.IsZero() {
		agent.FirstTokenAt = agent.EndedAt
	}
	s.mu.Lock()
	s.turns++
	s.pending = &agent
	s.mu.Unlock()
	return nil
}

func (s *session) streamTurn(ctx context.Context, turn target.UserTurn) (target.AgentTurn, error) {
	agent := target.AgentTurn{}
	if err := s.stream.Send(&turnv1.ClientEvent{Event: &turnv1.ClientEvent_UserTurn{UserTurn: &turnv1.UserTurn{Text: turn.Text}}}); err != nil {
		return agent, fmt.Errorf("send: %w", err)
	}
	var text strings.Builder
	var raw []any
	for {
		select {
		case <-ctx.Done():
			return agent, fmt.Errorf("no TurnEnd within %s: %w", s.t.cfg.Timeout, ctx.Err())
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				err := ev.err
				if err == nil {
					err = errors.New("stream closed")
				}
				return agent, fmt.Errorf("connection closed by the agent: %w", err)
			}
			s.mu.Lock()
			s.received++
			s.mu.Unlock()
			raw = append(raw, rawOf(ev.ev))
			switch e := ev.ev.Event.(type) {
			case *turnv1.ServerEvent_TextDelta:
				if agent.FirstTokenAt.IsZero() && e.TextDelta.Text != "" {
					agent.FirstTokenAt = time.Now()
				}
				text.WriteString(e.TextDelta.Text)
			case *turnv1.ServerEvent_ToolCall:
				agent.ToolCalls = append(agent.ToolCalls, toolCallOf(e.ToolCall))
				s.event("tool_call", e.ToolCall.Name)
			case *turnv1.ServerEvent_TurnEnd:
				agent.Text = text.String()
				agent.Raw = raw
				agent.Done = e.TurnEnd.EndSession
				return agent, nil
			}
		}
	}
}

func (s *session) unaryTurn(ctx context.Context, turn target.UserTurn) (target.AgentTurn, error) {
	resp, err := s.client.Turn(ctx, &turnv1.TurnRequest{SessionId: s.sessionID, Text: turn.Text, Metadata: map[string]string{"test": s.run.TestName}})
	if err != nil {
		return target.AgentTurn{}, fmt.Errorf("turn: %w", err)
	}
	s.mu.Lock()
	s.received++
	s.mu.Unlock()
	agent := target.AgentTurn{Text: resp.Text, Done: resp.EndSession, Raw: rawOf(resp)}
	for _, c := range resp.ToolCalls {
		agent.ToolCalls = append(agent.ToolCalls, toolCallOf(c))
		s.event("tool_call", c.Name)
	}
	return agent, nil
}

func toolCallOf(c *turnv1.ToolCall) target.ToolCall {
	call := target.ToolCall{Name: c.Name}
	if c.ArgumentsJson != "" {
		var args map[string]any
		if json.Unmarshal([]byte(c.ArgumentsJson), &args) == nil {
			call.Args = args
		}
	}
	return call
}

// rawOf is a message as a JSON-shaped value for the turn's raw payload.
func rawOf(m any) any {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprint(m)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func (s *session) unsolicited(ev *turnv1.ServerEvent) {
	if ev != nil {
		s.event("unexpected_event", fmt.Sprintf("%T before the first turn", ev.Event))
	}
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("grpc: Receive without a pending Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close ends the session (a SessionEnd on the stream) and the connection.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return target.Artifacts{}, nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.stream != nil {
		_ = s.stream.Send(&turnv1.ClientEvent{Event: &turnv1.ClientEvent_SessionEnd{SessionEnd: &turnv1.SessionEnd{}}})
		_ = s.stream.CloseSend()
		// Give the agent a moment to take the end before the connection goes.
		drain := time.NewTimer(500 * time.Millisecond)
	loop:
		for {
			select {
			case _, ok := <-s.events:
				if !ok {
					break loop
				}
			case <-drain.C:
				break loop
			}
		}
		drain.Stop()
	}
	s.cancel()
	_ = s.conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	return target.Artifacts{
		Metrics: map[string]float64{"grpc_messages_received": float64(s.received), "turns": float64(s.turns)},
		Events:  append([]target.Event(nil), s.evs...),
	}, nil
}
