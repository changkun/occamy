// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// failingServer is a session server whose sessions' remotes fail at
// once, as one that cannot be reached does.
func failingServer(t *testing.T) *testServer {
	t.Helper()
	ts, _ := sessionServer(t, func(f *config.File) { f.Limits.SessionsPerUser = 100 })
	ts.s.backends.register("fake", "", "fake", func(target, screen) (backend.Backend, error) {
		return failing{backendtest.NewTerminal(80, 24, 0)}, nil
	})
	return ts
}

// openEnded opens a session on ops-shell for token with POST
// /api/v1/sessions and returns it once its remote has failed: 200 ms
// later, as a client a little further away would open its stream, and
// not before the session has ended.
func openEnded(t *testing.T, ts *testServer, token string) openResponse {
	t.Helper()
	o := ts.openSession(t, token, "ops-shell")
	time.Sleep(200 * time.Millisecond)
	for deadline := time.Now().Add(10 * time.Second); ts.s.session(o.ID) != nil; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("session %s still runs", o.ID)
		}
	}
	return o
}

// refused opens session id's stream with token, and returns the close
// it gets instead of welcome, checking that the WebSocket then closes
// with the same code.
func refused(t *testing.T, ts *testServer, id, token string) protocol.Close {
	t.Helper()
	c, status, err := dial(ts.srv.URL+"/api/v1/sessions/"+id+"/stream", withToken(token), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	t.Cleanup(func() { _ = c.ws.CloseNow() })
	m, err := c.next()
	if err != nil {
		t.Fatal(err)
	}
	cl, ok := m.(*protocol.Close)
	if !ok {
		t.Fatalf("%#v, want close", m)
	}
	if _, err := c.next(); websocket.CloseStatus(err) != websocket.StatusCode(cl.Code) {
		t.Errorf("WebSocket closed with %v, want %v", err, cl.Code)
	}
	return *cl
}

// The closes of a session whose remote failed, and of no session.
var (
	failedClose   = protocol.Close{Code: protocol.CloseBackendFailed, Reason: "failed"}
	noSuchSession = protocol.Close{Code: protocol.CloseForbidden, Reason: "no such session"}
)

// sessionToken signs a session token for session id with role, for
// the static user name.
func sessionToken(t *testing.T, ts *testServer, id, role, name string) string {
	t.Helper()
	tok, err := ts.s.signer.SessionFor(id, role, auth.User{IdP: auth.IdPLocal, Subject: name}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestEndedSessionOwner opens a session whose remote fails at once, and
// its stream 200 ms later, after the session has ended: the owner gets
// backend-failed, as docs/api.md says, with the stream token and with an
// access token alike, rather than no such session.
func TestEndedSessionOwner(t *testing.T) {
	ts := failingServer(t)
	alice := ts.token(t, "alice", "ops")
	o := openEnded(t, ts, alice)
	for name, token := range map[string]string{"the stream token": o.StreamToken, "an access token": alice} {
		if c := refused(t, ts, o.ID, token); c != failedClose {
			t.Errorf("%s: close %v %q, want %v %q", name, c.Code, c.Reason, failedClose.Code, failedClose.Reason)
		}
	}
}

// TestEndedSessionOthers checks that nobody but the owner learns that
// an ended session existed: everyone else gets what a session that
// never existed gives.
func TestEndedSessionOthers(t *testing.T) {
	ts := failingServer(t)
	alice := ts.token(t, "alice", "ops")
	o := openEnded(t, ts, alice)
	other := ts.openSession(t, alice, "ops-shell").ID // ended or not, another session
	for name, token := range map[string]string{
		"another user's access token":           ts.token(t, "bob", "ops"),
		"another user's owner session token":    sessionToken(t, ts, o.ID, auth.RoleOwner, "bob"),
		"the owner's viewer session token":      sessionToken(t, ts, o.ID, auth.RoleViewer, "alice"),
		"the owner's read-only session token":   sessionToken(t, ts, o.ID, auth.RoleReadOnly, "alice"),
		"the owner's token for another session": sessionToken(t, ts, other, auth.RoleOwner, "alice"),
	} {
		if c := refused(t, ts, o.ID, token); c != noSuchSession {
			t.Errorf("%s: close %v %q, want %v %q", name, c.Code, c.Reason, noSuchSession.Code, noSuchSession.Reason)
		}
	}
	// A session that never existed answers the same.
	if c := refused(t, ts, "never-existed", ts.token(t, "bob", "ops")); c != noSuchSession {
		t.Errorf("a session that never existed: close %v %q", c.Code, c.Reason)
	}
}

// setOwnerGrace gives the sessions ts opens from now on grace as their
// owner grace.
func setOwnerGrace(ts *testServer, grace time.Duration) {
	ts.s.mu.Lock()
	ts.s.ownerGrace = grace
	ts.s.mu.Unlock()
}

// TestEndedSessionsBounded checks that an ended session is kept for its
// owner grace and no longer, and that no more are kept than the server
// keeps, the oldest going first, so that a client cannot grow them by
// opening sessions whose remotes fail.
func TestEndedSessionsBounded(t *testing.T) {
	kept := func(ts *testServer) int {
		ts.s.mu.Lock()
		defer ts.s.mu.Unlock()
		return ts.s.ended.len()
	}
	t.Run("time", func(t *testing.T) {
		const grace = 300 * time.Millisecond
		ts := failingServer(t)
		setOwnerGrace(ts, grace)
		alice := ts.token(t, "alice", "ops")
		o := openEnded(t, ts, alice)
		time.Sleep(grace)
		if c := refused(t, ts, o.ID, alice); c != noSuchSession {
			t.Errorf("after the owner grace: close %v %q, want no such session", c.Code, c.Reason)
		}
		if n := kept(ts); n != 0 {
			t.Errorf("%d ended sessions kept after the owner grace, want 0", n)
		}
	})
	t.Run("count", func(t *testing.T) {
		const most = 2
		ts := failingServer(t)
		ts.s.mu.Lock()
		ts.s.ended.most = most
		ts.s.mu.Unlock()
		alice := ts.token(t, "alice", "ops")
		var ids []string
		for range 4 {
			ids = append(ids, openEnded(t, ts, alice).ID)
			if n := kept(ts); n > most {
				t.Fatalf("%d ended sessions kept, want at most %d", n, most)
			}
		}
		for i, id := range ids {
			want := failedClose
			if i < len(ids)-most {
				want = noSuchSession
			}
			if c := refused(t, ts, id, alice); c != want {
				t.Errorf("session %d of %d: close %v %q, want %v %q", i+1, len(ids), c.Code, c.Reason, want.Code, want.Reason)
			}
		}
	})
}

// TestEndedSessionsSized checks that occamyd keeps as many ended
// sessions as it runs at once, and 1024 at the least, so that every
// owner of sessions that all end at once still learns how theirs ended.
func TestEndedSessionsSized(t *testing.T) {
	for _, tt := range []struct{ sessions, want int }{{0, 1024}, {100, 1024}, {5000, 5000}} {
		ts, _ := sessionServer(t, func(f *config.File) { f.Limits.Sessions = tt.sessions })
		if got := ts.s.ended.most; got != tt.want {
			t.Errorf("limits.sessions %d: %d ended sessions kept, want %d", tt.sessions, got, tt.want)
		}
	}
}

// TestEndedSessionOwnerGrace checks that a session waits for its owner,
// and keeps its end for them, for one owner grace: a session whose owner
// never comes ends with owner-left once it has passed, and its owner's
// stream gets owner-left until it has passed again.
func TestEndedSessionOwnerGrace(t *testing.T) {
	const grace = time.Second
	ts, _ := sessionServer(t, nil)
	setOwnerGrace(ts, grace)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	waitFor(t, "the session to end without its owner", func() bool { return ts.s.session(o.ID) == nil })
	if c := refused(t, ts, o.ID, alice); c.Code != protocol.CloseOwnerLeft {
		t.Errorf("within the owner grace: close %v %q, want owner-left", c.Code, c.Reason)
	}
	time.Sleep(grace)
	if c := refused(t, ts, o.ID, alice); c != noSuchSession {
		t.Errorf("after the owner grace: close %v %q, want no such session", c.Code, c.Reason)
	}
}

// TestJoinEndingSession joins a session that has ended but is still
// among the running ones, as for the moment before occamyd moves it to
// the ended ones: the owner gets how it ended, anyone else no such
// session.
func TestJoinEndingSession(t *testing.T) {
	ts := failingServer(t)
	alice := ts.token(t, "alice", "ops")
	claims, err := ts.s.verifier.VerifyAccess(alice)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ts.s.ids.of(claims)
	if err != nil {
		t.Fatal(err)
	}
	id, sess, v, err := ts.s.registry.Open(session.Config{Backend: failing{backendtest.NewTerminal(80, 24, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	v.Leave()
	<-sess.Done()
	ts.s.mu.Lock()
	ts.s.sessions[id] = &sessionInfo{id: id, s: sess, owner: p, protocol: "fake", opened: time.Now()}
	ts.s.mu.Unlock()
	if c := refused(t, ts, id, alice); c != failedClose {
		t.Errorf("the owner: close %v %q, want %v %q", c.Code, c.Reason, failedClose.Code, failedClose.Reason)
	}
	for name, token := range map[string]string{
		"another user's access token":      ts.token(t, "bob", "ops"),
		"the owner's viewer session token": sessionToken(t, ts, id, auth.RoleViewer, "alice"),
	} {
		if c := refused(t, ts, id, token); c != noSuchSession {
			t.Errorf("%s: close %v %q, want no such session", name, c.Code, c.Reason)
		}
	}
}

// held is a backend whose Run, once its session ends, holds on until
// release is closed, as a remote slow to hang up does: its session has
// ended, but is not done.
type held struct {
	backend.Backend
	cancelled chan struct{} // closed once Run's context is done
	release   chan struct{}
}

func (h held) Run(ctx context.Context, _ backend.Sink) error {
	<-ctx.Done()
	close(h.cancelled)
	<-h.release
	return ctx.Err()
}

// TestJoinEndingSessionWaits joins a session between its end and its
// backend's return, while it is still among the running ones: anyone
// but the owner gets no such session at once, and the owner's stream
// waits for the end, and gets its close.
func TestJoinEndingSessionWaits(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	h := held{backendtest.NewTerminal(80, 24, 0), make(chan struct{}), make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(h.release) }) }
	t.Cleanup(release)
	ts.s.backends.register("fake", "", "fake", func(target, screen) (backend.Backend, error) { return h, nil })
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	go ts.s.session(o.ID).s.Close()
	<-h.cancelled
	for name, token := range map[string]string{
		"another user's access token":      ts.token(t, "bob", "ops"),
		"the owner's viewer session token": sessionToken(t, ts, o.ID, auth.RoleViewer, "alice"),
	} {
		if c := refused(t, ts, o.ID, token); c != noSuchSession {
			t.Errorf("%s: close %v %q, want no such session", name, c.Code, c.Reason)
		}
	}
	c, status, err := dial(ts.srv.URL+"/api/v1/sessions/"+o.ID+"/stream", withToken(o.StreamToken), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	t.Cleanup(func() { _ = c.ws.CloseNow() })
	got := make(chan protocol.Message, 1)
	go func() {
		m, _ := c.next()
		got <- m
	}()
	select {
	case m := <-got:
		t.Fatalf("the owner got %#v before the backend returned", m)
	case <-time.After(300 * time.Millisecond):
	}
	if ts.s.session(o.ID) == nil {
		t.Fatal("the session is no longer among the running ones")
	}
	release()
	if cl, ok := (<-got).(*protocol.Close); !ok || cl.Code != protocol.CloseSessionClosed {
		t.Errorf("the owner: %#v, want close session-closed", cl)
	}
}
