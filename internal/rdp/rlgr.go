// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"math"
	mbits "math/bits"
)

// The entropy coders of RemoteFX: RLGR1 and RLGR3 ([MS-RDPRFX]
// 3.1.8.1.7), which code a tile's coefficients, and the simplified
// run-length coder and raw bits of RemoteFX Progressive's upgrades
// ([MS-RDPEGFX] 3.1.8.1.5, 3.2.8.1.5.2.1). All read their bits most
// significant first; past the end of the data they read zeros, which
// RLGR and SRL take as runs of zeros, so a short stream decodes to its
// coefficients and zeros after.

// bitReader reads bits most significant first, and zeros past the end.
type bitReader struct {
	b   []byte
	acc uint64 // the bits to read next, from the top
	n   uint   // how many of acc's bits are the data's
}

// fill tops acc up to at least 57 bits, with zeros past the end.
func (r *bitReader) fill() {
	for r.n <= 56 {
		if len(r.b) > 0 {
			r.acc |= uint64(r.b[0]) << (56 - r.n)
			r.b = r.b[1:]
		}
		r.n += 8
	}
}

// read returns the next n bits, n at most 32.
func (r *bitReader) read(n uint) uint32 {
	if n == 0 {
		return 0
	}
	if r.n < n {
		r.fill()
	}
	v := uint32(r.acc >> (64 - n)) // #nosec G115 -- n bits, n ≤ 32
	r.acc <<= n
	r.n -= n
	return v
}

// ones counts the 1 bits before the next 0, and reads them and the 0.
func (r *bitReader) ones() int {
	vk := 0
	for {
		if r.n < 57 {
			r.fill()
		}
		k := uint(mbits.LeadingZeros64(^r.acc))
		if k < r.n {
			vk += int(k) // #nosec G115 -- at most 64
			r.acc <<= k + 1
			r.n -= k + 1
			return vk
		}
		vk += int(r.n) // #nosec G115 -- at most 64
		r.acc, r.n = 0, 0
	}
}

// zeros counts the 0 bits before the next 1, at most limit of them, and
// reads them, and the 1 if it comes before limit.
func (r *bitReader) zeros(limit int) int {
	z := 0
	for z < limit {
		if r.n < 57 {
			r.fill()
		}
		k := min(mbits.LeadingZeros64(r.acc), int(r.n), limit-z) // #nosec G115 -- at most 64
		z += k
		r.acc <<= uint(k) // #nosec G115 -- k ≥ 0
		r.n -= uint(k)    // #nosec G115 -- k ≥ 0
		if z < limit && r.n > 0 && r.acc>>63 == 1 {
			r.acc <<= 1
			r.n--
			return z
		}
	}
	return z
}

// RLGR's constants ([MS-RDPRFX] 3.1.8.1.7.3).
const (
	rlgrKPMax = 80 // the most that kp and krp reach
	rlgrLSGR  = 3  // kp >> rlgrLSGR is k
	rlgrUpGR  = 4  // kp's rise after a run of zeros
	rlgrDnGR  = 6  // kp's fall after a value in run-length mode
	rlgrUqGR  = 3  // kp's rise after a zero in Golomb-Rice mode
	rlgrDqGR  = 3  // kp's fall after a value in Golomb-Rice mode
	// rlgrMaxOnes bounds the unary part of a Golomb-Rice code as a
	// value, so that the magnitude fits: a longer one reads on, and
	// saturates.
	rlgrMaxOnes = 1 << 16
)

// The two RLGR modes, as TS_RFX_TILESET's entropy algorithm names them
// ([MS-RDPRFX] 2.2.2.3.4).
const (
	rlgr1 = 1
	rlgr3 = 4
)

var errRLGR3 = errors.New("rdp: RLGR3 pair whose first value exceeds the sum")

// param adds d to p, clamped to [0, rlgrKPMax], and returns p >> rlgrLSGR.
func param(p *int, d int) int {
	*p = min(max(*p+d, 0), rlgrKPMax)
	return *p >> rlgrLSGR
}

// grCode reads a Golomb-Rice code of parameter *kr and adapts it.
func grCode(r *bitReader, krp, kr *int) uint32 {
	vk := min(r.ones(), rlgrMaxOnes)
	mag := uint32(vk)<<uint(*kr) | r.read(uint(*kr)) // #nosec G115 -- vk ≤ 2^16, kr ≤ 10
	switch {
	case vk == 0:
		*kr = param(krp, -2)
	case vk > 1:
		*kr = param(krp, vk)
	}
	return mag
}

// fromTwoMs is the value that 2·magnitude − sign codes.
func fromTwoMs(v uint32) int32 {
	return int32(v>>1) ^ -int32(v&1) // #nosec G115 -- v < 2^27
}

// sat16 saturates v to an int16.
func sat16(v int32) int16 {
	return int16(min(max(v, math.MinInt16), math.MaxInt16)) // #nosec G115 -- clamped
}

// rlgrDecode decodes data, in RLGR1 or RLGR3, into out, all of it
// ([MS-RDPRFX] 3.1.8.1.7.3.1). Values saturate to 16 bits. Only an
// RLGR3 pair whose first value is above their sum is an error.
func rlgrDecode(mode int, data []byte, out []int16) error {
	r := bitReader{b: data}
	k, kp, kr, krp := 1, 1<<rlgrLSGR, 1, 1<<rlgrLSGR
	i, n := 0, len(out)
	for i < n {
		if k != 0 {
			// Run-length mode: each 0 a run of 1<<k zeros, then a 1, the
			// rest of the run in k bits, and a value.
			for i < n && r.read(1) == 0 {
				run := min(1<<k, n-i)
				clear(out[i : i+run])
				i += run
				k = param(&kp, rlgrUpGR)
			}
			if i == n {
				break
			}
			run := min(int(r.read(uint(k))), n-i) // #nosec G115 -- k ≤ 10
			clear(out[i : i+run])
			i += run
			if i == n {
				break
			}
			sign := r.read(1)
			mag := int32(grCode(&r, &krp, &kr)) + 1 // #nosec G115 -- below 2^27
			if sign != 0 {
				mag = -mag
			}
			out[i] = sat16(mag)
			i++
			k = param(&kp, -rlgrDnGR)
			continue
		}
		// Golomb-Rice mode.
		mag := grCode(&r, &krp, &kr)
		if mode == rlgr1 {
			out[i] = sat16(fromTwoMs(mag))
			i++
			if mag == 0 {
				k = param(&kp, rlgrUqGR)
			} else {
				k = param(&kp, -rlgrDqGR)
			}
			continue
		}
		// RLGR3: the sum of two values, then the first in as many bits as
		// the sum takes.
		v1 := r.read(uint(mbits.Len32(mag)))
		if v1 > mag {
			return errRLGR3
		}
		v2 := mag - v1
		switch {
		case v1 != 0 && v2 != 0:
			k = param(&kp, -2*rlgrDqGR)
		case v1 == 0 && v2 == 0:
			k = param(&kp, 2*rlgrUqGR)
		}
		out[i] = sat16(fromTwoMs(v1))
		i++
		if i < n {
			out[i] = sat16(fromTwoMs(v2)) // #nosec G602 -- i < n, out's length
			i++
		}
	}
	return nil
}

// srl reads the simplified run-length stream of an upgrade pass
// ([MS-RDPEGFX] 3.1.8.1.5): runs of zeros coded as RLGR codes them,
// but with their own kp, then a value in sign and unary.
type srl struct {
	r     bitReader
	kp    int
	zeros int  // the zeros left of the run being read
	value bool // a value follows the run
}

func newSRL(data []byte) srl { return srl{r: bitReader{b: data}, kp: 8} }

// next returns the next value, whose magnitude takes at most nBits.
func (s *srl) next(nBits int) int32 {
	if s.zeros > 0 {
		s.zeros--
		return 0
	}
	if !s.value {
		k := s.kp >> rlgrLSGR
		if s.r.read(1) == 0 {
			// A run of 1<<k zeros, this one the first, with no value
			// after it.
			s.zeros = 1<<k - 1
			s.kp = min(s.kp+rlgrUpGR, rlgrKPMax)
			return 0
		}
		s.zeros = int(s.r.read(uint(k))) // #nosec G115 -- k ≤ 10
		s.kp = max(s.kp-rlgrDnGR, 0)
		if s.zeros > 0 {
			s.zeros--
			s.value = true
			return 0
		}
	}
	s.value = false
	neg := s.r.read(1) != 0
	// magnitude − 1 zeros, then a 1 unless the magnitude is the most
	// that nBits hold.
	mag := int32(1 + s.r.zeros(1<<nBits-2)) // #nosec G115 -- below 2^15
	if neg {
		return -mag
	}
	return mag
}
