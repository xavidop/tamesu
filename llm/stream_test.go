//go:build llmfake

package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/llm/llmfake"
)

func TestOpenTranscriptionStreams(t *testing.T) {
	c := client(t)
	before := c.Usage()
	st, err := c.OpenTranscription(context.Background(), model.TranscriptionStreamRequest{Rate: 16000, Language: "en"})
	require.NoError(t, err)
	for range 10 {
		require.NoError(t, st.Write(context.Background(), make([]int16, 1600)))
	}
	assert.Equal(t, before.Calls, c.Usage().Calls, "nothing is billed before Close")
	tr, err := st.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our return window is 30 days from delivery, so a return after 40 days is not possible.", tr.Text)
	assert.Equal(t, 0.99, tr.Confidence)
	after := c.Usage()
	assert.Equal(t, before.Calls+1, after.Calls, "a streamed turn is one call")
	assert.InDelta(t, before.CostUSD+0.01/60, after.CostUSD, 1e-9, "one second of audio at fake/stt's per-minute rate")
	assert.Equal(t, before.Fallbacks, after.Fallbacks)
}

func TestOpenTranscriptionFallsBackToBatch(t *testing.T) {
	c := client(t)
	fakeInstance.SetScript(llmfake.Script{Fail: map[string]int{"stt": 1}})
	t.Cleanup(func() { fakeInstance.SetScript(llmfake.Script{}) })
	before := c.Usage()
	st, err := c.OpenTranscription(context.Background(), model.TranscriptionStreamRequest{Rate: 16000})
	require.NoError(t, err, "a stream that does not open is not an error: the audio is buffered")
	require.NoError(t, st.Write(context.Background(), make([]int16, 16000)))
	tr, err := st.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our return window is 30 days from delivery, so a return after 40 days is not possible.", tr.Text)
	assert.NotEmpty(t, tr.TraceID, "the batch flow ran")
	after := c.Usage()
	assert.Equal(t, before.Fallbacks+1, after.Fallbacks)
	assert.Equal(t, before.Calls+1, after.Calls)

	// A model with no streaming path is buffered and transcribed at Close.
	st, err = c.OpenTranscription(context.Background(), model.TranscriptionStreamRequest{Rate: 16000, Model: "fake/down"})
	require.NoError(t, err)
	require.NoError(t, st.Write(context.Background(), make([]int16, 160)))
	_, err = st.Close(context.Background())
	assert.ErrorContains(t, err, "down")

	empty, err := c.OpenTranscription(context.Background(), model.TranscriptionStreamRequest{Rate: 16000})
	require.NoError(t, err)
	tr, err = empty.Close(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tr.Text, "no audio, no call")
}

func TestOpenTranscriptionErrors(t *testing.T) {
	c := client(t)
	_, err := c.OpenTranscription(context.Background(), model.TranscriptionStreamRequest{})
	assert.ErrorContains(t, err, "sample rate")
	bare, err := New(context.Background(), Config{Plugins: []string{"fake"}, Models: Models{Default: "fake/text"}})
	require.NoError(t, err)
	_, err = bare.OpenTranscription(context.Background(), model.TranscriptionStreamRequest{Rate: 16000})
	assert.ErrorIs(t, err, ErrNoSpeechModel)
}
