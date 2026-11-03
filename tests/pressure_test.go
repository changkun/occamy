// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// pressureConnections are the demo connections the storm opens: one to
// each demo server's protocol.
var pressureConnections = []string{"ssh", "vnc", "rdp"}

// TestConnectionPressure opens many concurrent sessions through
// occamy-v1 to every demo server, half of which send garbage once they
// are welcomed, and then checks that occamyd is still healthy.
// Individual sessions may fail under the load. occamyd must not crash
// or lock up, its goroutines must return to their number before the
// storm once the sessions are closed, and a new session must work.
// OCCAMY_PRESSURE_N sets the number of rounds, each of which opens two
// sessions per demo server.
//
// The test is skipped unless OCCAMY_PRESSURE_N is set. CI runs it with
// OCCAMY_PRESSURE_N=10. It reads occamyd's goroutines from occamy-v1's
// debug listener, OCCAMY_E2E_V1_DEBUG, which make run OCCAMY_DEBUG=1
// turns on.
func TestConnectionPressure(t *testing.T) {
	v := os.Getenv("OCCAMY_PRESSURE_N")
	if v == "" {
		t.Skip("OCCAMY_PRESSURE_N not set, see the test documentation")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("bad OCCAMY_PRESSURE_N: %v", err)
	}
	debug := os.Getenv("OCCAMY_E2E_V1_DEBUG")
	if debug == "" {
		t.Fatal("OCCAMY_E2E_V1_DEBUG not set: occamy-v1's debug listener, such as localhost:5638, which make run OCCAMY_DEBUG=1 publishes")
	}
	addr := endpointV1(t)
	token, err := signIn(addr)
	if err != nil {
		t.Fatal(err)
	}
	before := goroutines(t, debug)

	connectors := map[string]func(ctx context.Context, addr, conn, token string) (string, error){
		"success": successConnect, "fail": failConnect,
	}
	var wg sync.WaitGroup
	var sessions, failed atomic.Int32
	var mu sync.Mutex
	var ids []string
	start := time.Now()
	for i := 1; i <= n; i++ {
		for _, conn := range pressureConnections {
			for name, connector := range connectors {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sessions.Add(1)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					id, err := connector(ctx, addr, conn, token)
					if id != "" {
						mu.Lock()
						ids = append(ids, id)
						mu.Unlock()
					}
					if err != nil {
						failed.Add(1)
						t.Logf("%s-%s-%d: %v", conn, name, i, err)
					}
				}()
			}
		}
	}
	wg.Wait()
	t.Logf("%d sessions in %v, %d failed", sessions.Load(), time.Since(start).Round(time.Millisecond), failed.Load())

	// A session whose owner left waits for the owner grace, so close
	// them, as a client that is done does. One that has ended already
	// answers 404, which is of no interest.
	for _, id := range ids {
		_ = apiV1(addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(id), token, nil, nil)
	}
	waitGoroutines(t, debug, before)

	checkSessionV1(t, addr, "vnc", "vnc")
}

// pressureOptions are the options of the storm's sessions.
var pressureOptions = client.Options{Name: "occamy-e2e/1", Width: 1024, Height: 768, Cols: 80, Rows: 24}

// successConnect opens conn and waits for its first frame that draws
// something, a screen's or a terminal's, and returns the session's ID.
func successConnect(ctx context.Context, addr, conn, token string) (string, error) {
	cl, err := client.Dial(ctx, "ws://"+addr+"/api/v1/connections/"+url.PathEscape(conn)+"/stream", token, pressureOptions)
	if err != nil {
		return "", err
	}
	defer cl.Close()
	for {
		ev, err := cl.Next(ctx)
		if err != nil {
			return cl.SessionID(), err
		}
		switch ev := ev.(type) {
		case client.End:
			return cl.SessionID(), fmt.Errorf("ended: %v %q (%v)", ev.Code, ev.Reason, ev.Err)
		case client.Frame:
			if cl.Class() == protocol.ClassGraphical {
				return cl.SessionID(), nil
			}
		case client.TermData:
			return cl.SessionID(), nil
		}
	}
}

// failConnect opens conn, and once welcomed sends a message that is no
// v1 message, which occamyd must refuse without harm. It returns the
// session's ID.
func failConnect(ctx context.Context, addr, conn, token string) (string, error) {
	q, err := protocol.EncodeHelloQuery(pressureOptions.Hello())
	if err != nil {
		return "", err
	}
	u := "ws://" + addr + "/api/v1/connections/" + url.PathEscape(conn) + "/stream?" + url.Values{protocol.HelloParameter: {q}}.Encode()
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{ //nolint:bodyclose // websocket.Dial closes it
		Subprotocols: []string{protocol.Subprotocol, protocol.TokenSubprotocol(token)},
	})
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer ws.CloseNow()
	cl, err := client.New(ctx, parityConn{ws}, pressureOptions)
	if err != nil {
		return "", fmt.Errorf("welcome: %w", err)
	}
	return cl.SessionID(), ws.Write(ctx, websocket.MessageBinary, []byte("bad message"))
}

// goroutineSlack is how many goroutines over its baseline occamyd may run
// once every session has ended. Each session holds several, so a leak in
// the storm's sixty or more sessions shows well above it.
const goroutineSlack = 5

// goroutines returns the number of goroutines occamyd runs, from the
// debug listener at debug.
func goroutines(t *testing.T, debug string) int {
	t.Helper()
	n, _, err := goroutineProfile(debug)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// waitGoroutines waits until occamyd runs no more than goroutineSlack
// goroutines over baseline. A backend can take its connect timeout to
// give up on a remote, so it waits up to a minute, and then fails the
// test with the goroutine profile.
func waitGoroutines(t *testing.T, debug string, baseline int) {
	t.Helper()
	start := time.Now()
	for {
		n, profile, err := goroutineProfile(debug)
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
// profile, grouped by stack, from the debug listener at debug. Idle
// keep-alive connections to the API are closed first, since occamyd runs
// a goroutine for each.
func goroutineProfile(debug string) (int, string, error) {
	httpClient.CloseIdleConnections()
	resp, err := httpClient.Get("http://" + debug + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		return 0, "", fmt.Errorf("goroutine profile: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("goroutine profile: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("goroutine profile: %s", resp.Status)
	}
	var n int
	if _, err := fmt.Sscanf(string(b), "goroutine profile: total %d", &n); err != nil {
		return 0, "", fmt.Errorf("goroutine profile: %w", err)
	}
	return n, string(b), nil
}
