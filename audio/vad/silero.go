//go:build onnx

package vad

import (
	_ "embed"
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// sileroModel is Silero VAD v5 (MIT, github.com/snakers4/silero-vad).
//
//go:embed silero_vad.onnx
var sileroModel []byte

// SileroAvailable reports whether this build can run the Silero VAD.
func SileroAvailable() bool { return true }

var ortOnce sync.Once
var ortErr error

func initORT(libraryPath string) error {
	ortOnce.Do(func() {
		if libraryPath == "" {
			libraryPath = os.Getenv("TAMESU_ONNXRUNTIME")
		}
		if libraryPath != "" {
			ort.SetSharedLibraryPath(libraryPath)
		}
		ortErr = ort.InitializeEnvironment()
	})
	return ortErr
}

// Silero runs the Silero neural VAD on 32 ms frames (512 samples at 16 kHz,
// 256 at 8 kHz) and reports speech when the probability is above Threshold.
type Silero struct {
	session   *ort.AdvancedSession
	input     *ort.Tensor[float32]
	state     *ort.Tensor[float32]
	sr        *ort.Tensor[int64]
	output    *ort.Tensor[float32]
	stateOut  *ort.Tensor[float32]
	context   []float32
	rate      int
	frame     int
	threshold float32
}

// NewSilero loads the embedded model. LibraryPath points at the onnxruntime
// shared library (or set TAMESU_ONNXRUNTIME); Rate is 16000 (default) or 8000;
// Threshold defaults to 0.5.
func NewSilero(o SileroOptions) (*Silero, error) {
	if o.Rate == 0 {
		o.Rate = 16000
	}
	if o.Rate != 16000 && o.Rate != 8000 {
		return nil, fmt.Errorf("vad: silero runs at 8000 or 16000 Hz, not %d", o.Rate)
	}
	if o.Threshold == 0 {
		o.Threshold = 0.5
	}
	if err := initORT(o.LibraryPath); err != nil {
		return nil, fmt.Errorf("vad: onnxruntime: %w (set TAMESU_ONNXRUNTIME to the shared library)", err)
	}
	frame, context := 512, 64
	if o.Rate == 8000 {
		frame, context = 256, 32
	}
	s := &Silero{rate: o.Rate, frame: frame, threshold: float32(o.Threshold), context: make([]float32, context)}
	var err error
	if s.input, err = ort.NewEmptyTensor[float32](ort.NewShape(1, int64(frame+context))); err != nil {
		return nil, err
	}
	if s.state, err = ort.NewEmptyTensor[float32](ort.NewShape(2, 1, 128)); err != nil {
		return nil, err
	}
	if s.sr, err = ort.NewTensor(ort.NewShape(1), []int64{int64(o.Rate)}); err != nil {
		return nil, err
	}
	if s.output, err = ort.NewEmptyTensor[float32](ort.NewShape(1, 1)); err != nil {
		return nil, err
	}
	if s.stateOut, err = ort.NewEmptyTensor[float32](ort.NewShape(2, 1, 128)); err != nil {
		return nil, err
	}
	s.session, err = ort.NewAdvancedSessionWithONNXData(sileroModel,
		[]string{"input", "state", "sr"}, []string{"output", "stateN"},
		[]ort.Value{s.input, s.state, s.sr}, []ort.Value{s.output, s.stateOut}, nil)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("vad: silero session: %w", err)
	}
	return s, nil
}

func (s *Silero) Rate() int      { return s.rate }
func (s *Silero) FrameSize() int { return s.frame }

// Reset clears the recurrent state.
func (s *Silero) Reset() {
	clear(s.state.GetData())
	clear(s.context)
}

// Speech implements Detector; it returns false (and keeps going) on an
// inference error so a glitch does not end a run.
func (s *Silero) Speech(frame []int16) bool {
	p, err := s.Probability(frame)
	return err == nil && p >= s.threshold
}

// Probability runs the model on one frame.
func (s *Silero) Probability(frame []int16) (float32, error) {
	if len(frame) != s.frame {
		return 0, fmt.Errorf("vad: silero wants %d-sample frames, got %d", s.frame, len(frame))
	}
	in := s.input.GetData()
	copy(in, s.context)
	for i, v := range frame {
		in[len(s.context)+i] = float32(v) / 32768
	}
	if err := s.session.Run(); err != nil {
		return 0, err
	}
	copy(s.state.GetData(), s.stateOut.GetData())
	copy(s.context, in[len(in)-len(s.context):])
	return s.output.GetData()[0], nil
}

// Close frees the session and tensors.
func (s *Silero) Close() {
	for _, v := range []interface{ Destroy() error }{s.session, s.input, s.state, s.sr, s.output, s.stateOut} {
		if v != nil {
			_ = v.Destroy()
		}
	}
}
