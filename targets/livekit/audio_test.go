package livekit

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// fakeSpeech speaks a tone (60 ms per word at 24 kHz) and transcribes with a
// scripted text that records how much audio it was given.
type fakeSpeech struct {
	mu          sync.Mutex
	synthesized []string
	transcribed []time.Duration
	stt         string
}

func (f *fakeSpeech) Synthesize(_ context.Context, req target.SynthesizeRequest) (target.Audio, error) {
	f.mu.Lock()
	f.synthesized = append(f.synthesized, req.Text)
	f.mu.Unlock()
	words := len(strings.Fields(req.Text))
	d := time.Duration(max(300, 60*words)) * time.Millisecond
	tone := audio.Tone(24000, 220, d, 0.3)
	return target.Audio{Rate: tone.Rate, Samples: tone.Samples}, nil
}

func (f *fakeSpeech) Transcribe(_ context.Context, req target.TranscribeRequest) (target.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcribed = append(f.transcribed, audio.Duration(len(req.Audio.Samples), req.Audio.Rate))
	text := f.stt
	if text == "" {
		text = fmt.Sprintf("(stt heard %d ms)", audio.Duration(len(req.Audio.Samples), req.Audio.Rate).Milliseconds())
	}
	return target.Transcript{Text: text, Confidence: 0.9}, nil
}

// fakeAudioRoom is a room with a spoken agent in it: it listens to the
// caller's frames with an energy detector, answers respondAfter the caller
// stops with speakFor of tone (and the reply as a transcription unless
// noText), and stops stopAfter the caller talks over it.
type fakeAudioRoom struct {
	fakeRoom
	respondAfter time.Duration
	speakFor     time.Duration
	stopAfter    time.Duration
	noText       bool

	amu          sync.Mutex
	frames       int
	noisy        int // frames with energy while the caller is not speaking
	userSpeaking bool
	silentFor    time.Duration
	speaking     bool
	interrupted  bool
	published    bool
	closedPub    bool
	level        float64
}

func (r *fakeAudioRoom) connect(ctx context.Context, cfg connectConfig, ev events) (room, error) {
	_, err := r.fakeRoom.connect(ctx, cfg, ev)
	return r, err
}

func (r *fakeAudioRoom) PublishAudio(context.Context) (audioPublisher, error) {
	r.amu.Lock()
	r.published = true
	r.amu.Unlock()
	return r, nil
}

func (r *fakeAudioRoom) Close() error {
	r.amu.Lock()
	r.closedPub = true
	r.amu.Unlock()
	return nil
}

// WriteFrame is the agent's ear: 20 ms of the caller at 48 kHz.
func (r *fakeAudioRoom) WriteFrame(pcm []int16) error {
	level := audio.RMS(pcm)
	r.amu.Lock()
	defer r.amu.Unlock()
	r.frames++
	r.level = math.Max(r.level, level)
	speech := audio.DBFS(level) > -22 // the caller's tone is at -13.5 dBFS; cafe noise 15 dB under it is not speech
	switch {
	case speech:
		if !r.userSpeaking {
			r.userSpeaking = true
			if r.speaking && !r.interrupted {
				r.interrupted = true
				go r.stopSpeaking()
			}
		}
		r.silentFor = 0
	case r.userSpeaking:
		if level > 0 {
			r.noisy++
		}
		r.silentFor += FrameDur
		if r.silentFor >= 200*time.Millisecond {
			r.userSpeaking = false
			go r.respond(200 * time.Millisecond)
		}
	default:
		if level > 0 {
			r.noisy++
		}
	}
	return nil
}

func (r *fakeAudioRoom) stopSpeaking() {
	time.Sleep(r.stopAfter)
	r.amu.Lock()
	r.speaking = false
	r.amu.Unlock()
}

// respond speaks the next reply as a tone, lateBy after the caller's speech
// really ended (the detector's hangover), then publishes the text.
func (r *fakeAudioRoom) respond(lateBy time.Duration) {
	r.mu.Lock()
	reply := "no more replies"
	if r.i < len(r.replies) {
		reply = r.replies[r.i]
	}
	r.i++
	last := r.i >= len(r.replies)
	r.mu.Unlock()
	time.Sleep(r.respondAfter - lateBy)
	if !r.stateless {
		r.ev.onAttributes(r.agentID, map[string]string{AttrAgentState: StateSpeaking})
	}
	r.amu.Lock()
	r.speaking = true
	r.interrupted = false
	r.amu.Unlock()
	tone := audio.Tone(AudioRate, 300, FrameDur, 0.3).Samples
	ticker := time.NewTicker(FrameDur)
	defer ticker.Stop()
	start := time.Now()
	for range ticker.C {
		r.amu.Lock()
		speaking := r.speaking
		r.amu.Unlock()
		if !speaking || time.Since(start) >= r.speakFor {
			break
		}
		r.ev.onAudio(r.agentID, tone)
	}
	r.amu.Lock()
	r.speaking = false
	r.amu.Unlock()
	if !r.noText {
		r.ev.onText(r.agentID, TranscriptionTopic, reply, map[string]string{AttrTranscriptionFinal: "true"})
	}
	if last {
		r.ev.onParticipantLeft(r.agentID)
		return
	}
	if !r.stateless {
		r.ev.onAttributes(r.agentID, map[string]string{AttrAgentState: StateListening})
	}
}

func newAudioRoom(replies ...string) *fakeAudioRoom {
	return &fakeAudioRoom{fakeRoom: fakeRoom{replies: replies}, respondAfter: 400 * time.Millisecond, speakFor: 700 * time.Millisecond, stopAfter: 150 * time.Millisecond}
}

func newAudioTarget(r *fakeAudioRoom) target.Target {
	return New("voice", Config{
		URL: "ws://lk", APIKey: "k", APISecret: "s", Mode: ModeAudio,
		TurnIdle: 400 * time.Millisecond, JoinTimeout: 300 * time.Millisecond, TurnTimeout: 5 * time.Second,
		connect: r.connect,
	})
}

func audioRunCtx(t *testing.T, sp target.Speech, v *target.Voice) *target.RunContext {
	rc := runCtx(t)
	rc.Speech = sp
	rc.Voice = v
	return rc
}

func TestAudioTurn(t *testing.T) {
	r := newAudioRoom("Our window is 30 days.", "Bye now")
	sp := &fakeSpeech{}
	sess, err := newAudioTarget(r).Start(context.Background(), audioRunCtx(t, sp, &target.Voice{Hint: "en-GB-female", Seed: 1}))
	require.NoError(t, err)
	assert.True(t, r.cfg.Audio, "the connection subscribes to the agent's audio")

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Can I return shoes I bought forty days ago", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text)
	assert.Equal(t, "agent", turn.TranscriptSource, "the agent published its own transcription")
	assert.False(t, turn.Interrupted)
	assert.False(t, turn.Done)
	assert.True(t, turn.FirstTokenAt.After(turn.StartedAt), "first audio comes after the turn was sent")
	assert.Equal(t, []string{"Can I return shoes I bought forty days ago"}, sp.synthesized)
	assert.Empty(t, sp.transcribed, "no STT when the agent's transcript is used")

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "ok bye", At: time.Now()}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bye now", turn.Text)
	assert.True(t, turn.Done)

	dir := t.TempDir()
	sess.(*session).run.Dir = dir
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, r.closedPub)
	assert.Equal(t, map[string]string{WAVFile: WAVFile}, arts.Files)
	rate, ch, err := wav.ReadFile(filepath.Join(dir, WAVFile))
	require.NoError(t, err)
	assert.Equal(t, AudioRate, rate)
	require.Len(t, ch, 2, "stereo: left the caller, right the agent")
	assert.Greater(t, audio.RMS(ch[0]), 0.01, "the caller's speech is on the left")
	assert.Greater(t, audio.RMS(ch[1]), 0.01, "the agent's speech is on the right")
	assert.InDelta(t, len(ch[0]), len(ch[1]), AudioRate/2, "both channels cover the session")

	m := arts.Metrics
	t.Logf("metrics: %v", m)
	assert.InDelta(t, 400, m["time_to_first_audio_ms"], 60, "user speech end → agent speech start")
	assert.InDelta(t, 400, m["time_to_first_audio_ms_p95"], 60)
	assert.InDelta(t, 1400, m["agent_speech_ms"], 150, "two replies of 700 ms")
	assert.Greater(t, m["user_speech_ms"], 700.0, "eight words plus two")
	assert.Equal(t, 0.0, m["talk_over_ratio"])
	assert.Equal(t, 0.0, m["interruptions"])
	assert.InDelta(t, 400, m["silence_gap_max_ms"], 60)
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Equal(t, []string{"joined", "agent_joined", "audio_published", "user_speech_start", "user_speech_end", "agent_speech_start", "agent_speech_end", "transcript_source"}, kinds[:8])
}

func TestAudioSTTFallbackAndForced(t *testing.T) {
	r := newAudioRoom("ignored by the caller", "ignored too")
	r.noText = true
	sp := &fakeSpeech{stt: "I heard the agent."}
	sess, err := newAudioTarget(r).Start(context.Background(), audioRunCtx(t, sp, &target.Voice{Seed: 2}))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello there"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "I heard the agent.", turn.Text)
	assert.Equal(t, "stt", turn.TranscriptSource, "no agent transcription: tamesu transcribes the audio")
	require.Len(t, sp.transcribed, 1)
	assert.InDelta(t, 700, sp.transcribed[0].Milliseconds(), 120, "the clip is the agent's utterance")
	_, _ = sess.Close(context.Background())

	// transcript_source: stt ignores the agent's text even when present.
	r = newAudioRoom("agent text", "more")
	sp = &fakeSpeech{stt: "stt text"}
	sess, err = newAudioTarget(r).Start(context.Background(), audioRunCtx(t, sp, &target.Voice{Seed: 3, TranscriptSource: "stt"}))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "stt text", turn.Text)
	assert.Equal(t, "stt", turn.TranscriptSource)
	_, _ = sess.Close(context.Background())
}

func TestAudioInterruption(t *testing.T) {
	r := newAudioRoom("first long reply that will be cut", "second", "third")
	r.speakFor = 3 * time.Second
	r.noText = true
	sp := &fakeSpeech{stt: "partial"}
	v := &target.Voice{Seed: 7, InterruptProbability: 1}
	sess, err := newAudioTarget(r).Start(context.Background(), audioRunCtx(t, sp, v))
	require.NoError(t, err)

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "I want to complain"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.True(t, turn.Interrupted, "Receive returns early so the caller can talk over the agent")
	assert.Equal(t, "partial", turn.Text)

	// The next turn is spoken while the agent is still talking; the fake
	// agent stops 150 ms after it hears the caller. No more interruptions.
	sess.(*session).mu.Lock()
	sess.(*session).audio.voice.InterruptProbability = 0
	sess.(*session).mu.Unlock()
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "stop talking please"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.False(t, turn.Interrupted)

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	m := arts.Metrics
	t.Logf("metrics: %v", m)
	assert.Equal(t, 1.0, m["interruptions"])
	assert.InDelta(t, 150, m["interruption_stop_ms"], 60, "caller starts → agent audio stops")
	assert.Greater(t, m["talk_over_ratio"], 0.0)
	assert.Less(t, m["talk_over_ratio"], 0.5)
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "interrupting")
	assert.Contains(t, kinds, "interruption")
	assert.Contains(t, kinds, "interruption_stopped")
}

func TestAudioNoiseLineAndPause(t *testing.T) {
	r := newAudioRoom("fine", "bye")
	sp := &fakeSpeech{}
	v := &target.Voice{Seed: 5, BackgroundNoise: "cafe", NoiseSNRdB: 15, LineQuality: "pstn", SilenceMin: 100 * time.Millisecond, SilenceMax: 200 * time.Millisecond}
	sess, err := newAudioTarget(r).Start(context.Background(), audioRunCtx(t, sp, v))
	require.NoError(t, err)
	sent := time.Now()
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "a quiet question"}))
	assert.GreaterOrEqual(t, time.Since(sent), 100*time.Millisecond, "the caller pauses before speaking")
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fine", turn.Text)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	r.amu.Lock()
	defer r.amu.Unlock()
	assert.Greater(t, r.noisy, 10, "the agent hears background noise between the caller's turns")
	assert.Greater(t, r.frames, 20)
	assert.Contains(t, arts.Metrics, "time_to_first_audio_ms")
}

func TestAudioDTMFAndEmptyTurn(t *testing.T) {
	r := newAudioRoom("pressed", "bye")
	sess, err := newAudioTarget(r).Start(context.Background(), audioRunCtx(t, &fakeSpeech{}, &target.Voice{Seed: 1}))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{DTMF: "12"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pressed", turn.Text)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "dtmf_sent")
}

func TestAudioNeedsSpeechAndOpus(t *testing.T) {
	r := newAudioRoom("hi")
	_, err := newAudioTarget(r).Start(context.Background(), runCtx(t))
	assert.ErrorIs(t, err, ErrNoSpeech)
	// A text-only room cannot do audio.
	plain := &fakeRoom{replies: []string{"hi"}}
	tgt := New("voice", Config{URL: "ws://lk", APIKey: "k", APISecret: "s", Mode: ModeAudio, JoinTimeout: 300 * time.Millisecond, connect: plain.connect})
	_, err = tgt.Start(context.Background(), audioRunCtx(t, &fakeSpeech{}, nil))
	assert.ErrorContains(t, err, "cannot publish audio")
	assert.True(t, plain.closed)
	_, err = New("voice", Config{URL: "ws://lk", APIKey: "k", APISecret: "s", Mode: "video", connect: plain.connect}).Start(context.Background(), runCtx(t))
	assert.ErrorContains(t, err, "mode must be text or audio")
}

func TestAudioConformance(t *testing.T) {
	targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
		r := newAudioRoom(replies...)
		r.respondAfter = 250 * time.Millisecond
		r.speakFor = 300 * time.Millisecond
		return &speechTarget{Target: newAudioTarget(r)}
	}})
}

// speechTarget injects the fake speech layer into the harness's RunContext.
type speechTarget struct{ target.Target }

func (s *speechTarget) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	run.Speech = &fakeSpeech{}
	run.Voice = &target.Voice{Seed: 1}
	return s.Target.Start(ctx, run)
}

func TestOverlapAndTimeline(t *testing.T) {
	assert.Equal(t, 50, overlap([]segment{{0, 100}}, []segment{{50, 200}}))
	assert.Equal(t, 0, overlap([]segment{{0, 100}}, []segment{{100, 200}}))
	assert.Equal(t, 300, totalSamples([]segment{{0, 100}, {100, 300}}))
	tl := &timeline{start: time.Now()}
	from := tl.appendAt(tl.start.Add(10*time.Millisecond), make([]int16, 960))
	assert.Equal(t, 0, from, "small jitter appends contiguously")
	from = tl.appendAt(tl.start.Add(2*time.Second), make([]int16, 960))
	assert.Equal(t, 2*AudioRate, from, "a long gap is padded with silence")
	assert.Len(t, tl.slice(-5, 10*AudioRate), tl.pos())
}
