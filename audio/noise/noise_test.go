package noise

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
)

func TestPresets(t *testing.T) {
	for _, name := range PresetNames() {
		t.Run(name, func(t *testing.T) {
			n, err := Generate(name, 16000, time.Second, 1)
			require.NoError(t, err)
			assert.Equal(t, 16000, len(n.Samples))
			assert.InDelta(t, 0.3, audio.RMS(n.Samples), 0.03, "presets are normalised to -10 dBFS")
			again, _ := Generate(name, 16000, time.Second, 1)
			assert.Equal(t, n.Samples[:100], again.Samples[:100], "deterministic for a seed")
			other, _ := Generate(name, 16000, time.Second, 2)
			assert.NotEqual(t, n.Samples[:100], other.Samples[:100])
		})
	}
	_, err := Generate("rain", 16000, time.Second, 1)
	assert.ErrorContains(t, err, `unknown preset "rain"`)
}

func TestMixAtSNR(t *testing.T) {
	speech := audio.Tone(16000, 300, time.Second, 0.3)
	noise, _ := Generate("white", 16000, 500*time.Millisecond, 3)
	for _, snr := range []float64{20, 10, 0} {
		mixed := MixAt(speech, noise, snr)
		assert.Len(t, mixed.Samples, len(speech.Samples), "the noise is looped under the signal")
		// Subtract the signal to recover the noise and measure the ratio.
		residual := make([]int16, len(mixed.Samples))
		for i := range residual {
			residual[i] = int16(int(mixed.Samples[i]) - int(speech.Samples[i]))
		}
		got := audio.DBFS(audio.RMS(speech.Samples)) - audio.DBFS(audio.RMS(residual))
		assert.InDelta(t, snr, got, 1.0, "SNR %g dB", snr)
	}
	// A signal with leading silence is measured on its active part.
	padded := audio.Concat(audio.Silence(16000, time.Second), speech)
	mixed := MixAt(padded, noise, 10)
	head := audio.RMS(mixed.Samples[:16000])
	want := audio.DBFS(audio.RMS(speech.Samples)) - 10
	assert.InDelta(t, want, audio.DBFS(head), 1.0, "the noise under the silence sits 10 dB under the speech level")
	silent := MixAt(audio.Silence(16000, 200*time.Millisecond), noise, 10)
	assert.InDelta(t, -40.0, audio.DBFS(audio.RMS(silent.Samples)), 1.0, "a silent signal gets noise at -40 dBFS")
	assert.Equal(t, speech.Samples, MixAt(speech, audio.PCM{}, 10).Samples)
}

func TestFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.wav")
	n, _ := Generate("cafe", 8000, 100*time.Millisecond, 1)
	require.NoError(t, wav.WriteFile(path, 8000, n.Samples))
	got, err := FromFile(path, 16000)
	require.NoError(t, err)
	assert.Equal(t, 16000, got.Rate)
	assert.InDelta(t, 1600, len(got.Samples), 2)
	_, err = FromFile(filepath.Join(t.TempDir(), "missing.wav"), 16000)
	assert.Error(t, err)
	assert.False(t, math.IsNaN(audio.RMS(got.Samples)))
}
