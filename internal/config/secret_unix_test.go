// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build unix

package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// TestEndlessSecretFile checks that a secret file that never ends, as
// /dev/zero or a FIFO kept open, is refused once it has given more than
// 64 KiB, instead of being read until occamyd's memory runs out. The
// FIFO stands in for /dev/zero, so that code without the bound blocks
// instead of filling the test host's memory.
func TestEndlessSecretFile(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "token")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO: %v", err)
	}
	release := make(chan struct{})
	written := make(chan struct{})
	go func() {
		defer close(written)
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0) // #nosec G304 -- the test's own FIFO
		if err != nil {
			return
		}
		defer w.Close()
		// More than the bound, and then the FIFO stays open: the
		// write fails once the reader has stopped and closed it.
		_, _ = w.Write(bytes.Repeat([]byte("t"), 2*config.MaxSecretFile))
		<-release
	}()
	t.Cleanup(func() {
		close(release)
		<-written
	})
	done := make(chan error, 1)
	go func() {
		_, err := config.Credentials{TokenFile: fifo}.Token()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "too many for a secret") {
			t.Errorf("Token() of an endless file = %v, want a refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Token() still reads an endless file")
	}
}
