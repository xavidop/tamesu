// Package line simulates a phone line on the outgoing audio: a G.711 round
// trip at 8 kHz for PSTN, plus jitter and packet loss profiles, so the agent
// hears what a real caller would sound like.
package line

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/audio/g711"
)

// Profile is one line quality.
type Profile struct {
	Name string
	// Rate is the line's sample rate; the audio is resampled down and back.
	Rate int
	// Codec is mulaw, alaw or "" (no codec).
	Codec string
	// LossPct is the percentage of 20 ms frames lost (replaced by the
	// previous frame, as a decoder's loss concealment would).
	LossPct float64
	// JitterPct is the percentage of frames delivered late enough to be
	// swapped with the next one.
	JitterPct float64
}

// Profiles are the built-in line qualities (design.md §5 line_quality).
var Profiles = map[string]Profile{
	"pstn":        {Name: "pstn", Rate: 8000, Codec: "mulaw"},
	"mobile_poor": {Name: "mobile_poor", Rate: 8000, Codec: "alaw", LossPct: 3, JitterPct: 5},
	"voip_good":   {Name: "voip_good", Rate: 16000, LossPct: 0.5},
}

// Names lists the profiles, sorted.
func Names() []string {
	names := make([]string, 0, len(Profiles))
	for n := range Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Lookup returns a profile by name.
func Lookup(name string) (Profile, error) {
	p, ok := Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("line: unknown quality %q (have %v)", name, Names())
	}
	return p, nil
}

// Apply runs pcm through the profile and returns audio at pcm's rate.
func Apply(pcm audio.PCM, p Profile, seed uint64) audio.PCM {
	if len(pcm.Samples) == 0 {
		return pcm.Clone()
	}
	rate := p.Rate
	if rate == 0 {
		rate = pcm.Rate
	}
	down := audio.Resample(pcm, rate)
	switch p.Codec {
	case "mulaw":
		down.Samples = g711.DecodeMuLaw(g711.EncodeMuLaw(down.Samples))
	case "alaw":
		down.Samples = g711.DecodeALaw(g711.EncodeALaw(down.Samples))
	}
	if p.LossPct > 0 || p.JitterPct > 0 {
		down = impair(down, p, seed)
	}
	return audio.Resample(down, pcm.Rate)
}

// impair drops and reorders 20 ms frames.
func impair(pcm audio.PCM, p Profile, seed uint64) audio.PCM {
	rng := rand.New(rand.NewPCG(seed, seed^0x5bd1e995))
	frames := audio.Frames(pcm, 20*time.Millisecond)
	if len(frames) == 0 {
		return pcm
	}
	out := make([]int16, 0, len(pcm.Samples))
	prev := make([]int16, len(frames[0]))
	for i := 0; i < len(frames); i++ {
		f := frames[i]
		switch {
		case rng.Float64()*100 < p.LossPct:
			f = prev // concealment: repeat the last frame
		case rng.Float64()*100 < p.JitterPct && i+1 < len(frames):
			frames[i], frames[i+1] = frames[i+1], frames[i]
			f = frames[i]
		}
		out = append(out, f...)
		prev = f
	}
	return audio.PCM{Rate: pcm.Rate, Samples: out}
}
