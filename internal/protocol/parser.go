// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// InstructionMaxLength is the maximum number of characters per
	// instruction.
	InstructionMaxLength = 8192
	// InstructionMaxDigits is the maximum number of digits to allow per
	// length prefix.
	InstructionMaxDigits = 5
	// InstructionMaxElements is the maximum number of elements per
	// instruction, including the opcode.
	InstructionMaxElements = 128
)

// ParserState is the parsing state of a parser
type ParserState int

// All possible states of the instruction parser.
const (
	ParserStateLength ParserState = iota
	ParserStateContent
	ParserStateComplete
	ParserStateError
)

// Parser parses an occamy instruction
type Parser struct {
	state ParserState
}

// NewParser creates an occamy protocol parser.
func NewParser() Parser {
	return Parser{}
}

// Parse parses raw inputs into a occamy instruction
func (p Parser) Parse(raw []byte, ins *Instruction) (err error) {
	cursor := 0
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
			return ErrInstructionMissDot
		}
		length, err := strconv.Atoi(string(raw[cursor:lengthEnd]))
		if err != nil {
			return ErrInstructionBadDigit
		}

		// 2. parse rune
		cursor = lengthEnd + 1
		element := new(strings.Builder)
		element.Grow(length)
		for i := 1; i <= length; i++ {
			r, n := utf8.DecodeRune(raw[cursor:])
			if r == utf8.RuneError {
				return ErrInstructionBadRune
			}
			cursor += n
			element.WriteRune(r)
		}
		ins.elements = append(ins.elements, element.String())

		// 3. done
		if cursor == bytes-1 {
			break
		}

		// 4. parse next
		if raw[cursor]^',' != 0 {
			return ErrInstructionMissComma
		}

		cursor++
	}

	return nil
}
