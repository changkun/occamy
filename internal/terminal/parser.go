// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"unicode/utf8"
)

// The parser follows the DEC state machine described by Paul Williams
// (vt100.net/emu/dec_ansi_parser), reading UTF-8 in the ground state.

type pstate uint8

const (
	stGround pstate = iota
	stEscape
	stEscInter
	stCSIEntry
	stCSIParam
	stCSIInter
	stCSIIgnore
	stOSC
	stString // DCS, SOS, PM and APC, which are ignored up to ST
)

const (
	maxParams = 32
	maxParam  = 65535
	maxOSC    = 4096
)

type parser struct {
	state pstate

	// CSI parameters. A missing parameter is -1. Bit i of sub is set when
	// parameter i followed a colon, as in SGR 38:2::r:g:b.
	params [maxParams]int32
	sub    uint64
	n      int
	marker byte // a private marker, one of < = > ?
	inter  byte // the first intermediate byte

	osc []byte

	// A UTF-8 sequence cut by the end of a write.
	u8     [utf8.UTFMax]byte
	u8n    int
	u8need int
}

// param returns parameter i, or def when it is missing.
func (p *parser) param(i int, def int) int {
	if i >= p.n || p.params[i] < 0 {
		return def
	}
	return int(p.params[i])
}

// count returns parameter i as a count: missing and zero mean 1.
func (p *parser) count(i int) int {
	return max(p.param(i, 1), 1)
}

func (p *parser) clear() {
	p.n = 1
	p.params[0] = -1
	p.sub = 0
	p.marker = 0
	p.inter = 0
}

// write runs the parser over p.
func (t *Terminal) write(b []byte) {
	p := &t.p
	for i := 0; i < len(b); {
		c := b[i]
		if p.state == stGround {
			if p.u8need > 0 {
				i += t.continueUTF8(b[i:])
				continue
			}
			switch {
			case c >= 0x20 && c < 0x7f:
				j := i + 1
				for j < len(b) && b[j] >= 0x20 && b[j] < 0x7f {
					j++
				}
				t.printASCII(b[i:j])
				i = j
				continue
			case c >= 0x80:
				r, size := utf8.DecodeRune(b[i:])
				if r == utf8.RuneError && size <= 1 {
					if !utf8.FullRune(b[i:]) && utf8.RuneStart(c) && c >= 0xc2 && c <= 0xf4 {
						// Keep the start of a sequence cut by the end of b.
						p.u8n = copy(p.u8[:], b[i:])
						p.u8need = utf8LenFromLead(c)
						i = len(b)
						continue
					}
					r = utf8.RuneError
				}
				t.print(r)
				i += size
				continue
			}
		}
		t.step(c)
		i++
	}
}

// continueUTF8 completes a UTF-8 sequence started by an earlier write and
// returns how many bytes of b it used.
func (t *Terminal) continueUTF8(b []byte) int {
	p := &t.p
	used := 0
	for used < len(b) && p.u8n < p.u8need {
		c := b[used]
		if c&0xc0 != 0x80 {
			// Interrupted: the partial sequence is invalid, and c is
			// processed on its own.
			p.u8n, p.u8need = 0, 0
			t.print(utf8.RuneError)
			return used
		}
		p.u8[p.u8n] = c
		p.u8n++
		used++
	}
	if p.u8n == p.u8need {
		r, _ := utf8.DecodeRune(p.u8[:p.u8n])
		p.u8n, p.u8need = 0, 0
		t.print(r)
	}
	return used
}

func utf8LenFromLead(c byte) int {
	switch {
	case c >= 0xf0:
		return 4
	case c >= 0xe0:
		return 3
	}
	return 2
}

// step runs the state machine on one byte outside of printable text.
func (t *Terminal) step(c byte) {
	p := &t.p
	// Controls that act in every state.
	switch c {
	case 0x18, 0x1a: // CAN, SUB
		if p.state == stOSC {
			p.osc = p.osc[:0]
		}
		p.state = stGround
		return
	case 0x1b:
		if p.state == stOSC {
			t.oscDispatch()
		}
		p.state = stEscape
		p.inter = 0
		return
	}
	switch p.state {
	case stGround:
		t.execute(c)
	case stEscape:
		switch {
		case c < 0x20:
			t.execute(c)
		case c < 0x30:
			p.inter = c
			p.state = stEscInter
		case c == '[':
			p.clear()
			p.state = stCSIEntry
		case c == ']':
			p.osc = p.osc[:0]
			p.state = stOSC
		case c == 'P' || c == 'X' || c == '^' || c == '_':
			p.state = stString
		case c < 0x7f:
			p.state = stGround
			t.escDispatch(c, 0)
		}
	case stEscInter:
		switch {
		case c < 0x20:
			t.execute(c)
		case c < 0x30:
		case c < 0x7f:
			p.state = stGround
			t.escDispatch(c, p.inter)
		}
	case stCSIEntry, stCSIParam:
		switch {
		case c < 0x20:
			t.execute(c)
		case c >= '0' && c <= '9':
			p.state = stCSIParam
			v := &p.params[p.n-1]
			*v = min(max(*v, 0)*10+int32(c-'0'), maxParam)
		case c == ';' || c == ':':
			p.state = stCSIParam
			if p.n < maxParams {
				p.params[p.n] = -1
				if c == ':' {
					p.sub |= 1 << p.n
				}
				p.n++
			}
		case c >= '<' && c <= '?':
			if p.state == stCSIEntry {
				p.marker = c
				p.state = stCSIParam
			} else {
				p.state = stCSIIgnore
			}
		case c < 0x30:
			p.inter = c
			p.state = stCSIInter
		case c >= 0x40 && c < 0x7f:
			p.state = stGround
			t.csiDispatch(c)
		}
	case stCSIInter:
		switch {
		case c < 0x20:
			t.execute(c)
		case c < 0x30:
		case c < 0x40:
			p.state = stCSIIgnore
		case c < 0x7f:
			p.state = stGround
			t.csiDispatch(c)
		}
	case stCSIIgnore:
		switch {
		case c < 0x20:
			t.execute(c)
		case c >= 0x40 && c < 0x7f:
			p.state = stGround
		}
	case stOSC:
		switch {
		case c == 0x07:
			t.oscDispatch()
			p.state = stGround
		case c < 0x20:
		default:
			if len(p.osc) < maxOSC {
				p.osc = append(p.osc, c)
			}
		}
	case stString:
		// Ignored up to ST, which ESC above begins.
	}
}
