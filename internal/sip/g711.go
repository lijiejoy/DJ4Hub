package sip

// G.711 mu-law (PCMU) conversion, 8 kHz, 16-bit linear PCM.
// Standard Sun/ITU-T G.711 algorithm (public domain formulation).

const (
	ulawBias = 0x84 // 132
	ulawClip = 32635
)

// LinearToUlaw converts one 16-bit linear PCM sample to an 8-bit mu-law byte.
func LinearToUlaw(sample int16) byte {
	sign := 0
	s := int(sample)
	if s < 0 {
		s = -s
		sign = 0x80
	}
	if s > ulawClip {
		s = ulawClip
	}
	s += ulawBias
	exponent := 7
	for expMask := 0x4000; expMask != 0 && (s&expMask) == 0; expMask >>= 1 {
		exponent--
	}
	mantissa := (s >> (exponent + 3)) & 0x0F
	return byte(^(sign | (exponent << 4) | mantissa))
}

// UlawToLinear converts one 8-bit mu-law byte to a 16-bit linear PCM sample.
func UlawToLinear(b byte) int16 {
	b = ^b
	sign := b & 0x80
	exponent := (b >> 4) & 0x07
	mantissa := b & 0x0F
	sample := ((int(mantissa) << 3) + ulawBias) << exponent
	sample -= ulawBias
	if sign != 0 {
		sample = -sample
	}
	return int16(sample)
}

// PCMToUlaw converts a slice of 16-bit PCM samples to mu-law bytes.
func PCMToUlaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = LinearToUlaw(s)
	}
	return out
}

// UlawToPCM converts a slice of mu-law bytes to 16-bit PCM samples.
func UlawToPCM(data []byte) []int16 {
	out := make([]int16, len(data))
	for i, b := range data {
		out[i] = UlawToLinear(b)
	}
	return out
}

// ulawSilence is the mu-law byte for digital silence (0xFF).
const ulawSilence = 0xFF
