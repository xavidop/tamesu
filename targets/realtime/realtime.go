// Package realtime is the target for agents that speak the OpenAI Realtime
// protocol over a websocket: the Realtime API itself (your instructions and
// tools as the agent) or a server that fronts your agent with the same
// events. Text mode sends each user turn as a conversation item and reads the
// response's text deltas; audio mode speaks the turn as 24 kHz PCM and reads
// the response's audio and transcript. Function calls are recorded, not
// executed; a call to the done tool ends the session. Timestamps come from
// the transport.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/xavidop/tamesu/target"
)

// Defaults.
const (
	DefaultURL      = "wss://api.openai.com/v1/realtime"
	DefaultDoneTool = "end_session"
	DefaultTimeout  = 60 * time.Second
	// HandshakeTimeout bounds the wait for session.updated after connecting;
	// a server that never sends it is given up on, not failed.
	HandshakeTimeout = 3 * time.Second
)

// Protocol dialects: the GA event names (gpt-realtime, session.type
// "realtime", output_modalities) or the beta ones (modalities, the
// OpenAI-Beta: realtime=v1 header).
const (
	ProtocolGA   = "ga"
	ProtocolBeta = "beta"
)

// Modes.
const (
	ModeText  = "text"
	ModeAudio = "audio"
)

// Output audio formats of audio mode.
const (
	FormatPCM16    = "pcm16"
	FormatG711ULaw = "g711_ulaw"
	FormatG711ALaw = "g711_alaw"
	// G711Rate is the sample rate of G.711 audio.
	G711Rate = 8000
)

// AudioFormats are the accepted values of Config.AudioFormat.
var AudioFormats = []string{FormatPCM16, FormatG711ULaw, FormatG711ALaw}

// outputFormatGA is the GA session's audio.output.format for an AudioFormat.
func outputFormatGA(format string) map[string]any {
	switch format {
	case FormatG711ULaw:
		return map[string]any{"type": "audio/pcmu"}
	case FormatG711ALaw:
		return map[string]any{"type": "audio/pcma"}
	}
	return map[string]any{"type": "audio/pcm", "rate": AudioRate}
}

// ErrNoSpeech is returned by Start in audio mode when the run has no speech
// layer.
var ErrNoSpeech = errors.New("audio mode needs the speech models: set speech.tts.model in tamesu.yaml")

// Config describes a Realtime agent. String fields are templates rendered
// with the run's scope ({{secrets.X}}, {{vars.x}}).
type Config struct {
	// URL is the websocket endpoint; empty means DefaultURL.
	URL string
	// Model is added as ?model= when the URL has none.
	Model string
	// Headers go on the handshake: Authorization: Bearer {{secrets.OPENAI_API_KEY}}.
	Headers map[string]string
	// SessionUpdate is merged over the mode's defaults into session.update
	// (instructions, tools, voice, output modalities...).
	SessionUpdate map[string]any
	// Protocol is ga (default) or beta.
	Protocol string
	// Mode is text (default) or audio.
	Mode string
	// DoneTool is the function whose call ends the session (default end_session).
	DoneTool string
	// ToolResults answers the agent's function calls, by function name: the
	// value (any JSON value; string leaves may use {{args.<key>}}) goes back
	// as a function_call_output and the response continues. Calls to
	// functions not listed, and to DoneTool, are recorded only.
	ToolResults map[string]any
	// AudioFormat is the agent's output audio in audio mode: FormatPCM16
	// (default), FormatG711ULaw or FormatG711ALaw.
	AudioFormat string
	// Timeout bounds one turn (and the handshake); zero means DefaultTimeout.
	Timeout time.Duration
	// Client overrides the HTTP client of the handshake.
	Client *http.Client
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.URL == "" {
		cfg.URL = DefaultURL
	}
	if cfg.Protocol == "" {
		cfg.Protocol = ProtocolGA
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeText
	}
	if cfg.DoneTool == "" {
		cfg.DoneTool = DefaultDoneTool
	}
	if cfg.AudioFormat == "" {
		cfg.AudioFormat = FormatPCM16
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Target{name: name, cfg: cfg}
}

// Target is the Realtime target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// event is one server event, or the error that ended the connection.
type event struct {
	data map[string]any
	raw  []byte
	err  error
}

type session struct {
	t    *Target
	run  *target.RunContext
	conn *websocket.Conn
	// events is fed by the reader goroutine; stop ends it.
	events chan event
	stop   context.CancelFunc

	mu       sync.Mutex
	pending  *target.AgentTurn
	closed   bool
	turns    int
	received int
	metrics  map[string]float64
	evs      []target.Event
	tokensIn float64
	tokensOu float64
	audio    *audioState
}

// Start connects, sends session.update and waits briefly for session.updated.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if t.cfg.Mode == ModeAudio && run.Speech == nil {
		return nil, fmt.Errorf("realtime %s: %w", t.name, ErrNoSpeech)
	}
	s := &session{t: t, run: run, metrics: map[string]float64{}}
	u, err := run.Render(t.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("realtime %s: url: %w", t.name, err)
	}
	if t.cfg.Model != "" {
		if u, err = withModel(u, t.cfg.Model); err != nil {
			return nil, fmt.Errorf("realtime %s: url: %w", t.name, err)
		}
	}
	hdr := http.Header{}
	for k, v := range t.cfg.Headers {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("realtime %s: header %s: %w", t.name, k, err)
		}
		hdr.Set(k, rv)
	}
	if t.cfg.Protocol == ProtocolBeta && hdr.Get("OpenAI-Beta") == "" {
		hdr.Set("OpenAI-Beta", "realtime=v1")
	}
	dctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	conn, _, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: t.cfg.Client, HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("realtime %s: connect %s: %w", t.name, redactQuery(u), err)
	}
	conn.SetReadLimit(64 << 20)
	s.conn = conn
	s.events = make(chan event)
	readCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = stop
	go s.read(readCtx)
	if t.cfg.Mode == ModeAudio {
		s.startAudio(run)
	}
	if err := s.write(dctx, map[string]any{"type": "session.update", "session": s.sessionConfig()}); err != nil {
		_ = s.closeConn()
		return nil, fmt.Errorf("realtime %s: session.update: %w", t.name, err)
	}
	// session.created and session.updated, or an error that rejects the
	// session; a server that sends neither within HandshakeTimeout is fine.
	hctx, hcancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer hcancel()
	for {
		select {
		case <-hctx.Done():
			s.event("handshake", "no session.updated within "+HandshakeTimeout.String())
			return s, nil
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				_ = s.closeConn()
				return nil, fmt.Errorf("realtime %s: connection closed during the handshake: %w", t.name, errOr(ev.err))
			}
			switch typ := ev.data["type"]; typ {
			case "session.updated":
				return s, nil
			case "session.created":
				s.event("session_created", "")
			case "error":
				_ = s.closeConn()
				return nil, fmt.Errorf("realtime %s: %s", t.name, errorMessage(ev.data))
			default:
				s.unsolicited(ev)
			}
		}
	}
}

// sessionConfig is the session object of session.update: the mode's defaults
// for the protocol, with the suite's session_update merged on top.
func (s *session) sessionConfig() map[string]any {
	cfg := s.t.cfg
	var base map[string]any
	switch {
	case cfg.Protocol == ProtocolBeta && cfg.Mode == ModeAudio:
		base = map[string]any{"modalities": []any{"audio", "text"}, "input_audio_format": "pcm16", "output_audio_format": cfg.AudioFormat, "turn_detection": nil}
	case cfg.Protocol == ProtocolBeta:
		base = map[string]any{"modalities": []any{"text"}, "turn_detection": nil}
	case cfg.Mode == ModeAudio:
		base = map[string]any{"type": "realtime", "output_modalities": []any{"audio"}, "audio": map[string]any{
			"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": AudioRate}, "turn_detection": nil},
			"output": map[string]any{"format": outputFormatGA(cfg.AudioFormat)},
		}}
	default:
		base = map[string]any{"type": "realtime", "output_modalities": []any{"text"}}
	}
	return merge(base, cfg.SessionUpdate)
}

// merge returns base with over merged on top, recursively for maps.
func merge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if om, ok := v.(map[string]any); ok {
			if bm, ok := out[k].(map[string]any); ok {
				out[k] = merge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// read feeds the events channel until the connection ends or ctx is done.
func (s *session) read(ctx context.Context) {
	defer close(s.events)
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			select {
			case s.events <- event{err: err}:
			case <-ctx.Done():
			}
			return
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			m = map[string]any{"type": "unparsable", "raw": string(data)}
		}
		select {
		case s.events <- event{data: m, raw: data}:
		case <-ctx.Done():
			return
		}
	}
}

func (s *session) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// Send creates the user's item, asks for a response and reads it.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("realtime: session is closed")
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
	} else if turn.Text != "" {
		item := map[string]any{"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": turn.Text}},
		}}
		if err := s.write(tctx, item); err != nil {
			return fmt.Errorf("realtime: conversation.item.create: %w", err)
		}
	}
	if err := s.write(tctx, map[string]any{"type": "response.create"}); err != nil {
		return fmt.Errorf("realtime: response.create: %w", err)
	}
	agent, calls, err := s.readTurn(tctx, started, spoken)
	if err != nil {
		return fmt.Errorf("realtime: %w", err)
	}
	// Answer the function calls that have a configured result, and read the
	// response that follows, up to maxToolRounds times.
	for round := 0; round < maxToolRounds && !agent.Done; round++ {
		answered := 0
		for i, call := range calls {
			out, ok := s.toolOutput(call.call)
			if !ok {
				continue
			}
			item := map[string]any{"type": "conversation.item.create", "item": map[string]any{"type": "function_call_output", "call_id": call.id, "output": out}}
			if err := s.write(tctx, item); err != nil {
				return fmt.Errorf("realtime: function_call_output: %w", err)
			}
			s.event("tool_result", call.call.Name)
			answered++
			_ = i
		}
		if answered == 0 {
			break
		}
		if err := s.write(tctx, map[string]any{"type": "response.create"}); err != nil {
			return fmt.Errorf("realtime: response.create: %w", err)
		}
		next, nextCalls, err := s.readTurn(tctx, started, spoken)
		if err != nil {
			return fmt.Errorf("realtime: %w", err)
		}
		mergeTurn(&agent, next)
		calls = nextCalls
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

// idCall is a recorded function call with the call id its answer needs.
type idCall struct {
	id   string
	call target.ToolCall
}

// readTurn assembles the agent's turn from the events up to response.done,
// returning the function calls of that response with their ids.
func (s *session) readTurn(ctx context.Context, started time.Time, spoken *spokenTurn) (target.AgentTurn, []idCall, error) {
	agent := target.AgentTurn{}
	var text strings.Builder
	var raw []any
	var calls []idCall
	seen := map[string]bool{}
	var plan *interruptPlan
	if s.audio != nil {
		plan = s.audio.planInterrupt()
	}
	var itemID string
	for {
		select {
		case <-ctx.Done():
			return agent, calls, fmt.Errorf("no response.done within %s: %w", s.t.cfg.Timeout, ctx.Err())
		case ev, ok := <-s.events:
			if !ok || ev.err != nil {
				return agent, calls, fmt.Errorf("connection closed by the agent: %w", errOr(ev.err))
			}
			s.mu.Lock()
			s.received++
			s.mu.Unlock()
			raw = append(raw, ev.data)
			typ, _ := ev.data["type"].(string)
			switch typ {
			case "response.output_text.delta", "response.text.delta",
				"response.output_audio_transcript.delta", "response.audio_transcript.delta":
				delta, _ := ev.data["delta"].(string)
				if agent.FirstTokenAt.IsZero() && delta != "" {
					agent.FirstTokenAt = time.Now()
				}
				text.WriteString(delta)
			case "response.output_audio.delta", "response.audio.delta":
				if agent.FirstTokenAt.IsZero() {
					agent.FirstTokenAt = time.Now()
				}
				if s.audio != nil {
					s.agentAudio(ev.data, spoken)
					if plan != nil && plan.due(spoken) {
						s.interrupt(ctx, plan, itemID, spoken)
						agent.Interrupted = true
					}
				}
			case "response.output_item.added":
				if item, ok := ev.data["item"].(map[string]any); ok && item["type"] == "message" {
					itemID, _ = item["id"].(string)
				}
			case "response.function_call_arguments.done":
				calls = s.toolCall(&agent, calls, seen, ev.data["call_id"], ev.data["name"], ev.data["arguments"])
			case "response.output_item.done":
				if item, ok := ev.data["item"].(map[string]any); ok && item["type"] == "function_call" {
					calls = s.toolCall(&agent, calls, seen, item["call_id"], item["name"], item["arguments"])
				}
			case "response.done":
				resp, _ := ev.data["response"].(map[string]any)
				status, _ := resp["status"].(string)
				s.usage(resp)
				if status == "failed" {
					msg := "the response failed"
					if d, ok := resp["status_details"].(map[string]any); ok {
						if e, ok := d["error"].(map[string]any); ok {
							msg = fmt.Sprintf("the response failed: %s", strOf(e["message"], e["code"], e["type"]))
						}
					}
					return agent, calls, errors.New(msg)
				}
				if status != "" && status != "completed" && !(agent.Interrupted && status == "cancelled") {
					s.event("response_"+status, "")
				}
				if plan != nil && !plan.cancelledAt.IsZero() {
					s.audio.interrupted(plan)
				}
				agent.Text = text.String()
				agent.Raw = raw
				if s.audio != nil {
					s.finishAudio(spoken, &agent)
				}
				return agent, calls, nil
			case "error":
				return agent, calls, errors.New(errorMessage(ev.data))
			default:
				s.unsolicited(ev)
			}
		}
	}
}

// toolCall records a function call once per call id; the done tool ends the
// session.
func (s *session) toolCall(agent *target.AgentTurn, calls []idCall, seen map[string]bool, callID, name, args any) []idCall {
	id, _ := callID.(string)
	n, _ := name.(string)
	if n == "" || (id != "" && seen[id]) {
		return calls
	}
	seen[id] = true
	call := target.ToolCall{Name: n}
	switch a := args.(type) {
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(a), &m) == nil {
			call.Args = m
		}
	case map[string]any:
		call.Args = a
	}
	agent.ToolCalls = append(agent.ToolCalls, call)
	s.event("tool_call", n)
	if n == s.t.cfg.DoneTool {
		agent.Done = true
	}
	return append(calls, idCall{id: id, call: call})
}

// interrupt makes the caller talk over the agent: the response is cancelled
// (and the spoken item truncated to what was heard) and the turn ends when
// the server confirms.
func (s *session) interrupt(ctx context.Context, plan *interruptPlan, itemID string, sp *spokenTurn) {
	plan.cancelledAt = time.Now()
	s.event("interruption", "the caller spoke over the agent")
	_ = s.write(ctx, map[string]any{"type": "response.cancel"})
	if itemID != "" && sp != nil && !sp.firstAudio.IsZero() {
		_ = s.write(ctx, map[string]any{"type": "conversation.item.truncate", "item_id": itemID, "content_index": 0,
			"audio_end_ms": plan.cancelledAt.Sub(sp.firstAudio).Milliseconds()})
	}
}

// usage adds the response's token usage to the session's metrics.
func (s *session) usage(resp map[string]any) {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := u["input_tokens"].(float64); ok {
		s.tokensIn += v
	}
	if v, ok := u["output_tokens"].(float64); ok {
		s.tokensOu += v
	}
}

// unsolicited notes server events outside a turn's contract.
func (s *session) unsolicited(ev event) {
	typ, _ := ev.data["type"].(string)
	switch typ {
	case "unparsable":
		s.event("unparsable_message", fmt.Sprint(ev.data["raw"]))
	case "conversation.item.input_audio_transcription.completed":
		if t, ok := ev.data["transcript"].(string); ok {
			s.event("user_transcript", t)
		}
	}
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("realtime: Receive without a pending Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close ends the connection and returns the artifacts: metrics (agent token
// usage, messages, the audio metrics) and the stereo WAV in audio mode.
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
	if s.tokensIn > 0 || s.tokensOu > 0 {
		arts.Metrics["agent_input_tokens"] = s.tokensIn
		arts.Metrics["agent_output_tokens"] = s.tokensOu
	}
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

// withModel adds ?model= when the URL has no model parameter.
func withModel(raw, model string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if q.Get("model") == "" {
		q.Set("model", model)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// redactQuery drops the query string of a URL for error messages.
func redactQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func errorMessage(ev map[string]any) string {
	e, _ := ev["error"].(map[string]any)
	if e == nil {
		return "the agent reported an error"
	}
	return "the agent reported an error: " + strOf(e["message"], e["code"], e["type"])
}

// strOf returns the first non-empty string of the values.
func strOf(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "unknown"
}

func errOr(err error) error {
	if err == nil {
		return errors.New("closed")
	}
	return err
}
