package opus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/audio"
)

func TestSamplesAndFrames(t *testing.T) {
	assert.Equal(t, 960, Samples(20*time.Millisecond, SampleRate))
	assert.True(t, validFrame(960, SampleRate))
	assert.True(t, validFrame(160, 16000))
	assert.False(t, validFrame(1000, SampleRate))
}

func TestRoundTrip(t *testing.T) {
	if !Available() {
		_, err := NewEncoder(SampleRate, 1, 0)
		assert.ErrorIs(t, err, ErrUnavailable)
		_, err = NewDecoder(SampleRate, 1)
		assert.ErrorIs(t, err, ErrUnavailable)
		t.Skip("libopus not linked into this build")
	}
	assert.NotEmpty(t, Version())
	enc, err := NewEncoder(SampleRate, 1, 32000)
	require.NoError(t, err)
	defer enc.Close()
	dec, err := NewDecoder(SampleRate, 1)
	require.NoError(t, err)
	defer dec.Close()

	tone := audio.Tone(SampleRate, 440, time.Second, 0.3)
	frames := audio.Frames(tone, 20*time.Millisecond)
	require.Len(t, frames, 50)
	var out []int16
	var bytes int
	for _, f := range frames {
		pkt, err := enc.Encode(f)
		require.NoError(t, err)
		bytes += len(pkt)
		pcm, err := dec.Decode(pkt, len(f))
		require.NoError(t, err)
		out = append(out, pcm...)
	}
	assert.Len(t, out, len(tone.Samples))
	assert.Less(t, bytes, 6000, "32 kbit/s for one second is about 4 KB")
	// Skip the first frames: the codec needs a few to settle.
	assert.InDelta(t, audio.RMS(tone.Samples[9600:]), audio.RMS(out[9600:]), 0.03, "the tone's level survives the codec")

	_, err = enc.Encode(make([]int16, 1000))
	assert.ErrorContains(t, err, "not a valid frame")
	plc, err := dec.Decode(nil, 960)
	require.NoError(t, err)
	assert.Len(t, plc, 960, "a lost packet is concealed")
}
