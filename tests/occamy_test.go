// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package tests runs end-to-end and pressure tests against a running
// occamy, normally the demo stack from docker/docker-compose.yaml:
//
//	make build
//	make run
//	make e2e
//
// The tests are skipped unless OCCAMY_E2E_ENDPOINT is set.
package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"changkun.de/x/occamy/internal/protocol"
)

type jwtInput struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type jwtOutput struct {
	Code   int       `json:"code"`
	Expire time.Time `json:"expire"`
	Token  string    `json:"token"`
}

// credentials address the demo servers by their compose service names, as
// occamy resolves them inside the stack.
var credentials = map[string]jwtInput{
	"vnc": {
		Protocol: "vnc",
		Host:     "vnc:5900",
		Password: "vncpassword",
	},
	"ssh": {
		Protocol: "ssh",
		Host:     "ssh:22",
		Username: "occamy",
		Password: "occamy",
	},
	"rdp": {
		Protocol: "rdp",
		Host:     "rdp:3389",
		Username: "occamy",
		Password: "occamy",
	},
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// endpoint returns the occamy address from OCCAMY_E2E_ENDPOINT once it
// accepts logins, and skips the test if the variable is unset.
func endpoint(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("OCCAMY_E2E_ENDPOINT")
	if addr == "" {
		t.Skip("OCCAMY_E2E_ENDPOINT not set, see the package documentation")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := login(addr, credentials["vnc"])
		if err == nil {
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("occamy at %s is not ready: %v", addr, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// login asks occamy for a token for the given demo server and returns the
// WebSocket URL to connect with.
func login(addr string, credential jwtInput) (string, error) {
	b, err := json.Marshal(credential)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	resp, err := httpClient.Post("http://"+addr+"/api/v1/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	d, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login: %s: %s", resp.Status, d)
	}
	var out jwtOutput
	if err := json.Unmarshal(d, &out); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	return "ws://" + addr + "/api/v1/connect?token=" + out.Token, nil
}

func dial(url string) (*websocket.Conn, error) {
	d := websocket.Dialer{Subprotocols: []string{"guacamole"}, HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.Dial(url, nil) //nolint:bodyclose // gorilla/websocket: the response body need not be closed
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return ws, nil
}

// errDisconnected reports that occamy ended the session before it showed
// the remote desktop.
var errDisconnected = errors.New("disconnected before the desktop arrived")

// session connects to url, answers sync, and returns once every opcode in
// want has arrived, or with an error when the timeout passes first.
func session(url string, timeout time.Duration, want ...string) error {
	ws, err := dial(url)
	if err != nil {
		return err
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(timeout))

	seen := map[string]int{}
	missing := func() []string {
		var m []string
		for _, op := range want {
			if seen[op] == 0 {
				m = append(m, op)
			}
		}
		return m
	}
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("waiting for %v, saw %v: %w", missing(), seen, err)
		}
		r := bufio.NewReader(bytes.NewReader(msg))
		for {
			raw, err := protocol.ReadRaw(r)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("bad instruction from occamy: %w", err)
			}
			ins, err := protocol.ParseInstruction(raw)
			if err != nil {
				return fmt.Errorf("bad instruction from occamy: %w", err)
			}
			seen[ins.Opcode()]++
			switch ins.Opcode() {
			case "sync":
				ws.WriteMessage(websocket.TextMessage, raw)
			case "error":
				return fmt.Errorf("occamy sent error: %s", strings.Join(ins.Args(), ", "))
			case "disconnect":
				if len(missing()) > 0 {
					return fmt.Errorf("saw %v: %w", seen, errDisconnected)
				}
			}
		}
		if len(missing()) == 0 {
			ws.WriteMessage(websocket.TextMessage, []byte("10.disconnect;"))
			return nil
		}
	}
}

func protocols() []string {
	var protos []string
	for proto := range credentials {
		protos = append(protos, proto)
	}
	slices.Sort(protos)
	return protos
}

// checkSession opens a session to the given demo server and fails the test
// unless the remote desktop arrives. A session that starts while the
// previous one with the same claims is still tearing down joins it and is
// disconnected (specs/003-session-lifecycle-hardening.md, defect 6), so
// that case is retried briefly.
func checkSession(t *testing.T, addr string, credential jwtInput) {
	t.Helper()
	proto := credential.Protocol
	for attempt := 1; ; attempt++ {
		url, err := login(addr, credential)
		if err != nil {
			t.Fatal(err)
		}
		err = session(url, 15*time.Second, "size", "img", "sync")
		if err == nil {
			return
		}
		if !errors.Is(err, errDisconnected) || attempt == 5 {
			t.Fatalf("%s session: %v", proto, err)
		}
		t.Logf("%s attempt %d: %v", proto, attempt, err)
		time.Sleep(time.Second)
	}
}

// TestMain checks that occamy is still up after the tests. Tearing down a
// session used to crash occamyd after the client had already left, which
// no session in the tests would notice.
func TestMain(m *testing.M) {
	code := m.Run()
	if addr := os.Getenv("OCCAMY_E2E_ENDPOINT"); addr != "" && code == 0 {
		time.Sleep(2 * time.Second)
		if _, err := login(addr, credentials["vnc"]); err != nil {
			fmt.Fprintf(os.Stderr, "occamy did not survive the tests: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}
