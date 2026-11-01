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
// session and stops it. While it is stopped, an xclip that waits asks
// for the clipboard, and 0.3 s later another, which is killed 0.3 s
// after that, time enough as a rule for it to ask; then x11vnc
// continues and answers both, in that order. It checks the window of
// the first, which is there, and as a rule answers the second within
// 0.2 s, unchecked. Then the session sends text that the probe echoes.
//
// The test fails if x11vnc died: the session ended, another process
// serves the port, or the vnc container's log tells, since the test
// began, of an X error that x11vnc did not trap or of its watchdog. If
// x11vnc lived, the same process, but the xclip that waited did not get
// the text or the echo did not come, that is no sign of the error, and
// the test tries the whole sequence once more, and skips if it misses
// again, with what it missed; both say "the clipboard went missing with
// x11vnc alive", so that CI's logs show how often. A stop of 1.3 s, with
// 0.6 s for each step, missed the echo in two of sixteen runs back to
// back, for reasons not found; this one, with the echo's nonce first,
// missed none in twenty.
//
// With the variable, nothing can kill x11vnc here. Without it, the test
// cannot fail every time: whether the error comes before x11vnc stops
// trapping is a race; a host slow enough to put more than 0.2 s between
// the two answers has x11vnc check the second; and once a check of
// x11vnc's has found a window gone, it waits for the X server for good,
// until it is started again. The x11vnc that CI tests has served the
// parity matrix first, and if it stalled for more than 2 s there while
// it owned the clipboard, a check may have found one of the probe's
// xclips gone, and the test cannot tell the variable's absence. On a
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	began := time.Now()
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
	// signs counts the lines in the vnc container's log that tell that
	// the x11vnc on port 5901 met an X error that it did not trap, or
	// that its watchdog killed or started it.
	signs := func() int {
		t.Helper()
		out, n := compose.run(t, "logs", "vnc"), 0
		for _, s := range []string{"[5901] Xerror:", "[5901] caught X11 error", "[5901] watchdog:"} {
			n += strings.Count(out, s)
		}
		return n
	}
	// dead fails the test, with the end of the vnc container's log.
	dead := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s\nthe end of the vnc container's log:\n%s",
			fmt.Sprintf(format, args...), compose.run(t, "logs", "--tail", "30", "vnc"))
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
	start, x11vnc := signs(), pid()
	if x11vnc == "" {
		dead("no x11vnc serves port 5901")
	}
	// alive fails the test unless the x11vnc on port 5901 is the one
	// that served it at first, and the log tells of nothing since.
	alive := func() {
		t.Helper()
		if now := pid(); now != x11vnc {
			dead("x11vnc on port 5901 was process %s and is %q now", x11vnc, now)
		}
		if n := signs() - start; n > 0 {
			dead("the log of port 5901 has %d lines of an X error or of its watchdog since the test began", n)
		}
	}

	// next takes the session's events until done or d, which
	// acknowledges its frames, as occamyd drops a viewer that
	// acknowledges none for 30 s, and reports whether done came. The
	// session's end fails the test.
	next := func(d time.Duration, done func(client.Event) bool) bool {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		for {
			ev, err := cl.Next(rctx)
			if err != nil {
				if rctx.Err() != nil && ctx.Err() == nil {
					return false
				}
				dead("reading the session: %v", err)
			}
			if e, ok := ev.(client.End); ok {
				dead("the session ended: %v %q (%v)", e.Code, e.Reason, e.Err)
			}
			if done != nil && done(ev) {
				return true
			}
		}
	}

	// attempt runs the sequence and returns what went missing with
	// x11vnc alive, if anything, and how long its steps took.
	attempt := func(ownFor time.Duration) (missing []string, took string) {
		t.Helper()
		// Text that the probe leaves alone, so that x11vnc keeps the
		// clipboard. x11vnc drops cut text until it has made its
		// selection window, which it does only once its latest client
		// has been connected for 45 s, or for 8 s once it has seen a
		// window manager (guess_dm_gone in xevents.c), and keeps from
		// then on; so the session sends the text until the clipboard
		// holds it.
		text := fmt.Sprintf("%d x11vnc requestor", time.Now().UnixNano())
		t0 := time.Now()
		for deadline := t0.Add(ownFor); ; {
			if _, err := cl.Clipboard(ctx, "text/plain", []byte(text)); err != nil {
				t.Fatal(err)
			}
			next(time.Second, nil)
			if inVNC(readClipboard) == text {
				break
			}
			if time.Now().After(deadline) {
				alive()
				return []string{fmt.Sprintf("the clipboard did not hold %q after %v", text, ownFor)},
					fmt.Sprintf("from %v into the test", t0.Sub(began).Round(time.Millisecond))
			}
		}

		t1 := time.Now()
		got := inVNC(goneRequestor, "PID="+x11vnc)
		t2 := time.Now()
		next(3*time.Second, nil) // the watchdog looks every 2 s
		alive()
		if got != text {
			missing = append(missing, fmt.Sprintf("the xclip that waited got %q, want %q", got, text))
		}

		// The probe answers "owner: " text with "echo: " text, which
		// takes the clipboard from x11vnc again, for the tests that
		// follow. x11vnc sends the clipboard to its clients only if its
		// length or its first 32 bytes differ from those it read last
		// (CHKSZ in selection.c), so the text starts with its nonce: one
		// that came after them made runs less than 10 s apart, whose
		// echoes then had the same 32 bytes, fail.
		barrier := fmt.Sprintf("%d x11vnc barrier", time.Now().UnixNano())
		if _, err := cl.Clipboard(ctx, "text/plain", []byte("owner: "+barrier)); err != nil {
			t.Fatal(err)
		}
		t3 := time.Now()
		echoed := next(20*time.Second, func(ev client.Event) bool {
			c, ok := ev.(client.Clipboard)
			return ok && string(c.Data) == "echo: "+barrier
		})
		t4 := time.Now()
		alive()
		if !echoed {
			missing = append(missing, fmt.Sprintf("no echo of %q after %v", "owner: "+barrier, t4.Sub(t3).Round(time.Millisecond)))
		}
		return missing, fmt.Sprintf("from %v into the test: the clipboard held the text after %v, the stop took %v, the echo %v",
			t0.Sub(began).Round(time.Millisecond), t1.Sub(t0).Round(time.Millisecond),
			t2.Sub(t1).Round(time.Millisecond), t4.Sub(t3).Round(time.Millisecond))
	}

	first, took1 := attempt(90 * time.Second)
	if len(first) > 0 {
		t.Logf("the clipboard went missing with x11vnc alive, process %s: %s (%s); trying once more",
			x11vnc, strings.Join(first, "; "), took1)
		second, took2 := attempt(30 * time.Second)
		if len(second) > 0 {
			closeSessionV1(t, addr, token, cl.SessionID())
			t.Skipf("the clipboard went missing with x11vnc alive, process %s, twice: %s (%s); then %s (%s)",
				x11vnc, strings.Join(first, "; "), took1, strings.Join(second, "; "), took2)
		}
	}
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
// that waits, and 0.3 s later with one that it kills 0.3 s after that,
// which as a rule leaves each time to ask, in that order; the kill
// fails if the second has exited. It then continues x11vnc, and prints
// what the first got. x11vnc stays stopped for about 0.7 s, much less
// than the 2 s the probe's own xclips wait, so the first request x11vnc
// answers, which it checks, comes from a window that is there; had one
// of the probe's died first, x11vnc's check of it would make it wait
// for the X server for good.
const goneRequestor = `trap 'kill -CONT "$PID" 2>/dev/null' EXIT
kill -STOP "$PID" || exit
timeout 10 xclip -o -selection clipboard >/tmp/requestor.out 2>&1 &
waiting=$!
sleep 0.3
xclip -o -selection clipboard >/dev/null 2>&1 &
gone=$!
sleep 0.3
kill -KILL "$gone" 2>/dev/null || echo "the xclip to kill had exited"
wait "$gone" 2>/dev/null
sleep 0.1
kill -CONT "$PID"
wait "$waiting"
cat /tmp/requestor.out`
