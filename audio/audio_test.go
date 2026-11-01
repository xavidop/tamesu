package audio

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToneSilenceAndLevels(t *testing.T) {
	tone := Tone(16000, 440, time.Second, 0.5)
	assert.Equal(t, 16000, len(tone.Samples))
	assert.Equal(t, time.Second, tone.Duration())
	assert.InDelta(t, 0.5/math.Sqrt2, RMS(tone.Samples), 0.01, "a sine's RMS is amplitude/√2")
	assert.InDelta(t, -9.0, DBFS(RMS(tone.Samples)), 0.2)
	assert.True(t, math.IsInf(DBFS(0), -1))
	s := Silence(8000, 250*time.Millisecond)
	assert.Equal(t, 2000, len(s.Samples))
	assert.Equal(t, 0.0, RMS(s.Samples))
}

func TestResample(t *testing.T) {
	tests := []struct {
		name     string
		from, to int
	}{
		{"48k to 16k", 48000, 16000},
		{"16k to 48k", 16000, 48000},
		{"24k to 8k", 24000, 8000},
		{"8k to 16k", 8000, 16000},
		{"same", 16000, 16000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Tone(tt.from, 300, 500*time.Millisecond, 0.5)
			out := Resample(in, tt.to)
			assert.Equal(t, tt.to, out.Rate)
			assert.InDelta(t, 0.5, out.Duration().Seconds(), 0.001)
			assert.InDelta(t, RMS(in.Samples), RMS(out.Samples), 0.02, "a 300 Hz tone keeps its level through resampling")
			// Zero crossings per second give the frequency back.
			zc := 0
			for i := 1; i < len(out.Samples); i++ {
				if (out.Samples[i-1] < 0) != (out.Samples[i] < 0) {
					zc++
				}
			}
			assert.InDelta(t, 300.0, float64(zc)/2/out.Duration().Seconds(), 3)
		})
	}
	assert.Empty(t, Resample(PCM{Rate: 16000}, 8000).Samples)
}

func TestMixGainConcatFrames(t *testing.T) {
	a := Tone(16000, 440, 100*time.Millisecond, 0.5)
	b := Tone(8000, 440, 200*time.Millisecond, 0.5)
	m := Mix(a, b, 1)
	assert.Equal(t, 16000, m.Rate)
	assert.Equal(t, 3200, len(m.Samples), "as long as the longer input, at a's rate")
	loud := Gain(a, 10)
	assert.Equal(t, int16(32767), maxAbs(loud.Samples), "clipped at full scale")
	c := Concat(a, b)
	assert.Equal(t, 4800, len(c.Samples))
	assert.Equal(t, 10, len(Frames(a, 10*time.Millisecond)))
	assert.Nil(t, Frames(a, 0))
	f32 := ToFloat32(a.Samples)
	assert.InDelta(t, float64(a.Samples[10])/32768, float64(f32[10]), 1e-6)
	assert.Equal(t, a.Samples[:100], FromFloat32(f32)[:100])
}

func maxAbs(s []int16) int16 {
	var m int16
	for _, v := range s {
		if v < 0 {
			v = -v
		}
		if v > m {
			m = v
		}
	}
	return m
}

func TestSampleCount(t *testing.T) {
	assert.Equal(t, 480, SampleCount(10*time.Millisecond, 48000))
	assert.Equal(t, 160, SampleCount(20*time.Millisecond, 8000))
	require.Equal(t, time.Duration(0), Duration(100, 0))
}

func TestSliceAndInterleave(t *testing.T) {
	a := Tone(8000, 440, time.Second, 0.5)
	s := a.Slice(250*time.Millisecond, 500*time.Millisecond)
	assert.Equal(t, 2000, s.Len())
	assert.Equal(t, a.Samples[2000:4000], s.Samples)
	assert.Equal(t, 0, a.Slice(2*time.Second, 3*time.Second).Len(), "clamped to the buffer")
	st := Interleave(PCM{Rate: 8000, Samples: []int16{1, 2}}, PCM{Rate: 8000, Samples: []int16{9}})
	assert.Equal(t, []int16{1, 9, 2, 0}, st)
}
