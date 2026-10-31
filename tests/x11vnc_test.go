// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
)

// TestX11vncClipboardRequestorGone checks that the demo x11vnc, and the
// session on it, live through a request for the clipboard from a window
// that is gone by the time x11vnc answers.
//
// x11vnc owns the clipboard after a client's cut text, and answers each
// request for it with a ChangeProperty and a SendEvent on the
// requestor's window. It traps X errors meanwhile, but checks the window
// only when 0.2 s have passed since its last check, and otherwise stops
// trapping without waiting for the X server: a BadWindow that comes
// later reaches its fatal handler, and its watchdog
// (docker/demo/vnc/start.sh) kills it and starts it again, which ends
// the sessions on it as backend-failed. The desktop's clipboard probe
// asks with an xclip that it kills after 2 s, so an x11vnc that stalled
// for longer met such requests. X11VNC_SENDEVENT_SYNC, which start.sh
// sets, makes x11vnc wait for the X server before it stops trapping.
//
// The test makes the x11vnc on port 5901 own the clipboard through a
// session, stops it, asks for the clipboard with one xclip that waits
// and then with one that it kills, and continues it. x11vnc checks the
// window of the first; the second comes within 0.2 s, unchecked. The
// first xclip must get the text, the same x11vnc must serve the port,
// and the session must still carry the clipboard both ways. With the
// variable, nothing can kill x11vnc here. Without it, whether the error
// comes before x11vnc stops trapping is a race, and once a check of
// x11vnc's has found a window gone, it waits for the X server for good,
// until it is started again; so the test cannot fail every time. On a
// Mac, against x11vnc processes just started, it failed in five of six
// runs without the variable, each time as x11vnc first owned the
// clipboard, before the stop, and passed in five of five with it.
func TestX11vncClipboardRequestorGone(t *testing.T) {
	addr := endpointV1(t)
	compose := composeE2E(t)
	token, err := signIn(addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cl := openDesktopV1(t, ctx, addr, "/api/v1/connections/vnc-none-native/stream", token,
		client.Options{Width: 1024, Height: 768})

	inVNC := func(script string, env ...string) string {
		t.Helper()
		args := []string{"exec", "-T", "-e", "DISPLAY=:0"}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		return strings.TrimSpace(compose.run(t, append(args, "vnc", "bash", "-c", script)...))
	}
	watchdog := func() int {
		return strings.Count(compose.run(t, "logs", "vnc"), "[5901] watchdog:")
	}
	// read takes the session's events for d, which acknowledges its
	// frames, as occamyd drops a viewer that acknowledges none for 30 s.
	// The session's end fails the test.
	read := func(d time.Duration) {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		for {
			ev, err := cl.Next(rctx)
			if err != nil {
				if rctx.Err() != nil && ctx.Err() == nil {
					return
				}
				t.Fatalf("reading the session: %v", err)
			}
			if e, ok := ev.(client.End); ok {
				t.Fatalf("the session ended: %v %q (%v)", e.Code, e.Reason, e.Err)
			}
		}
	}

	// pid prints the process id of the x11vnc on port 5901, or nothing.
	pid := func() string {
		t.Helper()
		out := inVNC(x11vncPID)
		if strings.Trim(out, "0123456789") != "" {
			t.Fatalf("looking for the x11vnc on port 5901: %q", out)
		}
		return out
	}

	// Text that the probe leaves alone, so that x11vnc keeps the
	// clipboard. x11vnc drops cut text until it has made its selection
	// window, which it does only once its latest client has been
	// connected for some seconds (guess_dm_gone in xevents.c; about 13 s
	// on the demo desktop, 45 s at most), and keeps from then on; so the
	// session sends the text until the clipboard holds it.
	text := fmt.Sprintf("x11vnc requestor %d", time.Now().UnixNano())
	for deadline := time.Now().Add(90 * time.Second); ; {
		if _, err := cl.Clipboard(ctx, "text/plain", []byte(text)); err != nil {
			t.Fatal(err)
		}
		read(time.Second)
		if inVNC(readClipboard) == text {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the clipboard does not hold %q after 90 s", text)
		}
	}
	before, x11vnc := watchdog(), pid()
	if x11vnc == "" {
		t.Fatal("no x11vnc serves port 5901")
	}

	got := inVNC(goneRequestor, "PID="+x11vnc)
	if got != text {
		t.Errorf("the xclip that waited got %q, want %q", got, text)
	}
	read(3 * time.Second) // the watchdog looks every 2 s
	if now := pid(); now != x11vnc {
		t.Errorf("x11vnc on port 5901 was process %s and is %q now", x11vnc, now)
	}
	if n := watchdog() - before; n > 0 {
		t.Errorf("the watchdog of port 5901 logged %d lines:\n%s", n, compose.run(t, "logs", "--tail", "20", "vnc"))
	}

	// The probe answers "owner: " text with "echo: " text, which takes
	// the clipboard from x11vnc again, for the tests that follow.
	barrier := fmt.Sprintf("x11vnc requestor %d", time.Now().UnixNano())
	if _, err := cl.Clipboard(ctx, "text/plain", []byte("owner: "+barrier)); err != nil {
		t.Fatal(err)
	}
	echoCtx, cancelEcho := context.WithTimeout(ctx, 20*time.Second)
	defer cancelEcho()
	awaitV1(t, echoCtx, cl, "the echo of the clipboard", func(ev client.Event) bool {
		c, ok := ev.(client.Clipboard)
		return ok && string(c.Data) == "echo: "+barrier
	})
	closeSessionV1(t, addr, token, cl.SessionID())
}

// readClipboard prints the clipboard of the vnc container's screen :0.
const readClipboard = `timeout 2 xclip -o -selection clipboard 2>/dev/null; true`

// x11vncPID prints the process id of the x11vnc on port 5901. The image
// has no procps, so it reads /proc. A process may exit before its turn,
// so the standard error is closed before the input is opened.
const x11vncPID = `for p in /proc/[0-9]*; do
	case "$(tr '\0' ' ' 2>/dev/null <"$p/cmdline")" in
	"x11vnc "*"-rfbport 5901 "*) echo "${p#/proc/}" ;;
	esac
done`

// goneRequestor stops x11vnc $PID, asks for the clipboard with an xclip
// that waits, and then with one that is killed while x11vnc is stopped,
// continues x11vnc, and prints what the first got. The probe's own
// xclips wait 2 s, longer than x11vnc is stopped, so the first request
// x11vnc answers, which it checks, comes from a window that is there.
const goneRequestor = `kill -STOP "$PID"
trap 'kill -CONT "$PID" 2>/dev/null' EXIT
timeout 10 xclip -o -selection clipboard >/tmp/requestor.out 2>&1 &
waiting=$!
sleep 0.3
timeout 0.4 xclip -o -selection clipboard >/dev/null 2>&1
sleep 0.3
kill -CONT "$PID"
wait "$waiting"
cat /tmp/requestor.out`
