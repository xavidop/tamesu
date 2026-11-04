// Package vapi is the target for a Vapi assistant: tamesu creates a call on
// the vapi.websocket transport, connects to the call's websocket, streams the
// simulated caller's speech as raw PCM and reads the assistant's audio and
// control messages (transcripts, speech updates, tool calls, the end of the
// call) back. Audio only, 16 kHz PCM by default; no phone number is needed.
package vapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/target"
)

// Defaults.
const (
	DefaultBaseURL    = "https://api.vapi.ai"
	DefaultSampleRate = 16000
	DefaultIdle       = 2 * time.Second
	DefaultTimeout    = 60 * time.Second
	// GreetingTimeout bounds the wait for the assistant's first message after
	// the websocket opens; an assistant that waits for the caller is given
	// up on, not failed.
	GreetingTimeout = 8 * time.Second
	// WAVFile is the stereo artifact: left the caller, right the assistant.
	WAVFile     = "audio.wav"
	chunkMs     = 20
	silenceTail = 600 * time.Millisecond
)

// ErrNoSpeech is returned by Start when the run has no speech layer.
var ErrNoSpeech = errors.New("the vapi target needs the speech models: set speech.tts.model and speech.stt.model in tamesu.yaml")

// Config is the target's configuration.
type Config struct {
	// BaseURL is the Vapi API; empty means DefaultBaseURL.
	BaseURL string
	// APIKey is the private API key ({{secrets.VAPI_API_KEY}}).
	APIKey string
	// AssistantID names a saved assistant; Assistant is a transient one
	// (the assistant object of the call API); SquadID a squad. One of them.
	AssistantID string
	Assistant   map[string]any
	SquadID     string
	// AssistantOverrides go on the call as assistantOverrides; Variables
	// become its variableValues (templates render).
	AssistantOverrides map[string]any
	Variables          map[string]string
	// Metadata goes on the call.
	Metadata map[string]any
	// SampleRate of the PCM audio in both directions (default 16000).
	SampleRate int
	// Idle ends a turn after this silence from the assistant (default 2s).
	Idle time.Duration
	// Timeout bounds one turn (default 60s).
	Timeout time.Duration
	// GreetingTimeout bounds the wait for the assistant's first message
	// (default GreetingTimeout).
	GreetingTimeout time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how creating the call and connecting are retried.
	Connect target.ConnectPolicy
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = DefaultSampleRate
	}
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.GreetingTimeout <= 0 {
		cfg.GreetingTimeout = GreetingTimeout
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the vapi target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// control is a JSON message of the websocket, in either direction. Vapi's
// client messages share the shape: type, then fields by type.
type control struct {
	Type string `json:"type"`
	// transcript
	Role           string `json:"role,omitempty"`
	TranscriptType string `json:"transcriptType,omitempty"`
	Transcript     string `json:"transcript,omitempty"`
	// speech-update and status-update
	Status      string `json:"status,omitempty"`
	EndedReason string `json:"endedReason,omitempty"`
	// tool-calls and function-call
	ToolCallList []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		} `json:"function"`
	} `json:"toolCallList,omitempty"`
	FunctionCall *struct {
		Name       string `json:"name"`
		Parameters any    `json:"parameters"`
	} `json:"functionCall,omitempty"`
}

type event struct {
	msg   *control
	raw   any
	audio []int16
	err   error
	at    time.Time
}

type session struct {
	closeOnce sync.Once
	t         *Target
	run       *target.RunContext
	conn      *websocket.Conn
	events    chan event
	stop      context.CancelFunc
	callID    string
	rate      int

	mu       sync.Mutex
	closed   bool
	ended    bool
	pending  *target.AgentTurn
	turns    int
	received int
	metrics  map[string]float64
	evs      []target.Event
	audio    *audioState

	connectedAt time.Time
	// played is how much assistant audio has arrived, as wall time at rate.
	played time.Duration
	first  time.Time
}

// callResponse is what POST /call answers, as far as the target reads it.
type callResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Transport struct {
		Provider         string `json:"provider"`
		WebsocketCallURL string `json:"websocketCallUrl"`
	} `json:"transport"`
	Message any `json:"message,omitempty"`
}

// Start creates the call, connects to its websocket and reads the
// assistant's first message when it speaks first.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if run.Speech == nil {
		return nil, fmt.Errorf("vapi %s: %w", t.name, ErrNoSpeech)
	}
	apiKey, err := run.Render(t.cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("vapi %s: api_key: %w", t.name, err)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("vapi %s: api_key is empty", t.name)
	}
	body, err := t.callBody(run)
	if err != nil {
		return nil, fmt.Errorf("vapi %s: %w", t.name, err)
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+apiKey)
	hdr.Set("Content-Type", "application/json")
	call, err := target.Connect(ctx, t.cfg.Connect, run.Logger, "vapi "+t.name+" create call", func(ctx context.Context) (callResponse, error) {
		return t.createCall(ctx, hdr, body)
	})
	if err != nil {
		return nil, fmt.Errorf("vapi %s: create call: %w", t.name, err)
	}
	if call.Transport.WebsocketCallURL == "" {
		return nil, fmt.Errorf("vapi %s: the call %s has no websocketCallUrl (transport %q)", t.name, call.ID, call.Transport.Provider)
	}
	wsHdr := http.Header{}
	wsHdr.Set("Authorization", "Bearer "+apiKey)
	conn, err := target.Connect(ctx, t.cfg.Connect, run.Logger, "vapi "+t.name, func(ctx context.Context) (*websocket.Conn, error) {
		dctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
		defer cancel()
		conn, resp, err := websocket.Dial(dctx, call.Transport.WebsocketCallURL, &websocket.DialOptions{HTTPClient: t.cfg.Client, HTTPHeader: wsHdr})
		return conn, target.HandshakeError(err, resp)
	})
	if err != nil {
		return nil, fmt.Errorf("vapi %s: connect %s: %w", t.name, call.Transport.WebsocketCallURL, err)
	}
	conn.SetReadLimit(16 << 20)
	s := &session{t: t, run: run, conn: conn, events: make(chan event, 1024), metrics: map[string]float64{}, callID: call.ID, rate: t.cfg.SampleRate}
	s.audio = newAudioState(run)
	readCtx, stopRead := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = stopRead
	go s.read(readCtx)
	s.connectedAt = time.Now()
	s.event("call", call.ID)
	s.greeting(ctx)
	return s, nil
}

// callBody builds the create-call request.
func (t *Target) callBody(run *target.RunContext) (map[string]any, error) {
	body := map[string]any{
		"transport": map[string]any{
			"provider":    "vapi.websocket",
			"audioFormat": map[string]any{"format": "pcm_s16le", "container": "raw", "sampleRate": t.cfg.SampleRate},
		},
	}
	switch {
	case t.cfg.AssistantID != "":
		id, err := run.Render(t.cfg.AssistantID)
		if err != nil {
			return nil, fmt.Errorf("assistant_id: %w", err)
		}
		body["assistantId"] = id
	case t.cfg.Assistant != nil:
		a, err := renderValue(t.cfg.Assistant, run.Render)
		if err != nil {
			return nil, fmt.Errorf("assistant: %w", err)
		}
		body["assistant"] = a
	case t.cfg.SquadID != "":
		id, err := run.Render(t.cfg.SquadID)
		if err != nil {
			return nil, fmt.Errorf("squad_id: %w", err)
		}
		body["squadId"] = id
	default:
		return nil, errors.New("one of assistant_id, assistant or squad_id is required")
	}
	overrides := map[string]any{}
	if t.cfg.AssistantOverrides != nil {
		o, err := renderValue(t.cfg.AssistantOverrides, run.Render)
		if err != nil {
			return nil, fmt.Errorf("assistant_overrides: %w", err)
		}
		if m, ok := o.(map[string]any); ok {
			overrides = m
		}
	}
	if len(t.cfg.Variables) > 0 {
		vars := map[string]any{}
		for k, v := range t.cfg.Variables {
			rv, err := run.Render(v)
			if err != nil {
				return nil, fmt.Errorf("variables.%s: %w", k, err)
			}
			vars[k] = rv
		}
		overrides["variableValues"] = vars
	}
	if len(overrides) > 0 {
		body["assistantOverrides"] = overrides
	}
	if t.cfg.Metadata != nil {
		body["metadata"] = t.cfg.Metadata
	}
	return body, nil
}

// createCall performs POST /call.
func (t *Target) createCall(ctx context.Context, hdr http.Header, body map[string]any) (callResponse, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return callResponse{}, target.NoRetry(err)
	}
	rctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, strings.TrimRight(t.cfg.BaseURL, "/")+"/call", bytes.NewReader(b))
	if err != nil {
		return callResponse{}, target.NoRetry(err)
	}
	req.Header = hdr.Clone()
	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		return callResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
		return callResponse{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 300:
		return callResponse{}, target.NoRetry(fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerpt(raw)))
	}
	var out callResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return callResponse{}, target.NoRetry(fmt.Errorf("unexpected response: %s", excerpt(raw)))
	}
	return out, nil
}

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
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

// greeting reads the assistant's first message, when it speaks first.
func (s *session) greeting(ctx context.Context) {
	gctx, cancel := context.WithTimeout(ctx, s.t.cfg.GreetingTimeout)
	defer cancel()
	turn, err := s.readTurn(gctx, nil, true)
	if err != nil || turn.FirstTokenAt.IsZero() {
		return
	}
	s.mu.Lock()
	s.metrics[metrics.GreetingMs] = float64(turn.FirstTokenAt.Sub(s.connectedAt).Microseconds()) / 1000
	s.mu.Unlock()
	s.event("greeting", turn.Text)
}

// read decodes the websocket into events until the connection ends: binary
// frames are the assistant's PCM, text frames its control messages.
func (s *session) read(ctx context.Context) {
	defer close(s.events)
	for {
		kind, data, err := s.conn.Read(ctx)
		if err != nil {
			select {
			case s.events <- event{err: err}:
			case <-ctx.Done():
			}
			return
		}
		ev := event{at: time.Now()}
		if kind == websocket.MessageBinary {
			ev.audio = bytesToPCM16(data)
		} else {
			var m control
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			ev.msg = &m
			_ = json.Unmarshal(data, &ev.raw)
		}
		select {
		case s.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// sendControl writes one JSON control message.
func (s *session) sendControl(ctx context.Context, m map[string]any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// Send speaks the caller's turn and reads the assistant's reply.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	closed, ended := s.closed, s.ended
	s.mu.Unlock()
	if closed {
		return errors.New("vapi: session is closed")
	}
	if ended {
		return errors.New("vapi: the call has ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	started := time.Now()
	a := s.audio
	s.mu.Lock()
	interrupting := a.interruptNext && !s.first.IsZero() && s.played > time.Since(s.first)
	if a.interruptNext && !interrupting {
		s.evs = append(s.evs, target.Event{At: started, Kind: "interrupt_missed", Detail: "the assistant had finished before the caller spoke"})
	}
	a.interruptNext = false
	s.mu.Unlock()
	if turn.DTMF != "" {
		s.event("dtmf_unsupported", turn.DTMF+" (the websocket transport carries no DTMF)")
	}
	spoken, err := s.speak(tctx, turn, started)
	if err != nil {
		return err
	}
	spoken.startedAt = started
	spoken.interrupting = interrupting
	if interrupting {
		s.event("interruption", "the caller spoke over the assistant")
	}
	agent, err := s.readTurn(tctx, spoken, false)
	if err != nil {
		return fmt.Errorf("vapi: %w", err)
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

// readTurn reads the assistant's audio and messages until Idle after its
// last sound, the end of the call, or the timeout. Final assistant
// transcripts become the turn's text; without them the audio is transcribed.
func (s *session) readTurn(ctx context.Context, spoken *spokenTurn, greeting bool) (target.AgentTurn, error) {
	agent := target.AgentTurn{}
	var last time.Time
	idle := time.NewTimer(s.t.cfg.Timeout)
	defer idle.Stop()
	a := s.audio
	var plan *interruptPlan
	if spoken != nil && !greeting && !spoken.interrupting {
		plan = a.planInterrupt()
	}
	var interruptedAt time.Time
	var transcript strings.Builder
	var raws []any
	finish := func() (target.AgentTurn, error) {
		s.finishAudio(ctx, spoken, &agent, transcript.String())
		if spoken != nil && spoken.interrupting {
			s.recordInterruption(spoken, interruptedAt)
		}
		agent.Raw = raws
		return agent, nil
	}
	for {
		select {
		case <-ctx.Done():
			if !last.IsZero() || greeting {
				return finish()
			}
			return agent, fmt.Errorf("the assistant did not answer within %s: %w", s.t.cfg.Timeout, ctx.Err())
		case <-idle.C:
			if last.IsZero() && !greeting {
				return agent, fmt.Errorf("the assistant did not answer within %s", s.t.cfg.Timeout)
			}
			return finish()
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				agent.Done = true
				s.mu.Lock()
				s.ended = true
				s.mu.Unlock()
				if !last.IsZero() || greeting {
					s.event("closed", "the call's websocket closed")
					return finish()
				}
				return agent, fmt.Errorf("the call ended before the assistant answered: %w", errOr(ev.err))
			}
			s.mu.Lock()
			s.received++
			s.mu.Unlock()
			alive := false
			switch ev.msg {
			case nil:
				if agent.FirstTokenAt.IsZero() {
					agent.FirstTokenAt = time.Now()
				}
				s.agentAudio(ev.audio, spoken)
				alive = true
				if plan != nil && plan.due(spoken) {
					agent.Interrupted = true
					s.mu.Lock()
					a.interruptNext = true
					s.mu.Unlock()
					s.event("interrupting", "the caller will speak over the assistant")
					return finish()
				}
			default:
				raws = append(raws, ev.raw)
				switch ev.msg.Type {
				case "transcript":
					if ev.msg.Role == "assistant" && ev.msg.TranscriptType == "final" {
						if transcript.Len() > 0 {
							transcript.WriteString(" ")
						}
						transcript.WriteString(strings.TrimSpace(ev.msg.Transcript))
						alive = true
					}
				case "tool-calls", "function-call":
					for _, tc := range ev.msg.ToolCallList {
						agent.ToolCalls = append(agent.ToolCalls, target.ToolCall{Name: tc.Function.Name, Args: argsOf(tc.Function.Arguments)})
					}
					if fc := ev.msg.FunctionCall; fc != nil && fc.Name != "" {
						agent.ToolCalls = append(agent.ToolCalls, target.ToolCall{Name: fc.Name, Args: argsOf(fc.Parameters)})
					}
					s.event("tool_call", toolNames(agent.ToolCalls))
				case "user-interrupted":
					if interruptedAt.IsZero() {
						interruptedAt = ev.at
					}
					s.mu.Lock()
					s.played = 0
					s.mu.Unlock()
					s.event("user_interrupted", "the assistant stopped for the caller")
				case "status-update":
					s.event("status", strings.TrimSpace(ev.msg.Status+" "+ev.msg.EndedReason))
					if ev.msg.Status == "ended" {
						agent.Done = true
						s.mu.Lock()
						s.ended = true
						s.mu.Unlock()
						if ev.msg.EndedReason != "" {
							s.event("ended_reason", ev.msg.EndedReason)
						}
						return finish()
					}
				case "hang":
					s.event("hang", "the assistant is taking long to respond")
				case "speech-update":
					if ev.msg.Role == "assistant" && ev.msg.Status == "started" && agent.FirstTokenAt.IsZero() {
						agent.FirstTokenAt = ev.at
					}
				case "transfer-update":
					s.event("transfer", "the assistant transferred the call")
				}
			}
			if alive {
				last = time.Now()
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(s.t.cfg.Idle)
			}
		}
	}
}

func argsOf(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(x), &m) == nil {
			return m
		}
	}
	return nil
}

func toolNames(calls []target.ToolCall) string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

// Receive returns the assistant's turn for the last Send.
func (s *session) Receive(context.Context) (target.AgentTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("vapi: Receive without a Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close hangs up, ends the connection and returns the artifacts.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return target.Artifacts{}, nil
	}
	s.closed = true
	ended := s.ended
	s.mu.Unlock()
	if !ended {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = s.sendControl(sctx, map[string]any{"type": "hangup"})
		cancel()
		s.event("hangup", "by tamesu")
	}
	_ = s.closeConn()
	s.mu.Lock()
	defer s.mu.Unlock()
	arts := target.Artifacts{Metrics: s.metrics, Files: map[string]string{}}
	arts.Metrics[metrics.Turns] = float64(s.turns)
	arts.Metrics["ws_messages_received"] = float64(s.received)
	if !s.connectedAt.IsZero() {
		arts.Metrics[metrics.CallDurationMs] = float64(time.Since(s.connectedAt).Microseconds()) / 1000
	}
	s.closeAudioLocked(arts.Files)
	arts.Events = s.evs
	return arts, nil
}

func (s *session) closeConn() error {
	var err error
	s.closeOnce.Do(func() {
		if s.conn != nil {
			err = s.conn.Close(websocket.StatusNormalClosure, "done")
		}
		if s.stop != nil {
			s.stop()
		}
	})
	return err
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

func errOr(err error) error {
	if err == nil {
		return errors.New("websocket closed")
	}
	return err
}

// ---- audio

type audioState struct {
	speech target.Speech
	voice  target.Voice
	rng    *mrand.Rand
	start  time.Time
	user   []int16
	agent  []int16
	turn   []int16
	ttfa   []float64
	userMs float64
	agMs   float64
	// interrupts are the barge-in stop times: from the caller speaking over
	// the assistant to its user-interrupted message (or to the end of the
	// audio it had queued, when none came).
	interrupts    []float64
	interruptNext bool
}

func newAudioState(run *target.RunContext) *audioState {
	a := &audioState{speech: run.Speech, start: time.Now()}
	if run.Voice != nil {
		a.voice = *run.Voice
	}
	seed := uint64(a.voice.Seed)
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	a.rng = mrand.New(mrand.NewPCG(seed, seed^0x5a5a5a5a))
	return a
}

type interruptPlan struct{ after time.Duration }

func (a *audioState) planInterrupt() *interruptPlan {
	if a.voice.InterruptProbability <= 0 || a.rng.Float64() >= a.voice.InterruptProbability {
		return nil
	}
	return &interruptPlan{after: 600*time.Millisecond + time.Duration(a.rng.Float64()*float64(1400*time.Millisecond))}
}

func (p *interruptPlan) due(sp *spokenTurn) bool {
	return sp != nil && !sp.firstAudio.IsZero() && time.Since(sp.firstAudio) >= p.after
}

func (s *session) recordInterruption(sp *spokenTurn, interruptedAt time.Time) {
	a := s.audio
	s.mu.Lock()
	defer s.mu.Unlock()
	var stop time.Duration
	how := "the assistant stopped (user-interrupted)"
	switch {
	case !interruptedAt.IsZero():
		stop = interruptedAt.Sub(sp.startedAt)
	case !s.first.IsZero():
		stop = s.first.Add(s.played).Sub(sp.startedAt)
		how = "the assistant never signalled a stop; its queued audio played out"
	}
	stop = max(stop, 0)
	a.interrupts = append(a.interrupts, float64(stop.Microseconds())/1000)
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "interruption_stopped", Detail: fmt.Sprintf("%s after %d ms", how, stop.Milliseconds())})
}

type spokenTurn struct {
	endedAt      time.Time
	firstAudio   time.Time
	startedAt    time.Time
	interrupting bool
}

// speak synthesises the turn and streams it as 20 ms PCM frames, paced in
// real time, with a silence tail.
func (s *session) speak(ctx context.Context, turn target.UserTurn, started time.Time) (*spokenTurn, error) {
	a := s.audio
	sp := &spokenTurn{endedAt: started}
	if turn.Text == "" {
		return sp, nil
	}
	out, err := a.speech.Synthesize(ctx, target.SynthesizeRequest{Text: turn.Text, Voice: a.voice.Hint, Language: a.voice.Language})
	if err != nil {
		return nil, fmt.Errorf("vapi: synthesize: %w", err)
	}
	pcm := audio.Resample(audio.PCM{Rate: out.Rate, Samples: out.Samples}, s.rate)
	samples := append(pcm.Samples, make([]int16, audio.SampleCount(silenceTail, s.rate))...)
	chunk := s.rate * chunkMs / 1000
	tick := time.NewTicker(chunkMs * time.Millisecond)
	defer tick.Stop()
	for i := 0; i < len(samples); i += chunk {
		end := min(i+chunk, len(samples))
		if err := s.conn.Write(ctx, websocket.MessageBinary, pcm16ToBytes(samples[i:end])); err != nil {
			return nil, fmt.Errorf("vapi: audio: %w", err)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	sp.endedAt = time.Now()
	s.mu.Lock()
	pos := max(audio.SampleCount(started.Sub(a.start), s.rate), len(a.agent))
	a.user = padTo(a.user, pos)
	a.user = append(a.user, pcm.Samples...)
	a.userMs += float64(len(pcm.Samples)) * 1000 / float64(s.rate)
	a.turn = a.turn[:0]
	s.mu.Unlock()
	return sp, nil
}

func (s *session) agentAudio(pcm []int16, sp *spokenTurn) {
	if len(pcm) == 0 {
		return
	}
	a := s.audio
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.first.IsZero() || s.played-now.Sub(s.first) < 0 {
		s.first, s.played = now, 0
	}
	s.played += audio.Duration(len(pcm), s.rate)
	if sp != nil && sp.firstAudio.IsZero() {
		sp.firstAudio = now
		a.ttfa = append(a.ttfa, float64(sp.firstAudio.Sub(sp.endedAt).Microseconds())/1000)
		a.agent = padTo(a.agent, max(audio.SampleCount(sp.firstAudio.Sub(a.start), s.rate), len(a.user)))
	} else if sp == nil && len(a.turn) == 0 {
		a.agent = padTo(a.agent, max(audio.SampleCount(now.Sub(a.start), s.rate), len(a.user)))
	}
	a.agent = append(a.agent, pcm...)
	a.turn = append(a.turn, pcm...)
	a.agMs += float64(len(pcm)) * 1000 / float64(s.rate)
}

// finishAudio sets the turn's timing and its text: the assistant's own
// final transcripts when it sent any (transcript_source auto or agent),
// else tamesu's transcription of its audio.
func (s *session) finishAudio(ctx context.Context, sp *spokenTurn, agent *target.AgentTurn, transcript string) {
	a := s.audio
	if sp != nil && !sp.firstAudio.IsZero() {
		agent.FirstTokenAt = sp.firstAudio
	}
	s.mu.Lock()
	turn := append([]int16(nil), a.turn...)
	a.turn = a.turn[:0]
	s.mu.Unlock()
	if len(turn) > 0 {
		agent.Audio = pcm16ToBytes(turn)
	}
	source := a.voice.TranscriptSource
	if transcript != "" && source != "stt" {
		agent.Text = transcript
		agent.TranscriptSource = "agent"
		return
	}
	if len(turn) == 0 || source == "agent" {
		return
	}
	if tr, err := a.speech.Transcribe(ctx, target.TranscribeRequest{Audio: target.Audio{Rate: s.rate, Samples: turn}, Language: a.voice.Language}); err == nil {
		agent.Text = strings.TrimSpace(tr.Text)
		agent.TranscriptSource = "stt"
	} else {
		s.event("stt_error", err.Error())
	}
}

func (s *session) closeAudioLocked(files map[string]string) {
	a := s.audio
	n := max(len(a.user), len(a.agent))
	a.user, a.agent = padTo(a.user, n), padTo(a.agent, n)
	if s.run != nil && s.run.Dir != "" && n > 0 {
		path := filepath.Join(s.run.Dir, WAVFile)
		if err := wav.WriteFile(path, s.rate, a.user, a.agent); err == nil {
			files[WAVFile] = WAVFile
		} else {
			s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "wav_error", Detail: err.Error()})
		}
	}
	metrics.Summarize(s.metrics, metrics.TimeToFirstAudioMs, a.ttfa)
	s.metrics["agent_speech_ms"] = a.agMs
	s.metrics["user_speech_ms"] = a.userMs
	s.metrics["interruptions"] = float64(len(a.interrupts))
	metrics.Summarize(s.metrics, metrics.InterruptionStopMs, a.interrupts)
}

func padTo(samples []int16, n int) []int16 {
	if n > len(samples) {
		return append(samples, make([]int16, n-len(samples))...)
	}
	return samples
}

// pcm16ToBytes encodes samples as 16-bit little-endian PCM.
func pcm16ToBytes(samples []int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, v := range samples {
		b[2*i] = byte(v)
		b[2*i+1] = byte(uint16(v) >> 8)
	}
	return b
}

// bytesToPCM16 decodes 16-bit little-endian PCM.
func bytesToPCM16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
	}
	return out
}
