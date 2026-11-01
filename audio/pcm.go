// Package audio is tamesu's audio pipeline: PCM buffers, resampling, test
// signals and level maths, with WAV, VAD, noise, G.711 and line simulation in
// its subpackages. Everything here is pure Go; Opus and the Silero VAD need
// CGO and live behind build tags.
package audio

import (
	"math"
	"time"
)

// PCM is mono 16-bit audio at a sample rate.
type PCM struct {
	Rate    int
	Samples []int16
}

// Duration of n samples at rate.
func Duration(n, rate int) time.Duration {
	if rate <= 0 {
		return 0
	}
	return time.Duration(float64(n) / float64(rate) * float64(time.Second))
}

// SampleCount is how many samples d lasts at rate.
func SampleCount(d time.Duration, rate int) int {
	return int(math.Round(d.Seconds() * float64(rate)))
}

// Duration of the buffer.
func (p PCM) Duration() time.Duration { return Duration(len(p.Samples), p.Rate) }

// Clone copies the buffer.
func (p PCM) Clone() PCM {
	return PCM{Rate: p.Rate, Samples: append([]int16(nil), p.Samples...)}
}

// Silence of d at rate.
func Silence(rate int, d time.Duration) PCM {
	return PCM{Rate: rate, Samples: make([]int16, SampleCount(d, rate))}
}

// Tone is a sine test signal; amplitude is 0 to 1 of full scale.
func Tone(rate int, freq float64, d time.Duration, amplitude float64) PCM {
	n := SampleCount(d, rate)
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(amplitude * 32767 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return PCM{Rate: rate, Samples: s}
}

// Concat appends b to a; b is resampled to a's rate when they differ.
func Concat(a, b PCM) PCM {
	if a.Rate == 0 {
		return b.Clone()
	}
	if b.Rate != a.Rate {
		b = Resample(b, a.Rate)
	}
	out := make([]int16, 0, len(a.Samples)+len(b.Samples))
	out = append(out, a.Samples...)
	out = append(out, b.Samples...)
	return PCM{Rate: a.Rate, Samples: out}
}

// Resample converts p to rate with linear interpolation, good enough for
// speech between 8, 16, 24 and 48 kHz.
func Resample(p PCM, rate int) PCM {
	if p.Rate == rate || p.Rate <= 0 || rate <= 0 || len(p.Samples) == 0 {
		return PCM{Rate: rate, Samples: append([]int16(nil), p.Samples...)}
	}
	ratio := float64(p.Rate) / float64(rate)
	n := int(math.Round(float64(len(p.Samples)) / ratio))
	out := make([]int16, n)
	last := len(p.Samples) - 1
	for i := range out {
		pos := float64(i) * ratio
		j := int(pos)
		if j >= last {
			out[i] = p.Samples[last]
			continue
		}
		frac := pos - float64(j)
		out[i] = int16(float64(p.Samples[j])*(1-frac) + float64(p.Samples[j+1])*frac)
	}
	return PCM{Rate: rate, Samples: out}
}

// RMS is the root mean square level of the samples, 0 to 1 of full scale.
func RMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		f := float64(s) / 32768
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// DBFS converts a 0..1 level to decibels relative to full scale.
func DBFS(level float64) float64 {
	if level <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(level)
}

// Gain scales the samples by factor, clipping at full scale.
func Gain(p PCM, factor float64) PCM {
	out := make([]int16, len(p.Samples))
	for i, s := range p.Samples {
		out[i] = clip(float64(s) * factor)
	}
	return PCM{Rate: p.Rate, Samples: out}
}

// Mix adds b (scaled by gainB) onto a sample by sample, clipping; the result
// is as long as the longer input. b is resampled to a's rate when needed.
func Mix(a, b PCM, gainB float64) PCM {
	if b.Rate != a.Rate && b.Rate > 0 && a.Rate > 0 {
		b = Resample(b, a.Rate)
	}
	n := max(len(a.Samples), len(b.Samples))
	out := make([]int16, n)
	for i := range out {
		var v float64
		if i < len(a.Samples) {
			v += float64(a.Samples[i])
		}
		if i < len(b.Samples) {
			v += float64(b.Samples[i]) * gainB
		}
		out[i] = clip(v)
	}
	return PCM{Rate: a.Rate, Samples: out}
}

// Frames splits p into frames of d (the last partial frame is dropped).
func Frames(p PCM, d time.Duration) [][]int16 {
	size := SampleCount(d, p.Rate)
	if size <= 0 {
		return nil
	}
	var out [][]int16
	for i := 0; i+size <= len(p.Samples); i += size {
		out = append(out, p.Samples[i:i+size])
	}
	return out
}

// ToFloat32 converts samples to -1..1.
func ToFloat32(samples []int16) []float32 {
	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = float32(s) / 32768
	}
	return out
}

// FromFloat32 converts -1..1 samples to int16, clipping.
func FromFloat32(samples []float32) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		out[i] = clip(float64(s) * 32768)
	}
	return out
}

func clip(v float64) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(v)
}

// Len is the number of samples.
func (p PCM) Len() int { return len(p.Samples) }

// Slice returns the audio between from and to (clamped to the buffer).
func (p PCM) Slice(from, to time.Duration) PCM {
	i := min(max(SampleCount(from, p.Rate), 0), len(p.Samples))
	j := min(max(SampleCount(to, p.Rate), i), len(p.Samples))
	return PCM{Rate: p.Rate, Samples: append([]int16(nil), p.Samples[i:j]...)}
}

// Interleave zips left and right into stereo frames (L, R, L, R…), padding
// the shorter channel with silence; right is resampled to left's rate.
func Interleave(left, right PCM) []int16 {
	if right.Rate != left.Rate && right.Rate > 0 && left.Rate > 0 {
		right = Resample(right, left.Rate)
	}
	n := max(len(left.Samples), len(right.Samples))
	out := make([]int16, 0, 2*n)
	for i := 0; i < n; i++ {
		var l, r int16
		if i < len(left.Samples) {
			l = left.Samples[i]
		}
		if i < len(right.Samples) {
			r = right.Samples[i]
		}
		out = append(out, l, r)
	}
	return out
}
