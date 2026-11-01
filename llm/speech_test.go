//go:build llmfake

package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/internal/model"
)

func TestSynthesizeAndTranscribe(t *testing.T) {
	c := client(t)
	before := c.Usage()
	a, err := c.Synthesize(context.Background(), model.SynthesizeRequest{Text: "Hi, I bought a pair of shoes forty days ago", Voice: "en-GB-female"})
	require.NoError(t, err)
	assert.Equal(t, 24000, a.Rate)
	pcm := audio.PCM{Rate: a.Rate, Samples: a.Samples}
	assert.InDelta(t, 0.6, pcm.Duration().Seconds(), 0.01, "60 ms per word for 10 words")
	assert.InDelta(t, before.CostUSD+0.0043, c.Usage().CostUSD, 0.0001, "43 characters at the fake per-character rate, nothing for the audio")
	assert.InDelta(t, 0.3/1.414, audio.RMS(a.Samples), 0.02, "a -13.5 dBFS tone")
	after := c.Usage()
	assert.Equal(t, before.Calls+1, after.Calls)
	assert.Greater(t, after.CostUSD, before.CostUSD, "fake/tts is priced per character")

	tr, err := c.Transcribe(context.Background(), model.TranscribeRequest{Audio: model.Audio{Rate: 16000, Samples: audio.Tone(16000, 200, 500_000_000, 0.3).Samples}, Language: "en"})
	require.NoError(t, err)
	assert.Equal(t, "Our return window is 30 days from delivery, so a return after 40 days is not possible.", tr.Text)
	assert.Equal(t, 0.99, tr.Confidence)
	assert.NotEmpty(t, tr.TraceID)
	assert.Equal(t, after.Calls+1, c.Usage().Calls)

	_, err = c.Transcribe(context.Background(), model.TranscribeRequest{Audio: model.Audio{Rate: 16000}})
	assert.ErrorContains(t, err, "no audio to transcribe")
}

func TestSynthesizeNeedsAModel(t *testing.T) {
	c := client(t)
	saved := c.cfg.Speech
	c.cfg.Speech = SpeechConfig{}
	defer func() { c.cfg.Speech = saved }()
	_, err := c.Synthesize(context.Background(), model.SynthesizeRequest{Text: "hello"})
	assert.ErrorIs(t, err, ErrNoSpeechModel)
	_, err = c.Transcribe(context.Background(), model.TranscribeRequest{Audio: model.Audio{Rate: 16000, Samples: make([]int16, 1600)}})
	assert.ErrorIs(t, err, ErrNoSpeechModel)
	// A per-request model works without the config.
	a, err := c.Synthesize(context.Background(), model.SynthesizeRequest{Text: "hello", Model: "fake/tts", Voice: "male"})
	require.NoError(t, err)
	assert.Equal(t, 24000, a.Rate)
}
