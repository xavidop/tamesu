//go:build onnx

package vad

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
)

func TestSileroRuns(t *testing.T) {
	det, err := NewSilero(SileroOptions{})
	if err != nil {
		t.Skipf("onnxruntime not loadable here: %v", err)
	}
	defer det.Close()
	assert.Equal(t, 512, det.FrameSize())
	assert.Equal(t, 16000, det.Rate())

	silence := make([]int16, 512)
	for range 5 {
		p, err := det.Probability(silence)
		require.NoError(t, err)
		assert.Less(t, p, float32(0.3), "silence is not speech")
	}
	// A frame of the wrong size is refused.
	_, err = det.Probability(make([]int16, 480))
	assert.ErrorContains(t, err, "512-sample frames")

	// Shaped noise bursts are not speech either; the point is the model runs
	// end to end on real frames and its state carries over.
	det.Reset()
	tone := audio.Tone(16000, 220, time.Second, 0.3)
	var max float32
	for _, f := range audio.Frames(tone, 32*time.Millisecond) {
		p, err := det.Probability(f)
		require.NoError(t, err)
		if p > max {
			max = p
		}
	}
	t.Logf("max speech probability on a 220 Hz tone: %.3f", max)
	assert.False(t, det.Speech(silence))
}
