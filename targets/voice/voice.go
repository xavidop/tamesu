// Package voice is the target for a voice agent behind a websocket that
// carries audio: raw PCM or G.711 frames, binary or base64 inside JSON
// messages, with optional JSON control messages (transcripts, the end of the
// call, an interruption, tool calls) read by JSONPath. A call may be created
// first with session.open (a REST request whose captures render into the
// websocket URL). Hosted voice platforms (Vapi's websocket transport,
// ElevenLabs Conversational AI, Deepgram Voice Agent and the like) and
// in-house voice stacks are tested the same way, from YAML.
package voice

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/g711"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/target"
)

// Encodings and frame kinds.
const (
	EncodingPCM16 = "pcm16"
	EncodingMuLaw = "mulaw"
	EncodingALaw  = "alaw"
	FramesBinary  = "binary"
	FramesJSON    = "json"
)

// Encodings and Frames list the options.
var (
	Encodings = []string{EncodingPCM16, EncodingMuLaw, EncodingALaw}
	Frames    = []string{FramesBinary, FramesJSON}
)

// Defaults.
const (
	DefaultIdle            = 2 * time.Second
	DefaultTimeout         = 60 * time.Second
	DefaultGreetingTimeout = 8 * time.Second
	// WAVFile is the stereo artifact: left the caller, right the agent.
	WAVFile     = "audio.wav"
	chunkMs     = 20
	silenceTail = 600 * time.Millisecond
)

// ErrNoSpeech is returned by Start when the run has no speech layer.
var ErrNoSpeech = errors.New("the voice target needs the speech models: set speech.tts.model and speech.stt.model in tamesu.yaml")

// Request is an HTTP request template (session.open).
type Request struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    any
	// Capture are JSONPaths into the JSON reply, by name; the values render
	// as {{session.NAME}}.
	Capture map[string]string
}

// Audio says how the audio travels.
type Audio struct {
	// Encoding is pcm16 (default), mulaw or alaw.
	Encoding string
	// SampleRate in Hz; default 16000 for pcm16, 8000 for G.711.
	SampleRate int
	// Frames is binary (default: one audio chunk per binary websocket
	// frame) or json (the chunk travels base64-encoded inside a JSON
	// message).
	Frames string
	// Send is the JSON message template per outgoing chunk (frames json);
	// {{audio}} is the base64 chunk.
	Send any
	// Receive is the JSONPath of the base64 audio in an incoming JSON
	// message (frames json), evaluated against the message as a one-element
	// list.
	Receive string
}

// Messages are the JSON control messages of the protocol, all optional.
// The JSONPaths run against the incoming message wrapped in a one-element
// list, so a filter such as $[?(@.type=='transcript')].transcript selects.
type Messages struct {
	// Open is sent once after connecting.
	Open any
	// Transcript yields the agent's words from a message (its final
	// transcripts); without it the audio is transcribed.
	Transcript string
	// Done is truthy on the message that ends the call.
	Done string
	// Interrupted is truthy on the message that says the agent stopped for
	// the caller (its clear / interruption signal).
	Interrupted string
	// ToolCalls yields the tool calls of a message (names, or objects with
	// name and args).
	ToolCalls string
	// DTMF is the message template for a keypad digit; {{digit}} renders.
	DTMF any
	// Close is sent before the connection is closed (a hangup).
	Close any
}

// Config is the target's configuration.
type Config struct {
	// URL is the websocket; templates render, including the captures of
	// SessionOpen as {{session.NAME}}.
	URL     string
	Headers map[string]string
	// SessionOpen is a request made before connecting (create the call).
	SessionOpen *Request
	Audio       Audio
	Messages    Messages
	// Idle ends a turn after this silence from the agent (default 2s).
	Idle time.Duration
	// Timeout bounds one turn (default 60s).
	Timeout time.Duration
	// GreetingTimeout bounds the wait for the agent's first words.
	GreetingTimeout time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
	// Connect is how the call creation and the connection are retried.
	Connect target.ConnectPolicy
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Audio.Encoding == "" {
		cfg.Audio.Encoding = EncodingPCM16
	}
	if cfg.Audio.Frames == "" {
		cfg.Audio.Frames = FramesBinary
	}
	if cfg.Audio.SampleRate <= 0 {
		if cfg.Audio.Encoding == EncodingPCM16 {
			cfg.Audio.SampleRate = 16000
		} else {
			cfg.Audio.SampleRate = 8000
		}
	}
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.GreetingTimeout <= 0 {
		cfg.GreetingTimeout = DefaultGreetingTimeout
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the voice target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

type event struct {
	msg   any
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
	rate      int
	values    map[string]string
	paths     paths

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
	played      time.Duration
	first       time.Time
}

// paths are the compiled JSONPaths of the configuration.
type paths struct {
	receive, transcript, done, interrupted, tools jp.Expr
	capture                                       map[string]jp.Expr
}

func compile(cfg Config) (paths, error) {
	var p paths
	for _, f := range []struct {
		name string
		src  string
		dst  *jp.Expr
	}{{"audio.receive", cfg.Audio.Receive, &p.receive}, {"messages.transcript", cfg.Messages.Transcript, &p.transcript}, {"messages.done", cfg.Messages.Done, &p.done},
		{"messages.interrupted", cfg.Messages.Interrupted, &p.interrupted}, {"messages.tool_calls", cfg.Messages.ToolCalls, &p.tools}} {
		if f.src == "" {
			continue
		}
		x, err := jp.ParseString(f.src)
		if err != nil {
			return p, fmt.Errorf("%s %q: %w", f.name, f.src, err)
		}
		*f.dst = x
	}
	if cfg.SessionOpen != nil && len(cfg.SessionOpen.Capture) > 0 {
		p.capture = map[string]jp.Expr{}
		for name, path := range cfg.SessionOpen.Capture {
			x, err := jp.ParseString(path)
			if err != nil {
				return p, fmt.Errorf("session.open.capture %s %q: %w", name, path, err)
			}
			p.capture[name] = x
		}
	}
	return p, nil
}

// render substitutes the session's values ({{session.NAME}}) and the run's
// scope.
func (s *session) render(str string) (string, error) {
	for k, v := range s.values {
		str = strings.ReplaceAll(str, "{{"+k+"}}", v)
		str = strings.ReplaceAll(str, "{{ "+k+" }}", v)
	}
	return s.run.Render(str)
}

// payload renders a message template: a string as is, anything else as
// JSON; extra are per-message values such as audio or digit.
func (s *session) payload(tpl any, extra map[string]string) ([]byte, error) {
	rendered, err := renderValue(tpl, func(str string) (string, error) {
		for k, v := range extra {
			str = strings.ReplaceAll(str, "{{"+k+"}}", v)
			str = strings.ReplaceAll(str, "{{ "+k+" }}", v)
		}
		return s.render(str)
	})
	if err != nil {
		return nil, err
	}
	if str, ok := rendered.(string); ok {
		return []byte(str), nil
	}
	return json.Marshal(rendered)
}

// Start creates the call when asked, connects and reads the greeting.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if run.Speech == nil {
		return nil, fmt.Errorf("voice %s: %w", t.name, ErrNoSpeech)
	}
	p, err := compile(t.cfg)
	if err != nil {
		return nil, fmt.Errorf("voice %s: %w", t.name, err)
	}
	s := &session{t: t, run: run, events: make(chan event, 1024), metrics: map[string]float64{}, rate: t.cfg.Audio.SampleRate, values: map[string]string{}, paths: p}
	if t.cfg.SessionOpen != nil {
		if err := s.open(ctx); err != nil {
			return nil, fmt.Errorf("voice %s: session.open: %w", t.name, err)
		}
	}
	u, err := s.render(t.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("voice %s: url: %w", t.name, err)
	}
	if u == "" {
		return nil, fmt.Errorf("voice %s: url is empty", t.name)
	}
	hdr := http.Header{}
	for k, v := range t.cfg.Headers {
		rv, err := s.render(v)
		if err != nil {
			return nil, fmt.Errorf("voice %s: header %s: %w", t.name, k, err)
		}
		hdr.Set(k, rv)
	}
	conn, err := target.Connect(ctx, t.cfg.Connect, run.Logger, "voice "+t.name, func(ctx context.Context) (*websocket.Conn, error) {
		dctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
		defer cancel()
		conn, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: t.cfg.Client, HTTPHeader: hdr})
		return conn, target.HandshakeError(err, resp)
	})
	if err != nil {
		return nil, fmt.Errorf("voice %s: connect %s: %w", t.name, u, err)
	}
	conn.SetReadLimit(16 << 20)
	s.conn = conn
	s.audio = newAudioState(run)
	readCtx, stopRead := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = stopRead
	go s.read(readCtx)
	s.connectedAt = time.Now()
	s.event("connected", u)
	if t.cfg.Messages.Open != nil {
		b, err := s.payload(t.cfg.Messages.Open, nil)
		if err != nil {
			_ = s.closeConn()
			return nil, fmt.Errorf("voice %s: messages.open: %w", t.name, err)
		}
		octx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
		err = conn.Write(octx, websocket.MessageText, b)
		cancel()
		if err != nil {
			_ = s.closeConn()
			return nil, fmt.Errorf("voice %s: messages.open: %w", t.name, err)
		}
	}
	s.greeting(ctx)
	return s, nil
}

// open performs session.open and keeps its captures.
func (s *session) open(ctx context.Context) error {
	req := s.t.cfg.SessionOpen
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	u, err := s.render(req.URL)
	if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	var body []byte
	if req.Body != nil {
		if body, err = s.payload(req.Body, nil); err != nil {
			return fmt.Errorf("body: %w", err)
		}
	}
	hdr := http.Header{}
	hdr.Set("Accept", "application/json")
	if body != nil {
		hdr.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		rv, err := s.render(v)
		if err != nil {
			return fmt.Errorf("header %s: %w", k, err)
		}
		hdr.Set(k, rv)
	}
	type result struct {
		status int
		body   []byte
	}
	res, err := target.Connect(ctx, s.t.cfg.Connect, s.run.Logger, "voice "+s.t.name+" session.open", func(ctx context.Context) (result, error) {
		rctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
		defer cancel()
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		hreq, err := http.NewRequestWithContext(rctx, method, u, rd)
		if err != nil {
			return result{}, target.NoRetry(err)
		}
		hreq.Header = hdr.Clone()
		resp, err := s.t.cfg.Client.Do(hreq)
		if err != nil {
			return result{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
			return result{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode >= 400 {
			return result{}, target.NoRetry(fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerpt(raw)))
		}
		return result{status: resp.StatusCode, body: raw}, nil
	})
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, u, err)
	}
	data, err := oj.Parse(res.body)
	if err != nil && len(s.paths.capture) > 0 {
		return fmt.Errorf("%s %s: the reply is not JSON: %s", method, u, excerpt(res.body))
	}
	for name, x := range s.paths.capture {
		if got := x.Get(data); len(got) > 0 {
			s.values["session."+name] = asString(got[0])
		}
	}
	s.event("session_open", fmt.Sprintf("%s %s: HTTP %d", method, u, res.status))
	return nil
}

func excerpt(b []byte) string {
	str := strings.TrimSpace(string(b))
	if len(str) > 300 {
		return str[:300] + "..."
	}
	return str
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

// greeting reads the agent's first words, when it speaks first.
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

// decode turns a chunk of the wire encoding into PCM samples.
func (s *session) decode(b []byte) []int16 {
	switch s.t.cfg.Audio.Encoding {
	case EncodingMuLaw:
		return g711.DecodeMuLaw(b)
	case EncodingALaw:
		return g711.DecodeALaw(b)
	}
	return bytesToPCM16(b)
}

// encode turns PCM samples into the wire encoding.
func (s *session) encode(pcm []int16) []byte {
	switch s.t.cfg.Audio.Encoding {
	case EncodingMuLaw:
		return g711.EncodeMuLaw(pcm)
	case EncodingALaw:
		return g711.EncodeALaw(pcm)
	}
	return pcm16ToBytes(pcm)
}

// writeAudio sends one chunk: a binary frame, or the Send template with
// the chunk base64-encoded.
func (s *session) writeAudio(ctx context.Context, pcm []int16) error {
	data := s.encode(pcm)
	if s.t.cfg.Audio.Frames == FramesJSON {
		tpl := s.t.cfg.Audio.Send
		if tpl == nil {
			tpl = map[string]any{"audio": "{{audio}}"}
		}
		b, err := s.payload(tpl, map[string]string{"audio": base64.StdEncoding.EncodeToString(data)})
		if err != nil {
			return err
		}
		return s.conn.Write(ctx, websocket.MessageText, b)
	}
	return s.conn.Write(ctx, websocket.MessageBinary, data)
}

// read decodes the websocket into events until the connection ends: binary
// frames are audio, text frames are JSON messages (audio inside them when
// audio.receive says so).
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
			ev.audio = s.decode(data)
		} else {
			msg, err := oj.Parse(data)
			if err != nil {
				continue
			}
			ev.msg = msg
			if s.paths.receive != nil {
				for _, v := range s.paths.receive.Get([]any{msg}) {
					if str, ok := v.(string); ok {
						if raw, err := base64.StdEncoding.DecodeString(str); err == nil {
							ev.audio = append(ev.audio, s.decode(raw)...)
						}
					}
				}
			}
		}
		select {
		case s.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// Send speaks the caller's turn and reads the agent's reply.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	closed, ended := s.closed, s.ended
	s.mu.Unlock()
	if closed {
		return errors.New("voice: session is closed")
	}
	if ended {
		return errors.New("voice: the call has ended")
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
		s.evs = append(s.evs, target.Event{At: started, Kind: "interrupt_missed", Detail: "the agent had finished before the caller spoke"})
	}
	a.interruptNext = false
	s.mu.Unlock()
	for _, d := range turn.DTMF {
		if s.t.cfg.Messages.DTMF == nil {
			s.event("dtmf_unsupported", string(d)+" (no messages.dtmf template)")
			continue
		}
		b, err := s.payload(s.t.cfg.Messages.DTMF, map[string]string{"digit": string(d)})
		if err != nil {
			return fmt.Errorf("voice: dtmf: %w", err)
		}
		if err := s.conn.Write(tctx, websocket.MessageText, b); err != nil {
			return fmt.Errorf("voice: dtmf: %w", err)
		}
		s.event("dtmf", string(d))
	}
	spoken, err := s.speak(tctx, turn, started)
	if err != nil {
		return err
	}
	spoken.startedAt = started
	spoken.interrupting = interrupting
	if interrupting {
		s.event("interruption", "the caller spoke over the agent")
	}
	agent, err := s.readTurn(tctx, spoken, false)
	if err != nil {
		return fmt.Errorf("voice: %w", err)
	}
	agent.StartedAt = started
	if agent.EndedAt.IsZero() {
		agent.EndedAt = time.Now()
	}
	if agent.FirstTokenAt.IsZero() {
		agent.FirstTokenAt = agent.EndedAt
	}
	s.mu.Lock()
	s.turns++
	s.pending = &agent
	s.mu.Unlock()
	return nil
}

// readTurn reads the agent's audio and messages until Idle after its last
// sound, the end of the call, or the timeout. Transcript messages become
// the turn's text when configured; without them the audio is transcribed.
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
	// With transcript_source stt the agent's audio is transcribed as it
	// arrives (a model that does not stream is buffered behind the same
	// handle); under auto the agent's own transcript is expected, and the
	// audio is transcribed once at the end only when none came.
	var stt target.TranscriptionStream
	if a.voice.TranscriptSource == "stt" {
		var err error
		if stt, err = target.StartTranscription(ctx, a.speech, target.TranscriptionStreamRequest{Rate: s.rate, Language: a.voice.Language}); err != nil {
			s.event("stt_error", err.Error())
			stt = nil
		}
	}
	var interruptedAt time.Time
	var transcript strings.Builder
	var raws []any
	finish := func() (target.AgentTurn, error) {
		// The turn ended when the agent stopped; the transcription that
		// follows is tamesu's own time (stt_ms), not the agent's.
		agent.EndedAt = time.Now()
		s.finishAudio(ctx, spoken, &agent, transcript.String(), stt)
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
			return agent, fmt.Errorf("the agent did not answer within %s: %w", s.t.cfg.Timeout, ctx.Err())
		case <-idle.C:
			if last.IsZero() && !greeting {
				return agent, fmt.Errorf("the agent did not answer within %s", s.t.cfg.Timeout)
			}
			return finish()
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				agent.Done = true
				s.mu.Lock()
				s.ended = true
				s.mu.Unlock()
				if !last.IsZero() || greeting {
					s.event("closed", "the websocket closed")
					return finish()
				}
				return agent, fmt.Errorf("the connection closed before the agent answered: %w", errOr(ev.err))
			}
			s.mu.Lock()
			s.received++
			s.mu.Unlock()
			alive := false
			if len(ev.audio) > 0 {
				if agent.FirstTokenAt.IsZero() {
					agent.FirstTokenAt = time.Now()
				}
				s.agentAudio(ev.audio, spoken)
				if stt != nil {
					_ = stt.Write(ctx, ev.audio)
				}
				alive = true
				if plan != nil && plan.due(spoken) {
					agent.Interrupted = true
					s.mu.Lock()
					a.interruptNext = true
					s.mu.Unlock()
					s.event("interrupting", "the caller will speak over the agent")
					return finish()
				}
			}
			if ev.msg != nil {
				raws = append(raws, ev.msg)
				wrapped := []any{ev.msg}
				if s.paths.transcript != nil {
					for _, v := range s.paths.transcript.Get(wrapped) {
						if str := strings.TrimSpace(asString(v)); str != "" {
							if transcript.Len() > 0 {
								transcript.WriteString(" ")
							}
							transcript.WriteString(str)
							alive = true
						}
					}
				}
				if s.paths.tools != nil {
					for _, v := range s.paths.tools.Get(wrapped) {
						if call, ok := toolCallOf(v); ok {
							agent.ToolCalls = append(agent.ToolCalls, call)
							s.event("tool_call", call.Name)
						}
					}
				}
				if s.paths.interrupted != nil && truthy(s.paths.interrupted.First(wrapped)) {
					if interruptedAt.IsZero() {
						interruptedAt = ev.at
					}
					s.mu.Lock()
					s.played = 0
					s.mu.Unlock()
					s.event("agent_interrupted", "the agent stopped for the caller")
				}
				if s.paths.done != nil && truthy(s.paths.done.First(wrapped)) {
					agent.Done = true
					s.mu.Lock()
					s.ended = true
					s.mu.Unlock()
					s.event("ended", excerpt(mustJSON(ev.msg)))
					return finish()
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

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// toolCallOf reads a tool call from a name or an object.
func toolCallOf(v any) (target.ToolCall, bool) {
	switch tc := v.(type) {
	case string:
		return target.ToolCall{Name: tc}, tc != ""
	case map[string]any:
		call := target.ToolCall{Name: asString(firstOf(tc, "name", "tool", "function"))}
		if fn, ok := tc["function"].(map[string]any); ok {
			call.Name = asString(firstOf(fn, "name"))
			if args, ok := firstOf(fn, "arguments", "args", "parameters").(map[string]any); ok {
				call.Args = args
			} else if str, ok := firstOf(fn, "arguments").(string); ok {
				var m map[string]any
				if json.Unmarshal([]byte(str), &m) == nil {
					call.Args = m
				}
			}
		}
		if call.Args == nil {
			if args, ok := firstOf(tc, "args", "arguments", "input", "parameters").(map[string]any); ok {
				call.Args = args
			}
		}
		return call, call.Name != ""
	}
	return target.ToolCall{}, false
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
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
		return x != "" && x != "false"
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

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(context.Context) (target.AgentTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("voice: Receive without a Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close sends the close message, ends the connection and returns the
// artifacts.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return target.Artifacts{}, nil
	}
	s.closed = true
	ended := s.ended
	s.mu.Unlock()
	if !ended && s.t.cfg.Messages.Close != nil {
		if b, err := s.payload(s.t.cfg.Messages.Close, nil); err == nil {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = s.conn.Write(sctx, websocket.MessageText, b)
			cancel()
			s.event("close_message", "sent")
		}
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
	sttMs  []float64
	userMs float64
	agMs   float64
	// interrupts are the barge-in stop times: from the caller speaking over
	// the agent to its user-interrupted message (or to the end of the
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
	how := "the agent stopped (user-interrupted)"
	switch {
	case !interruptedAt.IsZero():
		stop = interruptedAt.Sub(sp.startedAt)
	case !s.first.IsZero():
		stop = s.first.Add(s.played).Sub(sp.startedAt)
		how = "the agent never signalled a stop; its queued audio played out"
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
		return nil, fmt.Errorf("voice: synthesize: %w", err)
	}
	pcm := audio.Resample(audio.PCM{Rate: out.Rate, Samples: out.Samples}, s.rate)
	samples := append(pcm.Samples, make([]int16, audio.SampleCount(silenceTail, s.rate))...)
	chunk := s.rate * chunkMs / 1000
	tick := time.NewTicker(chunkMs * time.Millisecond)
	defer tick.Stop()
	for i := 0; i < len(samples); i += chunk {
		end := min(i+chunk, len(samples))
		if err := s.writeAudio(ctx, samples[i:end]); err != nil {
			return nil, fmt.Errorf("voice: audio: %w", err)
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

// finishAudio sets the turn's timing and its text: the agent's own
// final transcripts when it sent any (transcript_source auto or agent),
// else tamesu's transcription of its audio, through the stream that heard
// it as it arrived when there is one.
func (s *session) finishAudio(ctx context.Context, sp *spokenTurn, agent *target.AgentTurn, transcript string, stt target.TranscriptionStream) {
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
		if stt != nil {
			_, _ = stt.Close(ctx)
		}
		return
	}
	started := time.Now()
	var tr target.Transcript
	var err error
	if stt != nil {
		tr, err = stt.Close(ctx)
	} else {
		tr, err = a.speech.Transcribe(ctx, target.TranscribeRequest{Audio: target.Audio{Rate: s.rate, Samples: turn}, Language: a.voice.Language})
	}
	s.mu.Lock()
	a.sttMs = append(a.sttMs, float64(time.Since(started).Microseconds())/1000)
	s.mu.Unlock()
	if err != nil {
		s.event("stt_error", err.Error())
		return
	}
	agent.Text = strings.TrimSpace(tr.Text)
	agent.TranscriptSource = "stt"
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
	metrics.Summarize(s.metrics, metrics.STTMs, a.sttMs)
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
