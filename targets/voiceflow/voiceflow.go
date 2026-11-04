// Package voiceflow is the target for an agent built on Voiceflow: each user
// turn is one request to the Conversations API (launch, then text), and the
// agent's turn is read from the traces it answers: its text and speak
// messages, the buttons it offers, the tools it called, a handoff to a live
// agent, the end of the conversation. The REST API at
// realtime-api.voiceflow.com (a project, an environment and a personal or
// service token) is the default; the legacy Dialog Manager API (a version
// and a Dialog API key) is supported for projects still on it.
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

// Defaults.
const (
	DefaultBaseURL       = "https://realtime-api.voiceflow.com/v1/stable"
	DefaultLegacyBaseURL = "https://general-runtime.voiceflow.com"
	DefaultEnvironment   = "main"
	DefaultVersion       = "published"
	DefaultUserID        = "tamesu-{{uuid}}"
	DefaultTimeout       = 60 * time.Second
)

// Versions of an environment the REST API can run.
var Versions = []string{"draft", "published"}

// Config describes a Voiceflow agent.
type Config struct {
	// BaseURL is the API; empty means DefaultBaseURL (REST) or
	// DefaultLegacyBaseURL (Dialog Manager).
	BaseURL string
	// APIKey is a personal or service token (REST), or a Dialog API key
	// (legacy); {{secrets.NAME}}.
	APIKey string
	// ProjectID, Environment and Version address the agent on the REST API.
	ProjectID   string
	Environment string
	Version     string
	// VersionID addresses the agent on the legacy Dialog Manager API
	// (the versionID header: an id, development or production).
	VersionID string
	// UserID is the conversation id template; empty means DefaultUserID.
	UserID string
	// Variables are set on the conversation before the launch (templates
	// render).
	Variables map[string]string
	// Reset deletes the conversation's state before the launch, for a user
	// id that is reused.
	Reset bool
	// Timeout bounds one request; zero means DefaultTimeout.
	Timeout time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how a request that never reached the API is retried.
	Connect target.ConnectPolicy
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.BaseURL == "" {
		if cfg.VersionID != "" && cfg.ProjectID == "" {
			cfg.BaseURL = DefaultLegacyBaseURL
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

// Legacy is whether the target talks to the Dialog Manager API.
func (t *Target) Legacy() bool { return t.cfg.ProjectID == "" && t.cfg.VersionID != "" }

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
	versID   string
	pending  *target.AgentTurn
	greeting *target.AgentTurn
	closed   bool
	ended    bool
	turns    int
	requests int
	traces   int
	events   []target.Event
}

// Start renders the config, resets and primes the conversation when asked,
// and launches the agent; its opening words are read with the first turn.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	s := &session{t: t, run: run}
	var err error
	for _, f := range []struct {
		name string
		src  string
		dst  *string
	}{{"api_key", t.cfg.APIKey, &s.apiKey}, {"user_id", t.cfg.UserID, &s.userID}, {"project_id", t.cfg.ProjectID, &s.project}, {"version_id", t.cfg.VersionID, &s.versID}} {
		if *f.dst, err = run.Render(f.src); err != nil {
			return nil, fmt.Errorf("voiceflow %s: %s: %w", t.name, f.name, err)
		}
	}
	if s.apiKey == "" {
		return nil, fmt.Errorf("voiceflow %s: api_key is empty", t.name)
	}
	if s.project == "" && s.versID == "" {
		return nil, fmt.Errorf("voiceflow %s: project_id (REST API) or version_id (Dialog Manager API) is required", t.name)
	}
	vars := map[string]any{}
	for k, v := range t.cfg.Variables {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("voiceflow %s: variables.%s: %w", t.name, k, err)
		}
		vars[k] = rv
	}
	if t.cfg.Reset {
		if _, err := s.request(ctx, http.MethodDelete, s.stateURL(""), nil); err != nil {
			return nil, fmt.Errorf("voiceflow %s: reset: %w", t.name, err)
		}
		s.event("reset", s.userID)
	}
	launch := map[string]any{"type": "launch"}
	var body map[string]any
	if t.Legacy() {
		body = map[string]any{"action": launch, "config": map[string]any{"tts": false, "stripSSML": true}}
		if len(vars) > 0 {
			body["state"] = map[string]any{"variables": vars}
		}
	} else {
		if len(vars) > 0 {
			if _, err := s.request(ctx, http.MethodPatch, s.stateURL("/variables"), map[string]any{"variables": vars}); err != nil {
				return nil, fmt.Errorf("voiceflow %s: variables: %w", t.name, err)
			}
			s.event("variables", fmt.Sprintf("%d set", len(vars)))
		}
		body = map[string]any{"action": launch, "version": t.cfg.Version}
	}
	started := time.Now()
	traces, status, raw, err := s.interact(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("voiceflow %s: launch: %w", t.name, err)
	}
	s.event("launch", s.userID)
	greeting := s.absorb(traces)
	greeting.Raw = raw
	greeting.Status = status
	greeting.StartedAt = started
	greeting.EndedAt = time.Now()
	greeting.FirstTokenAt = greeting.EndedAt
	if greeting.Text != "" {
		s.event("greeting", greeting.Text)
	}
	s.greeting = &greeting
	return s, nil
}

// conversationURL is the REST interact URL, or the legacy one.
func (s *session) conversationURL() string {
	base := strings.TrimRight(s.t.cfg.BaseURL, "/")
	if s.t.Legacy() {
		return base + "/state/user/" + url.PathEscape(s.userID) + "/interact"
	}
	return fmt.Sprintf("%s/conversation/%s?%s", base, url.PathEscape(s.userID), s.query(false))
}

// stateURL is the REST conversation-state URL with a suffix, or the legacy
// state URL.
func (s *session) stateURL(suffix string) string {
	base := strings.TrimRight(s.t.cfg.BaseURL, "/")
	if s.t.Legacy() {
		return base + "/state/user/" + url.PathEscape(s.userID) + suffix
	}
	return fmt.Sprintf("%s/conversation-state/%s%s?%s", base, url.PathEscape(s.userID), suffix, s.query(true))
}

func (s *session) query(withVersion bool) string {
	q := url.Values{"projectID": {s.project}, "environmentAlias": {s.t.cfg.Environment}}
	if withVersion {
		q.Set("version", s.t.cfg.Version)
	}
	return q.Encode()
}

// request performs one API call and returns the status and body; a 4xx or
// 5xx other than a gateway error is a final error with the body.
func (s *session) request(ctx context.Context, method, u string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	s.requests++
	type result struct {
		status int
		body   []byte
	}
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
		if s.t.Legacy() {
			req.Header.Set("Authorization", s.apiKey)
			req.Header.Set("versionID", s.versID)
		} else {
			req.Header.Set("Authorization", "Bearer "+s.apiKey)
		}
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
		return result{status: resp.StatusCode, body: raw}, nil
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

// interact sends one action and parses the traces: the REST API wraps them
// as {traces: [...]}, the legacy API returns the array.
func (s *session) interact(ctx context.Context, body map[string]any) ([]trace, int, any, error) {
	method := http.MethodPut
	if s.t.Legacy() {
		method = http.MethodPost
	}
	raw, err := s.request(ctx, method, s.conversationURL(), body)
	if err != nil {
		return nil, 0, nil, err
	}
	// Strip control characters the runtime may leave in strings.
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\r' {
			return -1
		}
		return r
	}, string(raw))
	var decoded any
	if err := json.Unmarshal([]byte(clean), &decoded); err != nil {
		return nil, 0, nil, fmt.Errorf("unexpected response: %s", excerpt(raw))
	}
	var list []trace
	items := decoded
	if m, ok := decoded.(map[string]any); ok {
		items = m["traces"]
	}
	b, _ := json.Marshal(items)
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, 0, nil, fmt.Errorf("unexpected traces: %s", excerpt(raw))
	}
	rawTraces := items
	if rawTraces == nil {
		rawTraces = []any{}
	}
	return list, http.StatusOK, rawTraces, nil
}

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
		case "debug", "log", "block", "flow", "path", "reasoning", "knowledgeBase", "visual", "entity-filling", "goto", "channel-action":
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
	action := map[string]any{"type": "text", "payload": turn.Text}
	if turn.Text == "" && turn.DTMF != "" {
		action = map[string]any{"type": "dtmf", "payload": map[string]any{"digits": turn.DTMF}}
	}
	var body map[string]any
	if s.t.Legacy() {
		body = map[string]any{"action": action, "config": map[string]any{"tts": false, "stripSSML": true}}
	} else {
		body = map[string]any{"action": action, "version": s.t.cfg.Version}
	}
	started := time.Now()
	traces, status, raw, err := s.interact(ctx, body)
	if err != nil {
		return fmt.Errorf("voiceflow: %w", err)
	}
	agent := s.absorb(traces)
	agent.Raw = raw
	agent.Status = status
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

// Close ends the session; a conversation the agent did not end is left for
// the next test of the same user id to reset.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	if s.closed {
		return target.Artifacts{}, nil
	}
	s.closed = true
	metrics := map[string]float64{"http_requests": float64(s.requests), "voiceflow_traces": float64(s.traces)}
	return target.Artifacts{Metrics: metrics, Events: s.events}, nil
}

func (s *session) event(kind, detail string) {
	s.events = append(s.events, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}
