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
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// The demo stack's occamy-v1 service (docker/occamy-v1.yaml): its user,
// and its connections to the demo VNC and RDP servers with the backend
// that each session's state should report: the -native connections
// leave backend out, so they get the native backends, the default, and
// the -guacd ones set backend: guacd, the fallback and the benchmark's
// baseline.
const (
	v1User     = "demo"
	v1Password = "occamy"
)

var v1Connections = []struct{ id, backend string }{
	{"vnc-native", "vnc"},
	{"vnc-guacd", "guacd"},
	{"rdp-native", "rdp"},
	{"rdp-guacd", "guacd"},
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

// TestE2EV1 streams the demo VNC and RDP servers through occamy-v1 with
// the Go client, through the native backends and through guacd, twice
// each: the desktop must arrive, the session's state must name the
// backend the connection picks, and input must reach the remote without
// ending the session.
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
	cl := openDesktopV1(t, ctx, addr, "/api/v1/connections/"+url.PathEscape(connection)+"/stream", token,
		client.Options{Width: 1024, Height: 768})
	if state := sessionV1(t, addr, token, cl.SessionID()); state.Backend != backend {
		t.Errorf("the session's backend is %q, want %q", state.Backend, backend)
	}
	typeV1(t, ctx, cl)
	closeSessionV1(t, addr, token, cl.SessionID())
}

// TestE2EV1RDP checks, against the demo xrdp server through the native
// RDP backend, what an owner of an rdp connection does beyond
// TestE2EV1's desktop, input and a session opened again after one
// ended: the clipboard both ways, a resize, a viewer joined by a share,
// and the owner leaving the session and coming back to it.
func TestE2EV1RDP(t *testing.T) {
	addr := endpointV1(t)
	openFor := func(t *testing.T, d time.Duration) (context.Context, string, *client.Session) {
		token, err := signIn(addr)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), d)
		t.Cleanup(cancel)
		cl := openDesktopV1(t, ctx, addr, "/api/v1/connections/rdp-native/stream", token,
			client.Options{Width: 1024, Height: 768})
		return ctx, token, cl
	}
	open := func(t *testing.T) (context.Context, string, *client.Session) { return openFor(t, time.Minute) }

	// paste pastes text and waits for the demo's echo of it, which a
	// slow server must not take for a clipboard that does not answer, and
	// returns the text.
	paste := func(t *testing.T, ctx context.Context, cl *client.Session) string {
		t.Helper()
		pasted := fmt.Sprintf("occamy-e2e:%d", time.Now().UnixNano())
		if _, err := cl.Clipboard(ctx, "text/plain", []byte(pasted)); err != nil {
			t.Fatal(err)
		}
		awaitV1(t, ctx, cl, "the echo of the pasted text", func(ev client.Event) bool {
			if s, ok := ev.(client.ClipboardStatus); ok && !s.Answering {
				t.Error("a clipboard that answers was reported as one that does not")
			}
			c, ok := ev.(client.Clipboard)
			return ok && strings.HasPrefix(c.MIME, "text/plain") && string(c.Data) == "echo:"+pasted
		})
		return pasted
	}

	// Text the owner pastes reaches the X session's CLIPBOARD through
	// xrdp's clipboard channel, where the demo image's clipboard echo
	// (docker/demo/rdp/clipboard-echo.sh) puts it back with echo: in
	// front, and xrdp offers that to the viewers.
	clipboard := func(t *testing.T) {
		t.Helper()
		ctx, token, cl := open(t)
		paste(t, ctx, cl)
		closeSessionV1(t, addr, token, cl.SessionID())
	}
	t.Run("clipboard", clipboard)

	// The owner's resize goes through Display Control, and xrdp resizes
	// the X session: the screen and the session's size follow. The
	// second resize leaves the X session, which xrdp keeps for the next
	// connection, at the size the other tests ask for.
	t.Run("resize", func(t *testing.T) {
		ctx, token, cl := open(t)
		for _, size := range []image.Point{{800, 600}, {1024, 768}} {
			if _, err := cl.Resize(ctx, uint16(size.X), uint16(size.Y)); err != nil {
				t.Fatal(err)
			}
			awaitV1(t, ctx, cl, fmt.Sprintf("the desktop at %d×%d", size.X, size.Y), func(ev client.Event) bool {
				_, ok := ev.(client.Frame)
				return ok && cl.Size() == size
			})
			if s := sessionV1(t, addr, token, cl.SessionID()).Size; s.Width != size.X || s.Height != size.Y {
				t.Errorf("the session's size is %d×%d, want %d×%d", s.Width, s.Height, size.X, size.Y)
			}
		}
		typeV1(t, ctx, cl)
		closeSessionV1(t, addr, token, cl.SessionID())
	})

	// A viewer joined by the owner's share sees the owner's desktop, at
	// the owner's size whatever its hello asks for, and its input
	// reaches the remote too.
	t.Run("shared-viewer", func(t *testing.T) {
		ctx, token, cl := open(t)
		id := cl.SessionID()
		// The owner reads on, and so acknowledges its frames, while the
		// viewer joins and types, so that occamy does not drop it as too
		// slow after streams.viewer_timeout.
		drainCtx, stopDrain := context.WithCancel(ctx)
		defer stopDrain()
		// drained carries what went wrong, or "" once the drain stopped.
		drained := make(chan string, 1)
		go func() {
			for {
				ev, err := cl.Next(drainCtx)
				switch e, end := ev.(client.End); {
				case err != nil && drainCtx.Err() != nil:
					drained <- ""
				case err != nil:
					drained <- "reading the owner's events: " + err.Error()
				case end:
					drained <- fmt.Sprintf("the owner's session ended while the viewer joined: %v %q (%v)", e.Code, e.Reason, e.Err)
				default:
					continue
				}
				return
			}
		}()
		var share struct {
			Token string `json:"token"`
		}
		if err := apiV1(addr, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(id)+"/shares", token, []byte(`{"role":"viewer"}`), &share); err != nil {
			t.Fatal(err)
		}
		viewer := openDesktopV1(t, ctx, addr, "/api/v1/sessions/"+url.PathEscape(id)+"/stream", share.Token,
			client.Options{Width: 640, Height: 480})
		if viewer.SessionID() != id || viewer.Role() != protocol.RoleViewer || viewer.Size() != cl.Size() {
			t.Errorf("the viewer joined %q as %v at %v, want %q as a viewer at the owner's %v", viewer.SessionID(), viewer.Role(), viewer.Size(), id, cl.Size())
		}
		var roles []string
		for _, v := range sessionV1(t, addr, token, id).Viewers {
			roles = append(roles, v.Role)
		}
		if !slices.Equal(roles, []string{"owner", "viewer"}) {
			t.Errorf("the session's viewers are %q, want the owner and the viewer", roles)
		}
		typeV1(t, ctx, viewer)
		stopDrain()
		if msg := <-drained; msg != "" {
			t.Fatal(msg)
		}
		typeV1(t, ctx, cl)
		closeSessionV1(t, addr, token, id)
	})

	// The owner closes the stream and opens the session's own stream
	// with the access token, within the owner grace: the same session
	// and its RDP connection carry on.
	t.Run("reconnect", func(t *testing.T) {
		ctx, token, cl := open(t)
		id := cl.SessionID()
		typeV1(t, ctx, cl)
		if err := cl.Close(); err != nil {
			t.Fatal(err)
		}
		// Once the session lists no viewer, the owner has left, so it
		// comes back through the grace rather than taking over from its
		// older stream.
		for len(sessionV1(t, addr, token, id).Viewers) > 0 {
			select {
			case <-ctx.Done():
				t.Fatal("the session still lists the owner's closed stream")
			case <-time.After(50 * time.Millisecond):
			}
		}
		back := openDesktopV1(t, ctx, addr, "/api/v1/sessions/"+url.PathEscape(id)+"/stream", token,
			client.Options{Width: 1024, Height: 768})
		if back.SessionID() != id || back.Role() != protocol.RoleOwner {
			t.Errorf("the owner came back to %q as %v, want %q as the owner", back.SessionID(), back.Role(), id)
		}
		typeV1(t, ctx, back)
		closeSessionV1(t, addr, token, id)
	})

	// A channel server that goes during a connection leaves the channel
	// silent while the session goes on: nothing restarts it before the
	// next connection, so this needs no change to the demo's restart. Text
	// sent then waits, even the text the last process took, and the owner
	// is told once the wait for an answer ends. The next connection has
	// the clipboard again.
	t.Run("clipboard-unanswered", func(t *testing.T) {
		compose := composeE2E(t)
		ctx, token, cl := openFor(t, 3*time.Minute)
		again := paste(t, ctx, cl)
		compose.run(t, "exec", "-T", "rdp", "sh", "-c", killChansrv)
		for deadline := time.Now().Add(10 * time.Second); compose.run(t, "exec", "-T", "-u", "occamy", "rdp", "sh", "-c", chansrvDisplays) != ""; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("a channel server still runs after it was killed")
			}
		}
		if _, err := cl.Clipboard(ctx, "text/plain", []byte(again)); err != nil {
			t.Fatal(err)
		}
		pasted := time.Now()
		awaitV1(t, ctx, cl, "word that the clipboard has not taken the text", func(ev client.Event) bool {
			if c, ok := ev.(client.Clipboard); ok && string(c.Data) == "echo:"+again {
				t.Error("the pasted text came back with no channel server")
			}
			s, ok := ev.(client.ClipboardStatus)
			return ok && !s.Answering
		})
		t.Logf("the owner heard %v after the paste", time.Since(pasted).Round(time.Second))
		typeV1(t, ctx, cl) // the session goes on
		closeSessionV1(t, addr, token, cl.SessionID())
		clipboard(t)
	})

	// xrdp's channel server, xrdp-chansrv, carries the clipboard. xrdp
	// 0.10.1's can exit when a connection closes just after attaching to
	// it, and xrdp starts it only with the session. The demo image's
	// reconnectwm.sh, which xrdp runs on each reconnection, starts it
	// again (docker/demo/rdp/reconnectwm.sh). A connection while it runs
	// starts none. Once it is killed, the next connection has the
	// clipboard: xrdp retries the channel server for 10 seconds, and a
	// connection that gets none in that time never has a clipboard. The
	// log then names the restart.
	//
	// The user may have more than one X session, as when two of
	// TestConnectionPressure's connections opened at once, each with a
	// channel server for its own display. Another display's channel
	// server must not stand in for the session's, so one runs on display
	// :99 while the session's is gone.
	t.Run("clipboard-after-chansrv-exit", func(t *testing.T) {
		compose := composeE2E(t)
		restarts := func() int {
			return strings.Count(compose.run(t, "logs", "rdp"), "starting xrdp-chansrv again")
		}
		// running counts the channel servers that run, by display.
		running := func() map[string]int {
			n := map[string]int{}
			for d := range strings.FieldsSeq(compose.run(t, "exec", "-T", "-u", "occamy", "rdp", "sh", "-c", chansrvDisplays)) {
				n[d]++
			}
			return n
		}
		before, alive := restarts(), running()
		t.Logf("channel servers by display before the first clipboard check: %v", alive)
		clipboard(t)
		display := sessionDisplay(t, compose)
		want := before
		if alive[display] == 0 { // an earlier connection's left none
			want++
		}
		if n := restarts(); n != want {
			t.Errorf("a connection with %d channel servers running on its display :%s restarted it %d times, want %d",
				alive[display], display, n-before, want-before)
		}
		// xrdp's channel server may exit as the check's connection closes,
		// so none is as good as one here: only a second is a fault.
		if n := running()[display]; n > 1 {
			t.Fatalf("%d channel servers run on the session's display :%s, want at most 1", n, display)
		}

		t.Logf("killing the channel servers, and starting one on display :%s", otherDisplay)
		compose.run(t, "exec", "-T", "rdp", "sh", "-c", killChansrv)
		for deadline := time.Now().Add(10 * time.Second); len(running()) > 0; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("a channel server still runs after it was killed")
			}
		}
		other := strings.TrimSpace(compose.run(t, "exec", "-T", "-u", "occamy", "-e", "DISPLAY=:"+otherDisplay, "rdp", "sh", "-c", startChansrv))
		t.Cleanup(func() {
			compose.run(t, "exec", "-T", "-u", "occamy", "rdp", "sh", "-c", "kill "+other+" 2>/dev/null; true")
		})
		for deadline := time.Now().Add(10 * time.Second); running()[otherDisplay] == 0; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("no channel server runs on display :%s", otherDisplay)
			}
		}
		t.Log("checking the clipboard again")
		clipboard(t)
		if n := restarts(); n != want+1 {
			t.Errorf("the channel server was restarted %d times after it was killed, want 1", n-want)
		}
		if n := running(); n[display] > 1 || n[otherDisplay] != 1 {
			t.Errorf("channel servers by display after the restart: %v, want at most 1 on :%s and 1 on :%s", n, display, otherDisplay)
		}
	})
}

// otherDisplay is the display that clipboard-after-chansrv-exit runs a
// channel server for, which no X session of the demo's has.
const otherDisplay = "99"

// sessionDisplay returns the number of the X display that xrdp's latest
// connection attached to, by its log.
func sessionDisplay(t *testing.T, compose composeCmd) string {
	t.Helper()
	const prefix = "session is available on display "
	log := compose.run(t, "logs", "rdp")
	i := strings.LastIndex(log, prefix)
	if i < 0 {
		t.Fatal("the rdp log names no display a connection attached to")
	}
	display, _, _ := strings.Cut(log[i+len(prefix):], " ")
	if _, err := strconv.Atoi(display); err != nil {
		t.Fatalf("the rdp log names display %q", display)
	}
	return display
}

// chansrvDisplays prints the display number of each xrdp-chansrv process
// of the user it runs as that runs in the rdp container, by the DISPLAY
// it was started with, leaving out those that have exited but are not
// yet reaped. The image has no procps, so it reads /proc. Only the user
// may read a process's environment: the container's root may not.
const chansrvDisplays = `for p in /proc/[0-9]*; do
	[ "$(cat "$p/comm" 2>/dev/null)" = xrdp-chansrv ] || continue
	read -r _ _ state _ 2>/dev/null < "$p/stat" || continue
	[ "$state" = Z ] && continue
	d=$(tr '\0' '\n' < "$p/environ" 2>/dev/null | sed -n 's/^DISPLAY=//p')
	d=${d#*:}
	d=${d%%.*}
	echo "${d:-none}"
done
true`

// killChansrv kills every xrdp-chansrv process in the rdp container.
const killChansrv = `for p in /proc/[0-9]*; do
	[ "$(cat "$p/comm" 2>/dev/null)" = xrdp-chansrv ] && kill "${p#/proc/}"
done
true`

// startChansrv starts a channel server in the rdp container for the
// DISPLAY it runs with, as the user it runs as, detached from the
// command that starts it, and prints its process ID. It listens in the
// user's socket directory, as sesman has the channel servers it starts
// do.
const startChansrv = `XRDP_SOCKET_PATH=/run/xrdp/sockdir/$(id -u) setsid /usr/sbin/xrdp-chansrv </dev/null >/dev/null 2>&1 &
echo "$!"`

// composeCmd runs the demo stack's compose commands.
type composeCmd []string

// composeE2E returns OCCAMY_E2E_COMPOSE's command, which make e2e sets
// to the demo stack's compose, or skips the test if it is not set.
func composeE2E(t *testing.T) composeCmd {
	t.Helper()
	c := strings.Fields(os.Getenv("OCCAMY_E2E_COMPOSE"))
	if len(c) == 0 {
		t.Skip("OCCAMY_E2E_COMPOSE not set, see the package documentation")
	}
	return c
}

// run runs the compose command with args and returns what it printed
// to its standard output: for exec, what the command printed to its
// own, and for logs, the containers' logs. Compose prints its own notes
// to its standard error: podman compose, for one, a banner before it
// runs its provider, unless PODMAN_COMPOSE_WARNING_LOGS is false. As
// compose logs may print a container's standard error there too, the
// demo images must log what the tests count to their standard output,
// as vnc/start.sh does for x11vnc and its watchdog, and rdp's
// reconnectwm.sh for its restarts of xrdp-chansrv. A failure reports
// both streams.
func (c composeCmd) run(t *testing.T, args ...string) string {
	t.Helper()
	// podman took 21 s for podman ps alone at a load of 250.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c[0], append(slices.Clone(c[1:]), args...)...)
	cmd.Env = append(os.Environ(), "PODMAN_COMPOSE_WARNING_LOGS=false")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s%s", strings.Join(c, " "), strings.Join(args, " "), err, stdout.Bytes(), stderr.Bytes())
	}
	return stdout.String()
}

// openDesktopV1 opens the stream at path with token and waits for the
// desktop: a frame of a screen that is not empty. The stream closes
// when the test ends, and so does the session if the stream joined as
// its owner.
func openDesktopV1(t *testing.T, ctx context.Context, addr, path, token string, opts client.Options) *client.Session {
	t.Helper()
	opts.Name = "occamy-e2e/1"
	cl, err := client.Dial(ctx, "ws://"+addr+path, token, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	if cl.Role() == protocol.RoleOwner {
		// A test that fails before it closes the session would leave it
		// for the owner grace, connected to the remote, which the next
		// test reaches as the same user, and counted against the user's
		// sessions. Its error, such as the 404 after the test closed
		// the session, is of no interest.
		id := cl.SessionID()
		t.Cleanup(func() {
			_ = apiV1(addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(id), token, nil, nil)
		})
	}
	awaitV1(t, ctx, cl, "the desktop", func(ev client.Event) bool {
		_, ok := ev.(client.Frame)
		return ok && !cl.Size().Eq(image.Point{})
	})
	return cl
}

// awaitV1 reads cl's events until done takes one. The session's end
// first fails the test.
func awaitV1(t *testing.T, ctx context.Context, cl *client.Session, what string, done func(client.Event) bool) {
	t.Helper()
	for {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if e, ok := ev.(client.End); ok {
			t.Fatalf("the session ended before %s: %v %q (%v)", what, e.Code, e.Reason, e.Err)
		}
		if done(ev) {
			return
		}
	}
}

// typeV1 checks that input reaches the remote: the frame that shows its
// effect, the xterm's echo of the key under the pointer, or a later one,
// reports it. The key carries its physical code, HID usage 0x04, as a
// browser's does, which the RDP backend sends as a scancode.
func typeV1(t *testing.T, ctx context.Context, cl *client.Session) {
	t.Helper()
	if _, err := cl.Pointer(ctx, 100, 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Key(ctx, true, 'a', 0x04); err != nil {
		t.Fatal(err)
	}
	seq, err := cl.Key(ctx, false, 'a', 0x04)
	if err != nil {
		t.Fatal(err)
	}
	awaitV1(t, ctx, cl, "a frame after the input", func(ev client.Event) bool {
		f, ok := ev.(client.Frame)
		return ok && f.LastSeq >= seq
	})
}

// sessionStateV1 is what GET /api/v1/sessions/{id} says of a session
// that the tests check.
type sessionStateV1 struct {
	Backend string `json:"backend"`
	Size    struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"size"`
	Viewers []struct {
		Role string `json:"role"`
	} `json:"viewers"`
}

// sessionV1 returns session id's state.
func sessionV1(t *testing.T, addr, token, id string) sessionStateV1 {
	t.Helper()
	var state sessionStateV1
	if err := apiV1(addr, http.MethodGet, "/api/v1/sessions/"+url.PathEscape(id), token, nil, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// closeSessionV1 closes session id.
func closeSessionV1(t *testing.T, addr, token, id string) {
	t.Helper()
	if err := apiV1(addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(id), token, nil, nil); err != nil {
		t.Fatal(err)
	}
}
