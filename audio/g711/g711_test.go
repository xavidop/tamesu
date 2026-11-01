package g711

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoundTrips(t *testing.T) {
	samples := make([]int16, 0, 2000)
	for i := -32768; i <= 32767; i += 33 {
		samples = append(samples, int16(i))
	}
	for _, tt := range []struct {
		name string
		enc  func([]int16) []byte
		dec  func([]byte) []int16
	}{{"mulaw", EncodeMuLaw, DecodeMuLaw}, {"alaw", EncodeALaw, DecodeALaw}} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.dec(tt.enc(samples))
			for i, s := range samples {
				// G.711 is logarithmic: the error is a fraction of the magnitude.
				tol := math.Max(64, math.Abs(float64(s))*0.07)
				diff := math.Abs(float64(got[i]) - float64(s))
				assert.LessOrEqualf(t, diff, tol, "sample %d: %d → %d", i, s, got[i])
			}
		})
	}
	// Silence stays silence, and the known μ-law byte for zero.
	assert.Equal(t, byte(0xFF), EncodeMuLaw([]int16{0})[0])
	assert.Equal(t, int16(0), DecodeMuLaw([]byte{0xFF})[0])
	assert.Equal(t, int16(8), DecodeALaw(EncodeALaw([]int16{0}))[0], "A-law has no exact zero")
}
