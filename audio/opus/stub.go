//go:build !cgo || lite

package opus

// Available reports whether libopus is linked into this build.
func Available() bool { return false }

// Version is the libopus version string ("" in this build).
func Version() string { return "" }

// Encoder is unavailable in this build.
type Encoder struct{}

// NewEncoder returns ErrUnavailable.
func NewEncoder(rate, channels, bitrate int) (*Encoder, error) { return nil, ErrUnavailable }

// Encode returns ErrUnavailable.
func (e *Encoder) Encode(pcm []int16) ([]byte, error) { return nil, ErrUnavailable }

// Close is a no-op.
func (e *Encoder) Close() {}

// Decoder is unavailable in this build.
type Decoder struct{}

// NewDecoder returns ErrUnavailable.
func NewDecoder(rate, channels int) (*Decoder, error) { return nil, ErrUnavailable }

// Decode returns ErrUnavailable.
func (d *Decoder) Decode(packet []byte, frameSamples int) ([]int16, error) {
	return nil, ErrUnavailable
}

// Close is a no-op.
func (d *Decoder) Close() {}
