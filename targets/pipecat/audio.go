package pipecat

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/pipecat/frames"
)

// WAVFile is the stereo artifact of audio mode: left the caller, right the bot.
const WAVFile = "audio.wav"

// chunkMs is the size of one AudioRawFrame sent.
const chunkMs = 20

// silenceTail is appended after the caller's words so the bot's voice
// activity detector sees the end of the utterance.
const silenceTail = 500 * time.Millisecond

type audioState struct {
	speech target.Speech
	voice  target.Voice
	rng    *rand.Rand
	rate   int
	start  time.Time
	user   []int16
	agent  []int16
	turn   []int16 // the bot's audio of the current turn, for transcription
	ttfa   []float64
	userMs float64
	agMs   float64
	// first and played are the playback clock: the bot's audio arrives as
	// it is made, and plays out from first for played.
	first  time.Time
	played time.Duration
	// interrupts are the barge-in stop times: from the caller speaking over
	// the bot to the bot's interruption signal (or to the end of the audio it
	// had sent, when it never yielded).
	interrupts []float64
	// interruptNext asks the next Send to speak while the bot still plays.
	interruptNext bool
}

type spokenTurn struct {
	endedAt    time.Time
	firstAudio time.Time
	// startedAt is when the caller began speaking; interrupting marks a
	// turn spoken over the bot.
	startedAt    time.Time
	interrupting bool
}

func (s *session) startAudio(run *target.RunContext) {
	a := &audioState{speech: run.Speech, rate: s.t.cfg.SampleRate, start: time.Now()}
	if run.Voice != nil {
		a.voice = *run.Voice
	}
	seed := uint64(a.voice.Seed)
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	a.rng = rand.New(rand.NewPCG(seed, seed^0x5a5a5a5a))
	s.audio = a
}

// interruptPlan is one turn's decision to talk over the bot: once it has
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

// due reports whether the bot has spoken long enough to be interrupted.
func (p *interruptPlan) due(sp *spokenTurn) bool {
	return sp != nil && !sp.firstAudio.IsZero() && time.Since(sp.firstAudio) >= p.after
}

// recordInterruption measures how long the bot kept talking after the
// caller spoke over it: until its interruption signal, or until the audio
// it had already sent would have finished playing.
func (s *session) recordInterruption(sp *spokenTurn, stoppedAt time.Time) {
	a := s.audio
	s.mu.Lock()
	defer s.mu.Unlock()
	var stop time.Duration
	how := "the bot yielded"
	switch {
	case !stoppedAt.IsZero():
		stop = stoppedAt.Sub(sp.startedAt)
	case !a.first.IsZero():
		stop = a.first.Add(a.played).Sub(sp.startedAt)
		how = "the bot never yielded; its audio played out"
	}
	stop = max(stop, 0)
	a.interrupts = append(a.interrupts, float64(stop.Microseconds())/1000)
	s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "interruption_stopped", Detail: fmt.Sprintf("%s after %d ms", how, stop.Milliseconds())})
}

// speak synthesises the turn and streams it as 20 ms AudioRawFrames at the
// transport's rate, paced in real time, with a silence tail.
func (s *session) speak(ctx context.Context, turn target.UserTurn, started time.Time) (*spokenTurn, error) {
	a := s.audio
	sp := &spokenTurn{endedAt: started}
	if turn.Text == "" {
		return sp, nil
	}
	out, err := a.speech.Synthesize(ctx, target.SynthesizeRequest{Text: turn.Text, Voice: a.voice.Hint, Language: a.voice.Language})
	if err != nil {
		return nil, fmt.Errorf("pipecat: synthesize: %w", err)
	}
	pcm := audio.Resample(audio.PCM{Rate: out.Rate, Samples: out.Samples}, a.rate)
	samples := append(pcm.Samples, make([]int16, audio.SampleCount(silenceTail, a.rate))...)
	chunk := a.rate * chunkMs / 1000
	tick := time.NewTicker(chunkMs * time.Millisecond)
	defer tick.Stop()
	for i := 0; i < len(samples); i += chunk {
		end := min(i+chunk, len(samples))
		f := &frames.Frame{Frame: &frames.Frame_Audio{Audio: &frames.AudioRawFrame{Audio: bytesOf(samples[i:end]), SampleRate: uint32(a.rate), NumChannels: 1}}}
		if err := s.sendFrame(ctx, f); err != nil {
			return nil, fmt.Errorf("pipecat: audio frame: %w", err)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	sp.endedAt = time.Now()
	s.mu.Lock()
	pos := max(audio.SampleCount(started.Sub(a.start), a.rate), len(a.agent))
	a.user = padTo(a.user, pos)
	a.user = append(a.user, pcm.Samples...)
	a.userMs += float64(len(pcm.Samples)) * 1000 / float64(a.rate)
	a.turn = a.turn[:0]
	s.mu.Unlock()
	return sp, nil
}

// agentAudio appends a bot AudioRawFrame to the right track.
func (s *session) agentAudio(ev event, sp *spokenTurn) {
	a := s.audio
	pcm := ev.audio
	if ev.rate > 0 && ev.rate != a.rate {
		pcm = audio.Resample(audio.PCM{Rate: ev.rate, Samples: pcm}, a.rate).Samples
	}
	if len(pcm) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if a.first.IsZero() || a.played-now.Sub(a.first) < 0 {
		// The playback had drained: it restarts now.
		a.first, a.played = now, 0
	}
	a.played += audio.Duration(len(pcm), a.rate)
	if sp != nil && sp.firstAudio.IsZero() {
		sp.firstAudio = now
		a.ttfa = append(a.ttfa, float64(sp.firstAudio.Sub(sp.endedAt).Microseconds())/1000)
		a.agent = padTo(a.agent, max(audio.SampleCount(sp.firstAudio.Sub(a.start), a.rate), len(a.user)))
	}
	a.agent = append(a.agent, pcm...)
	a.turn = append(a.turn, pcm...)
	a.agMs += float64(len(pcm)) * 1000 / float64(a.rate)
}

// finishAudio sets the turn's timing and, when the bot sent no text,
// transcribes its audio.
func (s *session) finishAudio(ctx context.Context, sp *spokenTurn, agent *target.AgentTurn, needText bool) {
	a := s.audio
	if sp != nil && !sp.firstAudio.IsZero() {
		agent.FirstTokenAt = sp.firstAudio
	}
	agent.TranscriptSource = "agent"
	s.mu.Lock()
	turn := append([]int16(nil), a.turn...)
	s.mu.Unlock()
	if needText && len(turn) > 0 && a.speech != nil {
		if tr, err := a.speech.Transcribe(ctx, target.TranscribeRequest{Audio: target.Audio{Rate: a.rate, Samples: turn}, Language: a.voice.Language}); err == nil {
			agent.Text = strings.TrimSpace(tr.Text)
			agent.TranscriptSource = "stt"
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
		if err := wav.WriteFile(path, a.rate, a.user, a.agent); err == nil {
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
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// toolOutput renders the configured result of a function call.
func (s *session) toolOutput(call target.ToolCall) (any, bool) {
	result, ok := s.t.cfg.ToolResults[call.Name]
	if !ok {
		return nil, false
	}
	return renderArgs(result, call.Args), true
}

// renderArgs returns v with every {{args.<key>}} in its string leaves
// replaced by the call's argument.
func renderArgs(v any, args map[string]any) any {
	switch x := v.(type) {
	case string:
		if !strings.Contains(x, "{{args.") {
			return x
		}
		out := x
		for k, a := range args {
			out = strings.ReplaceAll(out, "{{args."+k+"}}", argString(a))
		}
		return out
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = renderArgs(e, args)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = renderArgs(e, args)
		}
		return l
	}
	return v
}

func argString(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case nil:
		return ""
	}
	if b, err := json.Marshal(a); err == nil {
		return string(b)
	}
	return fmt.Sprint(a)
}
