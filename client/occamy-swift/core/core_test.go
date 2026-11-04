// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build darwin && cgo

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

// interop is the URL of client/internal/webinterop, the stream code of
// occamyd in miniature, which TestMain runs for the tests: /graphical
// serves a scripted desktop and /terminal an echoing terminal, both to
// the token "interop".
var interop string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "occamy-core-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "webinterop")
	build := exec.Command("go", "build", "-o", bin, "changkun.de/x/occamy/client/internal/webinterop")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the interop server:", err)
		return 1
	}
	srv := exec.Command(bin)
	stdin, err := srv.StdinPipe() // closing it stops the server
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := srv.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		_ = stdin.Close()
		_ = srv.Wait()
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "the interop server printed no URL:", err)
		return 1
	}
	interop = strings.TrimSpace(line)
	return m.Run()
}

// ws is the stream URL of an interop route.
func ws(route string) string { return "ws" + strings.TrimPrefix(interop, "http") + route }

const token = "interop"

// dial opens a session of route, which the test releases.
func dial(t *testing.T, route string, o *goOptions) handle {
	t.Helper()
	r := callDial(ws(route), token, o, 10_000)
	if r.Result != resOK {
		t.Fatalf("occamy_dial: %d: %s", r.Result, r.Message)
	}
	t.Cleanup(func() { callRelease(r.Handle) })
	return r.Handle
}

// until pulls events until one of kind for which ok holds, failing
// after 10 s.
func until(t *testing.T, h handle, kind int, ok func(goEvent) bool) goEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, ev := callNext(h, 1000)
		switch res {
		case resTimeout:
			continue
		case resOK:
		default:
			t.Fatalf("occamy_next: %d: %s", res, callError(h))
		}
		if !ev.zeroed {
			t.Fatalf("occamy_event_free left %+v", ev)
		}
		if ev.Kind == kind && (ok == nil || ok(ev)) {
			return ev
		}
		if ev.Kind == evEnd {
			t.Fatalf("the session ended: %d %s %s", ev.Code, ev.Text, ev.Detail)
		}
	}
	t.Fatalf("no event of kind %d", kind)
	return goEvent{}
}

// screen reads the interop desktop's framebuffer.
func screen(t *testing.T) (w, h int, pix []byte) {
	t.Helper()
	resp, err := http.Get(interop + "/screen")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s struct {
		Width, Height int
		Pixels        []byte
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s.Width, s.Height, s.Pixels
}

// inputs reads the input the interop desktop got.
func inputs(t *testing.T) []map[string]any {
	t.Helper()
	resp, err := http.Get(interop + "/inputs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var in []map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return in
}

func TestGraphical(t *testing.T) {
	h := dial(t, "/graphical", &goOptions{Name: "occamy-core-test", PixelRatio: 200})

	res, viewer, role, class, id := callInfo(h)
	if res != resOK || viewer == 0 || role != int(protocol.RoleOwner) || class != int(protocol.ClassGraphical) || id != "interop" {
		t.Fatalf("occamy_info: %d, viewer %d, role %d, class %d, session %q", res, viewer, role, class, id)
	}

	// The first frame redraws the whole screen after its resize; the
	// framebuffer, copied out by the frame's rectangles, comes to hold
	// the desktop.
	rs := until(t, h, evResize, nil)
	w, ht := rs.Width, rs.Height
	stride := 4*w + 12 // a row longer than the screen's, as a CGContext may have
	fb := make([]byte, stride*ht)
	frame := func(ev goEvent) bool {
		for _, r := range ev.Rects {
			res, got := callFramebuffer(h, w, ht, stride, r, 0)
			if res != resOK {
				t.Fatalf("occamy_framebuffer_read %v: %d: %s", r, res, callError(h))
			}
			for y := r.Min.Y; y < r.Max.Y; y++ {
				o := y*stride + 4*r.Min.X
				copy(fb[o:o+4*r.Dx()], got[o:o+4*r.Dx()])
			}
		}
		return true
	}
	first := until(t, h, evFrame, frame)
	if !slices.Contains(first.Rects, image.Rect(0, 0, w, ht)) {
		t.Errorf("the first frame changed %v, want the whole %d×%d screen", first.Rects, w, ht)
	}
	// The desktop's gradients go lossy, so the copy is close to the
	// desktop rather than equal.
	same := func() bool {
		sw, sh, want := screen(t)
		if sw != w || sh != ht {
			return false
		}
		got := make([]byte, 0, 4*w*ht)
		for y := range ht {
			got = append(got, fb[y*stride:y*stride+4*w]...)
		}
		return psnr(got, want) >= 30
	}
	// The first frame may show the screen before the desktop painted
	// all of it, and refinement sharpens what went lossy; later frames
	// bring the rest.
	for !same() {
		until(t, h, evFrame, frame)
	}

	// A key shows in the swatch, and its frame reports its sequence
	// number once the server applied it.
	res, seq := callKey(h, true, 'x', 0x1b)
	if res != resOK || seq == 0 {
		t.Fatalf("occamy_key: %d, seq %d", res, seq)
	}
	until(t, h, evFrame, func(ev goEvent) bool { frame(ev); return ev.LastSeq >= seq })
	// The swatch may go lossy, within a few levels of its colour.
	sw := backendtest.Swatch.Min
	got := fb[sw.Y*stride+4*sw.X:][:4]
	c := backendtest.SwatchColor('x')
	for i, v := range []byte{c.R, c.G, c.B, c.A} {
		if d := int(got[i]) - int(v); d < -4 || d > 4 {
			t.Errorf("the swatch is %v, want %v", got, c)
			break
		}
	}
	if res, _ := callKey(h, false, 'x', 0x1b); res != resOK {
		t.Fatalf("occamy_key up: %d", res)
	}

	// Pointer, wheel and text reach the remote.
	if res, _ := callPointer(h, 10, 20, uint8(protocol.ButtonLeft|protocol.ButtonRight)); res != resOK {
		t.Fatalf("occamy_pointer: %d", res)
	}
	if res, seq := callWheel(h, 0, 120, int32(protocol.WheelPixels)); res != resOK || seq == 0 {
		t.Fatalf("occamy_wheel: %d, seq %d", res, seq)
	}
	// Text commands of the interop desktop: a title and a bell.
	if res, _ := callText(h, "title hello"); res != resOK {
		t.Fatalf("occamy_text: %d", res)
	}
	if ev := until(t, h, evTitle, func(ev goEvent) bool { return ev.Text == "hello" }); ev.Text != "hello" {
		t.Errorf("title %q", ev.Text)
	}
	callText(h, "bell")
	until(t, h, evBell, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		in := inputs(t)
		got := map[string]bool{}
		for _, i := range in {
			got[fmt.Sprint(i["type"])] = true
		}
		if got["pointer"] && got["wheel"] && got["text"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the remote got %v", in)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The desktop echoes the clipboard.
	if res, seq := callClipboard(h, "text/plain", []byte("über")); res != resOK || seq == 0 {
		t.Fatalf("occamy_clipboard: %d, seq %d", res, seq)
	}
	cb := until(t, h, evClipboard, nil)
	if cb.Text != "text/plain" || string(cb.Data) != "über" {
		t.Errorf("clipboard %q %q", cb.Text, cb.Data)
	}

	// A resize request resizes the screen, and the framebuffer follows.
	if res, _ := callResize(h, 400, 300); res != resOK {
		t.Fatalf("occamy_resize: %d", res)
	}
	if rs := until(t, h, evResize, nil); rs.Width != 400 || rs.Height != 300 {
		t.Errorf("resized to %d×%d", rs.Width, rs.Height)
	}
	until(t, h, evFrame, nil)
	if res, _ := callFramebuffer(h, w, ht, stride, image.Rect(0, 0, 1, 1), 0); res != resErr || !strings.Contains(callError(h), "width×height") {
		t.Errorf("reading the old size: %d, %q", res, callError(h))
	}
	if res, got := callFramebuffer(h, 400, 300, 1600, image.Rect(399, 299, 400, 300), 0); res != resOK || got[299*1600+399*4+3] != 0xff {
		t.Errorf("reading the new size: %d", res)
	}

	// A prompt is answered once, by the owner.
	callText(h, "prompt")
	p := until(t, h, evPrompt, nil)
	if p.Text != "interop" || p.Detail != "Log in" || len(p.Fields) != 2 || p.Fields[0] != (protocol.PromptField{Label: "User"}) || p.Fields[1] != (protocol.PromptField{Label: "Password", Secret: true}) {
		t.Fatalf("prompt %+v", p)
	}
	if res := callPromptReply(h, p.ID, []string{"ada", "s3cret"}); res != resOK {
		t.Fatalf("occamy_prompt_reply: %d: %s", res, callError(h))
	}
	until(t, h, evTitle, func(ev goEvent) bool { return ev.Text == "answered ada,s3cret" })

	// Hidden, the client gets no frames; shown, one of the screen.
	if res := callView(h, false, image.Rectangle{}); res != resOK {
		t.Fatalf("occamy_view: %d", res)
	}
	if res := callView(h, true, image.Rect(0, 0, 200, 100)); res != resOK {
		t.Fatalf("occamy_view: %d", res)
	}
	if res := callView(h, true, image.Rect(-10, -10, -1, -1)); res != resErr || callError(h) == "" {
		t.Errorf("a viewport off the screen: %d, %q", res, callError(h))
	}

	// Closed, the session delivers End, then nothing.
	callClose(h)
	end := until(t, h, evEnd, nil)
	_ = end
	if res, _ := callNext(h, 100); res != resClosed {
		t.Errorf("occamy_next after End: %d", res)
	}
	if res, _ := callKey(h, true, 'y', 0x1c); res != resClosed {
		t.Errorf("occamy_key after close: %d", res)
	}
}

// until fails on End; this one wants it.
func TestEnd(t *testing.T) {
	h := dial(t, "/graphical", nil)
	until(t, h, evFrame, nil)
	callText(h, "end")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, ev := callNext(h, 1000)
		if res == resOK && ev.Kind == evEnd {
			if ev.Code != int(protocol.CloseRemoteEnded) || ev.Text == "" {
				t.Errorf("End %d %q %q", ev.Code, ev.Text, ev.Detail)
			}
			return
		}
		if res != resOK && res != resTimeout {
			t.Fatalf("occamy_next: %d", res)
		}
	}
	t.Fatal("no End")
}

func TestTerminal(t *testing.T) {
	h := dial(t, "/terminal", &goOptions{Name: "occamy-core-test"})
	if res, _, _, class, _ := callInfo(h); res != resOK || class != int(protocol.ClassTerminal) {
		t.Fatalf("occamy_info: %d, class %d", res, class)
	}
	if rs := until(t, h, evResize, nil); rs.Width != 80 || rs.Height != 24 {
		t.Errorf("the terminal is %d×%d", rs.Width, rs.Height)
	}
	until(t, h, evTermSnapshot, nil)
	until(t, h, evFrame, func(ev goEvent) bool { return len(ev.Rects) == 0 && ev.rectsNil })
	if res, seq := callTermInput(h, []byte("hi\r")); res != resOK || seq == 0 {
		t.Fatalf("occamy_term_input: %d, seq %d", res, seq)
	}
	var echoed []byte
	until(t, h, evTermData, func(ev goEvent) bool {
		echoed = append(echoed, ev.Data...)
		return bytes.Contains(echoed, []byte("hi"))
	})
	if res, _ := callResize(h, 100, 30); res != resOK {
		t.Fatalf("occamy_resize: %d", res)
	}
	if rs := until(t, h, evResize, nil); rs.Width != 100 || rs.Height != 30 {
		t.Errorf("resized to %d×%d", rs.Width, rs.Height)
	}
}

func TestDialErrors(t *testing.T) {
	r := callDial(ws("/graphical"), "wrong", nil, 10_000)
	if r.Result != resErr || r.CloseCode != int(protocol.CloseUnauthorized) || r.Message == "" {
		t.Errorf("a wrong token: %+v", r)
	}
	// The app tells an end from the server's reason, which must come
	// alone, not inside the client's error.
	if r.CloseReason == "" || strings.Contains(r.CloseReason, "client:") || !strings.Contains(r.Message, r.CloseReason) {
		t.Errorf("a wrong token's close reason: %+v", r)
	}
	r = callDial("ws://127.0.0.1:1/nothing", token, nil, 10_000)
	if r.Result != resErr || r.CloseCode != 0 || r.CloseReason != "" || !strings.Contains(r.Message, "dial") {
		t.Errorf("nothing listening: %+v", r)
	}
	if r := callDialNil(); r.Result != resErr || r.Message == "" {
		t.Errorf("no URL: %+v", r)
	}
	// A timeout of 0 ms has passed before the handshake.
	if r := callDial(ws("/graphical"), token, nil, 0); r.Result != resErr {
		t.Errorf("no time: %+v", r)
	}
}

func TestArguments(t *testing.T) {
	h := dial(t, "/graphical", nil)
	if res := callNextNil(h); res != resErr || callError(h) == "" {
		t.Errorf("occamy_next without an event: %d %q", res, callError(h))
	}
	rs := until(t, h, evResize, nil)
	until(t, h, evFrame, nil)
	for _, c := range []struct {
		name         string
		w, h, stride int
		r            image.Rectangle
		wantInError  string
	}{
		{"short stride", rs.Width, rs.Height, 4*rs.Width - 1, image.Rect(0, 0, 1, 1), "stride"},
		{"outside", rs.Width, rs.Height, 4 * rs.Width, image.Rect(rs.Width-1, 0, rs.Width+1, 1), "within"},
		{"negative", rs.Width, rs.Height, 4 * rs.Width, image.Rect(-1, 0, 1, 1), "within"},
	} {
		if res, _ := callFramebuffer(h, c.w, c.h, c.stride, c.r, 0); res != resErr || !strings.Contains(callError(h), c.wantInError) {
			t.Errorf("%s: %d, %q", c.name, res, callError(h))
		}
	}
	// Reading an empty rectangle writes nothing.
	if res, got := callFramebuffer(h, rs.Width, rs.Height, 4*rs.Width, image.Rectangle{}, 7); res != resOK || bytes.ContainsFunc(got, func(r rune) bool { return r != 7 }) {
		t.Errorf("an empty rectangle: %d", res)
	}
	// A released handle, and one never given out, are refused.
	callRelease(h)
	for _, x := range []handle{h, 1 << 40} {
		if res, _ := callNext(x, 0); res != resClosed {
			t.Errorf("occamy_next on %d: %d", x, res)
		}
		if res, _, _, _, _ := callInfo(x); res != resClosed {
			t.Errorf("occamy_info on %d: %d", x, res)
		}
		if res := callView(x, true, image.Rectangle{}); res != resClosed {
			t.Errorf("occamy_view on %d: %d", x, res)
		}
		if res := callPromptReply(x, 1, nil); res != resClosed {
			t.Errorf("occamy_prompt_reply on %d: %d", x, res)
		}
		if callError(x) != "" {
			t.Errorf("occamy_error on %d", x)
		}
		callClose(x)
		callRelease(x)
	}
}

// A prompt reply from a viewer other than the owner is refused, as is
// a read-only viewer's input; the interop server makes everyone the
// owner, so only the mapping of the client's errors is checked.
func TestResults(t *testing.T) {
	ss := &session{}
	for _, c := range []struct {
		err  error
		want int
	}{
		{client.ErrClosed, resClosed},
		{fmt.Errorf("wrapped: %w", client.ErrReadOnly), resReadOnly},
		{client.ErrNotOwner, resNotOwner},
		{fmt.Errorf("other"), resErr},
	} {
		if got := int(ss.fail(c.err)); got != c.want {
			t.Errorf("%v: %d, want %d", c.err, got, c.want)
		}
	}
}

// occamy.h declares the functions that core.go exports, with the same
// types but for const, which cgo's own declarations leave out.
func TestHeader(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("go", "tool", "cgo", "-objdir", dir, "-exportheader", filepath.Join(dir, "export.h"), "core.go")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	exported, err := os.ReadFile(filepath.Join(dir, "export.h"))
	if err != nil {
		t.Fatal(err)
	}
	declared, err := os.ReadFile("occamy.h")
	if err != nil {
		t.Fatal(err)
	}
	want := signatures(t, string(exported), `extern (.+?);`)
	got := signatures(t, string(declared), `(?ms)^((?:int|void|char \*) ?\**occamy_\w+\(.*?\));`)
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Errorf("occamy.h declares\n%s\ncore.go exports\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// signatures returns the function declarations pattern finds in a
// header, without parameter names, const, _Bool's spelling and spaces,
// sorted.
func signatures(t *testing.T, header, pattern string) []string {
	t.Helper()
	params := regexp.MustCompile(`(\w[\w\s\*]*?)\s*\b\w+\s*([,)])`)
	var out []string
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(header, -1) {
		d := strings.Join(strings.Fields(m[1]), " ")
		if !strings.Contains(d, "occamy_") {
			continue
		}
		d = strings.ReplaceAll(d, "const ", "")
		d = strings.ReplaceAll(d, "*const", "*")
		d = strings.ReplaceAll(d, "_Bool", "bool")
		name, args, _ := strings.Cut(d, "(")
		args = params.ReplaceAllString("("+args, "$1$2")
		out = append(out, strings.ReplaceAll(name+args, " ", ""))
	}
	slices.Sort(out)
	return out
}

// psnr compares the colour channels of two images of the same size.
func psnr(a, b []byte) float64 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	n := 0
	for i := 0; i < len(a); i += 4 {
		for k := range 3 {
			d := float64(a[i+k]) - float64(b[i+k])
			sum += d * d
			n++
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sum/float64(n)))
}
