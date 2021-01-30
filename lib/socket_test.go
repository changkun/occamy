// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib_test

import (
	"syscall"
	"testing"

	"changkun.de/x/occamy/lib"
)

func TestNewSocket(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Error("cannot create socketpair")
		t.FailNow()
	}

	sock1, err := lib.NewSocket(fds[0])
	if err != nil {
		t.Error("create lib socket error: ", err)
		t.FailNow()
	}
	sock2, err := lib.NewSocket(fds[1])
	if err != nil {
		t.Error("create lib socket error: ", err)
		t.FailNow()
	}

	t.Run("io", func(t *testing.T) {
		message := "Wir müssen wissen, wir werden wissen."
		err := sock1.Write([]byte(message))
		if err != nil {
			t.Error("sock1.Write error: ", err)
			t.FailNow()
		}
		buf := make([]byte, len(message))
		_, err = sock2.Read(buf)
		if err != nil {
			t.Error("sock2.Read error: ", err)
			t.FailNow()
		}
		if string(buf) != message {
			t.Error("message incorrect, got: ", string(buf))
			t.FailNow()
		}
	})

	sock1.Close()
	sock2.Close()
}
