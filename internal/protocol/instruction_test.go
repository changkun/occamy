// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"syscall"
	"testing"

	"changkun.de/x/occamy/internal/protocol"
)

func TestNewInstruction(t *testing.T) {
	ins := protocol.NewInstruction("hello", "世界")
	want := "5.hello,2.世界;"
	if want != ins.String() {
		t.Errorf("encode instruction error, got: %s", ins.String())
		t.FailNow()
	}
	if !ins.Expect("hello") {
		t.FailNow()
	}

	ins = protocol.NewInstruction("fake", "")
	want = "4.fake,0.;"
	if want != ins.String() {
		t.Errorf("encode instruction error, got: %s", ins.String())
		t.FailNow()
	}
	if !ins.Expect("fake") {
		t.FailNow()
	}
}

func TestNewInstructionIO(t *testing.T) {
	raw := "5.hello,2.世界;"

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Error("cannot create socketpair")
		t.FailNow()
	}

	io := protocol.NewInstructionIO(fds[0])
	fdio := protocol.NewIO(fds[1])
	var ins *protocol.Instruction
	t.Run("read", func(t *testing.T) {
		n, err := fdio.Write([]byte(raw))
		if err != nil {
			t.Error("write instruction to fd error: ", err)
			t.FailNow()
		}
		if n != len(raw) {
			t.Error("write incomplete instruction, wrote: ", n)
			t.FailNow()
		}

		ins, err = io.Read()
		if err != nil {
			t.Error("read instruction error: ", err)
			t.FailNow()
		}
		if raw != ins.String() {
			t.Error("read instruction wrong, got: ", ins.String())
			t.FailNow()
		}
	})

	t.Run("write", func(t *testing.T) {
		n, err := io.Write(ins)
		if err != nil {
			t.Error("write instruction error: ", err)
			t.FailNow()
		}
		if n != len(ins.String()) {
			t.Error("write incomplete, got: ", n)
		}
		buf := make([]byte, len(ins.String()))
		_, err = fdio.Read(buf)
		if err != nil {
			t.Error("read instruction error: ", err)
			t.FailNow()
		}
		if string(buf) != raw {
			t.Error("write instruction wrong, got: ", string(buf))
		}
	})

	err = io.Close()
	if err != nil {
		t.Error("io close error: ", err)
	}
}
