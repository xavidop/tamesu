// Package voiceflow is the target for an agent built on Voiceflow: each user
// turn is one action (launch, then text) and the agent's turn is read from
// the traces it answers: its text and speak messages, the buttons it offers,
// the tools it called, a handoff to a live agent, the end of the
// conversation. Two transports of the Conversations API: REST (one request
// per turn against realtime-api.voiceflow.com, the default) and the
// WebSocket runtime (a socket.io session on general-runtime.voiceflow.com
// that pushes traces as they are produced).
package voiceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xavidop/tamesu/target"
)

// Transports.
const (
	TransportREST      = "rest"
	TransportWebSocket = "websocket"
)

// Transports lists the transports.
var Transports = []string{TransportREST, TransportWebSocket}

// Defaults.
const (
	DefaultBaseURL          = "https://realtime-api.voiceflow.com/v1/stable"
	DefaultWebSocketBaseURL = "https://general-runtime.voiceflow.com"
	WebSocketPath           = "/v4/interact/socket"
	DefaultEnvironment      = "main"
	DefaultVersion          = "published"
	DefaultUserID           = "tamesu-{{uuid}}"
	DefaultTimeout          = 60 * time.Second
)

// Versions of an environment the REST transport can run.
var Versions = []string{"draft", "published"}

// Config describes a Voiceflow agent.
type Config struct {
	// Transport is rest (default) or websocket.
	Transport string
	// BaseURL is the API; empty means DefaultBaseURL (rest) or
	// DefaultWebSocketBaseURL (websocket).
	BaseURL string
	// APIKey is a personal or service token (rest) or the project's API key
	// (websocket); {{secrets.NAME}}.
	APIKey string
	// ProjectID and Environment address the agent; Version picks the draft
	// or the published build (rest).
	ProjectID   string
	Environment string
	Version     string
	// UserID is the conversation id template; empty means DefaultUserID.
	UserID string
	// Variables are set on the conversation before the launch (rest).
	Variables map[string]string
	// Reset deletes the conversation's state before the launch (rest).
	Reset bool
	// CompletionEvents asks the websocket runtime to stream model output as
	// completion traces.
	CompletionEvents bool
	// Timeout bounds one request or one turn; zero means DefaultTimeout.
	Timeout time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how a request that never reached the API is retried.
	Connect target.ConnectPolicy
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Transport == "" {
		cfg.Transport = TransportREST
	}
	if cfg.BaseURL == "" {
		if cfg.Transport == TransportWebSocket {
			cfg.BaseURL = DefaultWebSocketBaseURL
		} else {
			cfg.BaseURL = DefaultBaseURL
		}
	}
	if cfg.Environment == "" {
		cfg.Environment = DefaultEnvironment
	}
	if cfg.Version == "" {
		cfg.Version = DefaultVersion
	}
	if cfg.UserID == "" {
		cfg.UserID = DefaultUserID
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the voiceflow target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// trace is one trace of the agent's answer, as far as the target reads it.
type trace struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

type session struct {
	t        *Target
	run      *target.RunContext
	apiKey   string
	userID   string
	project  string
	sock     *sioConn
	sessKey  string
	pending  *target.AgentTurn
	greeting *target.AgentTurn
	closed   bool
	ended    bool
	turns    int
	requests int
	traces   int
	events   []target.Event
}

// Start renders the config, opens the transport, resets and primes the
// conversation when asked, and launches the agent; its opening words are
// read with the first turn.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	s := &session{t: t, run: run}
	var err error
	for _, f := range []struct {
		name string
		src  string
		dst  *string
	}{{"api_key", t.cfg.APIKey, &s.apiKey}, {"user_id", t.cfg.UserID, &s.userID}, {"project_id", t.cfg.ProjectID, &s.project}} {
		if *f.dst, err = run.Render(f.src); err != nil {
			return nil, fmt.Errorf("voiceflow %s: %s: %w", t.name, f.name, err)
		}
	}
	if s.apiKey == "" {
		return nil, fmt.Errorf("voiceflow %s: api_key is empty", t.name)
	}
	if s.project == "" {
		return nil, fmt.Errorf("voiceflow %s: project_id is required", t.name)
	}
	vars := map[string]any{}
	for k, v := range t.cfg.Variables {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("voiceflow %s: variables.%s: %w", t.name, k, err)
		}
		vars[k] = rv
	}
	if t.cfg.Transport == TransportWebSocket {
		if err := s.openSocket(ctx); err != nil {
			return nil, fmt.Errorf("voiceflow %s: %w", t.name, err)
		}
	} else {
		if t.cfg.Reset {
			if _, err := s.request(ctx, http.MethodDelete, s.stateURL(""), nil); err != nil {
				return nil, fmt.Errorf("voiceflow %s: reset: %w", t.name, err)
			}
			s.event("reset", s.userID)
		}
		if len(vars) > 0 {
			if _, err := s.request(ctx, http.MethodPatch, s.stateURL("/variables"), map[string]any{"variables": vars}); err != nil {
				return nil, fmt.Errorf("voiceflow %s: variables: %w", t.name, err)
			}
			s.event("variables", fmt.Sprintf("%d set", len(vars)))
		}
	}
	started := time.Now()
	traces, raw, err := s.interact(ctx, map[string]any{"type": "launch", "payload": map[string]any{}})
	if err != nil {
		s.sock.close()
		return nil, fmt.Errorf("voiceflow %s: launch: %w", t.name, err)
	}
	s.event("launch", s.userID)
	greeting := s.absorb(traces)
	greeting.Raw = raw
	greeting.Status = http.StatusOK
	greeting.StartedAt = started
	greeting.EndedAt = time.Now()
	greeting.FirstTokenAt = greeting.EndedAt
	if greeting.Text != "" {
		s.event("greeting", greeting.Text)
	}
	s.greeting = &greeting
	return s, nil
}

// ---- REST transport

func (s *session) conversationURL() string {
	return fmt.Sprintf("%s/conversation/%s?%s", strings.TrimRight(s.t.cfg.BaseURL, "/"), url.PathEscape(s.userID), s.query(false))
}

func (s *session) stateURL(suffix string) string {
	return fmt.Sprintf("%s/conversation-state/%s%s?%s", strings.TrimRight(s.t.cfg.BaseURL, "/"), url.PathEscape(s.userID), suffix, s.query(true))
}

func (s *session) query(withVersion bool) string {
	q := url.Values{"projectID": {s.project}, "environmentAlias": {s.t.cfg.Environment}}
	if withVersion {
		q.Set("version", s.t.cfg.Version)
	}
	return q.Encode()
}

// request performs one REST call and returns the body; a 4xx or 5xx other
// than a gateway error is a final error with the body.
func (s *session) request(ctx context.Context, method, u string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	s.requests++
	type result struct{ body []byte }
	res, err := target.Connect(ctx, s.t.cfg.Connect, s.run.Logger, "voiceflow "+s.t.name, func(ctx context.Context) (result, error) {
		rctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
		defer cancel()
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(rctx, method, u, rd)
		if err != nil {
			return result{}, target.NoRetry(err)
		}
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := s.t.cfg.Client.Do(req)
		if err != nil {
			return result{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		switch {
		case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
			return result{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		case resp.StatusCode >= 400:
			return result{}, target.NoRetry(fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerpt(raw)))
		}
		return result{body: raw}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, u, err)
	}
	return res.body, nil
}

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
}

// cleanJSON strips the control characters the runtime may leave in strings.
func cleanJSON(raw []byte) []byte {
	return []byte(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\r' {
			return -1
		}
		return r
	}, string(raw)))
}

// interact sends one action and returns the traces, by transport.
func (s *session) interact(ctx context.Context, action map[string]any) ([]trace, any, error) {
	if s.t.cfg.Transport == TransportWebSocket {
		return s.interactSocket(ctx, action)
	}
	raw, err := s.request(ctx, http.MethodPut, s.conversationURL(), map[string]any{"action": action, "version": s.t.cfg.Version})
	if err != nil {
		return nil, nil, err
	}
	var decoded struct {
		Traces json.RawMessage `json:"traces"`
	}
	if err := json.Unmarshal(cleanJSON(raw), &decoded); err != nil {
		return nil, nil, fmt.Errorf("unexpected response: %s", excerpt(raw))
	}
	var list []trace
	var rawTraces any
	if len(decoded.Traces) > 0 {
		if err := json.Unmarshal(decoded.Traces, &list); err != nil {
			return nil, nil, fmt.Errorf("unexpected traces: %s", excerpt(raw))
		}
		_ = json.Unmarshal(decoded.Traces, &rawTraces)
	}
	if rawTraces == nil {
		rawTraces = []any{}
	}
	return list, rawTraces, nil
}

// ---- WebSocket transport (socket.io)

// openSocket connects to the runtime, sends client.start and creates the
// session the runtime asks for.
func (s *session) openSocket(ctx context.Context) error {
	hdr := http.Header{}
	sock, err := target.Connect(ctx, s.t.cfg.Connect, s.run.Logger, "voiceflow "+s.t.name+" socket", func(ctx context.Context) (*sioConn, error) {
		return sioDial(ctx, s.t.cfg.Client, s.t.cfg.BaseURL, WebSocketPath, hdr, s.t.cfg.Timeout)
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	s.sock = sock
	hctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	start := map[string]any{"userID": s.userID, "projectID": s.project, "authorization": s.apiKey}
	if s.t.cfg.Environment != "" {
		start["environmentID"] = s.t.cfg.Environment
	}
	if s.t.cfg.CompletionEvents {
		start["config"] = map[string]any{"completionEvents": true}
	}
	if err := sock.emit(hctx, "client.start", start); err != nil {
		sock.close()
		return fmt.Errorf("client.start: %w", err)
	}
	ev, err := sock.wait(hctx, map[string]bool{"client.started": true, "error": true}, nil)
	if err != nil {
		sock.close()
		return fmt.Errorf("client.start: %w", err)
	}
	if ev.Name == "error" {
		sock.close()
		return fmt.Errorf("client.start refused: %s", socketError(ev.Data))
	}
	var started struct {
		NewSessionRequired bool `json:"newSessionRequired"`
	}
	_ = json.Unmarshal(ev.Data, &started)
	s.event("client_started", fmt.Sprintf("new session required: %t", started.NewSessionRequired))
	if started.NewSessionRequired {
		if err := sock.emit(hctx, "session.create", map[string]any{}); err != nil {
			sock.close()
			return fmt.Errorf("session.create: %w", err)
		}
		ev, err = sock.wait(hctx, map[string]bool{"session.created": true, "error": true}, nil)
		if err != nil {
			sock.close()
			return fmt.Errorf("session.create: %w", err)
		}
		if ev.Name == "error" {
			sock.close()
			return fmt.Errorf("session.create refused: %s", socketError(ev.Data))
		}
		var created struct {
			SessionKey string `json:"sessionKey"`
		}
		_ = json.Unmarshal(ev.Data, &created)
		s.sessKey = created.SessionKey
		s.event("session_created", "")
	}
	return nil
}

func socketError(data json.RawMessage) string {
	var e struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return strings.TrimSpace(e.Code + " " + e.Error)
	}
	return excerpt(data)
}

// interactSocket sends action.send and collects the action.trace events
// until action.status says completed (or rejected), or session.ended.
func (s *session) interactSocket(ctx context.Context, action map[string]any) ([]trace, any, error) {
	if s.sock == nil {
		return nil, nil, errors.New("the socket is not connected")
	}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	s.requests++
	if err := s.sock.emit(tctx, "action.send", map[string]any{"action": action}); err != nil {
		return nil, nil, fmt.Errorf("action.send: %w", err)
	}
	var list []trace
	var raws []any
	for {
		ev, err := s.sock.wait(tctx, map[string]bool{"action.status": true, "action.trace": true, "session.ended": true, "error": true, "server.restart": true}, nil)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return list, raws, fmt.Errorf("the action did not complete within %s", s.t.cfg.Timeout)
			}
			return list, raws, err
		}
		switch ev.Name {
		case "action.trace":
			var p struct {
				Trace json.RawMessage `json:"trace"`
			}
			if json.Unmarshal(ev.Data, &p) != nil || len(p.Trace) == 0 {
				continue
			}
			var tr trace
			if json.Unmarshal(cleanJSON(p.Trace), &tr) == nil {
				list = append(list, tr)
			}
			var raw any
			_ = json.Unmarshal(cleanJSON(p.Trace), &raw)
			raws = append(raws, raw)
		case "action.status":
			var st struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(ev.Data, &st)
			switch st.Status {
			case "completed":
				if raws == nil {
					raws = []any{}
				}
				// The runtime may end the session right after the action
				// (an End step): give that event a moment to arrive.
				if s.sessionEndedSoon() {
					list = append(list, trace{Type: "end"})
				}
				return list, raws, nil
			case "rejected":
				return list, raws, fmt.Errorf("the runtime rejected the action: %s", strings.TrimSpace(st.Reason))
			}
		case "session.ended":
			var p struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(ev.Data, &p)
			s.ended = true
			s.event("session_ended", p.Reason)
			list = append(list, trace{Type: "end"})
			if raws == nil {
				raws = []any{}
			}
			return list, raws, nil
		case "error":
			return list, raws, fmt.Errorf("runtime error: %s", socketError(ev.Data))
		case "server.restart":
			return list, raws, errors.New("the runtime asked the client to reconnect (server.restart)")
		}
	}
}

// sessionEndedSoon waits briefly for a session.ended that follows a
// completed action; other events that arrive meanwhile are kept for the
// next turn by being re-queued.
func (s *session) sessionEndedSoon() bool {
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	var kept []sioEvent
	defer func() {
		for _, ev := range kept {
			select {
			case s.sock.events <- ev:
			default:
			}
		}
	}()
	for {
		select {
		case <-timer.C:
			return false
		case ev, ok := <-s.sock.events:
			if !ok {
				return false
			}
			if ev.Name == "session.ended" {
				var p struct {
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(ev.Data, &p)
				s.ended = true
				s.event("session_ended", p.Reason)
				return true
			}
			kept = append(kept, ev)
		}
	}
}

// ---- traces

// absorb reads the traces of one answer into a turn.
func (s *session) absorb(traces []trace) target.AgentTurn {
	var agent target.AgentTurn
	var parts []string
	for _, tr := range traces {
		s.traces++
		p := tr.Payload
		switch tr.Type {
		case "text", "speak":
			if msg := str(p["message"]); msg != "" {
				parts = append(parts, msg)
			}
		case "completion":
			if str(p["state"]) == "content" {
				if c := str(p["content"]); c != "" {
					parts = append(parts, c)
				}
			}
		case "choice":
			if names := buttonNames(p["buttons"]); len(names) > 0 {
				parts = append(parts, "[buttons: "+strings.Join(names, " | ")+"]")
			}
		case "cardV2":
			title := str(p["title"])
			if d, ok := p["description"].(map[string]any); ok {
				if t := str(d["text"]); t != "" {
					title = strings.TrimSpace(title + ": " + t)
				}
			}
			if title != "" {
				parts = append(parts, "[card: "+title+"]")
			}
			if names := buttonNames(p["buttons"]); len(names) > 0 {
				parts = append(parts, "[buttons: "+strings.Join(names, " | ")+"]")
			}
		case "carousel":
			var titles []string
			if cards, ok := p["cards"].([]any); ok {
				for _, c := range cards {
					if cm, ok := c.(map[string]any); ok {
						if t := str(cm["title"]); t != "" {
							titles = append(titles, t)
						}
					}
				}
			}
			if len(titles) > 0 {
				parts = append(parts, "[carousel: "+strings.Join(titles, " | ")+"]")
			}
		case "tool-call":
			// The payload carries callID and state (start / end); a name is
			// read when the runtime adds one.
			if st := str(p["state"]); st != "" && st != "start" {
				continue
			}
			call := target.ToolCall{Name: firstString(p, "name", "toolName", "tool", "functionName")}
			if args, ok := firstOf(p, "arguments", "args", "input", "parameters").(map[string]any); ok {
				call.Args = args
			}
			if out := firstOf(p, "output", "result", "response"); out != nil {
				call.Result = out
			}
			if call.Name != "" {
				agent.ToolCalls = append(agent.ToolCalls, call)
			}
			s.event("tool_call", strings.TrimSpace(call.Name+" "+str(p["callID"])))
		case "end":
			agent.Done = true
			s.ended = true
			s.event("end", "the conversation reached an End step")
		case "live-agent-handoff":
			detail := strings.TrimSpace(str(p["provider"]) + " " + str(p["message"]))
			s.event("live_agent_handoff", detail)
		case "no-reply":
			s.event("no_reply", fmt.Sprintf("reprompt after %v s", p["timeout"]))
		case "debug", "log", "block", "flow", "path", "reasoning", "knowledgeBase", "visual", "entity-filling", "goto", "channel-action", "":
			// Diagnostics and rendering hints: in the raw traces, not the text.
		default:
			s.event("trace", tr.Type)
		}
	}
	agent.Text = strings.Join(parts, "\n")
	return agent
}

func buttonNames(v any) []string {
	var names []string
	if list, ok := v.([]any); ok {
		for _, b := range list {
			if bm, ok := b.(map[string]any); ok {
				if n := str(bm["name"]); n != "" {
					names = append(names, n)
				}
			}
		}
	}
	return names
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	return str(firstOf(m, keys...))
}

// Send sends the user's text and reads the agent's traces; the launch's
// opening words are read with the first turn.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	if s.closed {
		return errors.New("voiceflow: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ended && s.t.cfg.Transport == TransportWebSocket {
		return errors.New("voiceflow: the session has ended")
	}
	action := map[string]any{"type": "text", "payload": turn.Text}
	if turn.Text == "" && turn.DTMF != "" {
		action = map[string]any{"type": "dtmf", "payload": map[string]any{"digits": turn.DTMF}}
	}
	started := time.Now()
	traces, raw, err := s.interact(ctx, action)
	if err != nil {
		return fmt.Errorf("voiceflow: %w", err)
	}
	agent := s.absorb(traces)
	agent.Raw = raw
	agent.Status = http.StatusOK
	agent.StartedAt = started
	agent.EndedAt = time.Now()
	agent.FirstTokenAt = agent.EndedAt
	if g := s.greeting; g != nil {
		s.greeting = nil
		if g.Text != "" {
			agent.Text = strings.TrimSpace(g.Text + "\n" + agent.Text)
		}
		agent.ToolCalls = append(g.ToolCalls, agent.ToolCalls...)
		agent.Done = agent.Done || g.Done
		agent.Raw = []any{g.Raw, raw}
	}
	s.turns++
	s.pending = &agent
	return nil
}

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("voiceflow: Receive without a pending Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close ends the session: the socket is closed; a REST conversation the
// agent did not end is left for the next test of the same user id to reset.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	if s.closed {
		return target.Artifacts{}, nil
	}
	s.closed = true
	metrics := map[string]float64{"voiceflow_traces": float64(s.traces)}
	if s.sock != nil {
		sent, recv := s.sock.counts()
		s.sock.close()
		metrics["ws_messages_sent"] = float64(sent)
		metrics["ws_messages_received"] = float64(recv)
	} else {
		metrics["http_requests"] = float64(s.requests)
	}
	return target.Artifacts{Metrics: metrics, Events: s.events}, nil
}

func (s *session) event(kind, detail string) {
	s.events = append(s.events, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}
