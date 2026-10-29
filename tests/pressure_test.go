// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func successConnect(url string) error {
	return session(url, 10*time.Second, "size", "img", "sync")
}

func failConnect(url string) error {
	ws, err := dial(url)
	if err != nil {
		return err
	}
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, _, err := ws.ReadMessage(); err != nil {
		return err
	}
	return ws.WriteMessage(websocket.TextMessage, []byte("bad instruction"))
}

// TestConnectionPressure opens many concurrent sessions to every demo
// server, half of which send garbage, and then checks that occamy still
// serves new sessions. Individual sessions may fail under the load; occamy
// must not crash or lock up. OCCAMY_PRESSURE_N sets the number of rounds.
//
// The test is skipped unless OCCAMY_PRESSURE_N is set: concurrent joins and
// teardowns still crash occamyd (#20,
// specs/003-session-lifecycle-hardening.md), which runs it in CI once fixed.
func TestConnectionPressure(t *testing.T) {
	v := os.Getenv("OCCAMY_PRESSURE_N")
	if v == "" {
		t.Skip("OCCAMY_PRESSURE_N not set, see the test documentation")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("bad OCCAMY_PRESSURE_N: %v", err)
	}
	addr := endpoint(t)

	connectors := map[string]func(string) error{"success": successConnect, "fail": failConnect}
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		for _, proto := range protocols() {
			for name, connector := range connectors {
				wg.Add(1)
				go func() {
					defer wg.Done()
					url, err := login(addr, credentials[proto])
					if err == nil {
						err = connector(url)
					}
					if err != nil {
						t.Logf("%s-%s-%d: %v", proto, name, i, err)
					}
				}()
			}
		}
	}
	wg.Wait()

	// A session with the storm's claims may never end and keep those claims
	// stuck (specs/003-session-lifecycle-hardening.md, defect 8), so check
	// with claims of its own. VNC ignores the username.
	fresh := credentials["vnc"]
	fresh.Username = "after-pressure"
	checkSession(t, addr, fresh)
}
