// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxInstructionLength is the maximum number of characters per instruction.
const MaxInstructionLength = 8192

// Errors
var (
	ErrInstructionMissDot   = errors.New("instruction without dot")
	ErrInstructionMissComma = errors.New("instruction without comma")
	ErrInstructionMissSemi  = errors.New("instruction withou semi")
	ErrInstructionBadDigit  = errors.New("instruction with bad digit")
	ErrInstructionBadRune   = errors.New("instruction with bad rune")
)

// Instruction ...
type Instruction struct {
	elements []string
}

// NewInstruction ...
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
			if r == utf8.RuneError {
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

// Args returns the arguments of an instruction
func (i Instruction) Args() []string {
	if len(i.elements) < 1 {
		return []string{}
	}
	return i.elements[1:]
}

// InstructionIO ...
type InstructionIO struct {
	conn   *IO
	input  *bufio.Reader
	output *bufio.Writer
}

// NewInstructionIO ...
func NewInstructionIO(fd int) *InstructionIO {
	conn := NewIO(fd)
	return &InstructionIO{
		conn:   conn,
		input:  bufio.NewReaderSize(conn, MaxInstructionLength),
		output: bufio.NewWriter(conn),
	}
}

// Close closes the InstructionIO
func (io *InstructionIO) Close() error {
	return io.conn.Close()
}

// ReadRaw reads raw data from io input
func (io *InstructionIO) ReadRaw() ([]byte, error) {
	return io.input.ReadBytes(byte(';'))
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
