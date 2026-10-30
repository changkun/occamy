// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func successConnect(url string) error {
	return session(url, 10*time.Second, "size", "img", "sync")
}

func failConnect(url string) error {
	ws, err := dial(url)
	if err != nil {
		return err
	}
	defer ws.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := ws.Read(ctx); err != nil {
		return err
	}
	return ws.Write(context.Background(), websocket.MessageText, []byte("bad instruction"))
}

// TestConnectionPressure opens many concurrent sessions to every demo
// server, half of which send garbage, and then checks that occamy is
// still healthy. Individual sessions may fail under the load. occamy must
// not crash or lock up, its goroutines must return to their number before
// the storm, and new sessions must work, including one with the storm's
// own claims. OCCAMY_PRESSURE_N sets the number of rounds, each of which
// opens two sessions per demo server.
//
// The test is skipped unless OCCAMY_PRESSURE_N is set. CI runs it with
// OCCAMY_PRESSURE_N=10.
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
	before := goroutines(t, addr)

	connectors := map[string]func(string) error{"success": successConnect, "fail": failConnect}
	var wg sync.WaitGroup
	var sessions, failed atomic.Int32
	start := time.Now()
	for i := 1; i <= n; i++ {
		for _, proto := range protocols() {
			for name, connector := range connectors {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sessions.Add(1)
					url, err := login(addr, credentials[proto])
					if err == nil {
						err = connector(url)
					}
					if err != nil {
						failed.Add(1)
						t.Logf("%s-%s-%d: %v", proto, name, i, err)
					}
				}()
			}
		}
	}
	wg.Wait()
	t.Logf("%d sessions in %v, %d failed", sessions.Load(), time.Since(start).Round(time.Millisecond), failed.Load())

	waitGoroutines(t, addr, before)

	// A failed connect used to keep its claims stuck until a restart
	// (specs/003-session-lifecycle-hardening.md, defect 8), so check the
	// storm's own claims, and then claims it never used. VNC ignores the
	// username.
	checkSession(t, addr, credentials["vnc"])
	fresh := credentials["vnc"]
	fresh.Username = "after-pressure"
	checkSession(t, addr, fresh)
}

// goroutineSlack is how many goroutines over its baseline occamyd may run
// once every session has ended. Each session holds three, so a leak in
// the storm's sixty or more sessions shows well above it.
const goroutineSlack = 5

// goroutines returns the number of goroutines occamyd runs, from the pprof
// routes that the demo stack serves in debug mode.
func goroutines(t *testing.T, addr string) int {
	t.Helper()
	n, _, err := goroutineProfile(addr)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// waitGoroutines waits until occamyd runs no more than goroutineSlack
// goroutines over baseline. Sessions can take guacd's handshake timeout,
// 15 s, to end, so it waits up to a minute, and then fails the test with
// the goroutine profile.
func waitGoroutines(t *testing.T, addr string, baseline int) {
	t.Helper()
	start := time.Now()
	for {
		n, profile, err := goroutineProfile(addr)
		if err != nil {
			t.Fatal(err)
		}
		if n <= baseline+goroutineSlack {
			t.Logf("goroutines: %d before the storm, %d after %v", baseline, n, time.Since(start).Round(time.Second))
			return
		}
		if time.Since(start) > time.Minute {
			t.Fatalf("occamyd runs %d goroutines, %d before the storm:\n%s", n, baseline, profile)
		}
		time.Sleep(time.Second)
	}
}

// goroutineProfile returns occamyd's goroutine count and its goroutine
// profile, grouped by stack. Idle keep-alive connections are closed first,
// since occamyd runs a goroutine for each.
func goroutineProfile(addr string) (int, string, error) {
	httpClient.CloseIdleConnections()
	resp, err := httpClient.Get("http://" + addr + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		return 0, "", fmt.Errorf("goroutine profile: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("goroutine profile: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("goroutine profile: %s, is occamy in debug mode?", resp.Status)
	}
	var n int
	if _, err := fmt.Sscanf(string(b), "goroutine profile: total %d", &n); err != nil {
		return 0, "", fmt.Errorf("goroutine profile: %w", err)
	}
	return n, string(b), nil
}
