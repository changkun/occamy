// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib_test

import (
	"syscall"
	"testing"

	"changkun.de/x/occamy/internal/lib"
)

func TestNewUser(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Error("cannot create socketpair")
		t.FailNow()
	}

	sock1, err := lib.NewSocket(fds[0])
	if err != nil {
		t.Error("create socket1 in NewUser error: ", err)
		t.FailNow()
	}
	sock2, err := lib.NewSocket(fds[1])
	if err != nil {
		t.Error("create socket2 in NewUser error: ", err)
		t.FailNow()
	}

	cli, err := lib.NewClient()
	if err != nil {
		t.Error("create client in NewUser error: ", err)
		t.FailNow()
	}
	user, err := lib.NewUser(sock1, cli, true)
	if err != nil {
		t.Error("NewUser error: ", err)
		t.FailNow()
	}

	t.Run("handle-conn", func(t *testing.T) {
		done := make(chan bool, 2)
		go func() {
			err := user.HandleConnection()
			if err != nil {
				t.Error("user handle connection error: ", err)
				t.FailNow()
			}
			done <- true
		}()
		go func() {
			buf := make([]byte, 10000)
			_, err := sock2.Read(buf)
			if err != nil {
				t.Error("read user handle connection message error: ", err)
				t.FailNow()
			}
			done <- true
		}()
		<-done
	})

	user.Close()
}
