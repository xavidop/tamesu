//go:build onnx

package vad

import (
	_ "embed"
	"fmt"
	"os"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// sileroModel is Silero VAD v5 (MIT, github.com/snakers4/silero-vad).
//
//go:embed silero_vad.onnx
var sileroModel []byte

// SileroAvailable reports whether this build can run the Silero VAD.
func SileroAvailable() bool { return true }

var (
	ortOnce sync.Once
	ortErr  error
	ortLib  string
)

// LibraryEnv names the onnxruntime shared library when it is not in one of
// the usual places.
const LibraryEnv = "TAMESU_ONNXRUNTIME"

// libraryCandidates are where a packaged onnxruntime lands: Homebrew on
// macOS, the distribution or /usr/local on Linux. Windows loads
// onnxruntime.dll from the PATH.
func libraryCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/lib/libonnxruntime.dylib", "/usr/local/lib/libonnxruntime.dylib", "/opt/homebrew/opt/onnxruntime/lib/libonnxruntime.dylib"}
	case "linux":
		return []string{"/usr/lib/libonnxruntime.so", "/usr/local/lib/libonnxruntime.so", "/usr/lib/x86_64-linux-gnu/libonnxruntime.so", "/usr/lib/aarch64-linux-gnu/libonnxruntime.so", "/usr/lib64/libonnxruntime.so"}
	}
	return nil
}

// findLibrary picks the shared library: the explicit path, then LibraryEnv,
// then the first candidate that exists; "" leaves the binding's default.
func findLibrary(libraryPath string) string {
	if libraryPath != "" {
		return libraryPath
	}
	if env := os.Getenv(LibraryEnv); env != "" {
		return env
	}
	for _, c := range libraryCandidates() {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func initORT(libraryPath string) error {
	ortOnce.Do(func() {
		ortLib = findLibrary(libraryPath)
		if ortLib != "" {
			ort.SetSharedLibraryPath(ortLib)
		}
		ortErr = ort.InitializeEnvironment()
		if ortErr != nil {
			ortErr = fmt.Errorf("%w; install onnxruntime (brew install onnxruntime, or a release from github.com/microsoft/onnxruntime) or set %s to the shared library", ortErr, LibraryEnv)
		} else if ortLib == "" {
			ortLib = "default library"
		}
	})
	return ortErr
}

// Probe loads onnxruntime without creating a detector and reports which
// shared library answered, for `tamesu doctor`.
func Probe(libraryPath string) (string, error) {
	if err := initORT(libraryPath); err != nil {
		return "", err
	}
	return ortLib, nil
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
		return nil, fmt.Errorf("vad: onnxruntime: %w", err)
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
