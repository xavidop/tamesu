// Package twilio is the twilio target: a voice agent behind Twilio Media
// Streams, tested without a phone number. tamesu plays Twilio's side of the
// websocket: it connects to the agent's stream URL, sends connected and
// start, streams the simulated caller's speech as μ-law media events, sends
// dtmf events, answers the agent's marks once their audio has "played",
// honours clear, and reads the agent's media as its speech. Audio only, at
// 8 kHz μ-law, as on the phone.
package twilio

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/g711"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/target"
)

// Defaults and constants of the Media Streams protocol.
const (
	Rate           = 8000
	DefaultIdle    = 2 * time.Second
	DefaultTimeout = 60 * time.Second
	// GreetingTimeout bounds the wait for the agent's opening words after
	// start; an agent that waits for the caller is given up on, not failed.
	GreetingTimeout = 3 * time.Second
	// WAVFile is the stereo artifact: left the caller, right the agent.
	WAVFile = "audio.wav"
	chunkMs = 20
	// silenceTail is appended after the caller's words so the agent's voice
	// activity detector sees the end of the utterance.
	silenceTail = 600 * time.Millisecond
)

// ErrNoSpeech is returned by Start when the run has no speech layer.
var ErrNoSpeech = errors.New("the twilio target needs the speech models: set speech.tts.model and speech.stt.model in tamesu.yaml")

// Config is the target's configuration.
type Config struct {
	// URL is the agent's Media Streams websocket (the <Stream url="..."> of
	// its TwiML).
	URL string
	// Headers go on the handshake.
	Headers map[string]string
	// Parameters are the customParameters of the start event (the <Parameter>
	// elements of the TwiML).
	Parameters map[string]string
	// From and To are the call's numbers, passed in customParameters as
	// From and To when set, since agents often read them from there.
	From, To string
	// Idle ends a turn after this silence from the agent (default 2s).
	Idle time.Duration
	// Timeout bounds one turn (default 60s).
	Timeout time.Duration
	// Client overrides the HTTP client of the handshake.
	Client *http.Client
	// Connect is how connecting to the agent is retried.
	Connect target.ConnectPolicy
}

// New returns the target.
func New(name string, cfg Config) target.Target {
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

// Target is the twilio target.
type Target struct {
	name string
	cfg  Config
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// message is a Media Streams event, in either direction.
type message struct {
	Event          string            `json:"event"`
	SequenceNumber string            `json:"sequenceNumber,omitempty"`
	StreamSid      string            `json:"streamSid,omitempty"`
	Protocol       string            `json:"protocol,omitempty"`
	Version        string            `json:"version,omitempty"`
	Start          *startPayload     `json:"start,omitempty"`
	Media          *mediaPayload     `json:"media,omitempty"`
	Mark           *markPayload      `json:"mark,omitempty"`
	DTMF           *dtmfPayload      `json:"dtmf,omitempty"`
	Stop           map[string]string `json:"stop,omitempty"`
}

type startPayload struct {
	AccountSid       string            `json:"accountSid"`
	StreamSid        string            `json:"streamSid"`
	CallSid          string            `json:"callSid"`
	Tracks           []string          `json:"tracks"`
	CustomParameters map[string]string `json:"customParameters"`
	MediaFormat      mediaFormat       `json:"mediaFormat"`
}

type mediaFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

type mediaPayload struct {
	Track     string `json:"track,omitempty"`
	Chunk     string `json:"chunk,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Payload   string `json:"payload"`
}

type markPayload struct {
	Name string `json:"name"`
}

type dtmfPayload struct {
	Track string `json:"track"`
	Digit string `json:"digit"`
}

// event is one decoded message from the agent, or the error that ended the
// connection.
type event struct {
	msg   *message
	audio []int16
	err   error
	// at is when the message arrived, so a turn read after the fact keeps
	// its timing.
	at time.Time
}

type session struct {
	closeOnce sync.Once
	t         *Target
	run       *target.RunContext
	conn      *websocket.Conn
	events    chan event
	stop      context.CancelFunc
	streamSid string
	callSid   string

	mu       sync.Mutex
	closed   bool
	pending  *target.AgentTurn
	seq      int
	chunk    int
	turns    int
	marks    int
	clears   int
	received int
	metrics  map[string]float64
	evs      []target.Event
	audio    *audioState

	connectedAt time.Time
	greetingAt  time.Time
	// played is how much agent audio has arrived, as wall time at Rate; the
	// mark replies are scheduled against it.
	played time.Duration
	first  time.Time
}

// Start connects, sends connected and start, and reads the greeting if one
// comes.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if run.Speech == nil {
		return nil, fmt.Errorf("twilio %s: %w", t.name, ErrNoSpeech)
	}
	u, err := run.Render(t.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("twilio %s: url: %w", t.name, err)
	}
	hdr := http.Header{}
	for k, v := range t.cfg.Headers {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("twilio %s: header %s: %w", t.name, k, err)
		}
		hdr.Set(k, rv)
	}
	params := map[string]string{}
	for k, v := range t.cfg.Parameters {
		rv, err := run.Render(v)
		if err != nil {
			return nil, fmt.Errorf("twilio %s: parameter %s: %w", t.name, k, err)
		}
		params[k] = rv
	}
	if t.cfg.From != "" {
		params["From"] = t.cfg.From
	}
	if t.cfg.To != "" {
		params["To"] = t.cfg.To
	}
	conn, err := target.Connect(ctx, t.cfg.Connect, run.Logger, "twilio "+t.name, func(ctx context.Context) (*websocket.Conn, error) {
		dctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
		defer cancel()
		conn, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: t.cfg.Client, HTTPHeader: hdr})
		return conn, target.HandshakeError(err, resp)
	})
	if err != nil {
		return nil, fmt.Errorf("twilio %s: connect %s: %w", t.name, u, err)
	}
	dctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	conn.SetReadLimit(16 << 20)
	s := &session{t: t, run: run, conn: conn, events: make(chan event, 1024), metrics: map[string]float64{}, streamSid: "MZ" + randomHex(16), callSid: "CA" + randomHex(16)}
	s.audio = newAudioState(run)
	readCtx, stopRead := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = stopRead
	go s.read(readCtx)
	if err := s.send(dctx, message{Event: "connected", Protocol: "Call", Version: "1.0.0"}); err != nil {
		_ = s.closeConn()
		return nil, fmt.Errorf("twilio %s: connected: %w", t.name, err)
	}
	start := message{Event: "start", StreamSid: s.streamSid, Start: &startPayload{
		AccountSid: "ACtamesu", StreamSid: s.streamSid, CallSid: s.callSid, Tracks: []string{"inbound"},
		CustomParameters: params, MediaFormat: mediaFormat{Encoding: "audio/x-mulaw", SampleRate: Rate, Channels: 1},
	}}
	if err := s.send(dctx, start); err != nil {
		_ = s.closeConn()
		return nil, fmt.Errorf("twilio %s: start: %w", t.name, err)
	}
	s.connectedAt = time.Now()
	s.event("start", s.streamSid)
	s.greeting(ctx)
	return s, nil
}

// greeting reads the agent's opening words, when it speaks first, into the
// recording and the greeting metrics; the turn itself is not a reply.
func (s *session) greeting(ctx context.Context) {
	gctx, cancel := context.WithTimeout(ctx, GreetingTimeout)
	defer cancel()
	turn, err := s.readTurn(gctx, nil, true)
	if err != nil || turn.FirstTokenAt.IsZero() {
		return
	}
	s.mu.Lock()
	s.greetingAt = turn.FirstTokenAt
	s.metrics[metrics.GreetingMs] = float64(turn.FirstTokenAt.Sub(s.connectedAt).Microseconds()) / 1000
	s.mu.Unlock()
	s.event("greeting", turn.Text)
}

func (s *session) send(ctx context.Context, m message) error {
	s.mu.Lock()
	s.seq++
	m.SequenceNumber = strconv.Itoa(s.seq)
	if m.StreamSid == "" && m.Event != "connected" {
		m.StreamSid = s.streamSid
	}
	s.mu.Unlock()
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// read decodes the agent's messages into events until the connection ends.
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
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		ev := event{msg: &m, at: time.Now()}
		if m.Event == "media" && m.Media != nil {
			if raw, err := base64.StdEncoding.DecodeString(m.Media.Payload); err == nil {
				ev.audio = g711.DecodeMuLaw(raw)
			}
		}
		select {
		case s.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// Send speaks the caller's turn (and digits) and reads the agent's reply.
func (s *session) Send(ctx context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("twilio: session is closed")
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
		if err := s.send(tctx, message{Event: "dtmf", DTMF: &dtmfPayload{Track: "inbound_track", Digit: string(d)}}); err != nil {
			return fmt.Errorf("twilio: dtmf: %w", err)
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
		return fmt.Errorf("twilio: %w", err)
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

// readTurn reads the agent's media until Idle after its last sound (or the
// timeout). Marks are answered once the audio before them has played;
// clear drops the playback queue. greeting makes a silent agent a non-event
// rather than a timeout.
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
	var clearedAt time.Time
	finish := func() (target.AgentTurn, error) {
		s.finishAudio(ctx, spoken, &agent)
		if spoken != nil && spoken.interrupting {
			s.recordInterruption(spoken, clearedAt)
		}
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
				if !last.IsZero() || greeting {
					s.event("closed", "the agent closed the stream")
					return finish()
				}
				return agent, fmt.Errorf("the stream was closed by the agent: %w", errOr(ev.err))
			}
			s.mu.Lock()
			s.received++
			s.mu.Unlock()
			alive := false
			switch ev.msg.Event {
			case "media":
				if agent.FirstTokenAt.IsZero() {
					agent.FirstTokenAt = time.Now()
				}
				s.agentAudio(ev.audio, spoken)
				alive = true
				if plan != nil && plan.due(spoken) {
					// The caller will talk over the agent: the turn ends here and
					// the next Send speaks while the agent's audio still plays.
					agent.Interrupted = true
					s.mu.Lock()
					a.interruptNext = true
					s.mu.Unlock()
					s.event("interrupting", "the caller will speak over the agent")
					return finish()
				}
			case "mark":
				if ev.msg.Mark != nil {
					s.answerMark(ev.msg.Mark.Name)
				}
				alive = true
			case "clear":
				s.mu.Lock()
				s.clears++
				s.played = 0
				s.mu.Unlock()
				if clearedAt.IsZero() {
					clearedAt = ev.at
				}
				s.event("clear", "the agent cleared its audio")
			default:
				s.event("unsolicited", ev.msg.Event)
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

// answerMark replies to a mark once the audio queued before it has played
// out, as Twilio does once the caller has heard it.
func (s *session) answerMark(name string) {
	s.mu.Lock()
	s.marks++
	wait := s.played - time.Since(s.first)
	s.mu.Unlock()
	if wait < 0 || s.first.IsZero() {
		wait = 0
	}
	go func() {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.send(ctx, message{Event: "mark", Mark: &markPayload{Name: name}})
		s.event("mark", name)
	}()
}

// Receive returns the agent's turn for the last Send.
func (s *session) Receive(context.Context) (target.AgentTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("twilio: Receive without a Send")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

// Close sends stop, ends the connection and returns the artifacts.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return target.Artifacts{}, nil
	}
	s.closed = true
	s.mu.Unlock()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	_ = s.send(sctx, message{Event: "stop", Stop: map[string]string{"accountSid": "ACtamesu", "callSid": s.callSid}})
	cancel()
	_ = s.closeConn()
	s.mu.Lock()
	defer s.mu.Unlock()
	arts := target.Artifacts{Metrics: s.metrics, Files: map[string]string{}}
	arts.Metrics[metrics.Turns] = float64(s.turns)
	arts.Metrics["ws_messages_received"] = float64(s.received)
	arts.Metrics["marks"] = float64(s.marks)
	arts.Metrics["clears"] = float64(s.clears)
	if !s.connectedAt.IsZero() {
		arts.Metrics[metrics.CallDurationMs] = float64(time.Since(s.connectedAt).Microseconds()) / 1000
	}
	s.closeAudioLocked(arts.Files)
	arts.Events = s.evs
	return arts, nil
}

// closeConn closes the websocket once and stops the reader.
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
		return errors.New("stream closed")
	}
	return err
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(b)
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
	// the agent to the agent's clear (or to the end of the audio it had
	// queued, when it never cleared).
	interrupts []float64
	// interruptNext asks the next Send to speak while the agent still plays.
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

// interruptPlan is one turn's decision to talk over the agent: once it has
// spoken for `after`, the caller stops listening and speaks next.
type interruptPlan struct{ after time.Duration }

// planInterrupt rolls the persona's interrupt_probability for the coming
// turn; nil when the caller will listen to the end.
func (a *audioState) planInterrupt() *interruptPlan {
	if a.voice.InterruptProbability <= 0 || a.rng.Float64() >= a.voice.InterruptProbability {
		return nil
	}
	return &interruptPlan{after: 600*time.Millisecond + time.Duration(a.rng.Float64()*float64(1400*time.Millisecond))}
}

// due reports whether the agent has spoken long enough to be interrupted.
func (p *interruptPlan) due(sp *spokenTurn) bool {
	return sp != nil && !sp.firstAudio.IsZero() && time.Since(sp.firstAudio) >= p.after
}

// recordInterruption measures how long the agent kept talking after the
// caller spoke over it: until its clear, or until the audio it had already
// queued would have finished playing.
func (s *session) recordInterruption(sp *spokenTurn, clearedAt time.Time) {
	a := s.audio
	s.mu.Lock()
	defer s.mu.Unlock()
	var stop time.Duration
	how := "the agent cleared its audio"
	switch {
	case !clearedAt.IsZero():
		stop = clearedAt.Sub(sp.startedAt)
	case !s.first.IsZero():
		stop = s.first.Add(s.played).Sub(sp.startedAt)
		how = "the agent never cleared; its queued audio played out"
	}
	stop = max(stop, 0)
	a.interrupts = append(a.interrupts, float64(stop.Microseconds())/1000)
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "interruption_stopped", Detail: fmt.Sprintf("%s after %d ms", how, stop.Milliseconds())})
}

type spokenTurn struct {
	endedAt    time.Time
	firstAudio time.Time
	// startedAt is when the caller began speaking; interrupting marks a
	// turn spoken over the agent.
	startedAt    time.Time
	interrupting bool
}

// speak synthesises the turn and streams it as 20 ms μ-law media events,
// paced in real time, with a silence tail.
func (s *session) speak(ctx context.Context, turn target.UserTurn, started time.Time) (*spokenTurn, error) {
	a := s.audio
	sp := &spokenTurn{endedAt: started}
	if turn.Text == "" {
		return sp, nil
	}
	out, err := a.speech.Synthesize(ctx, target.SynthesizeRequest{Text: turn.Text, Voice: a.voice.Hint, Language: a.voice.Language})
	if err != nil {
		return nil, fmt.Errorf("twilio: synthesize: %w", err)
	}
	pcm := audio.Resample(audio.PCM{Rate: out.Rate, Samples: out.Samples}, Rate)
	samples := append(pcm.Samples, make([]int16, audio.SampleCount(silenceTail, Rate))...)
	chunk := Rate * chunkMs / 1000
	tick := time.NewTicker(chunkMs * time.Millisecond)
	defer tick.Stop()
	for i := 0; i < len(samples); i += chunk {
		end := min(i+chunk, len(samples))
		s.mu.Lock()
		s.chunk++
		m := message{Event: "media", Media: &mediaPayload{Track: "inbound", Chunk: strconv.Itoa(s.chunk), Timestamp: strconv.Itoa(int(time.Since(s.connectedAt).Milliseconds())), Payload: base64.StdEncoding.EncodeToString(g711.EncodeMuLaw(samples[i:end]))}}
		s.mu.Unlock()
		if err := s.send(ctx, m); err != nil {
			return nil, fmt.Errorf("twilio: media: %w", err)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	sp.endedAt = time.Now()
	s.mu.Lock()
	pos := max(audio.SampleCount(started.Sub(a.start), Rate), len(a.agent))
	a.user = padTo(a.user, pos)
	a.user = append(a.user, pcm.Samples...)
	a.userMs += float64(len(pcm.Samples)) * 1000 / Rate
	a.turn = a.turn[:0]
	s.mu.Unlock()
	return sp, nil
}

// agentAudio appends the agent's media to the right track and the playback clock.
func (s *session) agentAudio(pcm []int16, sp *spokenTurn) {
	if len(pcm) == 0 {
		return
	}
	a := s.audio
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.first.IsZero() || s.played-now.Sub(s.first) < 0 {
		// The queue had drained: playback restarts now.
		s.first, s.played = now, 0
	}
	s.played += audio.Duration(len(pcm), Rate)
	if sp != nil && sp.firstAudio.IsZero() {
		sp.firstAudio = now
		a.ttfa = append(a.ttfa, float64(sp.firstAudio.Sub(sp.endedAt).Microseconds())/1000)
		a.agent = padTo(a.agent, max(audio.SampleCount(sp.firstAudio.Sub(a.start), Rate), len(a.user)))
	} else if sp == nil && len(a.turn) == 0 {
		a.agent = padTo(a.agent, max(audio.SampleCount(now.Sub(a.start), Rate), len(a.user)))
	}
	a.agent = append(a.agent, pcm...)
	a.turn = append(a.turn, pcm...)
	a.agMs += float64(len(pcm)) * 1000 / Rate
}

// finishAudio sets the turn's timing and transcribes the agent's audio.
func (s *session) finishAudio(ctx context.Context, sp *spokenTurn, agent *target.AgentTurn) {
	a := s.audio
	if sp != nil && !sp.firstAudio.IsZero() {
		agent.FirstTokenAt = sp.firstAudio
	}
	s.mu.Lock()
	turn := append([]int16(nil), a.turn...)
	a.turn = a.turn[:0]
	s.mu.Unlock()
	if len(turn) > 0 {
		agent.Audio = bytesOf(turn)
		if tr, err := a.speech.Transcribe(ctx, target.TranscribeRequest{Audio: target.Audio{Rate: Rate, Samples: turn}, Language: a.voice.Language}); err == nil {
			agent.Text = strings.TrimSpace(tr.Text)
			agent.TranscriptSource = "stt"
		} else {
			s.event("stt_error", err.Error())
		}
	}
}

// closeAudioLocked writes the WAV and the audio metrics.
func (s *session) closeAudioLocked(files map[string]string) {
	a := s.audio
	n := max(len(a.user), len(a.agent))
	a.user, a.agent = padTo(a.user, n), padTo(a.agent, n)
	if s.run != nil && s.run.Dir != "" && n > 0 {
		path := filepath.Join(s.run.Dir, WAVFile)
		if err := wav.WriteFile(path, Rate, a.user, a.agent); err == nil {
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

func bytesOf(samples []int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, v := range samples {
		b[2*i] = byte(v)
		b[2*i+1] = byte(uint16(v) >> 8)
	}
	return b
}
