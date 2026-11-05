// Package rest is the REST target: each user turn is one HTTP request built
// from a body template, and the agent's turn is read back from the response
// with JSONPaths (plain JSON, or server-sent events concatenated until the
// stream ends); over a websocket the turn is one message out and the messages
// back until a done marker or an idle timeout. Timestamps come from the
// transport.
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/xavidop/tamesu/target"
)

// Streaming modes.
const (
	StreamingNone      = "none"
	StreamingSSE       = "sse"
	StreamingWebSocket = "websocket"
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
	// Open is a message template sent once after a websocket connects
	// (a session setup message); nil sends nothing.
	Open any
	// TextPath, ToolCallsPath and DonePath are JSONPaths into each response
	// (each event, for SSE; each message, for a websocket). An empty TextPath
	// reads the whole body as text. DonePath marks the end of the session.
	TextPath      string
	ToolCallsPath string
	DonePath      string
	// TurnDonePath marks the end of a websocket turn; without it (and
	// without DonePath firing) a turn ends after Idle without a message.
	TurnDonePath string
	// Streaming is none, sse or websocket.
	Streaming string
	// Protocol is the websocket's framing: empty for plain JSON messages,
	// socketio for a socket.io server (Engine.IO v4 over the websocket:
	// messages are events, {"event": name, "data": payload} in both
	// directions, pings answered).
	Protocol string
	// Handshake are messages exchanged once after connecting (and after
	// Open): each step sends a message and, with Wait, reads messages until
	// one satisfies the JSONPath, capturing values from it as
	// {{session.NAME}}.
	Handshake []HandshakeStep
	// Join separates the matches of TextPath in one JSON response (plain
	// JSON only); empty concatenates them.
	Join string
	// Idle is the websocket end-of-turn silence; zero means DefaultIdle.
	Idle time.Duration
	// SessionID is the session id template; empty means {{uuid}}.
	SessionID string
	// SessionOpen is a request sent once when the session starts (to create
	// a conversation on the agent's side); its captures are {{session.NAME}}.
	SessionOpen *Request
	// SentCapture takes values from the reply to each turn's request; they
	// render as {{sent.NAME}} in the poll templates of the same turn.
	SentCapture map[string]string
	// Poll reads the agent's turn by polling a second endpoint after the
	// turn's request (nil: the reply is the request's response).
	Poll *Poll
	// Listen reads the agent's turn from the requests the agent makes to a
	// URL tamesu serves (nil: the reply is the request's response).
	Listen *Listen
	// Timeout bounds one request, or one websocket turn; zero means 60s.
	Timeout time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how a request that never reached the agent, or that the
	// agent answered with 502, 503 or 504, is retried.
	Connect target.ConnectPolicy
}

// ProtocolSocketIO is the socket.io framing of a websocket.
const ProtocolSocketIO = "socketio"

// HandshakeStep is one exchange of a websocket handshake.
type HandshakeStep struct {
	// Send is the message template (socket.io: {event, data}).
	Send any
	// Wait is a JSONPath that must be truthy on a received message before
	// the next step; empty sends and moves on.
	Wait string
	// Capture are JSONPaths into the awaited message, by name.
	Capture map[string]string
	// Timeout bounds the wait; zero means the target's Timeout.
	Timeout time.Duration
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
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
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
	mu   sync.Mutex
	// ln is the listener of response.listen, started by the first session
	// and shared by every session of the run.
	ln *listener
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// Start opens a session: it renders the session id and compiles the JSONPaths.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	s := &session{t: t, run: run, values: map[string]string{}}
	var err error
	if s.sessionID, err = run.Render(t.cfg.SessionID); err != nil {
		return nil, fmt.Errorf("rest %s: session id: %w", t.name, err)
	}
	for _, p := range []struct {
		name, path string
		dst        *jp.Expr
	}{{"response.text", t.cfg.TextPath, &s.textPath}, {"response.tool_calls", t.cfg.ToolCallsPath, &s.toolsPath}, {"response.done", t.cfg.DonePath, &s.donePath}, {"response.turn_done", t.cfg.TurnDonePath, &s.turnDonePath}} {
		if p.path == "" {
			continue
		}
		x, err := jp.ParseString(p.path)
		if err != nil {
			return nil, fmt.Errorf("rest %s: %s %q: %w", t.name, p.name, p.path, err)
		}
		*p.dst = x
	}
	if err := s.compileAsync(); err != nil {
		return nil, err
	}
	if t.cfg.Listen != nil {
		if err := s.listen(ctx); err != nil {
			return nil, err
		}
	}
	if t.cfg.SessionOpen != nil {
		if err := s.open(ctx); err != nil {
			return nil, err
		}
	}
	if t.cfg.Streaming == StreamingWebSocket {
		if err := s.dialWS(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

type session struct {
	t            *Target
	run          *target.RunContext
	sessionID    string
	textPath     jp.Expr
	toolsPath    jp.Expr
	donePath     jp.Expr
	turnDonePath jp.Expr
	pending      *target.AgentTurn
	closed       bool
	turns        int
	polls        int
	statuses     []float64
	ws           *wsState
	// values are the session's template values beyond the turn's:
	// session.NAME (session.open captures), sent.NAME (the last turn's
	// request captures), cursor (poll) and listen.url.
	values map[string]string
	// inbox receives the agent's requests to the listener (response.listen).
	inbox chan listened
	async asyncPaths
}

// render substitutes the turn's values ({{message}}, {{session}}, {{dtmf}}),
// the session's values ({{session.NAME}}, {{sent.NAME}}, {{cursor}},
// {{listen.url}}) and then the run's scope (vars, secrets, uuid...).
func (s *session) render(str string, turn target.UserTurn) (string, error) {
	values := map[string]string{"message": turn.Text, "session": s.sessionID, "dtmf": turn.DTMF}
	for k, v := range s.values {
		values[k] = v
	}
	for k, v := range values {
		str = strings.ReplaceAll(str, "{{"+k+"}}", v)
		str = strings.ReplaceAll(str, "{{ "+k+" }}", v)
	}
	return s.run.Render(str)
}

// body is the request body template, with the default when none is set.
func (s *session) body() any {
	if s.t.cfg.Body != nil {
		return s.t.cfg.Body
	}
	return map[string]any{"message": "{{message}}", "session_id": "{{session}}"}
}

// payload renders a body template for a turn: a string as is, anything else
// as JSON.
func (s *session) payload(body any, turn target.UserTurn) ([]byte, error) {
	rendered, err := renderValue(body, func(str string) (string, error) { return s.render(str, turn) })
	if err != nil {
		return nil, err
	}
	if str, ok := rendered.(string); ok {
		return []byte(str), nil
	}
	return json.Marshal(rendered)
}

// send performs the turn's request, retrying with the connect policy when
// it never reached the agent (refused, reset, timed out) or the agent
// answered 502, 503 or 504; the last such answer is returned as it came, so
// a status assertion still sees it.
func (s *session) send(ctx context.Context, method, url string, payload []byte, hdr http.Header) (*http.Response, error) {
	tries := s.t.cfg.Connect.Tries()
	for n := 1; ; n++ {
		var body io.Reader
		if len(payload) > 0 {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, fmt.Errorf("request: %w", err)
		}
		req.Header = hdr.Clone()
		resp, err := s.t.cfg.Client.Do(req)
		gateway := err == nil && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout)
		if err == nil && !gateway {
			return resp, nil
		}
		if ctx.Err() != nil || n >= tries {
			if err != nil && tries > 1 {
				return nil, fmt.Errorf("%w (after %d attempts)", err, n)
			}
			return resp, err
		}
		if gateway {
			_ = resp.Body.Close()
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		wait := s.t.cfg.Connect.Wait(n + 1)
		if s.run.Logger != nil {
			s.run.Logger.Warn("connect failed, retrying", "what", "rest "+s.t.name, "attempt", n, "of", tries, "wait", wait.Round(time.Millisecond).String(), "err", err)
		}
		if !target.Sleep(ctx, wait) {
			return nil, ctx.Err()
		}
	}
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
	if s.t.cfg.Streaming == StreamingWebSocket {
		return s.sendWS(ctx, turn)
	}
	payload, err := s.payload(s.body(), turn)
	if err != nil {
		return fmt.Errorf("rest: body: %w", err)
	}
	url, err := s.render(s.t.cfg.URL, turn)
	if err != nil {
		return fmt.Errorf("rest: url: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Accept", "application/json, text/event-stream")
	for k, v := range s.t.cfg.Headers {
		rv, err := s.render(v, turn)
		if err != nil {
			return fmt.Errorf("rest: header %s: %w", k, err)
		}
		hdr.Set(k, rv)
	}
	started := time.Now()
	resp, err := s.send(rctx, s.t.cfg.Method, url, payload, hdr)
	if err != nil {
		return fmt.Errorf("rest: %s %s: %w", s.t.cfg.Method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var agent target.AgentTurn
	switch {
	case s.t.cfg.Poll != nil || s.t.cfg.Listen != nil:
		agent, err = s.readAsync(ctx, resp, turn)
	case s.t.cfg.Streaming == StreamingSSE || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"):
		agent, err = s.readSSE(resp.Body)
	default:
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
		for i, v := range evalPath(s.textPath, s.t.cfg.TextPath, data) {
			str := asString(v)
			if text != nil {
				text.WriteString(str)
			} else {
				if i > 0 && s.t.cfg.Join != "" {
					agent.Text += s.t.cfg.Join
				}
				agent.Text += str
			}
		}
	} else if text == nil {
		b, _ := json.Marshal(data)
		agent.Text = string(b)
	}
	if s.toolsPath != nil {
		for _, v := range evalPath(s.toolsPath, s.t.cfg.ToolCallsPath, data) {
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
	if s.donePath != nil && matchesPath(s.donePath, s.t.cfg.DonePath, data) {
		agent.Done = true
	}
}

// matchesPath says whether a flag path fires on a payload: a filter path
// fires when it selects anything (the filter decided), a plain path when
// its value is truthy (true, "done", "end", a non-zero number).
func matchesPath(x jp.Expr, path string, data any) bool {
	got := evalPath(x, path, data)
	if len(got) == 0 {
		return false
	}
	if isFilterPath(path) {
		return got[0] != nil && got[0] != false
	}
	return truthy(got[0])
}

// evalPath runs a JSONPath on a payload. A path that starts with a filter
// ($[?(...)]) on an object payload runs against the object wrapped in a
// one-element list, so the filter selects the message itself; on a list
// payload it runs as is.
func evalPath(x jp.Expr, path string, data any) []any {
	if isFilterPath(path) {
		if _, isList := data.([]any); !isList {
			return x.Get([]any{data})
		}
	}
	return x.Get(data)
}

func firstOfPath(x jp.Expr, path string, data any) any {
	if got := evalPath(x, path, data); len(got) > 0 {
		return got[0]
	}
	return nil
}

func isFilterPath(path string) bool {
	return strings.HasPrefix(strings.TrimSpace(path), "$[?(")
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

// Close ends the session: a websocket is closed; HTTP has nothing to tear
// down. It returns the per-session metrics.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	if s.closed {
		return target.Artifacts{}, nil
	}
	s.closed = true
	metrics := map[string]float64{}
	if s.t.cfg.Streaming == StreamingWebSocket {
		s.closeWS(metrics)
	} else {
		metrics["http_requests"] = float64(s.turns)
	}
	if s.t.cfg.Poll != nil {
		metrics["poll_requests"] = float64(s.polls)
	}
	if s.inbox != nil {
		metrics["webhooks_received"] = float64(s.t.ln.unregister(s.sessionID))
	}
	return target.Artifacts{Metrics: metrics}, nil
}

// numberFilter is a string template that is one placeholder with the number
// filter ({{session|number}}): the rendered value becomes a JSON number, for
// APIs whose ids are integers.
var numberFilter = regexp.MustCompile(`^\{\{\s*([^|{}]+?)\s*\|\s*number\s*\}\}$`)

// renderValue renders every string in a JSON-like value.
func renderValue(v any, render func(string) (string, error)) (any, error) {
	switch x := v.(type) {
	case string:
		if m := numberFilter.FindStringSubmatch(x); m != nil {
			str, err := render("{{" + m[1] + "}}")
			if err != nil {
				return nil, err
			}
			str = strings.TrimSpace(str)
			if _, err := strconv.ParseFloat(str, 64); err != nil {
				return nil, fmt.Errorf("%s renders %q, which is not a number", x, str)
			}
			return json.Number(str), nil
		}
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
