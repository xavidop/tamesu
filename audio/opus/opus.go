// Package opus encodes and decodes Opus, the codec WebRTC (and so LiveKit)
// carries audio in. It is a thin cgo binding to libopus: building it needs
// the opus development package (`brew install opus`, `apt install
// libopus-dev`). Under `-tags lite` or without cgo the package compiles to a
// stub whose constructors return ErrUnavailable, and audio mode is off.
package opus

import (
	"errors"
	"time"
)

// SampleRate is the rate WebRTC runs Opus at.
const SampleRate = 48000

// ErrUnavailable is returned by the constructors of a build without libopus.
var ErrUnavailable = errors.New("opus: not available in this build (built with -tags lite or without cgo); install libopus and rebuild without the lite tag for audio mode")

// FrameSizes are the frame durations Opus accepts.
var FrameSizes = []time.Duration{
	2500 * time.Microsecond, 5 * time.Millisecond, 10 * time.Millisecond,
	20 * time.Millisecond, 40 * time.Millisecond, 60 * time.Millisecond,
}

// Samples is the samples per channel of a frame of d at rate.
func Samples(d time.Duration, rate int) int {
	return int(d.Seconds() * float64(rate))
}

// validFrame reports whether n samples per channel at rate is an Opus frame.
func validFrame(n, rate int) bool {
	for _, d := range FrameSizes {
		if Samples(d, rate) == n {
			return true
		}
	}
	return false
}
