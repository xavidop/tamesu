package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/xavidop/tamesu/target"
)

// Request is an HTTP request template: session.open and response.poll.
type Request struct {
	URL     string
	Method  string
	Headers map[string]string
	// Body is the body template; nil sends no body.
	Body any
	// Capture are JSONPaths into the JSON reply, by name.
	Capture map[string]string
}

// Poll reads the agent's turn by polling a second endpoint after each turn's
// request: the agent answers somewhere else than the request's response (a
// chat channel's history, a conversation's activities).
type Poll struct {
	Request
	// Interval is the time between polls; zero means DefaultPollInterval.
	Interval time.Duration
	// Timeout bounds the wait for a reply; zero means the target's Timeout.
	Timeout time.Duration
	// Idle keeps polling after the first reply until nothing new arrives for
	// this long (an agent that answers in several messages); zero completes
	// the turn on the first reply with content.
	Idle time.Duration
	// Cursor is a JSONPath into each poll reply whose value renders as
	// {{cursor}} in the next poll (a watermark, a timestamp); with it the
	// replies accumulate, without it each poll replaces the last.
	Cursor string
}

// Listen reads the agent's turn from the requests the agent makes to a URL
// tamesu serves: a bot that answers through the platform's send API.
type Listen struct {
	// Addr is the address to listen on; empty means 127.0.0.1 on a free
	// port. The URL renders as {{listen.url}}.
	Addr string
	// Path selects the requests that carry replies: an optional method, then
	// a path pattern with * wildcards ("POST /bot*/sendMessage"); a pattern
	// ending in / matches the prefix. Empty means every request. The other
	// requests are answered with Reply but not read.
	Path string
	// Session is a JSONPath into the incoming payload that holds the session
	// id, to route the request to its session; empty delivers to the oldest
	// open session.
	Session string
	// Reply is the body template tamesu answers the agent with; nil means
	// {"ok": true}.
	Reply any
	// Status is the status answered; zero means 200.
	Status int
	// Timeout bounds the wait for a reply; zero means the target's Timeout.
	Timeout time.Duration
	// Idle keeps reading after the first reply until nothing new arrives for
	// this long; zero completes the turn on the first request with content.
	Idle time.Duration
}

// DefaultPollInterval is the time between polls when none is set.
const DefaultPollInterval = 500 * time.Millisecond

// asyncPaths are the compiled JSONPaths of the asynchronous modes.
type asyncPaths struct {
	openCapture map[string]jp.Expr
	sentCapture map[string]jp.Expr
	cursor      jp.Expr
}

// compileAsync compiles the JSONPaths of session.open, request.capture and
// response.poll.
func (s *session) compileAsync() error {
	var err error
	if s.t.cfg.SessionOpen != nil {
		if s.t.cfg.SessionOpen.URL == "" {
			return fmt.Errorf("rest %s: session.open needs url", s.t.name)
		}
		if s.async.openCapture, err = compilePaths("session.open.capture", s.t.cfg.SessionOpen.Capture); err != nil {
			return fmt.Errorf("rest %s: %w", s.t.name, err)
		}
	}
	if s.async.sentCapture, err = compilePaths("request.capture", s.t.cfg.SentCapture); err != nil {
		return fmt.Errorf("rest %s: %w", s.t.name, err)
	}
	if p := s.t.cfg.Poll; p != nil {
		if p.URL == "" {
			return fmt.Errorf("rest %s: response.poll needs url", s.t.name)
		}
		if p.Cursor != "" {
			if s.async.cursor, err = jp.ParseString(p.Cursor); err != nil {
				return fmt.Errorf("rest %s: response.poll.cursor %q: %w", s.t.name, p.Cursor, err)
			}
		}
	}
	return nil
}

func compilePaths(what string, paths map[string]string) (map[string]jp.Expr, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make(map[string]jp.Expr, len(paths))
	for name, path := range paths {
		x, err := jp.ParseString(path)
		if err != nil {
			return nil, fmt.Errorf("%s %s %q: %w", what, name, path, err)
		}
		out[name] = x
	}
	return out, nil
}

// request performs one templated request and returns its status, its body
// parsed as JSON (or the body text when it is not JSON) and the raw body.
func (s *session) request(ctx context.Context, req Request, defaultMethod string, turn target.UserTurn) (int, any, []byte, error) {
	method := req.Method
	if method == "" {
		method = defaultMethod
	}
	u, err := s.render(req.URL, turn)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("url: %w", err)
	}
	var payload []byte
	if req.Body != nil {
		if payload, err = s.payload(req.Body, turn); err != nil {
			return 0, nil, nil, fmt.Errorf("body: %w", err)
		}
	}
	hdr := http.Header{}
	hdr.Set("Accept", "application/json")
	if payload != nil {
		hdr.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		rv, err := s.render(v, turn)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("header %s: %w", k, err)
		}
		hdr.Set(k, rv)
	}
	rctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	resp, err := s.send(rctx, method, u, payload, hdr)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s %s: %w", method, u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, nil, nil, fmt.Errorf("%s %s: reading the response: %w", method, u, err)
	}
	return resp.StatusCode, parseBody(raw, resp.Header.Get("Content-Type")), raw, nil
}

// parseBody parses a reply: JSON when it is JSON, a form into a map, the
// text otherwise.
func parseBody(raw []byte, contentType string) any {
	mt, _, _ := mime.ParseMediaType(contentType)
	if mt == "application/x-www-form-urlencoded" {
		if values, err := url.ParseQuery(string(raw)); err == nil {
			return formMap(values)
		}
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	data, err := oj.Parse(raw)
	if err != nil {
		return string(raw)
	}
	return data
}

func formMap(values url.Values) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		if len(v) == 1 {
			out[k] = v[0]
			continue
		}
		list := make([]any, len(v))
		for i, x := range v {
			list[i] = x
		}
		out[k] = list
	}
	return out
}

// captureInto takes the captures of a reply into the session's values under
// prefix (session., sent.); a path with no match clears the value.
func (s *session) captureInto(prefix string, paths map[string]jp.Expr, data any) {
	for name, x := range paths {
		key := prefix + name
		if got := x.Get(data); len(got) > 0 {
			s.values[key] = asString(got[0])
		} else {
			delete(s.values, key)
		}
	}
}

// open performs session.open and keeps its captures as {{session.NAME}}.
func (s *session) open(ctx context.Context) error {
	req := *s.t.cfg.SessionOpen
	status, data, raw, err := s.request(ctx, req, http.MethodPost, target.UserTurn{})
	if err != nil {
		return fmt.Errorf("rest %s: session.open: %w", s.t.name, err)
	}
	if status >= 400 {
		return fmt.Errorf("rest %s: session.open: HTTP %d: %s", s.t.name, status, excerpt(raw))
	}
	s.captureInto("session.", s.async.openCapture, data)
	if s.run.Logger != nil {
		s.run.Logger.Debug("session opened", "what", "rest "+s.t.name, "status", status)
	}
	return nil
}

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
}

// readAsync finishes a turn whose reply arrives outside the request's
// response: the response's captures are taken, then the reply is polled or
// listened for. A request the agent refused (4xx, 5xx) is the turn as it
// came, so a status assertion sees it.
func (s *session) readAsync(ctx context.Context, resp *http.Response, turn target.UserTurn) (target.AgentTurn, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return target.AgentTurn{}, err
	}
	data := parseBody(raw, resp.Header.Get("Content-Type"))
	if resp.StatusCode >= 400 {
		return target.AgentTurn{Text: excerpt(raw), Raw: data}, nil
	}
	s.captureInto("sent.", s.async.sentCapture, data)
	if s.t.cfg.Poll != nil {
		return s.poll(ctx, turn)
	}
	return s.listenTurn(ctx)
}

// hasContent is whether a payload carried any of the agent's turn.
func hasContent(a target.AgentTurn) bool {
	return a.Text != "" || len(a.ToolCalls) > 0 || a.Done
}

// extractOne reads one payload into a fresh turn.
func (s *session) extractOne(data any) target.AgentTurn {
	var candidate target.AgentTurn
	var text strings.Builder
	s.extract(data, &candidate, &text)
	if s.textPath == nil {
		b, _ := json.Marshal(data)
		text.WriteString(string(b))
	}
	candidate.Text = text.String()
	return candidate
}

// poll reads the agent's turn from response.poll: the endpoint is polled
// every Interval until a reply has content (text, tool calls or the done
// flag), then, with Idle, until nothing new arrives for that long.
func (s *session) poll(ctx context.Context, turn target.UserTurn) (target.AgentTurn, error) {
	p := s.t.cfg.Poll
	interval, timeout := p.Interval, p.Timeout
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = s.t.cfg.Timeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	agent := target.AgentTurn{}
	var payloads []any
	var got time.Time
	finish := func() (target.AgentTurn, error) {
		agent.Raw = payloads
		return agent, nil
	}
	for {
		status, data, raw, err := s.request(pctx, p.Request, http.MethodGet, turn)
		s.polls++
		if err != nil {
			if !got.IsZero() && (errors.Is(err, context.DeadlineExceeded) || pctx.Err() != nil) {
				return finish()
			}
			if pctx.Err() != nil && ctx.Err() == nil {
				return agent, fmt.Errorf("poll: no reply within %s: %w", timeout, err)
			}
			return agent, fmt.Errorf("poll: %w", err)
		}
		if status >= 400 {
			return agent, fmt.Errorf("poll: HTTP %d: %s", status, excerpt(raw))
		}
		if s.async.cursor != nil {
			if v := s.async.cursor.First(data); v != nil {
				s.values["cursor"] = asString(v)
			}
		}
		candidate := s.extractOne(data)
		changed := false
		if hasContent(candidate) {
			switch {
			case s.async.cursor != nil || got.IsZero():
				// Incremental pages, or the first reply: accumulate.
				agent.Text += candidate.Text
				agent.ToolCalls = append(agent.ToolCalls, candidate.ToolCalls...)
				agent.Done = agent.Done || candidate.Done
				payloads = append(payloads, data)
				changed = true
			case candidate.Text != agent.Text || len(candidate.ToolCalls) != len(agent.ToolCalls) || candidate.Done != agent.Done:
				// The endpoint returns the whole state: take the latest.
				agent.Text, agent.ToolCalls, agent.Done = candidate.Text, candidate.ToolCalls, candidate.Done
				payloads = []any{data}
				changed = true
			}
		}
		if changed {
			if got.IsZero() {
				agent.FirstTokenAt = time.Now()
			}
			got = time.Now()
			if p.Idle <= 0 || agent.Done {
				return finish()
			}
		}
		if !got.IsZero() && time.Since(got) >= p.Idle {
			return finish()
		}
		if !target.Sleep(pctx, interval) {
			if !got.IsZero() {
				return finish()
			}
			if ctx.Err() != nil {
				return agent, ctx.Err()
			}
			return agent, fmt.Errorf("poll: no reply within %s", timeout)
		}
	}
}

// listened is one request the agent made to the listener.
type listened struct {
	Method string
	Path   string
	Data   any
}

// listener serves response.listen for every session of a target.
type listener struct {
	srv      *http.Server
	url      string
	cfg      Listen
	session  jp.Expr
	method   string
	pattern  string
	mu       sync.Mutex
	sessions map[string]*session
	order    []string
	received map[string]int
	dropped  int
}

// listener starts the target's listener on first use.
func (t *Target) listener() (*listener, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ln != nil {
		return t.ln, nil
	}
	cfg := *t.cfg.Listen
	addr := cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln := &listener{cfg: cfg, sessions: map[string]*session{}, received: map[string]int{}}
	ln.method, ln.pattern = splitPattern(cfg.Path)
	if _, err := path.Match(ln.pattern, "/"); err != nil {
		return nil, fmt.Errorf("rest %s: response.listen.path %q: %w", t.name, cfg.Path, err)
	}
	if cfg.Session != "" {
		x, err := jp.ParseString(cfg.Session)
		if err != nil {
			return nil, fmt.Errorf("rest %s: response.listen.session %q: %w", t.name, cfg.Session, err)
		}
		ln.session = x
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rest %s: response.listen: %w", t.name, err)
	}
	host := l.Addr().String()
	if h, port, err := net.SplitHostPort(host); err == nil && (h == "" || h == "::" || h == "0.0.0.0") {
		host = net.JoinHostPort("127.0.0.1", port)
	}
	ln.url = "http://" + host
	ln.srv = &http.Server{Handler: http.HandlerFunc(ln.handle), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = ln.srv.Serve(l) }()
	t.ln = ln
	return ln, nil
}

// Shutdown stops the listener of response.listen, when one was started.
func (t *Target) Shutdown(ctx context.Context) error {
	t.mu.Lock()
	ln := t.ln
	t.ln = nil
	t.mu.Unlock()
	if ln == nil {
		return nil
	}
	return ln.srv.Shutdown(ctx)
}

// URL is the listener's base URL ({{listen.url}}), empty before a session
// started it.
func (t *Target) ListenURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ln == nil {
		return ""
	}
	return t.ln.url
}

func (ln *listener) register(s *session) {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	ln.sessions[s.sessionID] = s
	ln.order = append(ln.order, s.sessionID)
}

// unregister forgets a session and returns how many requests it received.
func (ln *listener) unregister(id string) int {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	delete(ln.sessions, id)
	for i, x := range ln.order {
		if x == id {
			ln.order = append(ln.order[:i], ln.order[i+1:]...)
			break
		}
	}
	n := ln.received[id]
	delete(ln.received, id)
	return n
}

// route finds the session of an incoming payload: the one whose id the
// session path yields, else the oldest open session.
func (ln *listener) route(data any) *session {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	if ln.session != nil {
		if v := ln.session.First(data); v != nil {
			if s, ok := ln.sessions[asString(v)]; ok {
				ln.received[s.sessionID]++
				return s
			}
		}
	}
	if len(ln.order) == 0 {
		ln.dropped++
		return nil
	}
	s := ln.sessions[ln.order[0]]
	ln.received[s.sessionID]++
	return s
}

// splitPattern separates the optional method of a listen path ("POST /x/*").
func splitPattern(p string) (method, pattern string) {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, ' '); i > 0 && !strings.HasPrefix(p, "/") {
		return strings.ToUpper(p[:i]), strings.TrimSpace(p[i+1:])
	}
	if p == "" {
		return "", "/"
	}
	return "", p
}

// matches is whether a request is one that carries replies.
func (ln *listener) matches(r *http.Request) bool {
	if ln.method != "" && ln.method != r.Method {
		return false
	}
	if strings.HasSuffix(ln.pattern, "/") {
		return strings.HasPrefix(r.URL.Path, ln.pattern)
	}
	ok, _ := path.Match(ln.pattern, r.URL.Path)
	return ok
}

// handle takes one request of the agent: its payload (JSON, a form or the
// query) goes to the session's inbox and the configured reply goes back.
// A request outside Path only gets the reply.
func (ln *listener) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	var data any
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case mt == "multipart/form-data":
		if err := r.ParseMultipartForm(16 << 20); err == nil {
			data = formMap(r.MultipartForm.Value)
		}
	case len(raw) > 0:
		data = parseBody(raw, r.Header.Get("Content-Type"))
	case len(r.URL.Query()) > 0:
		data = formMap(r.URL.Query())
	}
	var s *session
	if ln.matches(r) {
		s = ln.route(data)
	}
	if s != nil {
		select {
		case s.inbox <- listened{Method: r.Method, Path: r.URL.Path, Data: data}:
		default:
			if s.run.Logger != nil {
				s.run.Logger.Warn("listener inbox full, request dropped", "what", "rest "+s.t.name, "path", r.URL.Path)
			}
		}
	}
	reply := ln.cfg.Reply
	if reply == nil {
		reply = map[string]any{"ok": true}
	}
	var body []byte
	var err error
	if s != nil {
		body, err = s.payload(reply, target.UserTurn{})
	} else {
		body, err = json.Marshal(reply)
	}
	if err != nil {
		body = []byte(`{"ok":true}`)
	}
	status := ln.cfg.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// listen joins the session to the target's listener.
func (s *session) listen(_ context.Context) error {
	ln, err := s.t.listener()
	if err != nil {
		return err
	}
	s.inbox = make(chan listened, 256)
	s.values["listen.url"] = ln.url
	ln.register(s)
	return nil
}

// listenTurn reads the agent's turn from the requests it makes to the
// listener: until one has content (then, with Idle, until nothing new
// arrives for that long), or until Timeout. Requests that arrived before the
// turn (a greeting) are read with it.
func (s *session) listenTurn(ctx context.Context) (target.AgentTurn, error) {
	l := s.t.cfg.Listen
	timeout := l.Timeout
	if timeout <= 0 {
		timeout = s.t.cfg.Timeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	agent := target.AgentTurn{}
	var payloads []any
	idle := time.NewTimer(l.Idle)
	idle.Stop()
	defer idle.Stop()
	got := false
	finish := func() (target.AgentTurn, error) {
		agent.Raw = payloads
		return agent, nil
	}
	for {
		var idleC <-chan time.Time
		if got && l.Idle > 0 {
			idleC = idle.C
		}
		select {
		case <-tctx.Done():
			if got {
				return finish()
			}
			if ctx.Err() != nil {
				return agent, ctx.Err()
			}
			return agent, fmt.Errorf("listen: no request from the agent within %s", timeout)
		case <-idleC:
			return finish()
		case m := <-s.inbox:
			payloads = append(payloads, m.Data)
			candidate := s.extractOne(m.Data)
			if !hasContent(candidate) {
				continue
			}
			if !got {
				got = true
				agent.FirstTokenAt = time.Now()
			}
			agent.Text += candidate.Text
			agent.ToolCalls = append(agent.ToolCalls, candidate.ToolCalls...)
			agent.Done = agent.Done || candidate.Done
			if l.Idle <= 0 || agent.Done {
				return finish()
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(l.Idle)
		}
	}
}
