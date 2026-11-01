// Package g711 encodes and decodes μ-law and A-law, the 8-bit codecs of the
// telephone network, for line simulation.
package g711

const (
	bias = 0x84
	clip = 32635
)

// EncodeMuLaw compresses 16-bit samples to μ-law bytes.
func EncodeMuLaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = muLawByte(s)
	}
	return out
}

func muLawByte(s int16) byte {
	v := int(s)
	sign := byte(0)
	if v < 0 {
		v = -v
		sign = 0x80
	}
	if v > clip {
		v = clip
	}
	v += bias
	exp := 7
	for mask := 0x4000; exp > 0 && v&mask == 0; mask >>= 1 {
		exp--
	}
	mantissa := (v >> uint(exp+3)) & 0x0F
	return ^(sign | byte(exp<<4) | byte(mantissa))
}

// DecodeMuLaw expands μ-law bytes to 16-bit samples.
func DecodeMuLaw(data []byte) []int16 {
	out := make([]int16, len(data))
	for i, b := range data {
		b = ^b
		sign := b & 0x80
		exp := int((b >> 4) & 0x07)
		mantissa := int(b & 0x0F)
		v := ((mantissa << 3) + bias) << uint(exp)
		v -= bias
		if sign != 0 {
			v = -v
		}
		out[i] = int16(v)
	}
	return out
}

// EncodeALaw compresses 16-bit samples to A-law bytes.
func EncodeALaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = aLawByte(s)
	}
	return out
}

func aLawByte(s int16) byte {
	v := int(s)
	sign := byte(0x80)
	if v < 0 {
		v = -v - 1
		sign = 0
	}
	if v > 32767 {
		v = 32767
	}
	var b byte
	if v < 256 {
		b = byte(v >> 4)
	} else {
		exp := 7
		for mask := 0x4000; exp > 0 && v&mask == 0; mask >>= 1 {
			exp--
		}
		mantissa := (v >> uint(exp+3)) & 0x0F
		b = byte(exp<<4) | byte(mantissa)
	}
	return (b | sign) ^ 0x55
}

// DecodeALaw expands A-law bytes to 16-bit samples.
func DecodeALaw(data []byte) []int16 {
	out := make([]int16, len(data))
	for i, b := range data {
		b ^= 0x55
		sign := b & 0x80
		exp := int((b >> 4) & 0x07)
		mantissa := int(b & 0x0F)
		var v int
		if exp == 0 {
			v = (mantissa << 4) + 8
		} else {
			v = ((mantissa << 4) + 0x108) << uint(exp-1)
		}
		if sign == 0 {
			v = -v
		}
		out[i] = int16(v)
	}
	return out
}
