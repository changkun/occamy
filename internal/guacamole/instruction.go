// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxInstructionLength is the maximum number of characters per instruction
// element, the same as GUAC_INSTRUCTION_MAX_LENGTH in libguac.
const MaxInstructionLength = 8192

// Errors while parsing instruction
var (
	ErrInstructionMissDot   = errors.New("instruction without dot")
	ErrInstructionMissComma = errors.New("instruction without comma")
	ErrInstructionMissSemi  = errors.New("instruction without semi")
	ErrInstructionBadDigit  = errors.New("instruction with bad digit")
	ErrInstructionBadRune   = errors.New("instruction with bad rune")
	ErrInstructionTooLong   = errors.New("instruction element too long")

	ErrInstructionTooManyElements = errors.New("instruction with too many elements")
)

// Instruction is a guacamole instruction
type Instruction struct {
	elements []string // [0] is the opcode of instruction
}

// NewInstruction creates a instruction with given elements
func NewInstruction(elements []string) *Instruction {
	return &Instruction{elements}
}

// ParseInstruction parses an instruction: 1.a,2.bc,3.def,10.abcdefghij;
func ParseInstruction(raw []byte) (ins *Instruction, err error) {
	var (
		cursor   int
		elements []string
	)

	bytes := len(raw)
	for cursor < bytes {

		// 1. parse digit
		lengthEnd := -1
		for i := cursor; i < bytes; i++ {
			if raw[i]^'.' == 0 {
				lengthEnd = i
				break
			}
		}
		if lengthEnd == -1 { // cannot find '.'
			return nil, ErrInstructionMissDot
		}
		length, err := strconv.Atoi(string(raw[cursor:lengthEnd]))
		if err != nil {
			return nil, ErrInstructionBadDigit
		}

		// 2. parse rune
		cursor = lengthEnd + 1
		element := new(strings.Builder)
		for i := 1; i <= length; i++ {
			r, n := utf8.DecodeRune(raw[cursor:])
			if r == utf8.RuneError && n <= 1 {
				return nil, ErrInstructionBadRune
			}
			cursor += n
			element.WriteRune(r)
		}
		elements = append(elements, element.String())

		// 3. done
		if cursor == bytes-1 {
			break
		}

		// 4. parse next
		if raw[cursor]^',' != 0 {
			return nil, ErrInstructionMissComma
		}

		cursor++
	}

	return NewInstruction(elements), nil
}

func (i Instruction) String() string {
	buffer := new(bytes.Buffer)
	buffer.WriteString(strconv.FormatInt(int64(utf8.RuneCountInString(i.elements[0])), 10))
	buffer.WriteString(".")
	buffer.WriteString(i.elements[0])
	for index := 1; index < len(i.elements); index++ {
		buffer.WriteString(",")
		buffer.WriteString(strconv.FormatInt(int64(utf8.RuneCountInString(i.elements[index])), 10))
		buffer.WriteString(".")
		buffer.WriteString(i.elements[index])
	}
	buffer.WriteString(";")
	return buffer.String()
}

// Expect op code
func (i Instruction) Expect(op string) bool {
	if len(i.elements) == 0 {
		return false
	}
	return i.elements[0] == op
}

// Opcode returns the opcode of an instruction
func (i Instruction) Opcode() string {
	if len(i.elements) < 1 {
		return ""
	}
	return i.elements[0]
}

// Args returns the arguments of an instruction
func (i Instruction) Args() []string {
	if len(i.elements) < 1 {
		return []string{}
	}
	return i.elements[1:]
}

// InstructionIO reads and writes instructions over a stream.
type InstructionIO struct {
	conn   io.ReadWriteCloser
	input  *bufio.Reader
	output *bufio.Writer
}

// NewInstructionIO creates an InstructionIO over the given stream, such as
// a net.Conn to guacd.
func NewInstructionIO(rw io.ReadWriteCloser) *InstructionIO {
	return &InstructionIO{
		conn:   rw,
		input:  bufio.NewReaderSize(rw, MaxInstructionLength),
		output: bufio.NewWriter(rw),
	}
}

// Close closes the InstructionIO
func (io *InstructionIO) Close() error {
	return io.conn.Close()
}

// ReadRaw reads one raw instruction from io input, see ReadRaw.
func (io *InstructionIO) ReadRaw() ([]byte, error) {
	return ReadRaw(io.input)
}

// Read reads and parses the instruction from io input
func (io *InstructionIO) Read() (*Instruction, error) {
	raw, err := io.ReadRaw()
	if err != nil {
		return nil, err
	}
	return ParseInstruction(raw)
}

// WriteRaw writes raw buffer into io output
func (io *InstructionIO) WriteRaw(buf []byte) (n int, err error) {
	n, err = io.output.Write(buf)
	if err != nil {
		return
	}
	err = io.output.Flush()
	return
}

// Write writes and decodes an instruction to io output
func (io *InstructionIO) Write(ins *Instruction) (int, error) {
	return io.WriteRaw([]byte(ins.String()))
}

// ReadRaw reads one instruction from r and returns it raw, including the
// terminating semicolon. It frames the instruction by the length prefix of
// each element rather than by the first semicolon, so elements may contain
// ';' and ','. Lengths count Unicode code points.
//
// Like libguac's parser, it rejects elements longer than MaxInstructionLength
// and instructions with more than InstructionMaxElements elements. It returns
// io.EOF only if r ends before an instruction starts, and
// io.ErrUnexpectedEOF if r ends inside one.
func ReadRaw(r *bufio.Reader) ([]byte, error) {
	raw := make([]byte, 0, 64)
	for elements := 0; ; elements++ {
		if elements == InstructionMaxElements {
			return nil, ErrInstructionTooManyElements
		}

		// 1. length prefix
		length, digits := 0, 0
		for {
			c, err := r.ReadByte()
			if err != nil {
				return nil, unexpectedEOF(err, len(raw) > 0)
			}
			raw = append(raw, c)
			if c == '.' && digits > 0 {
				break
			}
			if c < '0' || c > '9' || digits == InstructionMaxDigits {
				return nil, ErrInstructionBadDigit
			}
			length = length*10 + int(c-'0')
			digits++
		}
		if length > MaxInstructionLength {
			return nil, ErrInstructionTooLong
		}
		raw = slices.Grow(raw, length+1)

		// 2. element: length code points. Each takes at least one byte, so
		// reading as many bytes as code points remain never reads past the
		// element; only a code point cut at the end needs more bytes.
		for need := length; need > 0; {
			off := len(raw)
			raw = append(raw, make([]byte, need)...)
			if _, err := io.ReadFull(r, raw[off:]); err != nil {
				return nil, unexpectedEOF(err, true)
			}
			for !utf8.FullRune(raw[lastRuneStart(raw, off):]) {
				c, err := r.ReadByte()
				if err != nil {
					return nil, unexpectedEOF(err, true)
				}
				raw = append(raw, c)
			}
			if !utf8.Valid(raw[off:]) {
				return nil, ErrInstructionBadRune
			}
			need -= utf8.RuneCount(raw[off:])
		}

		// 3. terminator
		c, err := r.ReadByte()
		if err != nil {
			return nil, unexpectedEOF(err, true)
		}
		raw = append(raw, c)
		switch c {
		case ';':
			return raw, nil
		case ',':
		default:
			return nil, ErrInstructionMissComma
		}
	}
}

// lastRuneStart returns the index of the first byte of the last code point
// in p, looking back no further than from.
func lastRuneStart(p []byte, from int) int {
	i := len(p) - 1
	for i > from && i > len(p)-utf8.UTFMax && !utf8.RuneStart(p[i]) {
		i--
	}
	return i
}

func unexpectedEOF(err error, started bool) error {
	if errors.Is(err, io.EOF) && started {
		return io.ErrUnexpectedEOF
	}
	return err
}
