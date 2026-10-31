// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
)

// The demo stack's occamy-v1 service (docker/occamy-v1.yaml): its user,
// and its connections to the demo VNC server with the backend that each
// session's state should report: vnc-native leaves backend out, so it
// gets vnc's default, the native backend, and vnc-guacd sets
// backend: guacd, the fallback and the benchmark's baseline.
const (
	v1User     = "demo"
	v1Password = "occamy"
)

var v1Connections = []struct{ id, backend string }{
	{"vnc-native", "vnc"},
	{"vnc-guacd", "guacd"},
}

// endpointV1 returns the occamy-v1 address from OCCAMY_E2E_V1_ENDPOINT
// once it accepts sign-ins, and skips the test if the variable is unset.
func endpointV1(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("OCCAMY_E2E_V1_ENDPOINT")
	if addr == "" {
		t.Skip("OCCAMY_E2E_V1_ENDPOINT not set, see the package documentation")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := signIn(addr)
		if err == nil {
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("occamy-v1 at %s is not ready: %v", addr, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// signIn returns an access token for the demo user.
func signIn(addr string) (string, error) {
	b, err := json.Marshal(map[string]string{"username": v1User, "password": v1Password})
	if err != nil {
		return "", err
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := apiV1(addr, http.MethodPost, "/api/v1/auth/token", "", b, &out); err != nil {
		return "", fmt.Errorf("sign-in: %w", err)
	}
	return out.AccessToken, nil
}

// apiV1 sends a request to API v1 and decodes a 2xx reply's JSON into
// out, unless out is nil.
func apiV1(addr, method, path, token string, body []byte, out any) error {
	req, err := http.NewRequest(method, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	d, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, d)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(d, out)
}

// TestE2EV1 streams the demo VNC server through occamy-v1 with the Go
// client, through the native backend and through guacd, twice each: the
// desktop must arrive, the session's state must name the backend the
// connection picks, and input must reach the remote without ending the
// session.
func TestE2EV1(t *testing.T) {
	addr := endpointV1(t)
	for _, c := range v1Connections {
		for _, run := range []string{"", "-again"} {
			t.Run(c.id+run, func(t *testing.T) {
				checkSessionV1(t, addr, c.id, c.backend)
			})
		}
	}
}

func checkSessionV1(t *testing.T, addr, connection, backend string) {
	token, err := signIn(addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, "ws://"+addr+"/api/v1/connections/"+url.PathEscape(connection)+"/stream", token,
		client.Options{Name: "occamy-e2e/1", Width: 1024, Height: 768})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	// The desktop arrives: a frame of a screen that is not empty.
	for shown := false; !shown; {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for the desktop: %v", err)
		}
		switch ev := ev.(type) {
		case client.Frame:
			shown = !cl.Size().Eq(image.Point{})
		case client.End:
			t.Fatalf("the session ended before the desktop arrived: %v %q (%v)", ev.Code, ev.Reason, ev.Err)
		}
	}
	var state struct {
		Backend string `json:"backend"`
	}
	if err := apiV1(addr, http.MethodGet, "/api/v1/sessions/"+url.PathEscape(cl.SessionID()), token, nil, &state); err != nil {
		t.Fatal(err)
	}
	if state.Backend != backend {
		t.Errorf("the session's backend is %q, want %q", state.Backend, backend)
	}

	// Input reaches the remote: the frame that shows its effect, the
	// xterm's echo of the key under the pointer, or a later one, reports
	// it.
	if _, err := cl.Pointer(ctx, 100, 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Key(ctx, true, 'a', 0); err != nil {
		t.Fatal(err)
	}
	seq, err := cl.Key(ctx, false, 'a', 0)
	if err != nil {
		t.Fatal(err)
	}
	for {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for a frame after the input: %v", err)
		}
		if f, ok := ev.(client.Frame); ok && f.LastSeq >= seq {
			break
		}
		if e, ok := ev.(client.End); ok {
			t.Fatalf("the session ended after the input: %v %q (%v)", e.Code, e.Reason, e.Err)
		}
	}
	if err := apiV1(addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(cl.SessionID()), token, nil, nil); err != nil {
		t.Fatal(err)
	}
}
