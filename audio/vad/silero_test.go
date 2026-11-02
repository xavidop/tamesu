//go:build onnx

package vad

import (
	"encoding/binary"
	"os"
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

// TestSileroHearsSpeech runs the model on 2.5 s of synthesized speech
// (testdata/speech-16k.wav, 16 kHz mono) and expects it to light up.
func TestSileroHearsSpeech(t *testing.T) {
	det, err := NewSilero(SileroOptions{})
	if err != nil {
		t.Skipf("onnxruntime not loadable here: %v", err)
	}
	defer det.Close()
	b, err := os.ReadFile("testdata/speech-16k.wav")
	require.NoError(t, err)
	require.Equal(t, "data", string(b[36:40]), "a plain 44-byte WAV header")
	pcm := make([]int16, (len(b)-44)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[44+2*i:]))
	}
	var max float32
	speech, frames := 0, 0
	for i := 0; i+512 <= len(pcm); i += 512 {
		p, err := det.Probability(pcm[i : i+512])
		require.NoError(t, err)
		frames++
		if p > max {
			max = p
		}
		if p >= 0.5 {
			speech++
		}
	}
	t.Logf("speech in %d of %d frames, max probability %.3f", speech, frames, max)
	assert.Greater(t, max, float32(0.9), "speech is heard with confidence")
	assert.Greater(t, float64(speech)/float64(frames), 0.4, "most of the clip is speech")
	det.Reset()
	assert.False(t, det.Speech(make([]int16, 512)), "silence after a reset is not speech")
}

func TestFindLibrary(t *testing.T) {
	assert.Equal(t, "/x/lib.so", findLibrary("/x/lib.so"), "an explicit path wins")
	t.Setenv(LibraryEnv, "/env/lib.so")
	assert.Equal(t, "/env/lib.so", findLibrary(""), "then the environment")
}
