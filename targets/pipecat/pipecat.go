// Package pipecat is the pipecat target: a Pipecat agent behind its
// websocket transport with the protobuf frame serializer. Text mode sends
// each user turn as an RTVI send-text message and reads the bot's text
// messages; audio mode streams the spoken turn as AudioRawFrames and reads
// the bot's audio and transcripts. Function calls are recorded, and answered
// from ToolResults.
package pipecat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/pipecat/frames"
)

// Defaults.
const (
	DefaultSampleRate = 16000
	DefaultIdle       = 2 * time.Second
	DefaultTimeout    = 60 * time.Second
	// ReadyTimeout bounds the wait for bot-ready after client-ready; a
	// transport that never sends it is given up on, not failed.
	ReadyTimeout = 3 * time.Second
	// maxToolRounds bounds the function-call answers within one turn.
	maxToolRounds = 5
)

// Modes.
const (
	ModeText  = "text"
	ModeAudio = "audio"
)

// ErrNoSpeech is returned by Start in audio mode when the run has no speech
// layer.
var ErrNoSpeech = errors.New("audio mode needs the speech models: set speech.tts.model in tamesu.yaml")

// Config is the pipecat target's configuration.
type Config struct {
	// URL is the transport's websocket endpoint.
	URL string
	// Headers go on the handshake.
	Headers map[string]string
	// Mode is text (default) or audio.
	Mode string
	// SampleRate is the transport's audio rate in Hz (default 16000).
	SampleRate int
	// Idle ends a turn after this silence when no bot-stopped-speaking or
	// bot-llm-stopped arrives (default 2s).
	Idle time.Duration
	// Timeout bounds one turn (default 60s).
	Timeout time.Duration
	// ToolResults answers the bot's function calls by name (string leaves
	// render {{args.<key>}}).
	ToolResults map[string]any
	// Client overrides the HTTP client of the handshake.
	Client *http.Client
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Mode == "" {
		cfg.Mode = ModeText
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
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the pipecat target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// event is one decoded frame from the transport, or the error that ended
// the connection.
type event struct {
	msg   *rtviMessage // an RTVI message (MessageFrame)
	audio []int16      // bot audio (AudioRawFrame)
	rate  int
	text  string // a TextFrame or TranscriptionFrame
	kind  string // "message", "audio", "text", "transcription", "interruption", "unparsable"
	err   error
}

type session struct {
	t      *Target
	run    *target.RunContext
	conn   *websocket.Conn
	events chan event
	stop   context.CancelFunc

	mu       sync.Mutex
	pending  *target.AgentTurn
	closed   bool
	turns    int
	received int
	metrics  map[string]float64
	evs      []target.Event
	audio    *audioState
}

// Start connects, sends client-ready and waits briefly for bot-ready.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if t.cfg.Mode == ModeAudio && run.Speech == nil {
		return nil, fmt.Errorf("pipecat %s: %w", t.name, ErrNoSpeech)
	}
	u, err := run.Render(t.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("pipecat %s: url: %w", t.name, err)
	}
	hdr := http.Header{}
	for k, v := range t.cfg.Headers {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("pipecat %s: header %s: %w", t.name, k, err)
		}
		hdr.Set(k, rv)
	}
	dctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	conn, _, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: t.cfg.Client, HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("pipecat %s: connect %s: %w", t.name, u, err)
	}
	conn.SetReadLimit(64 << 20)
	s := &session{t: t, run: run, conn: conn, metrics: map[string]float64{}, events: make(chan event)}
	readCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = stop
	go s.read(readCtx)
	if t.cfg.Mode == ModeAudio {
		s.startAudio(run)
	}
	if err := s.sendRTVI(dctx, newRTVI("client-ready", map[string]any{"version": "1.0.0", "about": map[string]any{"library": "tamesu"}})); err != nil {
		_ = s.closeConn()
		return nil, fmt.Errorf("pipecat %s: client-ready: %w", t.name, err)
	}
	rctx, rcancel := context.WithTimeout(ctx, ReadyTimeout)
	defer rcancel()
	for {
		select {
		case <-rctx.Done():
			s.event("handshake", "no bot-ready within "+ReadyTimeout.String())
			return s, nil
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				_ = s.closeConn()
				return nil, fmt.Errorf("pipecat %s: connection closed during the handshake: %w", t.name, errOr(ev.err))
			}
			if ev.msg != nil && ev.msg.Type == "bot-ready" {
				s.event("bot_ready", "")
				return s, nil
			}
			s.unsolicited(ev)
		}
	}
}

// read decodes frames into events until the connection ends.
func (s *session) read(ctx context.Context) {
	defer close(s.events)
	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			select {
			case s.events <- event{err: err}:
			case <-ctx.Done():
			}
			return
		}
		ev := decode(typ, data)
		select {
		case s.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// decode turns one websocket message into an event: a protobuf Frame, or a
// bare RTVI JSON message from a transport that sends text.
func decode(typ websocket.MessageType, data []byte) event {
	if typ == websocket.MessageText {
		if m, ok := parseRTVI(data); ok {
			return event{kind: "message", msg: &m}
		}
		return event{kind: "unparsable", text: string(data)}
	}
	var f frames.Frame
	if err := proto.Unmarshal(data, &f); err != nil {
		return event{kind: "unparsable", text: fmt.Sprintf("%d bytes", len(data))}
	}
	switch x := f.Frame.(type) {
	case *frames.Frame_Message:
		if m, ok := parseRTVI([]byte(x.Message.GetData())); ok {
			return event{kind: "message", msg: &m}
		}
		return event{kind: "unparsable", text: x.Message.GetData()}
	case *frames.Frame_Audio:
		return event{kind: "audio", audio: pcmOf(x.Audio.GetAudio(), int(x.Audio.GetNumChannels())), rate: int(x.Audio.GetSampleRate())}
	case *frames.Frame_Text:
		return event{kind: "text", text: x.Text.GetText()}
	case *frames.Frame_Transcription:
		return event{kind: "transcription", text: x.Transcription.GetText()}
	case *frames.Frame_Interruption:
		return event{kind: "interruption"}
	}
	return event{kind: "unparsable", text: "empty frame"}
}

// pcmOf reads little-endian PCM16, mixing channels down to mono.
func pcmOf(b []byte, channels int) []int16 {
	if channels <= 0 {
		channels = 1
	}
	n := len(b) / 2 / channels
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		var sum int
		for c := 0; c < channels; c++ {
			j := 2 * (i*channels + c)
			sum += int(int16(uint16(b[j]) | uint16(b[j+1])<<8))
		}
		out[i] = int16(sum / channels)
	}
	return out
}

func (s *session) sendRTVI(ctx context.Context, m rtviMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.sendFrame(ctx, &frames.Frame{Frame: &frames.Frame_Message{Message: &frames.MessageFrame{Data: string(b)}}})
}

func (s *session) sendFrame(ctx context.Context, f *frames.Frame) error {
	b, err := proto.Marshal(f)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageBinary, b)
}

// Send sends the user's turn and reads the bot's reply.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("pipecat: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, s.t.cfg.Timeout)
	defer cancel()
	started := time.Now()
	var spoken *spokenTurn
	if s.t.cfg.Mode == ModeAudio {
		var err error
		if spoken, err = s.speak(tctx, turn, started); err != nil {
			return err
		}
	} else {
		msg := newRTVI("send-text", map[string]any{"content": turn.Text, "options": map[string]any{"run_immediately": true, "audio_response": false}})
		if err := s.sendRTVI(tctx, msg); err != nil {
			return fmt.Errorf("pipecat: send-text: %w", err)
		}
	}
	agent, err := s.readTurn(tctx, spoken)
	if err != nil {
		return fmt.Errorf("pipecat: %w", err)
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

// turnText collects the bot's words from the message kinds a pipeline may
// emit, preferring the LLM's own text.
type turnText struct {
	llm, tts, transcription []string
}

func (tt *turnText) String() string {
	switch {
	case len(tt.llm) > 0:
		return strings.TrimSpace(strings.Join(tt.llm, ""))
	case len(tt.tts) > 0:
		return strings.TrimSpace(strings.Join(tt.tts, " "))
	}
	return strings.TrimSpace(strings.Join(tt.transcription, " "))
}

// readTurn reads events until the bot is done with the turn: bot-llm-stopped
// (text mode), bot-stopped-speaking, or Idle after its last message or audio.
// Function calls with a configured result are answered and the turn goes on.
func (s *session) readTurn(ctx context.Context, spoken *spokenTurn) (target.AgentTurn, error) {
	agent := target.AgentTurn{}
	var text turnText
	var raw []any
	seen := map[string]bool{}
	rounds := 0
	var last time.Time // the bot's last sign of life this turn
	idle := time.NewTimer(s.t.cfg.Timeout)
	defer idle.Stop()
	finish := func() (target.AgentTurn, error) {
		agent.Text = text.String()
		agent.Raw = raw
		if s.audio != nil {
			s.finishAudio(ctx, spoken, &agent, agent.Text == "")
		}
		return agent, nil
	}
	for {
		select {
		case <-ctx.Done():
			if !last.IsZero() {
				return finish()
			}
			return agent, fmt.Errorf("the bot did not answer within %s: %w", s.t.cfg.Timeout, ctx.Err())
		case <-idle.C:
			return finish()
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				if !last.IsZero() {
					return finish()
				}
				return agent, fmt.Errorf("connection closed by the bot: %w", errOr(ev.err))
			}
			s.mu.Lock()
			s.received++
			s.mu.Unlock()
			alive := true
			switch ev.kind {
			case "audio":
				if agent.FirstTokenAt.IsZero() {
					agent.FirstTokenAt = time.Now()
				}
				if s.audio != nil {
					s.agentAudio(ev, spoken)
				}
			case "text":
				raw = append(raw, map[string]any{"text": ev.text})
				text.llm = append(text.llm, ev.text)
				if agent.FirstTokenAt.IsZero() {
					agent.FirstTokenAt = time.Now()
				}
			case "transcription":
				raw = append(raw, map[string]any{"transcription": ev.text})
				s.event("user_transcript", ev.text)
				alive = false
			case "interruption":
				s.event("bot_interrupted", "")
			case "message":
				m := ev.msg
				raw = append(raw, map[string]any{"type": m.Type, "data": m.Data})
				switch m.Type {
				case "bot-llm-text":
					if agent.FirstTokenAt.IsZero() && m.text() != "" {
						agent.FirstTokenAt = time.Now()
					}
					text.llm = append(text.llm, m.text())
				case "bot-tts-text":
					text.tts = append(text.tts, m.text())
				case "bot-transcription":
					text.transcription = append(text.transcription, m.text())
				case "llm-function-call", "llm-function-call-in-progress", "llm-function-call-started":
					call, id, ok := toolCallOf(*m)
					if ok && !seen[id] {
						seen[id] = true
						agent.ToolCalls = append(agent.ToolCalls, call)
						s.event("tool_call", call.Name)
						if m.Type == "llm-function-call" && rounds < maxToolRounds {
							if out, ok := s.toolOutput(call); ok {
								rounds++
								res := newRTVI("llm-function-call-result", map[string]any{"function_name": call.Name, "tool_call_id": id, "arguments": call.Args, "result": out})
								if err := s.sendRTVI(ctx, res); err != nil {
									return agent, fmt.Errorf("llm-function-call-result: %w", err)
								}
								s.event("tool_result", call.Name)
							}
						}
					}
				case "bot-llm-stopped":
					if s.t.cfg.Mode == ModeText {
						return finish()
					}
				case "bot-stopped-speaking":
					return finish()
				case "bot-interrupted":
					s.event("bot_interrupted", "")
				case "error":
					return agent, errors.New(errorOf(*m))
				case "user-transcription", "user-started-speaking", "user-stopped-speaking", "vad-user-started-speaking", "vad-user-stopped-speaking", "bot-ready", "metrics", "bot-llm-started", "bot-tts-started", "bot-tts-stopped", "bot-started-speaking", "bot-output", "bot-llm-marker", "bot-audio-level", "user-audio-level":
					alive = m.Type != "metrics" && !strings.HasPrefix(m.Type, "user-") && !strings.HasPrefix(m.Type, "vad-")
				}
			default:
				s.unsolicited(ev)
				alive = false
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

// toolCallOf reads a function call message: its name, arguments and id.
func toolCallOf(m rtviMessage) (target.ToolCall, string, bool) {
	name, _ := m.Data["function_name"].(string)
	if name == "" {
		return target.ToolCall{}, "", false
	}
	id, _ := m.Data["tool_call_id"].(string)
	args, _ := m.Data["args"].(map[string]any)
	if args == nil {
		args, _ = m.Data["arguments"].(map[string]any)
	}
	if id == "" {
		id = name + ":" + fmt.Sprint(args)
	}
	return target.ToolCall{Name: name, Args: args}, id, true
}

func errorOf(m rtviMessage) string {
	if e, ok := m.Data["error"].(string); ok {
		return "the bot reported an error: " + e
	}
	if e, ok := m.Data["message"].(string); ok {
		return "the bot reported an error: " + e
	}
	return "the bot reported an error"
}

func (s *session) unsolicited(ev event) {
	if ev.kind == "unparsable" {
		s.event("unparsable_message", ev.text)
	}
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// Receive returns the bot's turn for the last Send.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("pipecat: Receive without a pending Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close ends the connection and returns the artifacts.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return target.Artifacts{}, nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.closeConn()
	s.mu.Lock()
	defer s.mu.Unlock()
	arts := target.Artifacts{Metrics: s.metrics, Files: map[string]string{}}
	arts.Metrics["ws_messages_received"] = float64(s.received)
	arts.Metrics["turns"] = float64(s.turns)
	if s.audio != nil {
		s.closeAudioLocked(arts.Files)
	}
	arts.Events = s.evs
	return arts, nil
}

func (s *session) closeConn() error {
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close(websocket.StatusNormalClosure, "done")
	s.stop()
	s.conn = nil
	return err
}

func errOr(err error) error {
	if err == nil {
		return errors.New("stream closed")
	}
	return err
}
