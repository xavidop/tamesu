// Package wav reads and writes 16-bit PCM WAV files, mono or multi-channel
// (tamesu records runs as stereo: left the user, right the agent).
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Write encodes channels (each the same length, 16-bit) as a WAV file.
func Write(w io.Writer, rate int, channels ...[]int16) error {
	if len(channels) == 0 {
		return errors.New("wav: no channels")
	}
	n := 0
	for _, c := range channels {
		n = max(n, len(c))
	}
	frames := interleave(channels, n)
	if err := writeHeader(w, rate, len(channels), len(frames)*2); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, frames)
}

// WriteFile is Write to a file.
func WriteFile(path string, rate int, channels ...[]int16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := Write(f, rate, channels...); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func interleave(channels [][]int16, n int) []int16 {
	out := make([]int16, 0, n*len(channels))
	for i := 0; i < n; i++ {
		for _, c := range channels {
			if i < len(c) {
				out = append(out, c[i])
			} else {
				out = append(out, 0)
			}
		}
	}
	return out
}

func writeHeader(w io.Writer, rate, channels, dataBytes int) error {
	var h [44]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	_, err := w.Write(h[:])
	return err
}

// Read decodes a 16-bit PCM WAV into its channels.
func Read(r io.Reader) (rate int, channels [][]int16, err error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, nil, err
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, nil, errors.New("wav: not a RIFF/WAVE file")
	}
	var nch, bits int
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4:]))
		body := b[off+8:]
		if size > len(body) {
			size = len(body)
		}
		body = body[:size]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return 0, nil, errors.New("wav: short fmt chunk")
			}
			if f := binary.LittleEndian.Uint16(body[0:]); f != 1 && f != 0xFFFE {
				return 0, nil, fmt.Errorf("wav: unsupported format %d (only PCM)", f)
			}
			nch = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			data = body
		}
		off += 8 + size + size%2
	}
	if nch == 0 || data == nil {
		return 0, nil, errors.New("wav: missing fmt or data chunk")
	}
	if bits != 16 {
		return 0, nil, fmt.Errorf("wav: %d-bit samples are not supported (only 16)", bits)
	}
	frames := len(data) / (2 * nch)
	channels = make([][]int16, nch)
	for c := range channels {
		channels[c] = make([]int16, frames)
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < nch; c++ {
			channels[c][i] = int16(binary.LittleEndian.Uint16(data[(i*nch+c)*2:]))
		}
	}
	return rate, channels, nil
}

// ReadFile is Read from a file.
func ReadFile(path string) (int, [][]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = f.Close() }()
	return Read(f)
}

// Writer streams frames to a seekable WAV and patches the sizes on Close.
type Writer struct {
	w        io.WriteSeeker
	rate     int
	channels int
	bytes    int
}

// NewWriter writes the header and returns a Writer for channels interleaved frames.
func NewWriter(w io.WriteSeeker, rate, channels int) (*Writer, error) {
	if err := writeHeader(w, rate, channels, 0); err != nil {
		return nil, err
	}
	return &Writer{w: w, rate: rate, channels: channels}, nil
}

// WriteFrames appends interleaved samples (channels values per frame).
func (w *Writer) WriteFrames(interleaved []int16) error {
	if err := binary.Write(w.w, binary.LittleEndian, interleaved); err != nil {
		return err
	}
	w.bytes += len(interleaved) * 2
	return nil
}

// Close patches the RIFF and data sizes.
func (w *Writer) Close() error {
	if _, err := w.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return writeHeader(w.w, w.rate, w.channels, w.bytes)
}
