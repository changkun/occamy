// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
)

// register makes protocol's sessions with make, under the name the
// session's state reports, such as a fake backend of a test.
func (b backends) register(protocol, name string, make func(target, screen) (backend.Backend, error)) {
	b[protocol] = backendMaker{name: name, make: make}
}

// waitFor waits up to five seconds for cond, and fails the test if it
// does not hold by then.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reply is a response with its body read.
type reply struct {
	code   int
	header http.Header
	body   []byte
}

// do sends a request and reads the reply.
func do(t *testing.T, method, u string, header http.Header, body string) reply {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, b}
}

// writerFunc is an io.Writer of a function.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
