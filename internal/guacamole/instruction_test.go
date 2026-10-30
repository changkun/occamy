// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"changkun.de/x/occamy/internal/guacamole"
)

func TestNewInstruction(t *testing.T) {
	ins := guacamole.NewInstruction([]string{"hello", "世界"})
	want := "5.hello,2.世界;"
	if want != ins.String() {
		t.Errorf("encode instruction error, got: %s", ins.String())
		t.FailNow()
	}
	if !ins.Expect("hello") {
		t.FailNow()
	}

	ins = guacamole.NewInstruction([]string{"fake", ""})
	want = "4.fake,0.;"
	if want != ins.String() {
		t.Errorf("encode instruction error, got: %s", ins.String())
		t.FailNow()
	}
	if !ins.Expect("fake") {
		t.FailNow()
	}
}

func TestParseInstructionReplacementCharacter(t *testing.T) {
	// U+FFFD is valid UTF-8 even though it decodes to utf8.RuneError.
	raw := "3.key,1.\uFFFD;"
	ins, err := guacamole.ParseInstruction([]byte(raw))
	if err != nil {
		t.Fatalf("parse instruction error: %v", err)
	}
	if ins.String() != raw {
		t.Fatalf("parse instruction wrong, got: %q", ins.String())
	}
	if _, err := guacamole.ParseInstruction([]byte("3.key,1.\xff;")); !errors.Is(err, guacamole.ErrInstructionBadRune) {
		t.Fatalf("parse invalid utf-8, got: %v, want: %v", err, guacamole.ErrInstructionBadRune)
	}
}

func TestNewInstructionIO(t *testing.T) {
	raw := "5.hello,2.世界;"

	// net.Pipe is synchronous: each write waits for the matching read.
	a, b := net.Pipe()
	conn := guacamole.NewInstructionIO(a)
	var ins *guacamole.Instruction
	t.Run("read", func(t *testing.T) {
		go b.Write([]byte(raw))

		var err error
		ins, err = conn.Read()
		if err != nil {
			t.Fatal("read instruction error: ", err)
		}
		if raw != ins.String() {
			t.Fatal("read instruction wrong, got: ", ins.String())
		}
	})

	t.Run("write", func(t *testing.T) {
		got := make(chan string, 1)
		go func() {
			buf := make([]byte, len(raw))
			n, _ := io.ReadFull(b, buf)
			got <- string(buf[:n])
		}()

		n, err := conn.Write(ins)
		if err != nil {
			t.Fatal("write instruction error: ", err)
		}
		if n != len(ins.String()) {
			t.Error("write incomplete, got: ", n)
		}
		if s := <-got; s != raw {
			t.Error("write instruction wrong, got: ", s)
		}
	})

	if err := conn.Close(); err != nil {
		t.Error("io close error: ", err)
	}
}

func readRaw(t *testing.T, r io.Reader) []string {
	t.Helper()
	br := bufio.NewReader(r)
	var got []string
	for {
		raw, err := guacamole.ReadRaw(br)
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("read instruction error: %v", err)
		}
		got = append(got, string(raw))
	}
}

func TestInstructionIOOverPipe(t *testing.T) {
	want := []string{"4.size,4.1024,3.768;", "5.hello,2.世界;", "4.args,3.a;b;", "4.sync,11.10574782313;"}
	a, b := net.Pipe()
	defer b.Close()
	go func() {
		for _, ins := range want {
			a.Write([]byte(ins))
		}
	}()

	conn := guacamole.NewInstructionIO(b)
	for _, w := range want {
		raw, err := conn.ReadRaw()
		if err != nil {
			t.Fatalf("read instruction error: %v", err)
		}
		if string(raw) != w {
			t.Fatalf("read instruction wrong, got: %q, want: %q", raw, w)
		}
	}
}

func TestReadRawSemicolonInValue(t *testing.T) {
	want := []string{"4.args,3.a;b;", "5.error,9.a, b; c.d,3.519;"}
	got := readRaw(t, strings.NewReader(strings.Join(want, "")))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("read instructions wrong, got: %q, want: %q", got, want)
	}
}

func TestReadRawMultibyte(t *testing.T) {
	want := []string{"5.hello,2.日本;", "3.key,4.🙂ü日a;"}
	got := readRaw(t, strings.NewReader(strings.Join(want, "")))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("read instructions wrong, got: %q, want: %q", got, want)
	}
}

func TestReadRawPartialReads(t *testing.T) {
	want := []string{"5.hello,2.日本;", "4.args,3.a;b;", "4.sync,11.10574782313;"}
	got := readRaw(t, iotest.OneByteReader(strings.NewReader(strings.Join(want, ""))))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("read instructions wrong, got: %q, want: %q", got, want)
	}
}

func TestReadRawSplitsMessage(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader([]byte("3.key,5.65307,1.1;3.key,5.65307,1.0;")))
	for _, want := range []string{"3.key,5.65307,1.1;", "3.key,5.65307,1.0;"} {
		raw, err := guacamole.ReadRaw(r)
		if err != nil || string(raw) != want {
			t.Fatalf("read instruction wrong, got: %q, %v, want: %q", raw, err, want)
		}
	}
	if _, err := guacamole.ReadRaw(r); !errors.Is(err, io.EOF) {
		t.Fatalf("read after last instruction, got: %v, want: io.EOF", err)
	}
}

func TestReadRawLimits(t *testing.T) {
	elements := func(n int) string {
		return strings.TrimSuffix(strings.Repeat("1.a,", n), ",") + ";"
	}
	element := func(n int) string {
		return "4.blob," + strconv.Itoa(n) + "." + strings.Repeat("x", n) + ";"
	}
	tests := []struct {
		name string
		raw  string
		err  error
	}{
		{"max elements", elements(guacamole.InstructionMaxElements), nil},
		{"too many elements", elements(guacamole.InstructionMaxElements + 1), guacamole.ErrInstructionTooManyElements},
		{"max element length", element(guacamole.MaxInstructionLength), nil},
		{"element too long", element(guacamole.MaxInstructionLength + 1), guacamole.ErrInstructionTooLong},
		{"six digit length", "123456.a;", guacamole.ErrInstructionBadDigit},
		{"no digits", ".;", guacamole.ErrInstructionBadDigit},
		{"letter in length", "4x.sync;", guacamole.ErrInstructionBadDigit},
		{"bad terminator", "4.sync:", guacamole.ErrInstructionMissComma},
		{"invalid utf-8", "1.\xff;", guacamole.ErrInstructionBadRune},
		{"truncated element", "4.syn", io.ErrUnexpectedEOF},
		{"truncated length", "4", io.ErrUnexpectedEOF},
		{"missing terminator", "4.sync", io.ErrUnexpectedEOF},
		{"empty", "", io.EOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := guacamole.ReadRaw(bufio.NewReader(strings.NewReader(tt.raw)))
			if !errors.Is(err, tt.err) {
				t.Fatalf("read instruction error, got: %v, want: %v", err, tt.err)
			}
			if err == nil && string(raw) != tt.raw {
				t.Fatalf("read instruction wrong, got %d bytes, want %d", len(raw), len(tt.raw))
			}
		})
	}
}

// TestMaxInstructionSize reads the largest instruction ReadRaw accepts:
// every element at the most elements, the longest length prefix, and the
// longest element in 4-byte code points.
func TestMaxInstructionSize(t *testing.T) {
	element := "0" + strconv.Itoa(guacamole.MaxInstructionLength) + "." + strings.Repeat("😀", guacamole.MaxInstructionLength)
	elements := make([]string, guacamole.InstructionMaxElements)
	for i := range elements {
		elements[i] = element
	}
	largest := strings.Join(elements, ",") + ";"
	if len(largest) != guacamole.MaxInstructionSize {
		t.Fatalf("largest instruction has %d bytes, MaxInstructionSize is %d", len(largest), guacamole.MaxInstructionSize)
	}
	raw, err := guacamole.ReadRaw(bufio.NewReader(strings.NewReader(largest)))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(largest) {
		t.Fatalf("read %d bytes, want %d", len(raw), len(largest))
	}
}

func FuzzReadRaw(f *testing.F) {
	f.Add([]byte("5.hello,2.世界;"))
	f.Add([]byte("4.args,3.a;b;"))
	f.Add([]byte("0.,4.ping,13.1730000000000;"))
	f.Add([]byte("4.sync,11.10574782313;4.sync"))
	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := guacamole.ReadRaw(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}
		if !bytes.HasPrefix(data, raw) {
			t.Fatalf("raw %q is not a prefix of the input", raw)
		}
		ins, err := guacamole.ParseInstruction(raw)
		if err != nil {
			t.Fatalf("accepted %q but ParseInstruction failed: %v", raw, err)
		}
		canonical := ins.String()
		again, err := guacamole.ReadRaw(bufio.NewReader(strings.NewReader(canonical)))
		if err != nil || string(again) != canonical {
			t.Fatalf("%q does not round-trip: %q, %v", canonical, again, err)
		}
	})
}
