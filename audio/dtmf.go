package audio

import (
	"math"
	"strings"
	"time"
)

// DTMF tone and gap lengths (ITU-T Q.23 asks for at least 40 ms each).
const (
	DTMFToneDuration = 80 * time.Millisecond
	DTMFGapDuration  = 80 * time.Millisecond
)

// dtmfTones maps a key to its low and high frequencies.
var dtmfTones = map[rune][2]float64{
	'1': {697, 1209}, '2': {697, 1336}, '3': {697, 1477}, 'A': {697, 1633},
	'4': {770, 1209}, '5': {770, 1336}, '6': {770, 1477}, 'B': {770, 1633},
	'7': {852, 1209}, '8': {852, 1336}, '9': {852, 1477}, 'C': {852, 1633},
	'*': {941, 1209}, '0': {941, 1336}, '#': {941, 1477}, 'D': {941, 1633},
}

// DTMF synthesises the dual-tone keypad digits (0-9, *, #, A-D; other
// characters are skipped) at rate, each tone followed by a gap of silence.
func DTMF(digits string, rate int) PCM {
	var out PCM
	out.Rate = rate
	tone := SampleCount(DTMFToneDuration, rate)
	gap := SampleCount(DTMFGapDuration, rate)
	for _, r := range strings.ToUpper(digits) {
		f, ok := dtmfTones[r]
		if !ok {
			continue
		}
		s := make([]int16, tone+gap)
		for i := 0; i < tone; i++ {
			t := float64(i) / float64(rate)
			v := 0.25 * (math.Sin(2*math.Pi*f[0]*t) + math.Sin(2*math.Pi*f[1]*t))
			s[i] = int16(v * 32767)
		}
		out.Samples = append(out.Samples, s...)
	}
	return out
}
