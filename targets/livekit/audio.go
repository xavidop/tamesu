package livekit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/line"
	"github.com/xavidop/tamesu/audio/noise"
	"github.com/xavidop/tamesu/audio/vad"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/target"
)

// Audio mode constants: WebRTC carries Opus at 48 kHz in 20 ms frames; the
// voice activity detector runs at 16 kHz.
const (
	AudioRate    = 48000
	FrameDur     = 20 * time.Millisecond
	FrameSamples = AudioRate / 50
	vadRate      = 16000
	// WAVFile is the stereo recording of a run: left the caller, right the agent.
	WAVFile = "audio.wav"
)

// ErrNoSpeech is returned by Start in audio mode when the run has no speech
// layer (no speech.tts / speech.stt models configured, or a lite build).
var ErrNoSpeech = errors.New("audio mode needs the speech models: set speech.tts.model and speech.stt.model in tamesu.yaml")

// audioPublisher publishes the caller's audio, one 20 ms frame at a time.
type audioPublisher interface {
	WriteFrame(pcm []int16) error
	Close() error
}

// audioRoom is a room that can publish the caller's audio; the agent's audio
// arrives through events.onAudio.
type audioRoom interface {
	room
	PublishAudio(ctx context.Context) (audioPublisher, error)
}

// timeline is one channel of the run's audio, indexed in samples from the
// session start; gaps in what arrives are padded with silence so positions
// stay aligned with the clock.
type timeline struct {
	start   time.Time
	samples []int16
}

// appendAt adds pcm and returns the index it starts at. A gap over 100 ms
// since the last sample realigns to the clock; smaller jitter is absorbed by
// appending contiguously.
func (t *timeline) appendAt(at time.Time, pcm []int16) int {
	pos := audio.SampleCount(at.Sub(t.start), AudioRate)
	if pos > len(t.samples)+AudioRate/10 {
		t.samples = append(t.samples, make([]int16, pos-len(t.samples))...)
	}
	from := len(t.samples)
	t.samples = append(t.samples, pcm...)
	return from
}

func (t *timeline) pos() int { return len(t.samples) }

func (t *timeline) slice(from, to int) []int16 {
	from = max(0, min(from, len(t.samples)))
	to = max(from, min(to, len(t.samples)))
	return append([]int16(nil), t.samples[from:to]...)
}

// segment is a stretch of speech, in samples of the shared timeline.
type segment struct{ from, to int }

// audioState is the audio-mode half of a session.
type audioState struct {
	speech target.Speech
	voice  target.Voice
	rng    *rand.Rand
	pub    audioPublisher
	user   *timeline
	agent  *timeline
	seg    *vad.Segmenter
	line   *line.Profile

	noise     audio.PCM
	noiseGain float64
	noisePos  int

	queue      []int16 // the caller's speech waiting to be published
	userSeg    *segment
	userSegs   []segment
	agentSegs  []segment
	agentOpen  *segment
	ttfa       []float64
	interrupts []float64
	gaps       []float64
	pendingInt *int // user speech start that interrupted the agent, until its audio stops

	interruptNext bool // the last Receive returned early: speak over the agent
	turns         int
	stop          chan struct{}
	done          chan struct{}
	stopped       bool
}

// startAudio sets audio mode up once the agent is in the room.
func (s *session) startAudio(ctx context.Context, r room, run *target.RunContext) error {
	if run.Speech == nil {
		return ErrNoSpeech
	}
	ar, ok := r.(audioRoom)
	if !ok {
		return errors.New("this connection cannot publish audio")
	}
	voice := target.Voice{NoiseSNRdB: 20}
	if run.Voice != nil {
		voice = *run.Voice
	}
	if voice.NoiseSNRdB == 0 {
		voice.NoiseSNRdB = 20
	}
	seed := uint64(voice.Seed)
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	a := &audioState{
		speech: run.Speech, voice: voice, rng: rand.New(rand.NewPCG(seed, seed^0xa5a5a5a5)),
		user: &timeline{start: s.startedAt}, agent: &timeline{start: s.startedAt},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	det, err := vad.New(voice.VAD, vad.Options{Rate: vadRate})
	if err != nil {
		return err
	}
	a.seg = vad.NewSegmenter(det, vad.SegmenterOptions{Hangover: 300 * time.Millisecond, MinSpeech: 60 * time.Millisecond})
	if voice.LineQuality != "" {
		p, err := line.Lookup(voice.LineQuality)
		if err != nil {
			return err
		}
		a.line = &p
	}
	if voice.BackgroundNoise != "" {
		if strings.HasSuffix(strings.ToLower(voice.BackgroundNoise), ".wav") {
			a.noise, err = noise.FromFile(voice.BackgroundNoise, AudioRate)
		} else {
			a.noise, err = noise.Generate(voice.BackgroundNoise, AudioRate, 10*time.Second, seed)
		}
		if err != nil {
			return err
		}
		// Until the caller has spoken, the noise sits at -40 dBFS, as a quiet
		// room would.
		a.noiseGain = 0.01 / max(audio.RMS(a.noise.Samples), 1e-6)
	}
	pub, err := ar.PublishAudio(ctx)
	if err != nil {
		return fmt.Errorf("publish audio: %w", err)
	}
	a.pub = pub
	s.audio = a
	go s.publishLoop()
	return nil
}

// publishLoop sends one frame every 20 ms: the caller's queued speech, with
// the background noise under it, or noise alone.
func (s *session) publishLoop() {
	a := s.audio
	defer close(a.done)
	ticker := time.NewTicker(FrameDur)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case now := <-ticker.C:
			frame := s.nextFrame(now)
			if err := a.pub.WriteFrame(frame); err != nil {
				s.event("publish_error", err.Error())
				return
			}
		}
	}
}

func (s *session) nextFrame(now time.Time) []int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.audio
	frame := make([]int16, FrameSamples)
	pos := a.user.pos()
	if len(a.queue) > 0 {
		n := copy(frame, a.queue)
		a.queue = a.queue[n:]
		if a.userSeg == nil {
			a.userSeg = &segment{from: pos}
			s.eventLocked("user_speech_start", "")
			if a.agentOpen != nil {
				start := pos
				a.pendingInt = &start
				s.eventLocked("interruption", "the caller spoke over the agent")
			}
			if s.cur != nil && s.cur.userStart < 0 {
				s.cur.userStart = pos
			}
		}
		if len(a.queue) == 0 {
			a.userSeg.to = pos + n
			a.userSegs = append(a.userSegs, *a.userSeg)
			a.userSeg = nil
			if s.cur != nil {
				s.cur.userEnd = pos + n
				s.cur.userEndAt = now
			}
			s.eventLocked("user_speech_end", "")
			s.signal()
		}
	}
	if len(a.noise.Samples) > 0 {
		for i := range frame {
			v := float64(frame[i]) + float64(a.noise.Samples[a.noisePos])*a.noiseGain
			frame[i] = audio.Clip(v)
			a.noisePos = (a.noisePos + 1) % len(a.noise.Samples)
		}
	}
	a.user.appendAt(now, frame)
	s.padAgent(now)
	return frame
}

// agentAudio is events.onAudio: the agent's decoded audio (48 kHz mono).
func (s *session) agentAudio(identity string, pcm []int16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.audio
	if a == nil || identity != s.agentID || len(pcm) == 0 {
		return
	}
	now := time.Now()
	before := a.agent.pos()
	a.agent.appendAt(now, pcm)
	s.feedVAD(before, now)
}

// padAgent fills the agent timeline with silence up to 40 ms behind the
// clock, so the detector hears the agent stop even when its track goes quiet
// (DTX) and late frames still have room to land.
func (s *session) padAgent(now time.Time) {
	a := s.audio
	target := audio.SampleCount(now.Sub(a.agent.start), AudioRate) - 2*FrameSamples
	if before := a.agent.pos(); target > before {
		a.agent.samples = append(a.agent.samples, make([]int16, target-before)...)
		s.feedVAD(before, now)
	}
}

// feedVAD runs the detector over the agent timeline from index from and
// turns its events into segments, turn timings and metrics. The detector
// hears the padding too, so its clock is the timeline's.
func (s *session) feedVAD(from int, now time.Time) {
	a := s.audio
	chunk := audio.Resample(audio.PCM{Rate: AudioRate, Samples: a.agent.samples[from:]}, vadRate)
	for _, ev := range a.seg.Push(chunk.Samples) {
		at := audio.SampleCount(ev.At, AudioRate)
		switch ev.Kind {
		case vad.SpeechStart:
			a.agentOpen = &segment{from: at}
			s.eventLocked("agent_speech_start", "")
			if c := s.cur; c != nil {
				c.spoke = true
				if c.agentStart < 0 {
					c.agentStart = at
					c.firstAt = now
					if c.userEnd >= 0 {
						gap := samplesMs(at - c.userEnd)
						a.ttfa = append(a.ttfa, gap)
						a.gaps = append(a.gaps, gap)
					}
				} else if c.agentEnd >= 0 {
					a.gaps = append(a.gaps, samplesMs(at-c.agentEnd))
				}
			}
		case vad.SpeechEnd:
			if a.agentOpen != nil {
				a.agentOpen.to = at
				a.agentSegs = append(a.agentSegs, *a.agentOpen)
				a.agentOpen = nil
			}
			s.eventLocked("agent_speech_end", "")
			if c := s.cur; c != nil {
				c.agentEnd = at
				c.lastAt = now
			}
			if a.pendingInt != nil {
				a.interrupts = append(a.interrupts, samplesMs(at-*a.pendingInt))
				a.pendingInt = nil
				s.eventLocked("interruption_stopped", "")
			}
		}
	}
	if a.agentOpen != nil && s.cur != nil {
		s.cur.lastAt = now
	}
	s.signal()
}

func samplesMs(n int) float64 { return float64(n) * 1000 / AudioRate }

// sendAudio speaks the user turn: a pause, then the synthesised text (and
// DTMF digits) through the line simulation into the publish queue.
func (s *session) sendAudio(ctx context.Context, ut target.UserTurn, cur *turn) error {
	a := s.audio
	s.mu.Lock()
	a.turns++
	interrupting := a.interruptNext
	a.interruptNext = false
	cur.interrupt = a.rng.Float64() < a.voice.InterruptProbability
	cur.interruptAfter = 600*time.Millisecond + time.Duration(a.rng.Float64()*float64(1400*time.Millisecond))
	first := a.turns == 1
	s.mu.Unlock()
	if first {
		cur.audioFrom = 0
	}
	if ut.Text == "" && ut.DTMF == "" {
		s.mu.Lock()
		cur.userEnd = a.user.pos()
		cur.userEndAt = time.Now()
		s.mu.Unlock()
		return nil
	}
	if !interrupting && a.voice.SilenceMax > 0 {
		pause := a.voice.SilenceMin + time.Duration(a.rng.Float64()*float64(a.voice.SilenceMax-a.voice.SilenceMin))
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var pcm audio.PCM
	if ut.DTMF != "" {
		pcm = audio.DTMF(ut.DTMF, AudioRate)
		s.event("dtmf_sent", ut.DTMF)
	}
	if ut.Text != "" {
		spoken, err := a.speech.Synthesize(ctx, target.SynthesizeRequest{Text: ut.Text, Voice: a.voice.Hint, Language: a.voice.Language})
		if err != nil {
			return fmt.Errorf("synthesize: %w", err)
		}
		pcm = audio.Concat(pcm, audio.Resample(audio.PCM{Rate: spoken.Rate, Samples: spoken.Samples}, AudioRate))
	}
	if a.line != nil {
		pcm = line.Apply(pcm, *a.line, uint64(a.voice.Seed)+uint64(a.turns))
	}
	s.mu.Lock()
	if len(a.noise.Samples) > 0 && ut.Text != "" {
		// The noise sits NoiseSNRdB under the caller's voice.
		speechRMS, noiseRMS := audio.RMS(pcm.Samples), audio.RMS(a.noise.Samples)
		if speechRMS > 0 && noiseRMS > 0 {
			a.noiseGain = speechRMS / math.Pow(10, a.voice.NoiseSNRdB/20) / noiseRMS
		}
	}
	a.queue = append(a.queue, pcm.Samples...)
	s.mu.Unlock()
	return nil
}

// receiveAudio waits for the agent's spoken reply: the caller finished
// speaking, the agent started (VAD) and stopped, and it went back to
// listening or stayed silent for TurnIdle. With an interruption planned, it
// returns early once the agent has spoken long enough.
func (s *session) receiveAudio(ctx context.Context, cur *turn) (target.AgentTurn, error) {
	a := s.audio
	deadline := time.NewTimer(s.t.cfg.TurnTimeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		speaking := a.agentOpen != nil
		userDone := cur.userEnd >= 0
		started := cur.agentStart >= 0
		idle := time.Duration(0)
		if started && !speaking && !cur.lastAt.IsZero() {
			idle = time.Since(cur.lastAt)
		}
		listening := cur.spoke && s.agentState == StateListening
		done := userDone && started && !speaking && (idle >= s.t.cfg.TurnIdle || listening)
		interrupt := cur.interrupt && speaking && started && time.Since(cur.firstAt) >= cur.interruptAfter
		s.mu.Unlock()
		select {
		case <-s.agentLeft:
			return s.finishAudio(ctx, cur, false, true)
		default:
		}
		if interrupt {
			return s.finishAudio(ctx, cur, true, false)
		}
		if done {
			return s.finishAudio(ctx, cur, false, false)
		}
		wait := s.t.cfg.TurnIdle
		if started && !speaking {
			wait = max(s.t.cfg.TurnIdle-idle, 10*time.Millisecond)
		}
		if cur.interrupt && speaking {
			wait = min(wait, max(cur.interruptAfter-time.Since(cur.firstAt), 10*time.Millisecond))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return target.AgentTurn{}, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			if started {
				return s.finishAudio(ctx, cur, false, false)
			}
			return target.AgentTurn{}, fmt.Errorf("livekit: the agent did not speak within %s", s.t.cfg.TurnTimeout)
		case <-s.agentLeft:
			timer.Stop()
			return s.finishAudio(ctx, cur, false, true)
		case <-s.changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// finishAudio assembles the agent turn: its own transcription when it
// published one (and transcript_source allows), else tamesu's STT of its
// audio for this turn.
func (s *session) finishAudio(ctx context.Context, cur *turn, interrupted, left bool) (target.AgentTurn, error) {
	a := s.audio
	s.mu.Lock()
	text := strings.TrimSpace(cur.text.String())
	source := "agent"
	end := cur.agentEnd
	if end < 0 || a.agentOpen != nil {
		end = a.agent.pos()
	}
	from := cur.audioFrom
	if cur.agentStart >= 0 && cur.agentStart > from {
		from = cur.agentStart
	}
	clip := a.agent.slice(from, end)
	useSTT := cur.agentStart >= 0 && (a.voice.TranscriptSource == "stt" || (text == "" && a.voice.TranscriptSource != "agent"))
	s.mu.Unlock()
	if useSTT && len(clip) > 0 {
		pcm := audio.Resample(audio.PCM{Rate: AudioRate, Samples: clip}, vadRate)
		tr, err := a.speech.Transcribe(ctx, target.TranscribeRequest{Audio: target.Audio{Rate: pcm.Rate, Samples: pcm.Samples}, Language: a.voice.Language})
		if err != nil {
			return target.AgentTurn{}, fmt.Errorf("transcribe: %w", err)
		}
		text, source = tr.Text, "stt"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur.agentStart < 0 {
		source = ""
	}
	ended := cur.lastAt
	if ended.IsZero() {
		ended = time.Now()
	}
	at := target.AgentTurn{Text: text, ToolCalls: cur.tools, Done: left, Interrupted: interrupted, TranscriptSource: source, StartedAt: cur.sentAt, FirstTokenAt: cur.firstAt, EndedAt: ended}
	if at.FirstTokenAt.IsZero() {
		at.FirstTokenAt = ended
	}
	if interrupted {
		a.interruptNext = true
		s.eventLocked("interrupting", "the caller will speak over the agent")
	}
	if source != "" {
		s.eventLocked("transcript_source", source)
	}
	s.cur = nil
	return at, nil
}

// closeAudio stops publishing, writes the stereo WAV and computes the audio
// metrics into the session's metrics map.
func (s *session) closeAudio(dir string) map[string]string {
	a := s.audio
	s.mu.Lock()
	stopped := a.stopped
	a.stopped = true
	s.mu.Unlock()
	if !stopped {
		close(a.stop)
		<-a.done
		_ = a.pub.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files := map[string]string{}
	if dir != "" {
		path := filepath.Join(dir, WAVFile)
		if err := wav.WriteFile(path, AudioRate, a.user.samples, a.agent.samples); err == nil {
			files[WAVFile] = WAVFile
		} else {
			s.eventLocked("wav_error", err.Error())
		}
	}
	if a.agentOpen != nil {
		a.agentOpen.to = a.agent.pos()
		a.agentSegs = append(a.agentSegs, *a.agentOpen)
		a.agentOpen = nil
	}
	m := s.metrics
	metrics.Summarize(m, metrics.TimeToFirstAudioMs, a.ttfa)
	metrics.Summarize(m, metrics.InterruptionStopMs, a.interrupts)
	if len(a.gaps) > 0 {
		mx := 0.0
		for _, g := range a.gaps {
			mx = max(mx, g)
		}
		m[metrics.SilenceGapMaxMs] = mx
	}
	agentTotal, userTotal := totalSamples(a.agentSegs), totalSamples(a.userSegs)
	m["agent_speech_ms"] = samplesMs(agentTotal)
	m["user_speech_ms"] = samplesMs(userTotal)
	if agentTotal > 0 {
		m[metrics.TalkOverRatio] = float64(overlap(a.userSegs, a.agentSegs)) / float64(agentTotal)
	}
	m["interruptions"] = float64(len(a.interrupts))
	return files
}

func totalSamples(segs []segment) int {
	n := 0
	for _, s := range segs {
		n += s.to - s.from
	}
	return n
}

// overlap is the number of samples where both sides speak.
func overlap(a, b []segment) int {
	n := 0
	for _, x := range a {
		for _, y := range b {
			if lo, hi := max(x.from, y.from), min(x.to, y.to); hi > lo {
				n += hi - lo
			}
		}
	}
	return n
}
