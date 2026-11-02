//go:build !onnx

package vad

// SileroAvailable reports whether this build can run the Silero VAD.
func SileroAvailable() bool { return false }

// Silero is unavailable in this build.
type Silero struct{}

// LibraryEnv names the onnxruntime shared library in a build that can load it.
const LibraryEnv = "TAMESU_ONNXRUNTIME"

// Probe returns ErrSileroUnavailable: this build cannot load onnxruntime.
func Probe(string) (string, error) { return "", ErrSileroUnavailable }

// NewSilero returns ErrSileroUnavailable.
func NewSilero(o SileroOptions) (*Silero, error) { return nil, ErrSileroUnavailable }

func (s *Silero) Rate() int                 { return 0 }
func (s *Silero) FrameSize() int            { return 0 }
func (s *Silero) Reset()                    {}
func (s *Silero) Speech(frame []int16) bool { return false }
func (s *Silero) Close()                    {}
