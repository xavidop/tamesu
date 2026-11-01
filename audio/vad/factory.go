package vad

import (
	"errors"
	"fmt"
)

// ErrSileroUnavailable is returned by NewSilero in a build without onnxruntime support.
var ErrSileroUnavailable = errors.New("vad: the silero detector needs a build with -tags onnx and the onnxruntime shared library; use vad: energy or rebuild")

// SileroOptions configure the Silero detector.
type SileroOptions struct {
	Rate        int
	Threshold   float64
	LibraryPath string
}

// Names are the detector names accepted in configuration.
var Names = []string{"energy", "silero"}

// Options configure New.
type Options struct {
	Rate   int
	Energy EnergyOptions
	Silero SileroOptions
}

// New returns the named detector: "energy" (default, pure Go) or "silero".
func New(name string, o Options) (Detector, error) {
	switch name {
	case "", "energy":
		if o.Energy.Rate == 0 {
			o.Energy.Rate = o.Rate
		}
		return NewEnergy(o.Energy), nil
	case "silero":
		if o.Silero.Rate == 0 {
			o.Silero.Rate = o.Rate
		}
		return NewSilero(o.Silero)
	}
	return nil, fmt.Errorf("vad: unknown detector %q (have %v)", name, Names)
}
