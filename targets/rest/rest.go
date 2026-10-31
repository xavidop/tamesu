// Package rest is the REST target: each user turn is one HTTP request built
// from a body template, and the agent's turn is read back from the response
// with JSONPaths (plain JSON, or server-sent events concatenated until the
// stream ends). Timestamps come from the transport.
package rest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/xavidop/tamesu/target"
)

// Streaming modes.
const (
	StreamingNone = "none"
	StreamingSSE  = "sse"
)

// Config describes a REST agent. String fields are templates rendered with
// the run's scope ({{secrets.X}}, {{vars.x}}, {{session}}, {{message}}).
type Config struct {
	URL     string
	Method  string
	Headers map[string]string
	// Body is the request body template (a map, list or string); {{message}}
	// is the user turn and {{session}} the session id. nil sends
	// {"message": ..., "session_id": ...}.
	Body any
	// TextPath, ToolCallsPath and DonePath are JSONPaths into each response
	// (each event, for SSE). An empty TextPath reads the whole body as text.
	TextPath      string
	ToolCallsPath string
	DonePath      string
	// Streaming is none or sse.
	Streaming string
	// SessionID is the session id template; empty means {{uuid}}.
	SessionID string
	// Timeout bounds one request; zero means 60s.
	Timeout time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.Streaming == "" {
		cfg.Streaming = StreamingNone
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "{{uuid}}"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the REST target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// Start opens a session: it renders the session id and compiles the JSONPaths.
func (t *Target) Start(_ context.Context, run *target.RunContext) (target.Session, error) {
	s := &session{t: t, run: run}
	var err error
	if s.sessionID, err = run.Render(t.cfg.SessionID); err != nil {
		return nil, fmt.Errorf("rest %s: session id: %w", t.name, err)
	}
	for _, p := range []struct {
		name, path string
		dst        *jp.Expr
	}{{"response.text", t.cfg.TextPath, &s.textPath}, {"response.tool_calls", t.cfg.ToolCallsPath, &s.toolsPath}, {"response.done", t.cfg.DonePath, &s.donePath}} {
		if p.path == "" {
			continue
		}
		x, err := jp.ParseString(p.path)
		if err != nil {
			return nil, fmt.Errorf("rest %s: %s %q: %w", t.name, p.name, p.path, err)
		}
		*p.dst = x
	}
	return s, nil
}

type session struct {
	t         *Target
	run       *target.RunContext
	sessionID string
	textPath  jp.Expr
	toolsPath jp.Expr
	donePath  jp.Expr
	pending   *target.AgentTurn
	closed    bool
	turns     int
	statuses  []float64
}

// Send performs the request for a user turn and keeps the agent's turn for
// Receive.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	if s.closed {
		return errors.New("rest: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	values := map[string]string{"message": turn.Text, "session": s.sessionID, "dtmf": turn.DTMF}
	render := func(str string) (string, error) {
		// Run-time values first, then the run's scope (vars, secrets, uuid...).
		for k, v := range values {
			str = strings.ReplaceAll(str, "{{"+k+"}}", v)
			str = strings.ReplaceAll(str, "{{ "+k+" }}", v)
		}
		return s.run.Render(str)
	}
	body := s.t.cfg.Body
	if body == nil {
		body = map[string]any{"message": "{{message}}", "session_id": "{{session}}"}
	}
	rendered, err := renderValue(body, render)
	if err != nil {
		return fmt.Errorf("rest: body: %w", err)
	}
	var payload []byte
	if str, ok := rendered.(string); ok {
		payload = []byte(str)
	} else if payload, err = json.Marshal(rendered); err != nil {
		return fmt.Errorf("rest: body: %w", err)
	}
	url, err := render(s.t.cfg.URL)
	if err != nil {
		return fmt.Errorf("rest: url: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, s.t.cfg.Method, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("rest: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range s.t.cfg.Headers {
		rv, err := render(v)
		if err != nil {
			return fmt.Errorf("rest: header %s: %w", k, err)
		}
		req.Header.Set(k, rv)
	}
	started := time.Now()
	resp, err := s.t.cfg.Client.Do(req)
	if err != nil {
		return fmt.Errorf("rest: %s %s: %w", s.t.cfg.Method, url, err)
	}
	defer resp.Body.Close()
	var agent target.AgentTurn
	if s.t.cfg.Streaming == StreamingSSE || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		agent, err = s.readSSE(resp.Body)
	} else {
		agent, err = s.readJSON(resp.Body)
	}
	if err != nil {
		return fmt.Errorf("rest: response: %w", err)
	}
	agent.StartedAt = started
	agent.EndedAt = time.Now()
	if agent.FirstTokenAt.IsZero() {
		agent.FirstTokenAt = agent.EndedAt
	}
	agent.Status = resp.StatusCode
	s.turns++
	s.statuses = append(s.statuses, float64(resp.StatusCode))
	s.pending = &agent
	return nil
}

// readJSON reads a plain JSON response.
func (s *session) readJSON(r io.Reader) (target.AgentTurn, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return target.AgentTurn{}, err
	}
	data, err := oj.Parse(b)
	if err != nil {
		// Not JSON: the body is the reply.
		return target.AgentTurn{Text: strings.TrimSpace(string(b)), Raw: string(b)}, nil
	}
	agent := target.AgentTurn{Raw: data}
	s.extract(data, &agent, nil)
	return agent, nil
}

// readSSE concatenates the text of every data: event until the stream ends
// or a [DONE] sentinel. FirstTokenAt is the first event.
func (s *session) readSSE(r io.Reader) (target.AgentTurn, error) {
	agent := target.AgentTurn{}
	var text strings.Builder
	var events []any
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var data strings.Builder
	flush := func() {
		if data.Len() == 0 {
			return
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" || payload == "[DONE]" {
			return
		}
		if agent.FirstTokenAt.IsZero() {
			agent.FirstTokenAt = time.Now()
		}
		ev, err := oj.ParseString(payload)
		if err != nil {
			text.WriteString(payload)
			return
		}
		events = append(events, ev)
		s.extract(ev, &agent, &text)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return agent, err
	}
	agent.Text = text.String()
	agent.Raw = events
	return agent, nil
}

// extract reads the text, tool calls and done flag of one payload. With
// text != nil (SSE) the text is appended as a delta; otherwise it is set.
func (s *session) extract(data any, agent *target.AgentTurn, text *strings.Builder) {
	if s.textPath != nil {
		for _, v := range s.textPath.Get(data) {
			str := asString(v)
			if text != nil {
				text.WriteString(str)
			} else {
				agent.Text += str
			}
		}
	} else if text == nil {
		b, _ := json.Marshal(data)
		agent.Text = string(b)
	}
	if s.toolsPath != nil {
		for _, v := range s.toolsPath.Get(data) {
			switch tc := v.(type) {
			case string:
				agent.ToolCalls = append(agent.ToolCalls, target.ToolCall{Name: tc})
			case map[string]any:
				call := target.ToolCall{Name: asString(firstOf(tc, "name", "tool", "function"))}
				if args, ok := firstOf(tc, "args", "arguments", "input").(map[string]any); ok {
					call.Args = args
				} else if str, ok := firstOf(tc, "args", "arguments", "input").(string); ok && str != "" {
					var m map[string]any
					if json.Unmarshal([]byte(str), &m) == nil {
						call.Args = m
					}
				}
				if call.Name != "" {
					agent.ToolCalls = append(agent.ToolCalls, call)
				}
			}
		}
	}
	if s.donePath != nil {
		if v := s.donePath.First(data); truthy(v) {
			agent.Done = true
		}
	}
}

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("rest: Receive without a pending Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close ends the session. There is nothing to tear down over HTTP; it returns
// the per-session metrics.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	s.closed = true
	return target.Artifacts{Metrics: map[string]float64{"http_requests": float64(s.turns)}}, nil
}

// renderValue renders every string in a JSON-like value.
func renderValue(v any, render func(string) (string, error)) (any, error) {
	switch x := v.(type) {
	case string:
		return render(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			r, err := renderValue(val, render)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			r, err := renderValue(val, render)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "done" || x == "end"
	case nil:
		return false
	case float64:
		return x != 0
	case int64:
		return x != 0
	default:
		return true
	}
}
