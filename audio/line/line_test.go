package line

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
)

func TestProfiles(t *testing.T) {
	assert.Equal(t, []string{"mobile_poor", "pstn", "voip_good"}, Names())
	_, err := Lookup("satellite")
	assert.ErrorContains(t, err, `unknown quality "satellite"`)
	speech := audio.Tone(48000, 440, time.Second, 0.3)
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			p, err := Lookup(name)
			require.NoError(t, err)
			out := Apply(speech, p, 7)
			assert.Equal(t, 48000, out.Rate, "back at the input rate")
			assert.InDelta(t, 1.0, out.Duration().Seconds(), 0.002)
			assert.InDelta(t, audio.RMS(speech.Samples), audio.RMS(out.Samples), 0.03, "a 440 Hz tone survives the line")
			assert.NotEqual(t, speech.Samples[1000:1100], out.Samples[1000:1100], "the line changes the samples")
			again := Apply(speech, p, 7)
			assert.Equal(t, out.Samples, again.Samples, "deterministic for a seed")
		})
	}
	assert.Empty(t, Apply(audio.PCM{Rate: 16000}, Profiles["pstn"], 1).Samples)
}

func TestLossConceals(t *testing.T) {
	p := Profile{Name: "lossy", Rate: 8000, LossPct: 50}
	in := audio.Tone(8000, 200, 400*time.Millisecond, 0.3)
	out := Apply(in, p, 1)
	assert.Len(t, out.Samples, len(in.Samples))
	assert.Greater(t, audio.RMS(out.Samples), 0.1, "repeated frames keep the level up")
}
