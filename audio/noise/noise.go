// Package noise mixes background noise into the simulated user's audio at a
// signal-to-noise ratio: synthesised presets (cafe, street, office) or a WAV
// file, so the agent hears what a real caller's microphone would pick up.
package noise

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"time"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/wav"
)

// Presets are the built-in noises.
var Presets = map[string]string{
	"cafe":   "murmur and clatter: pink noise with slow amplitude swells",
	"street": "traffic: brown noise with passing-vehicle swells",
	"office": "a room tone: mains hum with a little pink noise",
	"white":  "flat white noise",
}

// PresetNames lists the presets, sorted.
func PresetNames() []string {
	names := make([]string, 0, len(Presets))
	for n := range Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Generate synthesises d of the named preset at rate, deterministic for a seed.
func Generate(name string, rate int, d time.Duration, seed uint64) (audio.PCM, error) {
	if _, ok := Presets[name]; !ok {
		return audio.PCM{}, fmt.Errorf("noise: unknown preset %q (have %v)", name, PresetNames())
	}
	n := audio.SampleCount(d, rate)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	out := make([]float64, n)
	switch name {
	case "white":
		for i := range out {
			out[i] = rng.Float64()*2 - 1
		}
	case "cafe":
		pink(out, rng)
		swell(out, rate, 0.4, 0.25, rng)
	case "street":
		brown(out, rng)
		swell(out, rate, 0.15, 0.5, rng)
	case "office":
		hum := 50.0
		for i := range out {
			out[i] = 0.6 * math.Sin(2*math.Pi*hum*float64(i)/float64(rate))
		}
		p := make([]float64, n)
		pink(p, rng)
		for i := range out {
			out[i] += 0.4 * p[i]
		}
	}
	normalize(out, 0.3)
	s := make([]int16, n)
	for i, v := range out {
		s[i] = int16(v * 32767)
	}
	return audio.PCM{Rate: rate, Samples: s}, nil
}

// pink fills out with 1/f noise (Voss-McCartney with 8 rows).
func pink(out []float64, rng *rand.Rand) {
	const rows = 8
	var vals [rows]float64
	var sum float64
	for i := range vals {
		vals[i] = rng.Float64()*2 - 1
		sum += vals[i]
	}
	for i := range out {
		row := 0
		for k := i; k&1 == 1 && row < rows-1; k >>= 1 {
			row++
		}
		sum -= vals[row]
		vals[row] = rng.Float64()*2 - 1
		sum += vals[row]
		out[i] = (sum/rows + (rng.Float64()*2-1)*0.2) / 1.2
	}
}

// brown fills out with integrated white noise (1/f²), leaky.
func brown(out []float64, rng *rand.Rand) {
	v := 0.0
	for i := range out {
		v = v*0.995 + (rng.Float64()*2-1)*0.05
		out[i] = v
	}
}

// swell modulates the amplitude with slow random swells (period around
// 1/rateHz seconds, depth 0..1).
func swell(out []float64, rate int, rateHz, depth float64, rng *rand.Rand) {
	phase := rng.Float64() * 2 * math.Pi
	for i := range out {
		m := 1 - depth + depth*(0.5+0.5*math.Sin(2*math.Pi*rateHz*float64(i)/float64(rate)+phase))
		out[i] *= m
	}
}

// normalize scales out so its RMS is level (0..1), clipping nothing.
func normalize(out []float64, level float64) {
	var sum float64
	for _, v := range out {
		sum += v * v
	}
	if len(out) == 0 || sum == 0 {
		return
	}
	rms := math.Sqrt(sum / float64(len(out)))
	g := level / rms
	for i := range out {
		v := out[i] * g
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		out[i] = v
	}
}

// FromFile loads a WAV as noise (the first channel), resampled to rate.
func FromFile(path string, rate int) (audio.PCM, error) {
	f, err := os.Open(path)
	if err != nil {
		return audio.PCM{}, err
	}
	defer func() { _ = f.Close() }()
	r, ch, err := wav.Read(f)
	if err != nil {
		return audio.PCM{}, fmt.Errorf("noise: %s: %w", path, err)
	}
	return audio.Resample(audio.PCM{Rate: r, Samples: ch[0]}, rate), nil
}

// MixAt adds noise under signal so the signal-to-noise ratio is snrDB,
// looping the noise to cover the signal. The signal's RMS is measured over
// its non-silent part; a silent signal gets the noise at -40 dBFS.
func MixAt(signal, noise audio.PCM, snrDB float64) audio.PCM {
	if len(noise.Samples) == 0 {
		return signal.Clone()
	}
	if noise.Rate != signal.Rate {
		noise = audio.Resample(noise, signal.Rate)
	}
	looped := make([]int16, len(signal.Samples))
	for i := range looped {
		looped[i] = noise.Samples[i%len(noise.Samples)]
	}
	sigRMS := activeRMS(signal.Samples)
	target := sigRMS / math.Pow(10, snrDB/20)
	if sigRMS == 0 {
		target = math.Pow(10, -40.0/20)
	}
	noiseRMS := audio.RMS(looped)
	gain := 0.0
	if noiseRMS > 0 {
		gain = target / noiseRMS
	}
	return audio.Mix(signal, audio.PCM{Rate: signal.Rate, Samples: looped}, gain)
}

// activeRMS is the RMS of the frames above -50 dBFS, so leading and trailing
// silence does not lower the measured signal level.
func activeRMS(samples []int16) float64 {
	const frame = 480
	var sum float64
	n := 0
	for i := 0; i+frame <= len(samples); i += frame {
		r := audio.RMS(samples[i : i+frame])
		if audio.DBFS(r) > -50 {
			sum += r * r * frame
			n += frame
		}
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(n))
}
