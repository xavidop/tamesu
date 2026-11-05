// Package a2a is the target for an agent that speaks the Agent2Agent
// protocol: an agent card, message/send (one reply per request) or
// message/stream (server-sent events while the agent works), and tasks that
// go through submitted, working, input-required and a final state, over any
// of the protocol's bindings (JSON-RPC, HTTP+JSON, gRPC). The agent is the
// thing under test, so each user turn is one message and the agent's turn is
// what the task answered: its status message, the text of its artifacts, or
// its reply message.
package a2a

import (
	"context"
	"encoding/base64"
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
	// pushSafetyPolls is how many poll intervals pass between tasks/get
	// safety reads while waiting for a push notification.
	pushSafetyPolls = 10
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

// settled says whether a task state is one the agent will not change on its
// own: an end state or a wait for the user.
func settled(state string) bool {
	return EndStates[state] || state == StateInputRequired || state == StateAuthRequired
}

// Config describes an A2A agent.
type Config struct {
	// URL is the agent's endpoint: the JSON-RPC URL, the base of the HTTP+JSON
	// binding or grpc://host:port. The card is looked for at the URL's origin
	// and under the URL (or read with GetAgentCard over gRPC).
	URL string
	// Transport is the binding: jsonrpc, http or grpc. Empty picks the card's
	// preferred transport when the card names this URL, grpc for a grpc://
	// URL, else jsonrpc. A transport the card lists under another URL is
	// reached at that URL.
	Transport string
	// Headers go on every request (Authorization: Bearer ...); templates
	// render with the run's scope. Over gRPC they are metadata.
	Headers map[string]string
	// Stream uses message/stream (server-sent events) instead of
	// message/send; it falls back to message/send when the card says the
	// agent does not stream.
	Stream bool
	// Push receives a working task's updates as push notifications on a
	// local listener instead of polling tasks/get; nil means off. It falls
	// back to polling when the card says the agent does not push.
	Push *Push
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
	// Connect is how a request that never reached the agent is retried, and
	// how many times a dropped stream is resubscribed.
	Connect target.ConnectPolicy
}

// Card is the agent card, as far as the target reads it.
type Card struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	URL                string   `json:"url"`
	Version            string   `json:"version"`
	ProtocolVersion    string   `json:"protocolVersion"`
	PreferredTransport string   `json:"preferredTransport"`
	DefaultInputModes  []string `json:"defaultInputModes"`
	DefaultOutputModes []string `json:"defaultOutputModes"`
	Capabilities       struct {
		Streaming         bool `json:"streaming"`
		PushNotifications bool `json:"pushNotifications"`
	} `json:"capabilities"`
	AdditionalInterfaces              []Interface           `json:"additionalInterfaces"`
	Security                          []map[string][]string `json:"security"`
	SupportsAuthenticatedExtendedCard bool                  `json:"supportsAuthenticatedExtendedCard"`
	Skills                            []Skill               `json:"skills"`
}

// Interface is one way to reach the agent, as the card lists it.
type Interface struct {
	Transport string `json:"transport"`
	URL       string `json:"url"`
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
	// extended is set once the authenticated extended card was read.
	extended bool
	push     *pushListener
	pushErr  error
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// Shutdown stops the push listener; sessions must be closed first.
func (t *Target) Shutdown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.push != nil {
		t.push.close()
		t.push = nil
	}
}

// PushURL is the base URL the agent posts notifications to, once a session
// with push on has started; empty otherwise.
func (t *Target) PushURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.push == nil {
		return ""
	}
	return t.push.url
}

// Start opens a session: the headers are rendered, the agent card is read
// (once per target), the transport chosen and the skill checked.
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
		if strings.EqualFold(k, "Authorization") || strings.HasPrefix(strings.ToLower(k), "x-api-key") {
			s.authed = true
		}
	}
	if !s.authed && len(t.cfg.Headers) > 0 {
		s.authed = true
	}
	kind := t.cfg.Transport
	if kind == "" && isGRPCURL(s.url) {
		kind = TransportGRPC
	}
	// A gRPC agent publishes its card with GetAgentCard; an HTTP URL has it
	// at the well-known paths whatever transport the messages take.
	cardKind := TransportJSONRPC
	if isGRPCURL(s.url) || (kind == TransportGRPC && !strings.HasPrefix(s.url, "http")) {
		cardKind = TransportGRPC
	}
	card, cardErr := t.readCard(ctx, s, cardKind)
	s.card = card
	kind, endpoint := chooseTransport(kind, s.url, card)
	if s.tr, err = s.newTransport(kind, endpoint); err != nil {
		return nil, fmt.Errorf("a2a %s: %w", t.name, err)
	}
	s.stream = t.cfg.Stream
	switch {
	case card != nil:
		if card.SupportsAuthenticatedExtendedCard && s.authed {
			if ext, err := t.readExtendedCard(ctx, s); err == nil {
				s.card = ext
				card = ext
			} else if run.Logger != nil {
				run.Logger.Warn("the authenticated extended card could not be read", "what", "a2a "+t.name, "err", err)
			}
		}
		names := make([]string, 0, len(card.Skills))
		for _, sk := range card.Skills {
			names = append(names, sk.ID)
		}
		s.event("card", fmt.Sprintf("%s %s: %s", card.Name, card.Version, strings.Join(names, ", ")))
		if t.cfg.Skill != "" && !hasSkill(card, t.cfg.Skill) {
			s.tr.close()
			return nil, fmt.Errorf("a2a %s: the agent card does not offer skill %q (has %s)", t.name, t.cfg.Skill, strings.Join(names, ", "))
		}
		if len(card.Security) > 0 && !s.authed {
			s.event("auth_missing", "the card requires credentials and the target sends none")
			if run.Logger != nil {
				run.Logger.Warn("the agent card requires credentials and the target sends none", "what", "a2a "+t.name)
			}
		}
		if s.stream && !card.Capabilities.Streaming {
			s.stream = false
			s.event("no_streaming", "the card says the agent does not stream; using message/send")
		}
	case cardErr != nil:
		if t.cfg.Skill != "" {
			s.tr.close()
			return nil, fmt.Errorf("a2a %s: skill %q cannot be checked: %w", t.name, t.cfg.Skill, cardErr)
		}
		s.event("card_missing", cardErr.Error())
		if run.Logger != nil {
			run.Logger.Warn("agent card not found", "what", "a2a "+t.name, "err", cardErr)
		}
	}
	s.event("transport", kind+" at "+endpoint)
	if t.cfg.Push != nil && !s.stream {
		switch {
		case card != nil && !card.Capabilities.PushNotifications:
			s.event("no_push", "the card says the agent does not push; polling tasks/get")
		default:
			if err := s.subscribePush(); err != nil {
				s.tr.close()
				return nil, fmt.Errorf("a2a %s: %w", t.name, err)
			}
		}
	}
	return s, nil
}

// chooseTransport decides the binding and its endpoint: the configured one
// (at the card's URL for it when the card lists one), else the card's
// preferred transport when the card is about this URL, else JSON-RPC.
func chooseTransport(configured, u string, card *Card) (kind, endpoint string) {
	if card == nil {
		if configured == "" {
			configured = TransportJSONRPC
		}
		return configured, u
	}
	preferred := cardTransports[card.PreferredTransport]
	if preferred == "" {
		preferred = TransportJSONRPC
	}
	if configured == "" {
		if sameURL(card.URL, u) {
			return preferred, u
		}
		for _, in := range card.AdditionalInterfaces {
			if sameURL(in.URL, u) && cardTransports[in.Transport] != "" {
				return cardTransports[in.Transport], u
			}
		}
		return TransportJSONRPC, u
	}
	if preferred == configured && (sameURL(card.URL, u) || card.URL == "") {
		return configured, u
	}
	if preferred == configured && card.URL != "" {
		return configured, card.URL
	}
	for _, in := range card.AdditionalInterfaces {
		if cardTransports[in.Transport] == configured && in.URL != "" {
			return configured, in.URL
		}
	}
	return configured, u
}

func sameURL(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimRight(s, "/")
		for _, p := range []string{"grpc://", "grpcs://", "https://", "http://"} {
			s = strings.TrimPrefix(s, p)
		}
		return s
	}
	return a != "" && norm(a) == norm(b)
}

func (s *session) newTransport(kind, endpoint string) (transport, error) {
	switch kind {
	case TransportGRPC:
		return newGRPCTransport(s, endpoint)
	case TransportHTTP:
		return newHTTPTransport(s, endpoint), nil
	case TransportJSONRPC, "":
		return &jsonrpcTransport{s: s, url: endpoint}, nil
	}
	return nil, fmt.Errorf("unknown transport %q (one of %s)", kind, strings.Join(Transports, ", "))
}

func hasSkill(card *Card, id string) bool {
	for _, sk := range card.Skills {
		if sk.ID == id || sk.Name == id {
			return true
		}
	}
	return false
}

// readCard fetches the agent card once per target: with GetAgentCard over
// gRPC, else from the well-known paths at the URL's origin and then under
// the URL.
func (t *Target) readCard(ctx context.Context, s *session, kind string) (*Card, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cardDone {
		return t.card, t.cardErr
	}
	t.cardDone = true
	if kind == TransportGRPC {
		g, err := newGRPCTransport(s, s.url)
		if err != nil {
			t.cardErr = err
			return nil, err
		}
		defer g.close()
		card, err := g.card(ctx)
		if err != nil {
			t.cardErr = fmt.Errorf("no agent card: %w", err)
			return nil, t.cardErr
		}
		t.card = card
		return card, nil
	}
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

// readExtendedCard fetches the authenticated extended card once per target.
func (t *Target) readExtendedCard(ctx context.Context, s *session) (*Card, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.extended {
		return t.card, nil
	}
	ext, err := s.tr.extendedCard(ctx)
	if err != nil {
		return nil, err
	}
	t.extended = true
	added := len(ext.Skills) - len(t.card.Skills)
	t.card = ext
	s.event("card_extended", fmt.Sprintf("%d skills (%+d)", len(ext.Skills), added))
	return ext, nil
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	card, err := parseCard(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return card, nil
}

type session struct {
	t      *Target
	run    *target.RunContext
	url    string
	hdr    http.Header
	authed bool
	card   *Card
	tr     transport
	stream bool
	// push is the channel notifications arrive on, when push is on.
	push      <-chan json.RawMessage
	pushURL   string
	pushToken string
	pushes    int
	// contextID ties the session's tasks together; taskID is the task that
	// is waiting for input, when one is.
	contextID string
	taskID    string
	// seen are the artifact ids whose text was already read.
	seen    map[string]bool
	pending *target.AgentTurn
	// ended is set when a task failed, was canceled or rejected: the agent
	// gave up on the conversation.
	ended        bool
	closed       bool
	turns        int
	tasks        int
	resubscribes int
	events       []target.Event
	calls        int
}

func (s *session) event(kind, detail string) {
	s.events = append(s.events, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// subscribePush starts the target's push listener if needed and subscribes
// this session.
func (s *session) subscribePush() error {
	t := s.t
	t.mu.Lock()
	if t.push == nil && t.pushErr == nil {
		t.push, t.pushErr = startPush(*t.cfg.Push)
	}
	p, err := t.push, t.pushErr
	t.mu.Unlock()
	if err != nil {
		return err
	}
	s.pushToken = uuid.NewString()
	s.push, s.pushURL = p.subscribe(s.pushToken)
	return nil
}

// parts builds the message parts of a user turn: its words, its data and its
// files (bytes inline, or a URI the agent fetches).
func parts(turn target.UserTurn) []part {
	var out []part
	if turn.Text != "" {
		out = append(out, part{Kind: "text", Text: turn.Text})
	}
	if len(turn.Data) > 0 {
		out = append(out, part{Kind: "data", Data: turn.Data})
	}
	for _, f := range turn.Files {
		file := map[string]any{}
		if f.Name != "" {
			file["name"] = f.Name
		}
		if f.MIME != "" {
			file["mimeType"] = f.MIME
		}
		if f.URI != "" {
			file["uri"] = f.URI
		} else {
			file["bytes"] = base64.StdEncoding.EncodeToString(f.Bytes)
		}
		out = append(out, part{Kind: "file", File: file})
	}
	if out == nil {
		out = []part{{Kind: "text", Text: ""}}
	}
	return out
}

// Send delivers a user turn as one message and reads the agent's turn.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	if s.closed {
		return errors.New("a2a: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := message{Kind: "message", MessageID: uuid.NewString(), Role: "user", Parts: parts(turn), TaskID: s.taskID, ContextID: s.contextID, Metadata: s.t.cfg.Metadata}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	started := time.Now()
	var agent target.AgentTurn
	var err error
	switch {
	case s.stream:
		agent, err = s.sendStream(tctx, msg)
	case s.push != nil:
		agent, err = s.sendPush(tctx, msg)
	default:
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

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
}

// readResult parses a send result or a task into an event.
func readResult(what string, raw json.RawMessage) (event, any, error) {
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, nil, fmt.Errorf("%s: unexpected result: %s", what, excerpt(raw))
	}
	var any any
	_ = json.Unmarshal(raw, &any)
	return ev, any, nil
}

// sendBlocking uses message/send with blocking on, then reads a task that
// is still working with tasks/get until it settles.
func (s *session) sendBlocking(ctx context.Context, msg message) (target.AgentTurn, error) {
	blocking := true
	result, err := s.tr.send(ctx, sendParams{Message: msg, Configuration: &sendConfig{Blocking: &blocking, AcceptedOutputModes: acceptedOutputModes}})
	if err != nil {
		return target.AgentTurn{}, err
	}
	ev, rawResult, err := readResult("message/send", result)
	if err != nil {
		return target.AgentTurn{}, err
	}
	agent := target.AgentTurn{}
	s.absorb(&ev, &agent, msg.MessageID)
	if ev.Kind == "task" && !settled(ev.Status.State) {
		// The agent did not block: wait for the task to settle.
		for {
			if !target.Sleep(ctx, s.t.cfg.PollInterval) {
				return agent, fmt.Errorf("the task did not settle within %s (state %s)", s.t.cfg.Timeout, ev.Status.State)
			}
			done, err := s.poll(ctx, ev.ID, &agent, msg.MessageID, &rawResult)
			if err != nil {
				return agent, err
			}
			if done {
				break
			}
		}
	}
	agent.Raw = rawResult
	agent.Done = s.ended
	return agent, nil
}

// poll reads a task once with tasks/get and absorbs it; done when it settled.
func (s *session) poll(ctx context.Context, id string, agent *target.AgentTurn, ourMessageID string, rawResult *any) (bool, error) {
	result, err := s.tr.get(ctx, id)
	if err != nil {
		return false, err
	}
	got, raw, err := readResult("tasks/get", result)
	if err != nil {
		return false, err
	}
	*rawResult = raw
	agent.Text = ""
	s.absorbTask(&got, agent, ourMessageID)
	return settled(got.Status.State), nil
}

// sendPush uses message/send without blocking and a push notification
// config, then waits for the agent to post the task's updates, reading the
// task with tasks/get now and then in case a notification was lost.
func (s *session) sendPush(ctx context.Context, msg message) (target.AgentTurn, error) {
	blocking := false
	cfg := &sendConfig{Blocking: &blocking, AcceptedOutputModes: acceptedOutputModes, Push: &pushConfig{URL: s.pushURL, Token: s.pushToken}}
	result, err := s.tr.send(ctx, sendParams{Message: msg, Configuration: cfg})
	if err != nil {
		return target.AgentTurn{}, err
	}
	ev, rawResult, err := readResult("message/send", result)
	if err != nil {
		return target.AgentTurn{}, err
	}
	agent := target.AgentTurn{}
	s.absorb(&ev, &agent, msg.MessageID)
	if ev.Kind != "task" || settled(ev.Status.State) {
		agent.Raw = rawResult
		agent.Done = s.ended
		return agent, nil
	}
	safety := time.NewTicker(s.t.cfg.PollInterval * pushSafetyPolls)
	defer safety.Stop()
	for {
		select {
		case <-ctx.Done():
			return agent, fmt.Errorf("no push notification settled the task within %s (state %s)", s.t.cfg.Timeout, ev.Status.State)
		case raw := <-s.push:
			s.pushes++
			got, r, err := readResult("push notification", raw)
			if err != nil {
				return agent, err
			}
			if got.Kind == "task" && got.ID != ev.ID {
				continue
			}
			if got.Kind == "status-update" && got.TaskID != ev.ID {
				continue
			}
			s.event("push_received", fmt.Sprintf("%s %s", got.ID+got.TaskID, got.Status.State))
			if agent.FirstTokenAt.IsZero() {
				agent.FirstTokenAt = time.Now()
			}
			if got.Kind == "task" {
				rawResult = r
				agent.Text = ""
				s.absorbTask(&got, &agent, msg.MessageID)
			} else {
				s.absorb(&got, &agent, msg.MessageID)
			}
			if settled(got.Status.State) {
				agent.Raw = rawResult
				agent.Done = s.ended
				return agent, nil
			}
		case <-safety.C:
			done, err := s.poll(ctx, ev.ID, &agent, msg.MessageID, &rawResult)
			if err != nil {
				return agent, err
			}
			if done {
				agent.Raw = rawResult
				agent.Done = s.ended
				return agent, nil
			}
		}
	}
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
		s.event("task_"+strings.ReplaceAll(state, "-", "_"), taskID)
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

// sendStream uses message/stream and reads events until the final one. A
// stream that ends early is picked up again with tasks/resubscribe, as many
// times as the connect policy allows.
func (s *session) sendStream(ctx context.Context, msg message) (target.AgentTurn, error) {
	es, err := s.tr.stream(ctx, sendParams{Message: msg, Configuration: &sendConfig{AcceptedOutputModes: acceptedOutputModes}})
	if err != nil {
		return target.AgentTurn{}, err
	}
	defer func() { es.close() }()
	agent := target.AgentTurn{}
	var raws []any
	taskID := ""
	attempts := 0
	maxAttempts := s.t.cfg.Connect.Attempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	for {
		raw, err := es.next()
		if err != nil {
			if ctx.Err() != nil {
				return agent, fmt.Errorf("message/stream: no final event within %s: %w", s.t.cfg.Timeout, ctx.Err())
			}
			// The stream ended without a final event: resubscribe to the task.
			if taskID == "" || attempts >= maxAttempts-1 {
				if errors.Is(err, io.EOF) {
					return agent, errors.New("message/stream: the stream ended without a final event")
				}
				return agent, fmt.Errorf("message/stream: %w", err)
			}
			attempts++
			es.close()
			if es, err = s.tr.resubscribe(ctx, taskID); err != nil {
				return agent, fmt.Errorf("tasks/resubscribe: %w", err)
			}
			s.resubscribes++
			s.event("resubscribed", fmt.Sprintf("%s (attempt %d)", taskID, attempts))
			continue
		}
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return agent, fmt.Errorf("message/stream: unexpected event: %s", excerpt(raw))
		}
		var r any
		_ = json.Unmarshal(raw, &r)
		raws = append(raws, r)
		if ev.Kind == "task" && ev.ID != "" {
			taskID = ev.ID
		} else if ev.TaskID != "" {
			taskID = ev.TaskID
		}
		before := len(agent.Text)
		s.absorb(&ev, &agent, msg.MessageID)
		if len(agent.Text) > before && agent.FirstTokenAt.IsZero() {
			agent.FirstTokenAt = time.Now()
		}
		if ev.Kind == "message" || (ev.Kind == "status-update" && ev.Final) || (ev.Kind == "task" && settled(ev.Status.State)) {
			agent.Raw = raws
			agent.Done = s.ended
			return agent, nil
		}
	}
}

// partsText joins the text parts; data parts are rendered as JSON and file
// parts as their name so a judge can read them.
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
		case "file":
			name, _ := p.File["name"].(string)
			if name == "" {
				name, _ = p.File["uri"].(string)
			}
			if name != "" {
				b.WriteString("[file " + name + "]")
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
		if _, err := s.tr.cancel(cctx, s.taskID); err == nil {
			s.event("task_canceled", s.taskID+" (by tamesu at close)")
		}
		cancel()
	}
	if s.pushToken != "" {
		s.t.mu.Lock()
		if s.t.push != nil {
			s.t.push.unsubscribe(s.pushToken)
		}
		s.t.mu.Unlock()
	}
	s.tr.close()
	metrics := map[string]float64{"a2a_tasks": float64(s.tasks), "a2a_requests": float64(s.calls)}
	if s.card != nil {
		metrics["a2a_skills"] = float64(len(s.card.Skills))
	}
	if s.pushToken != "" {
		metrics["a2a_pushes"] = float64(s.pushes)
	}
	if s.stream {
		metrics["a2a_resubscribes"] = float64(s.resubscribes)
	}
	return target.Artifacts{Metrics: metrics, Events: s.events}, nil
}
