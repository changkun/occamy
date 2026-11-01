// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/kubernetes/kubetest"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/protocol"
)

// These tests run the native Kubernetes backend end to end: a client
// signs in, streams a kubernetes connection through occamyd's routes,
// and drives a fake API server's exec stream through it.

// kubeToken is the bearer token the fake API server takes.
const kubeToken = "e2e-kube-T0KEN.must-not-leak"

func newKubeServer(t *testing.T, opts kubetest.Options) *kubetest.Server {
	t.Helper()
	s := kubetest.NewServer(opts)
	t.Cleanup(s.Close)
	return s
}

// kubeConnection is a connection for alice to pod web-0's container app
// in namespace web, through the API server s, whose certificate it
// pins.
func kubeConnection(id string, s *kubetest.Server, creds config.Credentials) config.Connection {
	return config.Connection{
		ID: id, Name: id, Protocol: "kubernetes", Backend: config.BackendNative, Target: s.Addr(),
		TLS: config.ConnectionTLS{Pins: []string{s.Fingerprint()}},
		Kubernetes: config.Kubernetes{
			Namespace: "web", Pod: "web-0", Container: "app",
			Command: slices.Clone(kubernetes.DefaultCommand),
		},
		Credentials: creds, Allow: config.Allow{Users: []string{"alice"}},
	}
}

// tokenFile stores token in a file for a connection's credentials.
func tokenFile(t *testing.T, token string) config.Credentials {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Credentials{TokenFile: p}
}

// acceptKube returns the backend's next exec stream on s.
func acceptKube(t *testing.T, s *kubetest.Server) *kubetest.Exec {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	e, err := s.Accept(ctx)
	if err != nil {
		t.Fatalf("the backend did not connect: %v", err)
	}
	return e
}

// nextKube returns the backend's next message to the fake API server of
// type M, skipping the others.
func nextKube[M kubetest.Message](t *testing.T, e *kubetest.Exec) M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	for {
		m, err := e.Next(ctx)
		if err != nil {
			var zero M
			t.Fatalf("waiting for the backend's %T: %v", zero, err)
		}
		if m, ok := m.(M); ok {
			return m
		}
	}
}

// awaitTerm reads cl's terminal output until it holds want.
func awaitTerm(t *testing.T, cl *client.Session, want string) {
	t.Helper()
	var out bytes.Buffer
	awaitEvent(t, cl, "terminal output "+want, func(ev client.Event) bool {
		switch ev := ev.(type) {
		case client.TermSnapshot:
			out.Reset()
			out.Write(ev.Data)
		case client.TermData:
			out.Write(ev.Data)
		}
		return bytes.Contains(out.Bytes(), []byte(want))
	})
}

// TestKubernetesEndToEnd streams a kubernetes connection with a token
// from a file: the backend runs the default command in the configured
// container with a terminal of the hello's size, the server sends the
// token only to the pinned certificate, the client's input and resizes
// reach the command, its output reaches the client, and its exit ends
// the stream with remote-ended.
func TestKubernetesEndToEnd(t *testing.T) {
	ks := newKubeServer(t, kubetest.Options{Token: kubeToken})
	ts, alice := vncTestServer(t, nil, kubeConnection("web", ks, tokenFile(t, kubeToken)))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()

	cl := ts.streamConnection(t, alice, "web", client.Options{Cols: 100, Rows: 30})
	if cl.Role() != protocol.RoleOwner || cl.Class() != protocol.ClassTerminal {
		t.Fatalf("role %v, class %v", cl.Role(), cl.Class())
	}
	e := acceptKube(t, ks)
	r := e.Request
	if r.Path != "/api/v1/namespaces/web/pods/web-0/exec" || r.Query.Get("container") != "app" ||
		!slices.Equal(r.Query["command"], kubernetes.DefaultCommand) ||
		r.Query.Get("stdin") != "true" || r.Query.Get("stdout") != "true" || r.Query.Get("stderr") != "false" || r.Query.Get("tty") != "true" {
		t.Errorf("request %s %v", r.Path, r.Query)
	}
	if e.Protocol != kubetest.ProtocolV5 {
		t.Errorf("subprotocol %q, want %s", e.Protocol, kubetest.ProtocolV5)
	}
	// The hello's size is the terminal's from the start.
	if m := nextKube[kubetest.ResizeMessage](t, e); m != (kubetest.ResizeMessage{Width: 100, Height: 30}) {
		t.Errorf("first size %+v, want the hello's 100×30", m)
	}

	if err := e.Stdout([]byte("web-0:/#")); err != nil {
		t.Fatal(err)
	}
	awaitTerm(t, cl, "web-0:/#")
	st := ts.api(t, http.MethodGet, "/api/v1/sessions/"+cl.SessionID(), alice, "")
	var state sessionStateResponse
	if err := json.Unmarshal(st.body, &state); err != nil || state.Backend != "kubernetes" || state.Protocol != "kubernetes" || state.Connection != "web" {
		t.Errorf("state %d %s", st.code, st.body)
	}

	if _, err := cl.TermInput(ctx, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if m := nextKube[kubetest.StdinMessage](t, e); string(m.Data) != "ls\r" {
		t.Errorf("stdin %q, want ls\\r", m.Data)
	}
	if _, err := cl.Resize(ctx, 120, 40); err != nil {
		t.Fatal(err)
	}
	if m := nextKube[kubetest.ResizeMessage](t, e); m != (kubetest.ResizeMessage{Width: 120, Height: 40}) {
		t.Errorf("size %+v, want 120×40", m)
	}
	awaitEvent(t, cl, "the resize", func(ev client.Event) bool { return ev == client.Resize{Width: 120, Height: 40} })
	if err := e.Stdout([]byte("bin:etc:usr\r\n")); err != nil {
		t.Fatal(err)
	}
	awaitTerm(t, cl, "bin:etc:usr")

	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	awaitEnd(t, cl, protocol.CloseRemoteEnded)
}

// TestKubernetesPrompt checks a connection that stores no token: the
// owner is asked for one, which the API server gets; an empty answer
// sends no Authorization header, which the server refuses with 401, and
// that ends the session with backend-failed.
func TestKubernetesPrompt(t *testing.T) {
	ks := newKubeServer(t, kubetest.Options{Token: kubeToken})
	ts, alice := vncTestServer(t, nil, kubeConnection("web", ks, config.Credentials{Prompt: true}))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	for _, answer := range []string{kubeToken, ""} {
		cl := ts.streamConnection(t, alice, "web", client.Options{})
		p := awaitEvent(t, cl, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok }).(client.Prompt)
		if len(p.Fields) != 1 || !p.Fields[0].Secret {
			t.Fatalf("prompt %+v, want one secret field", p)
		}
		if err := cl.PromptReply(ctx, p.ID, []string{answer}); err != nil {
			t.Fatal(err)
		}
		if answer == "" {
			awaitEnd(t, cl, protocol.CloseBackendFailed)
			reqs := ks.Requests()
			if len(reqs) != 2 || reqs[1].Header.Get("Authorization") != "" {
				t.Errorf("requests %+v, want the second without a token", reqs)
			}
			break
		}
		e := acceptKube(t, ks)
		if got := e.Header.Get("Authorization"); got != "Bearer "+kubeToken {
			t.Errorf("Authorization %q", got)
		}
		_ = e.Stdout([]byte("ok"))
		awaitTerm(t, cl, "ok")
		cl.Close()
	}
}

// TestKubernetesRefused checks connections that cannot run: a
// certificate that is not the pinned one ends the session with
// backend-failed before any request, and so the token, reaches the
// server; the API server's 403 ends it with backend-failed too, and
// neither tells the client the token. Either can end the session before
// the stream's welcome or after it, as the race goes; the second round
// makes it before.
func TestKubernetesRefused(t *testing.T) {
	ks := newKubeServer(t, kubetest.Options{Refuse: http.StatusForbidden, RefuseMessage: `pods "web-0" is forbidden: User "system:serviceaccount:occamy:occamy" cannot create resource "pods/exec"`})
	wrongPin := kubeConnection("wrong-pin", ks, tokenFile(t, kubeToken))
	cert, err := kubetest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pinned := newKubeServer(t, kubetest.Options{Certificate: cert})
	wrongPin.TLS.Pins = []string{pinned.Fingerprint()}
	if pinned.Fingerprint() == ks.Fingerprint() {
		t.Fatal("the two servers share a certificate")
	}
	ts, alice := vncTestServer(t, nil,
		wrongPin,
		kubeConnection("forbidden", ks, tokenFile(t, kubeToken)),
	)

	for _, early := range []bool{false, true} {
		if early {
			endBeforeWelcome(t)
		}
		check := func(id string, end client.End) {
			t.Helper()
			if strings.Contains(end.Reason, kubeToken) {
				t.Errorf("%s: the reason %q holds the token", id, end.Reason)
			}
			if early && !beforeWelcome(end) {
				t.Errorf("%s: the session ended after the welcome, want before", id)
			}
		}
		n := len(ks.Requests())
		check("wrong-pin", ts.failedStream(t, alice, "wrong-pin", protocol.CloseBackendFailed))
		if reqs := ks.Requests(); len(reqs) != n {
			t.Errorf("%d requests reached a server whose certificate is not pinned", len(reqs)-n)
		}
		check("forbidden", ts.failedStream(t, alice, "forbidden", protocol.CloseBackendFailed))
		if reqs := ks.Requests(); len(reqs) != n+1 || reqs[n].Header.Get("Authorization") != "Bearer "+kubeToken {
			t.Errorf("requests %+v, want one more, with the token", reqs[n:])
		}
	}
}
