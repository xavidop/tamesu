//go:build cgo && !lite

package opus

/*
#cgo pkg-config: opus
#include <opus.h>

static int tamesu_set_bitrate(OpusEncoder *e, opus_int32 b) {
	return opus_encoder_ctl(e, OPUS_SET_BITRATE(b));
}
static int tamesu_set_inband_fec(OpusEncoder *e, opus_int32 on) {
	return opus_encoder_ctl(e, OPUS_SET_INBAND_FEC(on));
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Available reports whether libopus is linked into this build.
func Available() bool { return true }

// Version is the libopus version string.
func Version() string { return C.GoString(C.opus_get_version_string()) }

func opusErr(code C.int) error {
	return fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(code)))
}

// Encoder turns 16-bit PCM frames into Opus packets.
type Encoder struct {
	enc      *C.OpusEncoder
	rate     int
	channels int
	buf      []byte
}

// NewEncoder returns a VOIP-tuned encoder for rate (8, 12, 16, 24 or 48 kHz)
// and channels (1 or 2) at bitrate bits per second (0 keeps libopus's default).
func NewEncoder(rate, channels, bitrate int) (*Encoder, error) {
	var code C.int
	enc := C.opus_encoder_create(C.opus_int32(rate), C.int(channels), C.OPUS_APPLICATION_VOIP, &code)
	if code != C.OPUS_OK {
		return nil, opusErr(code)
	}
	if bitrate > 0 {
		if c := C.tamesu_set_bitrate(enc, C.opus_int32(bitrate)); c != C.OPUS_OK {
			C.opus_encoder_destroy(enc)
			return nil, opusErr(c)
		}
	}
	C.tamesu_set_inband_fec(enc, 1)
	return &Encoder{enc: enc, rate: rate, channels: channels, buf: make([]byte, 4000)}, nil
}

// Encode compresses one frame (interleaved samples; the frame must be one of
// FrameSizes) and returns the packet, valid until the next call.
func (e *Encoder) Encode(pcm []int16) ([]byte, error) {
	if e.enc == nil {
		return nil, errors.New("opus: encoder closed")
	}
	n := len(pcm) / e.channels
	if !validFrame(n, e.rate) {
		return nil, fmt.Errorf("opus: %d samples is not a valid frame at %d Hz", n, e.rate)
	}
	written := C.opus_encode(e.enc, (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(n), (*C.uchar)(unsafe.Pointer(&e.buf[0])), C.opus_int32(len(e.buf)))
	if written < 0 {
		return nil, opusErr(C.int(written))
	}
	return e.buf[:int(written)], nil
}

// Close frees the encoder.
func (e *Encoder) Close() {
	if e.enc != nil {
		C.opus_encoder_destroy(e.enc)
		e.enc = nil
	}
}

// Decoder turns Opus packets back into 16-bit PCM.
type Decoder struct {
	dec      *C.OpusDecoder
	rate     int
	channels int
	buf      []int16
}

// NewDecoder returns a decoder producing rate Hz, channels audio.
func NewDecoder(rate, channels int) (*Decoder, error) {
	var code C.int
	dec := C.opus_decoder_create(C.opus_int32(rate), C.int(channels), &code)
	if code != C.OPUS_OK {
		return nil, opusErr(code)
	}
	// 120 ms is the largest Opus frame.
	return &Decoder{dec: dec, rate: rate, channels: channels, buf: make([]int16, Samples(120_000_000, rate)*channels)}, nil
}

// Decode decompresses one packet; a nil packet runs packet-loss concealment
// for frameSamples samples. The result is valid until the next call.
func (d *Decoder) Decode(packet []byte, frameSamples int) ([]int16, error) {
	if d.dec == nil {
		return nil, errors.New("opus: decoder closed")
	}
	var data *C.uchar
	if len(packet) > 0 {
		data = (*C.uchar)(unsafe.Pointer(&packet[0]))
	}
	n := C.opus_decode(d.dec, data, C.opus_int32(len(packet)), (*C.opus_int16)(unsafe.Pointer(&d.buf[0])), C.int(frameSamples), 0)
	if n < 0 {
		return nil, opusErr(C.int(n))
	}
	return d.buf[:int(n)*d.channels], nil
}

// Close frees the decoder.
func (d *Decoder) Close() {
	if d.dec != nil {
		C.opus_decoder_destroy(d.dec)
		d.dec = nil
	}
}
