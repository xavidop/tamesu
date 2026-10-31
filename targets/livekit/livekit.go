// Package livekit is the LiveKit target: tamesu joins a room as a simulated
// caller and talks to whatever agent is in it (LiveKit Agents, Voiceflow,
// Pipecat, a custom participant). Text mode sends user turns as text streams
// on the data topic and reads the agent's replies from its transcription and
// data streams; the end of an agent turn comes from the agent's
// lk.agent.state attribute, with an idle timeout as the fallback. Audio mode
// lands with the audio pipeline.
package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/target"
)

// Modes.
const (
	ModeText  = "text"
	ModeAudio = "audio"
)

// Topics and attributes LiveKit Agents use; a custom agent can use the same.
const (
	// DefaultDataTopic is the text-input topic (user turns).
	DefaultDataTopic = "lk.chat"
	// TranscriptionTopic is where agents publish their own speech as text.
	TranscriptionTopic = "lk.transcription"
	// ToolCallsTopic is a data-packet topic a custom agent can use to report
	// tool calls as JSON {"name": ..., "args": {...}} (or a list of them).
	ToolCallsTopic = "tamesu.tool_calls"
	// AttrAgentState is the agent's state attribute: initializing, listening,
	// thinking, speaking.
	AttrAgentState = "lk.agent.state"
	// AttrTranscriptionFinal marks the last segment of an agent utterance.
	AttrTranscriptionFinal = "lk.transcription_final"
)

// Agent states.
const (
	StateInitializing = "initializing"
	StateListening    = "listening"
	StateThinking     = "thinking"
	StateSpeaking     = "speaking"
)

// Defaults.
const (
	DefaultJoinTimeout = 30 * time.Second
	DefaultTurnIdle    = 1500 * time.Millisecond
	DefaultTurnTimeout = 60 * time.Second
)

// Config describes a LiveKit agent. String fields are templates rendered with
// the run's scope ({{secrets.X}}, {{vars.x}}, {{uuid}}).
type Config struct {
	// URL is the LiveKit server (ws:// or wss://).
	URL       string
	APIKey    string
	APISecret string
	// Room is the room name template; empty means tamesu-{{uuid}}.
	Room string
	// AgentName requests an explicit agent dispatch when the room is created.
	AgentName string
	// AgentIdentityPrefix recognises the agent participant; empty accepts a
	// participant of kind agent, or any remote participant.
	AgentIdentityPrefix string
	// Mode is text or audio.
	Mode string
	// DataTopic is the text-input topic (default lk.chat).
	DataTopic string
	// ReplyTopics are the text-stream topics the agent answers on (default
	// lk.transcription and the data topic).
	ReplyTopics []string
	// JoinTimeout bounds the wait for the agent participant.
	JoinTimeout time.Duration
	// TurnIdle is how long after the last text the turn is complete when the
	// agent publishes no state.
	TurnIdle time.Duration
	// TurnTimeout bounds one Receive.
	TurnTimeout time.Duration
	// Identity is the simulated caller's identity; empty means tamesu-sim-<uuid>.
	Identity string

	// connect overrides the SDK connection (tests).
	connect connector
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Mode == "" {
		cfg.Mode = ModeText
	}
	if cfg.Room == "" {
		cfg.Room = "tamesu-{{uuid}}"
	}
	if cfg.DataTopic == "" {
		cfg.DataTopic = DefaultDataTopic
	}
	if len(cfg.ReplyTopics) == 0 {
		cfg.ReplyTopics = []string{TranscriptionTopic, cfg.DataTopic}
	}
	if cfg.JoinTimeout == 0 {
		cfg.JoinTimeout = DefaultJoinTimeout
	}
	if cfg.TurnIdle == 0 {
		cfg.TurnIdle = DefaultTurnIdle
	}
	if cfg.TurnTimeout == 0 {
		cfg.TurnTimeout = DefaultTurnTimeout
	}
	if cfg.connect == nil {
		cfg.connect = sdkConnect
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the LiveKit target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// participant is what the session knows about a remote participant.
type participant struct {
	Identity   string
	IsAgent    bool
	Attributes map[string]string
}

// room is the slice of a LiveKit room the session uses; the SDK and the
// test fake implement it.
type room interface {
	SendText(ctx context.Context, topic, text string) error
	Participants() []participant
	Disconnect()
}

// events are what the room reports to the session.
type events struct {
	onParticipantJoined func(p participant)
	onParticipantLeft   func(identity string)
	onText              func(identity, topic, text string, attrs map[string]string)
	onData              func(identity, topic string, payload []byte)
	onAttributes        func(identity string, changed map[string]string)
	onDisconnected      func()
}

// connectConfig is what a connector needs.
type connectConfig struct {
	URL, APIKey, APISecret string
	Room, Identity         string
	AgentName              string
	ReplyTopics            []string
}

type connector func(ctx context.Context, cfg connectConfig, ev events) (room, error)

// Start renders the config, joins the room and waits for the agent.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if t.cfg.Mode != ModeText {
		return nil, fmt.Errorf("livekit %s: mode %q is not available yet; audio mode lands with the audio pipeline", t.name, t.cfg.Mode)
	}
	cc := connectConfig{ReplyTopics: t.cfg.ReplyTopics, AgentName: t.cfg.AgentName}
	var err error
	for _, f := range []struct {
		name string
		src  string
		dst  *string
	}{{"url", t.cfg.URL, &cc.URL}, {"api_key", t.cfg.APIKey, &cc.APIKey}, {"api_secret", t.cfg.APISecret, &cc.APISecret}, {"room", t.cfg.Room, &cc.Room}} {
		if *f.dst, err = run.Render(f.src); err != nil {
			return nil, fmt.Errorf("livekit %s: %s: %w", t.name, f.name, err)
		}
	}
	if cc.URL == "" || cc.APIKey == "" || cc.APISecret == "" {
		return nil, fmt.Errorf("livekit %s: url, api_key and api_secret are required", t.name)
	}
	cc.Identity = t.cfg.Identity
	if cc.Identity == "" {
		cc.Identity = "tamesu-sim-" + uuid.NewString()
	}
	s := &session{t: t, run: run, identity: cc.Identity, roomName: cc.Room, changed: make(chan struct{}, 1), agentJoined: make(chan struct{}), agentLeft: make(chan struct{}), startedAt: time.Now()}
	ev := events{
		onParticipantJoined: s.participantJoined,
		onParticipantLeft:   s.participantLeft,
		onText:              s.text,
		onData:              s.data,
		onAttributes:        s.attributes,
		onDisconnected:      s.disconnected,
	}
	r, err := t.cfg.connect(ctx, cc, ev)
	if err != nil {
		return nil, fmt.Errorf("livekit %s: join room %s at %s: %w", t.name, cc.Room, cc.URL, err)
	}
	s.room = r
	s.event("joined", cc.Room)
	for _, p := range r.Participants() {
		s.participantJoined(p)
	}
	jctx, cancel := context.WithTimeout(ctx, t.cfg.JoinTimeout)
	defer cancel()
	select {
	case <-s.agentJoined:
	case <-jctx.Done():
		r.Disconnect()
		return nil, fmt.Errorf("livekit %s: no agent joined room %s within %s (agent_identity_prefix=%q, dispatch=%q); is the agent running and dispatched to this room?", t.name, cc.Room, t.cfg.JoinTimeout, t.cfg.AgentIdentityPrefix, t.cfg.AgentName)
	}
	s.metrics["agent_join_ms"] = float64(time.Since(s.startedAt).Microseconds()) / 1000
	return s, nil
}

// turn is the agent reply being assembled for one user turn.
type turn struct {
	sentAt   time.Time
	text     strings.Builder
	firstAt  time.Time
	lastAt   time.Time
	final    bool
	spoke    bool // the agent went through thinking/speaking since the user turn
	tools    []target.ToolCall
	segments map[string]bool
}

type session struct {
	t        *Target
	run      *target.RunContext
	room     room
	identity string
	roomName string

	mu          sync.Mutex
	agentID     string
	agentState  string
	agentJoined chan struct{}
	agentLeft   chan struct{}
	joinedOnce  sync.Once
	leftOnce    sync.Once
	cur         *turn
	greeting    strings.Builder // agent text before the first user turn
	changed     chan struct{}
	events      []target.Event
	metrics     map[string]float64
	startedAt   time.Time
	closed      bool
}

func (s *session) isAgent(p participant) bool {
	if p.Identity == s.identity {
		return false
	}
	if s.t.cfg.AgentIdentityPrefix != "" {
		return strings.HasPrefix(p.Identity, s.t.cfg.AgentIdentityPrefix)
	}
	return true
}

func (s *session) participantJoined(p participant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentID != "" || !s.isAgent(p) {
		return
	}
	// Prefer a participant of kind agent when several are present.
	if !p.IsAgent && s.t.cfg.AgentIdentityPrefix == "" {
		for _, other := range s.room.Participants() {
			if other.IsAgent && other.Identity != s.identity {
				p = other
				break
			}
		}
	}
	s.agentID = p.Identity
	if st, ok := p.Attributes[AttrAgentState]; ok {
		s.agentState = st
	}
	s.eventLocked("agent_joined", p.Identity)
	s.joinedOnce.Do(func() { close(s.agentJoined) })
	s.signal()
}

func (s *session) participantLeft(identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != s.agentID {
		return
	}
	s.eventLocked("agent_left", identity)
	s.leftOnce.Do(func() { close(s.agentLeft) })
	s.signal()
}

func (s *session) disconnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventLocked("disconnected", s.roomName)
	s.leftOnce.Do(func() { close(s.agentLeft) })
	s.signal()
}

func (s *session) text(identity, topic, text string, attrs map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != s.agentID || text == "" {
		return
	}
	if s.cur == nil {
		s.greeting.WriteString(text)
		return
	}
	// A transcription segment may be re-sent as it grows; keep one copy per id.
	if id := attrs["lk.segment_id"]; id != "" {
		if s.cur.segments == nil {
			s.cur.segments = map[string]bool{}
		}
		if s.cur.segments[id] {
			return
		}
		s.cur.segments[id] = true
	}
	now := time.Now()
	if s.cur.firstAt.IsZero() {
		s.cur.firstAt = now
	}
	s.cur.lastAt = now
	if s.cur.text.Len() > 0 && !strings.HasSuffix(s.cur.text.String(), " ") {
		s.cur.text.WriteString(" ")
	}
	s.cur.text.WriteString(strings.TrimSpace(text))
	if attrs[AttrTranscriptionFinal] == "true" {
		s.cur.final = true
	}
	s.signal()
}

func (s *session) data(identity, topic string, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != s.agentID || s.cur == nil || topic != ToolCallsTopic {
		return
	}
	s.cur.tools = append(s.cur.tools, parseToolCalls(payload)...)
	s.signal()
}

func (s *session) attributes(identity string, changed map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != s.agentID {
		return
	}
	st, ok := changed[AttrAgentState]
	if !ok {
		return
	}
	s.agentState = st
	if s.cur != nil && (st == StateThinking || st == StateSpeaking) {
		s.cur.spoke = true
	}
	s.signal()
}

func (s *session) signal() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventLocked(kind, detail)
}

func (s *session) eventLocked(kind, detail string) {
	if s.metrics == nil {
		s.metrics = map[string]float64{}
	}
	s.events = append(s.events, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// Send publishes the user turn as text on the data topic.
func (s *session) Send(ctx context.Context, ut target.UserTurn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("livekit: session is closed")
	}
	cur := &turn{sentAt: time.Now()}
	if s.greeting.Len() > 0 {
		cur.text.WriteString(s.greeting.String())
		cur.firstAt, cur.lastAt = cur.sentAt, cur.sentAt
		s.greeting.Reset()
	}
	s.cur = cur
	s.mu.Unlock()
	if ut.Text == "" {
		return nil
	}
	return s.room.SendText(ctx, s.t.cfg.DataTopic, ut.Text)
}

// Receive waits for the agent's turn: text arrived and the agent is back to
// listening (or sent a final segment), or the idle timeout passed, or the
// agent left.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if cur == nil {
		return target.AgentTurn{}, errors.New("livekit: Receive without a pending Send")
	}
	deadline := time.NewTimer(s.t.cfg.TurnTimeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		hasText := cur.text.Len() > 0
		busy := s.agentState == StateThinking || s.agentState == StateSpeaking
		idleFor := time.Duration(0)
		if hasText {
			idleFor = time.Since(cur.lastAt)
		}
		done := hasText && !busy && (cur.final || (cur.spoke && s.agentState == StateListening) || idleFor >= s.t.cfg.TurnIdle)
		s.mu.Unlock()
		select {
		case <-s.agentLeft:
			return s.finish(cur, true), nil
		default:
		}
		if done {
			return s.finish(cur, false), nil
		}
		wait := s.t.cfg.TurnIdle
		if hasText && !busy {
			wait = s.t.cfg.TurnIdle - idleFor
		}
		if wait <= 0 {
			wait = 10 * time.Millisecond
		}
		idle := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			idle.Stop()
			return target.AgentTurn{}, ctx.Err()
		case <-deadline.C:
			idle.Stop()
			if hasText {
				return s.finish(cur, false), nil
			}
			return target.AgentTurn{}, fmt.Errorf("livekit: the agent did not answer within %s", s.t.cfg.TurnTimeout)
		case <-s.agentLeft:
			idle.Stop()
			return s.finish(cur, true), nil
		case <-s.changed:
			idle.Stop()
		case <-idle.C:
		}
	}
}

func (s *session) finish(cur *turn, agentLeft bool) target.AgentTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	ended := cur.lastAt
	if ended.IsZero() {
		ended = time.Now()
	}
	at := target.AgentTurn{Text: cur.text.String(), ToolCalls: cur.tools, Done: agentLeft, StartedAt: cur.sentAt, FirstTokenAt: cur.firstAt, EndedAt: ended}
	if at.FirstTokenAt.IsZero() {
		at.FirstTokenAt = ended
	}
	s.cur = nil
	return at
}

// Close leaves the room.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if !already && s.room != nil {
		s.room.Disconnect()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]float64{}
	for k, v := range s.metrics {
		m[k] = v
	}
	return target.Artifacts{Metrics: m, Events: append([]target.Event(nil), s.events...)}, nil
}

// parseToolCalls reads {"name","args"} or a list of them.
func parseToolCalls(payload []byte) []target.ToolCall {
	var one struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(payload, &one); err == nil && one.Name != "" {
		return []target.ToolCall{{Name: one.Name, Args: one.Args}}
	}
	var many []struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(payload, &many); err == nil {
		var out []target.ToolCall
		for _, c := range many {
			if c.Name != "" {
				out = append(out, target.ToolCall{Name: c.Name, Args: c.Args})
			}
		}
		return out
	}
	return nil
}
