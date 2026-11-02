package realtime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/target"
)

// Audio mode constants: the protocol's PCM16 rate, the append chunk and the
// stereo artifact.
const (
	AudioRate = 24000
	chunk     = AudioRate / 10 // 100 ms per input_audio_buffer.append
	WAVFile   = "audio.wav"
)

// audioState is the audio-mode half of a session: the two tracks of the
// stereo artifact (left the caller, right the agent) and the per-turn
// measurements.
type audioState struct {
	speech target.Speech
	voice  target.Voice
	start  time.Time
	user   []int16
	agent  []int16
	ttfa   []float64
	userMs float64
	agMs   float64
}

// spokenTurn is what Send measured while speaking the user's turn.
type spokenTurn struct {
	committedAt time.Time
	firstAudio  time.Time
	samples     int
}

func (s *session) startAudio(run *target.RunContext) {
	a := &audioState{speech: run.Speech, start: time.Now()}
	if run.Voice != nil {
		a.voice = *run.Voice
	}
	s.audio = a
}

// speak synthesises the user's turn, appends it as 100 ms PCM16 chunks and
// commits the buffer. An empty turn commits nothing and only asks for a
// response.
func (s *session) speak(ctx context.Context, turn target.UserTurn, started time.Time) (*spokenTurn, error) {
	sp := &spokenTurn{}
	if turn.Text == "" {
		sp.committedAt = started
		return sp, nil
	}
	a := s.audio
	out, err := a.speech.Synthesize(ctx, target.SynthesizeRequest{Text: turn.Text, Voice: a.voice.Hint, Language: a.voice.Language})
	if err != nil {
		return nil, fmt.Errorf("realtime: synthesize: %w", err)
	}
	pcm := audio.Resample(audio.PCM{Rate: out.Rate, Samples: out.Samples}, AudioRate)
	for i := 0; i < len(pcm.Samples); i += chunk {
		end := min(i+chunk, len(pcm.Samples))
		if err := s.write(ctx, map[string]any{"type": "input_audio_buffer.append", "audio": encodePCM(pcm.Samples[i:end])}); err != nil {
			return nil, fmt.Errorf("realtime: input_audio_buffer.append: %w", err)
		}
	}
	if err := s.write(ctx, map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		return nil, fmt.Errorf("realtime: input_audio_buffer.commit: %w", err)
	}
	sp.committedAt = time.Now()
	sp.samples = len(pcm.Samples)
	s.mu.Lock()
	// The caller's speech goes on the left track at the clock, or after the
	// agent's last audio when that ran ahead of it.
	pos := max(audio.SampleCount(started.Sub(a.start), AudioRate), len(a.agent))
	a.user = padTo(a.user, pos)
	a.user = append(a.user, pcm.Samples...)
	a.userMs += samplesMs(len(pcm.Samples))
	s.mu.Unlock()
	return sp, nil
}

// agentAudio appends one response.output_audio.delta to the agent track.
func (s *session) agentAudio(ev map[string]any, sp *spokenTurn) {
	b64, _ := ev["delta"].(string)
	pcm, err := decodePCM(b64)
	if err != nil || len(pcm) == 0 {
		return
	}
	a := s.audio
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp != nil && sp.firstAudio.IsZero() {
		sp.firstAudio = time.Now()
		a.ttfa = append(a.ttfa, float64(sp.firstAudio.Sub(sp.committedAt).Microseconds())/1000)
		// The reply starts after the caller finished speaking.
		a.agent = padTo(a.agent, max(audio.SampleCount(sp.firstAudio.Sub(a.start), AudioRate), len(a.user)))
	}
	a.agent = append(a.agent, pcm...)
	a.agMs += samplesMs(len(pcm))
}

// finishAudio attaches the turn's audio to the agent turn.
func (s *session) finishAudio(sp *spokenTurn, agent *target.AgentTurn) {
	if sp != nil && !sp.firstAudio.IsZero() {
		agent.FirstTokenAt = sp.firstAudio
	}
	agent.TranscriptSource = "agent"
}

// closeAudioLocked writes the stereo WAV and the audio metrics.
func (s *session) closeAudioLocked(files map[string]string) {
	a := s.audio
	n := max(len(a.user), len(a.agent))
	a.user, a.agent = padTo(a.user, n), padTo(a.agent, n)
	if s.run != nil && s.run.Dir != "" && n > 0 {
		path := filepath.Join(s.run.Dir, WAVFile)
		if err := wav.WriteFile(path, AudioRate, a.user, a.agent); err == nil {
			files[WAVFile] = WAVFile
		} else {
			s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "wav_error", Detail: err.Error()})
		}
	}
	metrics.Summarize(s.metrics, metrics.TimeToFirstAudioMs, a.ttfa)
	s.metrics["agent_speech_ms"] = a.agMs
	s.metrics["user_speech_ms"] = a.userMs
}

func padTo(samples []int16, n int) []int16 {
	if n > len(samples) {
		return append(samples, make([]int16, n-len(samples))...)
	}
	return samples
}

func samplesMs(n int) float64 { return float64(n) * 1000 / AudioRate }

// encodePCM is base64 of little-endian PCM16.
func encodePCM(samples []int16) string {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodePCM is the inverse of encodePCM.
func decodePCM(b64 string) ([]int16, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out, nil
}
