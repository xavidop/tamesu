// Package a2a is the target for an agent that speaks the Agent2Agent
// protocol: JSON-RPC over HTTP with an agent card, message/send (one reply
// per request) or message/stream (server-sent events while the agent works),
// and tasks that go through submitted, working, input-required and a final
// state. The agent is the thing under test, so each user turn is one message
// and the agent's turn is what the task answered: its status message, the
// text of its artifacts, or its reply message.
package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/target"
)

// Defaults.
const (
	DefaultTimeout      = 60 * time.Second
	DefaultPollInterval = 500 * time.Millisecond
	// ProtocolVersion is the A2A version the client speaks.
	ProtocolVersion = "0.3.0"
)

// CardPaths are the well-known locations of the agent card, tried in order
// at the URL's origin and then under the URL itself.
var CardPaths = []string{"/.well-known/agent-card.json", "/.well-known/agent.json"}

// Task states.
const (
	StateSubmitted     = "submitted"
	StateWorking       = "working"
	StateInputRequired = "input-required"
	StateCompleted     = "completed"
	StateCanceled      = "canceled"
	StateFailed        = "failed"
	StateRejected      = "rejected"
	StateAuthRequired  = "auth-required"
)

// EndStates are the states after which the agent will not continue the task;
// failed, canceled and rejected also end the session (completed does not: a
// conversation goes on in the same context with a new task).
var EndStates = map[string]bool{StateCompleted: true, StateCanceled: true, StateFailed: true, StateRejected: true}

// Config describes an A2A agent.
type Config struct {
	// URL is the agent's JSON-RPC endpoint; the card is looked for at the
	// URL's origin and under the URL.
	URL string
	// Headers go on every request (Authorization: Bearer ...); templates
	// render with the run's scope.
	Headers map[string]string
	// Stream uses message/stream (server-sent events) instead of
	// message/send; it falls back to message/send when the card says the
	// agent does not stream.
	Stream bool
	// Skill is a skill id the card must offer; Start fails otherwise.
	Skill string
	// Timeout bounds one turn, including the wait for a task that is still
	// working; zero means DefaultTimeout.
	Timeout time.Duration
	// PollInterval is how often a working task is read with tasks/get after
	// a blocking send that did not finish it; zero means DefaultPollInterval.
	PollInterval time.Duration
	// Metadata goes on every message (tenant, locale...).
	Metadata map[string]any
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how a request that never reached the agent is retried.
	Connect target.ConnectPolicy
}

// Card is the agent card, as far as the target reads it.
type Card struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	URL                string   `json:"url"`
	Version            string   `json:"version"`
	ProtocolVersion    string   `json:"protocolVersion"`
	DefaultInputModes  []string `json:"defaultInputModes"`
	DefaultOutputModes []string `json:"defaultOutputModes"`
	Capabilities       struct {
		Streaming         bool `json:"streaming"`
		PushNotifications bool `json:"pushNotifications"`
	} `json:"capabilities"`
	Skills []Skill `json:"skills"`
}

// Skill is one skill of the card.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples"`
}

// Protocol types, as far as the target needs them.

type part struct {
	Kind     string         `json:"kind"`
	Text     string         `json:"text,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	File     map[string]any `json:"file,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type message struct {
	Kind      string         `json:"kind"`
	MessageID string         `json:"messageId"`
	Role      string         `json:"role"`
	Parts     []part         `json:"parts"`
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type taskStatus struct {
	State   string   `json:"state"`
	Message *message `json:"message,omitempty"`
}

type artifact struct {
	ID    string `json:"artifactId"`
	Name  string `json:"name,omitempty"`
	Parts []part `json:"parts"`
}

type task struct {
	Kind      string     `json:"kind"`
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    taskStatus `json:"status"`
	Artifacts []artifact `json:"artifacts,omitempty"`
	History   []message  `json:"history,omitempty"`
}

// event is one streamed event, or a send result: a task, a message, a
// status-update or an artifact-update, told apart by kind.
type event struct {
	Kind      string     `json:"kind"`
	ID        string     `json:"id,omitempty"`
	TaskID    string     `json:"taskId,omitempty"`
	ContextID string     `json:"contextId,omitempty"`
	Status    taskStatus `json:"status"`
	Final     bool       `json:"final,omitempty"`
	Artifact  *artifact  `json:"artifact,omitempty"`
	Append    bool       `json:"append,omitempty"`
	Artifacts []artifact `json:"artifacts,omitempty"`
	History   []message  `json:"history,omitempty"`
	// message fields
	MessageID string `json:"messageId,omitempty"`
	Role      string `json:"role,omitempty"`
	Parts     []part `json:"parts,omitempty"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the A2A target.
type Target struct {
	name string
	cfg  Config
	mu   sync.Mutex
	card *Card
	// cardErr is why the card could not be read, once.
	cardErr  error
	cardDone bool
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// Start opens a session: the headers are rendered, the agent card is read
// (once per target) and the skill checked.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	s := &session{t: t, run: run, seen: map[string]bool{}}
	var err error
	if s.url, err = run.Render(t.cfg.URL); err != nil {
		return nil, fmt.Errorf("a2a %s: url: %w", t.name, err)
	}
	if s.url == "" {
		return nil, fmt.Errorf("a2a %s: url is empty", t.name)
	}
	s.hdr = http.Header{}
	s.hdr.Set("Content-Type", "application/json")
	s.hdr.Set("Accept", "application/json")
	for k, v := range t.cfg.Headers {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("a2a %s: header %s: %w", t.name, k, err)
		}
		s.hdr.Set(k, rv)
	}
	card, cardErr := t.readCard(ctx, s)
	s.card = card
	s.stream = t.cfg.Stream
	switch {
	case card != nil:
		names := make([]string, 0, len(card.Skills))
		for _, sk := range card.Skills {
			names = append(names, sk.ID)
		}
		s.events = append(s.events, target.Event{At: time.Now(), Kind: "card", Detail: fmt.Sprintf("%s %s: %s", card.Name, card.Version, strings.Join(names, ", "))})
		if t.cfg.Skill != "" && !hasSkill(card, t.cfg.Skill) {
			return nil, fmt.Errorf("a2a %s: the agent card does not offer skill %q (has %s)", t.name, t.cfg.Skill, strings.Join(names, ", "))
		}
		if s.stream && !card.Capabilities.Streaming {
			s.stream = false
			s.events = append(s.events, target.Event{At: time.Now(), Kind: "no_streaming", Detail: "the card says the agent does not stream; using message/send"})
		}
	case cardErr != nil:
		if t.cfg.Skill != "" {
			return nil, fmt.Errorf("a2a %s: skill %q cannot be checked: %w", t.name, t.cfg.Skill, cardErr)
		}
		s.events = append(s.events, target.Event{At: time.Now(), Kind: "card_missing", Detail: cardErr.Error()})
		if run.Logger != nil {
			run.Logger.Warn("agent card not found", "what", "a2a "+t.name, "err", cardErr)
		}
	}
	return s, nil
}

func hasSkill(card *Card, id string) bool {
	for _, sk := range card.Skills {
		if sk.ID == id || sk.Name == id {
			return true
		}
	}
	return false
}

// readCard fetches the agent card once per target, from the well-known
// paths at the URL's origin and then under the URL.
func (t *Target) readCard(ctx context.Context, s *session) (*Card, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cardDone {
		return t.card, t.cardErr
	}
	t.cardDone = true
	u, err := url.Parse(s.url)
	if err != nil {
		t.cardErr = err
		return nil, err
	}
	var candidates []string
	origin := u.Scheme + "://" + u.Host
	for _, p := range CardPaths {
		candidates = append(candidates, origin+p)
	}
	if strings.Trim(u.Path, "/") != "" {
		base := strings.TrimRight(s.url, "/")
		for _, p := range CardPaths {
			candidates = append(candidates, base+p)
		}
	}
	var errs []string
	for _, c := range candidates {
		card, err := s.fetchCard(ctx, c)
		if err == nil {
			t.card = card
			return card, nil
		}
		errs = append(errs, err.Error())
	}
	t.cardErr = fmt.Errorf("no agent card at %s", strings.Join(errs, "; "))
	return nil, t.cardErr
}

func (s *session) fetchCard(ctx context.Context, u string) (*Card, error) {
	rctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range s.hdr {
		if k != "Content-Type" {
			req.Header[k] = v
		}
	}
	resp, err := s.t.cfg.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	var card Card
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&card); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	if card.Name == "" && len(card.Skills) == 0 {
		return nil, fmt.Errorf("%s: not an agent card", u)
	}
	return &card, nil
}

type session struct {
	t      *Target
	run    *target.RunContext
	url    string
	hdr    http.Header
	card   *Card
	stream bool
	// contextID ties the session's tasks together; taskID is the task that
	// is waiting for input, when one is.
	contextID string
	taskID    string
	// seen are the artifact ids whose text was already read.
	seen    map[string]bool
	pending *target.AgentTurn
	// ended is set when a task failed, was canceled or rejected: the agent
	// gave up on the conversation.
	ended  bool
	closed bool
	turns  int
	tasks  int
	events []target.Event
	calls  int
}

// Send delivers a user turn as one message and reads the agent's turn.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	if s.closed {
		return errors.New("a2a: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := message{Kind: "message", MessageID: uuid.NewString(), Role: "user", Parts: []part{{Kind: "text", Text: turn.Text}}, TaskID: s.taskID, ContextID: s.contextID, Metadata: s.t.cfg.Metadata}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	started := time.Now()
	var agent target.AgentTurn
	var err error
	if s.stream {
		agent, err = s.sendStream(tctx, msg)
	} else {
		agent, err = s.sendBlocking(tctx, msg)
	}
	if err != nil {
		return fmt.Errorf("a2a: %w", err)
	}
	agent.StartedAt = started
	agent.EndedAt = time.Now()
	if agent.FirstTokenAt.IsZero() {
		agent.FirstTokenAt = agent.EndedAt
	}
	agent.Status = http.StatusOK
	s.turns++
	s.pending = &agent
	return nil
}

// call performs one JSON-RPC request and returns the raw result.
func (s *session) call(ctx context.Context, method string, params any, accept string) (*http.Response, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: uuid.NewString(), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	s.calls++
	return target.Connect(ctx, s.t.cfg.Connect, s.run.Logger, "a2a "+s.t.name, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
		if err != nil {
			return nil, target.NoRetry(err)
		}
		req.Header = s.hdr.Clone()
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := s.t.cfg.Client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return resp, nil
	})
}

// callJSON performs a JSON-RPC request with a JSON result.
func (s *session) callJSON(ctx context.Context, method string, params any) (json.RawMessage, error) {
	resp, err := s.call(ctx, method, params, "application/json")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, excerpt(raw))
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: not a JSON-RPC response: %s", method, excerpt(raw))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s: %w", method, out.Error)
	}
	return out.Result, nil
}

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
}

// sendBlocking uses message/send with blocking on, then reads a task that
// is still working with tasks/get until it settles.
func (s *session) sendBlocking(ctx context.Context, msg message) (target.AgentTurn, error) {
	blocking := true
	params := map[string]any{"message": msg, "configuration": map[string]any{"blocking": &blocking, "acceptedOutputModes": []string{"text/plain", "text/markdown", "application/json"}}}
	result, err := s.callJSON(ctx, "message/send", params)
	if err != nil {
		return target.AgentTurn{}, err
	}
	var ev event
	if err := json.Unmarshal(result, &ev); err != nil {
		return target.AgentTurn{}, fmt.Errorf("message/send: unexpected result: %s", excerpt(result))
	}
	agent := target.AgentTurn{}
	var rawResult any
	_ = json.Unmarshal(result, &rawResult)
	s.absorb(&ev, &agent, msg.MessageID)
	if ev.Kind == "task" && !EndStates[ev.Status.State] && ev.Status.State != StateInputRequired && ev.Status.State != StateAuthRequired {
		// The agent did not block: wait for the task to settle.
		for {
			if !target.Sleep(ctx, s.t.cfg.PollInterval) {
				return agent, fmt.Errorf("the task did not settle within %s (state %s)", s.t.cfg.Timeout, ev.Status.State)
			}
			result, err = s.callJSON(ctx, "tasks/get", map[string]any{"id": ev.ID})
			if err != nil {
				return agent, err
			}
			var got event
			if err := json.Unmarshal(result, &got); err != nil {
				return agent, fmt.Errorf("tasks/get: unexpected result: %s", excerpt(result))
			}
			_ = json.Unmarshal(result, &rawResult)
			agent.Text = ""
			s.absorbTask(&got, &agent, msg.MessageID)
			if EndStates[got.Status.State] || got.Status.State == StateInputRequired || got.Status.State == StateAuthRequired {
				break
			}
		}
	}
	agent.Raw = rawResult
	agent.Done = s.ended
	return agent, nil
}

// absorb reads one send result or streamed event into the turn and the
// session's task state.
func (s *session) absorb(ev *event, agent *target.AgentTurn, ourMessageID string) {
	switch ev.Kind {
	case "task":
		s.absorbTask(ev, agent, ourMessageID)
	case "message":
		if ev.ContextID != "" {
			s.contextID = ev.ContextID
		}
		if ev.TaskID != "" {
			s.taskID = ev.TaskID
		}
		agent.Text += partsText(ev.Parts)
	case "status-update":
		s.track(ev.TaskID, ev.ContextID, ev.Status.State)
		if ev.Status.Message != nil {
			agent.Text += partsText(ev.Status.Message.Parts)
		}
	case "artifact-update":
		if ev.Artifact != nil {
			agent.Text += partsText(ev.Artifact.Parts)
		}
	}
}

// absorbTask reads a whole task: its status message, the artifacts not read
// before and, when those say nothing, the agent's messages after ours.
func (s *session) absorbTask(ev *event, agent *target.AgentTurn, ourMessageID string) {
	if s.taskID != ev.ID {
		s.tasks++
	}
	s.track(ev.ID, ev.ContextID, ev.Status.State)
	var text strings.Builder
	if ev.Status.Message != nil && ev.Status.Message.Role != "user" {
		text.WriteString(partsText(ev.Status.Message.Parts))
	}
	for _, a := range ev.Artifacts {
		key := ev.ID + "/" + a.ID
		if s.seen[key] {
			continue
		}
		s.seen[key] = true
		text.WriteString(partsText(a.Parts))
	}
	if text.Len() == 0 {
		after := ourMessageID == ""
		for _, m := range ev.History {
			if m.MessageID == ourMessageID {
				after = true
				continue
			}
			if after && m.Role == "agent" {
				text.WriteString(partsText(m.Parts))
			}
		}
	}
	agent.Text += text.String()
}

// track records the task's state and decides what the next message refers
// to: the task when it waits for input, a new task in the same context
// otherwise.
func (s *session) track(taskID, contextID, state string) {
	if contextID != "" {
		s.contextID = contextID
	}
	if taskID != "" && (s.taskID != taskID || state != "") {
		s.events = append(s.events, target.Event{At: time.Now(), Kind: "task_" + strings.ReplaceAll(state, "-", "_"), Detail: taskID})
	}
	switch state {
	case StateInputRequired, StateAuthRequired, StateWorking, StateSubmitted:
		s.taskID = taskID
	case StateCompleted, StateCanceled, StateFailed, StateRejected:
		s.taskID = ""
		if state != StateCompleted {
			s.ended = true
		}
	}
}

// sendStream uses message/stream and reads events until the final one.
func (s *session) sendStream(ctx context.Context, msg message) (target.AgentTurn, error) {
	params := map[string]any{"message": msg, "configuration": map[string]any{"acceptedOutputModes": []string{"text/plain", "text/markdown", "application/json"}}}
	resp, err := s.call(ctx, "message/stream", params, "text/event-stream")
	if err != nil {
		return target.AgentTurn{}, fmt.Errorf("message/stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return target.AgentTurn{}, fmt.Errorf("message/stream: HTTP %d: %s", resp.StatusCode, excerpt(raw))
	}
	agent := target.AgentTurn{}
	var events []any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 32<<20)
	var data strings.Builder
	final := false
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return nil
		}
		var out rpcResponse
		if err := json.Unmarshal([]byte(payload), &out); err != nil {
			return fmt.Errorf("message/stream: not a JSON-RPC event: %s", excerpt([]byte(payload)))
		}
		if out.Error != nil {
			return fmt.Errorf("message/stream: %w", out.Error)
		}
		var ev event
		if err := json.Unmarshal(out.Result, &ev); err != nil {
			return fmt.Errorf("message/stream: unexpected event: %s", excerpt(out.Result))
		}
		var raw any
		_ = json.Unmarshal(out.Result, &raw)
		events = append(events, raw)
		before := len(agent.Text)
		s.absorb(&ev, &agent, msg.MessageID)
		if len(agent.Text) > before && agent.FirstTokenAt.IsZero() {
			agent.FirstTokenAt = time.Now()
		}
		if ev.Kind == "message" || (ev.Kind == "status-update" && ev.Final) || (ev.Kind == "task" && (EndStates[ev.Status.State] || ev.Status.State == StateInputRequired)) {
			final = true
		}
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return agent, err
			}
			if final {
				agent.Raw = events
				agent.Done = s.ended
				return agent, nil
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := flush(); err != nil {
		return agent, err
	}
	if err := sc.Err(); err != nil && !final {
		if ctx.Err() != nil {
			return agent, fmt.Errorf("message/stream: no final event within %s: %w", s.t.cfg.Timeout, ctx.Err())
		}
		return agent, fmt.Errorf("message/stream: %w", err)
	}
	agent.Raw = events
	agent.Done = s.ended
	return agent, nil
}

// partsText joins the text parts; data parts are rendered as JSON so a judge
// can read them.
func partsText(parts []part) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Kind {
		case "text":
			b.WriteString(p.Text)
		case "data":
			if len(p.Data) > 0 {
				if j, err := json.Marshal(p.Data); err == nil {
					b.Write(j)
				}
			}
		}
	}
	return b.String()
}

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("a2a: Receive without a pending Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close ends the session: a task still waiting for input is cancelled.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	if s.closed {
		return target.Artifacts{}, nil
	}
	s.closed = true
	if s.taskID != "" {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if _, err := s.callJSON(cctx, "tasks/cancel", map[string]any{"id": s.taskID}); err == nil {
			s.events = append(s.events, target.Event{At: time.Now(), Kind: "task_canceled", Detail: s.taskID + " (by tamesu at close)"})
		}
		cancel()
	}
	metrics := map[string]float64{"a2a_tasks": float64(s.tasks), "a2a_requests": float64(s.calls)}
	if s.card != nil {
		metrics["a2a_skills"] = float64(len(s.card.Skills))
	}
	return target.Artifacts{Metrics: metrics, Events: s.events}, nil
}
