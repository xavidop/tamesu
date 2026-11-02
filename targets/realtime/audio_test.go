package realtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
	"github.com/xavidop/tamesu/target"
)

// fakeSpeech speaks a 48 kHz tone whose length grows with the text.
type fakeSpeech struct{ requests []target.SynthesizeRequest }

func (f *fakeSpeech) Synthesize(_ context.Context, req target.SynthesizeRequest) (target.Audio, error) {
	f.requests = append(f.requests, req)
	tone := audio.Tone(48000, 220, time.Duration(len(req.Text))*20*time.Millisecond, 0.4)
	return target.Audio{Rate: 48000, Samples: tone.Samples}, nil
}

func (f *fakeSpeech) Transcribe(context.Context, target.TranscribeRequest) (target.Transcript, error) {
	return target.Transcript{}, nil
}

func TestAudioTurn(t *testing.T) {
	f := &fakeServer{replies: []string{"Our window is 30 days.", "Bye"}, audio: true}
	tgt := newTarget(t, f, nil)
	run := runCtx(t)
	speech := &fakeSpeech{}
	run.Speech = speech
	run.Voice = &target.Voice{Hint: "en-GB-female", Language: "en-GB"}
	sess, err := tgt.Start(context.Background(), run)
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Can I return these shoes?", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text, "the transcript deltas are the agent's words")
	assert.Equal(t, "agent", turn.TranscriptSource)
	assert.False(t, turn.FirstTokenAt.IsZero())
	require.Len(t, speech.requests, 1)
	assert.Equal(t, "en-GB-female", speech.requests[0].Voice)

	f.mu.Lock()
	assert.Equal(t, 1, f.commits)
	// 25 characters * 20 ms = 500 ms at 24 kHz = 5 chunks of 100 ms.
	assert.Equal(t, 5, f.appended)
	assert.Equal(t, []any{"audio"}, f.sessions[0]["output_modalities"])
	f.mu.Unlock()

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "thanks", At: time.Now()}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.True(t, turn.Done)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, WAVFile, arts.Files[WAVFile])
	rate, channels, err := wav.ReadFile(filepath.Join(run.Dir, WAVFile))
	require.NoError(t, err)
	assert.Equal(t, AudioRate, rate)
	require.Len(t, channels, 2)
	assert.Equal(t, len(channels[0]), len(channels[1]))
	assert.Greater(t, audio.RMS(channels[0]), 0.0, "the caller is on the left")
	assert.Greater(t, audio.RMS(channels[1]), 0.0, "the agent is on the right")
	assert.Greater(t, arts.Metrics["time_to_first_audio_ms_p50"], 0.0)
	// Each reply word brings 60 ms of tone: 5 + 1 words.
	assert.InDelta(t, 360, arts.Metrics["agent_speech_ms"], 1)
	assert.InDelta(t, 500+120, arts.Metrics["user_speech_ms"], 1)
}

func TestPCMCodec(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	out, err := decodePCM(encodePCM(in))
	require.NoError(t, err)
	assert.Equal(t, in, out)
	_, err = decodePCM("not base64!")
	assert.Error(t, err)
}
