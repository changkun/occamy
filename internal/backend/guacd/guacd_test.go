// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	bridge "changkun.de/x/occamy/internal/backend/guacd"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

const addr = "guacd:4822"

func instruction(elements ...string) string {
	return guacamole.NewInstruction(elements).String()
}

// events is the order in which the bridge drew into the sink and wrote
// to guacd, as both report it from the bridge's goroutines.
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *events) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.log
	e.log = nil
	return l
}

// orderSink records Unlock in events.
type orderSink struct {
	*backendtest.Sink
	events *events
}

func (s orderSink) Unlock(r image.Rectangle) {
	s.Sink.Unlock(r)
	s.events.add("unlock " + r.String())
}

// orderConn records writes of sync in events.
type orderConn struct {
	net.Conn
	events *events
}

func (c orderConn) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("4.sync,")) {
		c.events.add("write " + string(p))
	}
	return c.Conn.Write(p)
}

// harness runs a Backend against a scripted guacd on a net.Pipe.
type harness struct {
	t      *testing.T
	b      *bridge.Backend
	sink   *backendtest.Sink
	events *events
	logs   *bytes.Buffer // read only after Run returns
	cancel context.CancelFunc
	done   chan error

	conn *guacamole.InstructionIO // guacd's side
	got  chan *guacamole.Instruction
	nops chan time.Time
}

var config = guacd.Config{
	Protocol: "vnc",
	Params:   map[string]string{"hostname": "vnc", "port": "5900"},
	Width:    640, Height: 480, DPI: 96,
}

// start runs a Backend and plays guacd's side of the handshake. It must
// run in a synctest bubble.
func start(t *testing.T, sink *backendtest.Sink) *harness {
	t.Helper()
	cli, srv := net.Pipe()
	h := &harness{
		t:      t,
		sink:   sink,
		events: &events{},
		logs:   &bytes.Buffer{},
		done:   make(chan error, 1),
		conn:   guacamole.NewInstructionIO(srv),
		got:    make(chan *guacamole.Instruction, 1024),
		nops:   make(chan time.Time, 1024),
	}
	h.b = bridge.New(addr, config, bridge.Options{
		Logger: slog.New(slog.NewTextHandler(h.logs, nil)),
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != addr {
				t.Errorf("Dial(%q, %q), want tcp %s", network, address, addr)
			}
			return orderConn{Conn: cli, events: h.events}, nil
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	h.cancel = cancel
	go func() { h.done <- h.b.Run(ctx, orderSink{Sink: sink, events: h.events}) }()

	want := []string{
		instruction("select", "vnc"),
		instruction("size", "640", "480", "96"),
		instruction("audio"),
		instruction("video"),
		instruction("image", "image/png", "image/jpeg", "image/webp"),
		instruction("connect", "VERSION_1_5_0", "vnc", "5900", ""),
	}
	for i, w := range want {
		ins, err := h.conn.Read()
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
		if ins.String() != w {
			t.Fatalf("handshake instruction %d = %s, want %s", i, ins, w)
		}
		if i == 0 {
			h.send("args", "VERSION_1_5_0", "hostname", "port", "password")
		}
	}
	h.send("ready", "$conn")

	go func() {
		defer close(h.got)
		for {
			ins, err := h.conn.Read()
			if err != nil {
				return
			}
			if ins.Opcode() == "nop" {
				h.nops <- time.Now()
				continue
			}
			h.got <- ins
		}
	}()
	return h
}

func (h *harness) send(elements ...string) {
	h.t.Helper()
	if _, err := h.conn.Write(guacamole.NewInstruction(elements)); err != nil {
		h.t.Fatalf("guacd send %v: %v", elements, err)
	}
}

// next returns the next instruction the bridge sent, other than nop.
func (h *harness) next() *guacamole.Instruction {
	h.t.Helper()
	select {
	case ins, ok := <-h.got:
		if !ok {
			h.t.Fatal("the bridge closed the connection")
		}
		return ins
	case <-time.After(time.Minute):
		h.t.Fatal("nothing from the bridge for a minute")
	}
	return nil
}

func (h *harness) expect(elements ...string) {
	h.t.Helper()
	if got, want := h.next().String(), instruction(elements...); got != want {
		h.t.Fatalf("the bridge sent %s, want %s", got, want)
	}
}

// frame ends a frame with sync and waits for the bridge's answer.
func (h *harness) frame(ts int) {
	h.t.Helper()
	h.send("sync", strconv.Itoa(ts))
	h.expect("sync", strconv.Itoa(ts))
}

// wait returns what Run returned.
func (h *harness) wait() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(time.Minute):
		h.t.Fatal("Run did not return")
	}
	return nil
}

// end disconnects and checks that Run returns nil.
func (h *harness) end() {
	h.t.Helper()
	h.send("disconnect")
	if err := h.wait(); err != nil {
		h.t.Fatalf("Run after disconnect = %v", err)
	}
}

func pngBase64(t *testing.T, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

var (
	black = color.RGBA{A: 0xff}
	red   = color.RGBA{R: 0xff, A: 0xff}
	green = color.RGBA{G: 0xff, A: 0xff}
)

func wantPixel(t *testing.T, img *image.RGBA, x, y int, want color.RGBA) {
	t.Helper()
	if got := img.RGBAAt(x, y); got != want {
		t.Errorf("pixel (%d, %d) = %v, want %v", x, y, got, want)
	}
}

func TestClass(t *testing.T) {
	b := bridge.New(addr, config, bridge.Options{})
	if b.Class() != backend.Graphical {
		t.Errorf("Class = %v, want graphical", b.Class())
	}
}

func TestFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		h := start(t, sink)

		// The first frame sizes the screen and draws all of it.
		h.send("size", "0", "4", "4")
		h.frame(100)
		if got := sink.Size(); got != image.Pt(4, 4) {
			t.Fatalf("size = %v, want 4x4", got)
		}
		if got := sink.TakeDamage(); !slices.Equal(got, []image.Rectangle{image.Rect(0, 0, 4, 4)}) {
			t.Errorf("first frame damage = %v, want the screen", got)
		}
		h.events.take()

		// An image in blobs lands in the sink before the sync is answered.
		data, _ := base64.StdEncoding.DecodeString(pngBase64(t, 2, 2, red))
		h.send("img", "1", "14", "0", "image/png", "1", "1")
		h.send("blob", "1", base64.StdEncoding.EncodeToString(data[:10]))
		h.send("blob", "1", base64.StdEncoding.EncodeToString(data[10:]))
		h.send("end", "1")
		h.frame(200)
		want := []string{"unlock (1,1)-(3,3)", "write " + instruction("sync", "200")}
		if got := h.events.take(); !slices.Equal(got, want) {
			t.Errorf("events = %q, want %q", got, want)
		}
		px := sink.Pixels()
		wantPixel(t, px, 1, 1, red)
		wantPixel(t, px, 2, 2, red)
		wantPixel(t, px, 0, 0, black)
		wantPixel(t, px, 3, 3, black)
		if got := sink.TakeDamage(); !slices.Equal(got, []image.Rectangle{image.Rect(1, 1, 3, 3)}) {
			t.Errorf("damage = %v, want (1,1)-(3,3)", got)
		}

		// Areas far apart are drawn apart; a frame that draws nothing locks
		// nothing; a timestamp is answered once.
		h.send("size", "0", "100", "100")
		h.frame(300)
		sink.TakeDamage()
		h.send("rect", "0", "0", "0", "2", "2")
		h.send("cfill", "14", "0", "0", "255", "0", "255")
		h.send("rect", "0", "90", "90", "2", "2")
		h.send("cfill", "14", "0", "0", "255", "0", "255")
		h.frame(400)
		got := sink.TakeDamage()
		slices.SortFunc(got, func(a, b image.Rectangle) int { return a.Min.X - b.Min.X })
		if want := []image.Rectangle{image.Rect(0, 0, 2, 2), image.Rect(90, 90, 92, 92)}; !slices.Equal(got, want) {
			t.Errorf("damage = %v, want %v", got, want)
		}
		wantPixel(t, sink.Pixels(), 91, 91, green)
		locks := sink.Locks()
		h.send("sync", "400")
		h.frame(500)
		if sink.Locks() != locks {
			t.Errorf("%d locks for frames that drew nothing", sink.Locks()-locks)
		}
		h.end()
	})
}

func TestResize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		h := start(t, sink)

		// Nothing reaches the sink before the default layer has a size.
		h.send("rect", "0", "0", "0", "2", "2")
		h.send("cfill", "14", "0", "255", "0", "0", "255")
		h.frame(1)
		if sink.Resizes() != 0 || sink.Locks() != 0 {
			t.Fatalf("%d resizes and %d locks before size", sink.Resizes(), sink.Locks())
		}

		h.send("size", "0", "8", "4")
		h.send("rect", "0", "0", "0", "8", "4")
		h.send("cfill", "14", "0", "255", "0", "0", "255")
		h.frame(2)
		if sink.Size() != image.Pt(8, 4) || sink.Resizes() != 1 {
			t.Fatalf("size %v after %d resizes, want 8x4 after 1", sink.Size(), sink.Resizes())
		}
		wantPixel(t, sink.Pixels(), 7, 3, red)

		// The display keeps what fits, and the whole screen is drawn
		// after Resize.
		h.send("size", "0", "4", "6")
		h.frame(3)
		if sink.Size() != image.Pt(4, 6) || sink.Resizes() != 2 {
			t.Fatalf("size %v after %d resizes, want 4x6 after 2", sink.Size(), sink.Resizes())
		}
		px := sink.Pixels()
		wantPixel(t, px, 3, 3, red)
		wantPixel(t, px, 3, 5, black)

		// The same size again is no resize, and layers other than the
		// default do not resize the screen.
		h.send("size", "0", "4", "6")
		h.send("size", "1", "50", "50")
		h.frame(4)
		if sink.Resizes() != 2 {
			t.Errorf("%d resizes, want 2", sink.Resizes())
		}
		h.end()
	})
}

func TestCursorNameMouse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		h := start(t, sink)
		h.send("size", "0", "8", "8")

		// guacd draws the pointer on a buffer.
		h.send("rect", "-1", "0", "0", "2", "3")
		h.send("cfill", "12", "-1", "0", "255", "0", "255")
		h.send("cursor", "1", "2", "-1", "0", "0", "2", "3")
		h.send("name", "occamy@desktop")
		h.send("mouse", "5", "6", "0", "1234")
		h.frame(1)
		img, hot := sink.CursorImage()
		if img == nil || img.Bounds() != image.Rect(0, 0, 2, 3) || hot != image.Pt(1, 2) {
			t.Fatalf("cursor %v at %v, want 2x3 at (1,2)", img, hot)
		}
		wantPixel(t, img, 1, 2, green)
		if got := sink.TitleText(); got != "occamy@desktop" {
			t.Errorf("title = %q", got)
		}
		if got := sink.CursorPos(); got != image.Pt(5, 6) {
			t.Errorf("cursor position = %v, want (5,6)", got)
		}
		// The pointer is not part of the screen.
		wantPixel(t, sink.Pixels(), 0, 0, black)

		// guacd hides the pointer with one transparent pixel.
		h.send("rect", "-2", "0", "0", "1", "1")
		h.send("cfill", "12", "-2", "0", "0", "0", "0")
		h.send("cursor", "0", "0", "-2", "0", "0", "1", "1")
		h.frame(2)
		if img, _ := sink.CursorImage(); img != nil {
			t.Errorf("cursor = %v, want hidden", img.Bounds())
		}
		h.end()
	})
}

func TestClipboardFromGuacd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		h := start(t, sink)
		text := strings.Repeat("héllo, ", 1000)
		h.send("clipboard", "3", "text/plain")
		h.send("blob", "3", base64.StdEncoding.EncodeToString([]byte(text[:4096])))
		h.send("blob", "3", base64.StdEncoding.EncodeToString([]byte(text[4096:])))
		h.send("end", "3")
		h.frame(1)
		mime, data := sink.ClipboardContent()
		if mime != "text/plain" || string(data) != text {
			t.Errorf("clipboard = %q, %d bytes, want text/plain, %d bytes", mime, len(data), len(text))
		}
		h.end()
	})
}

func TestClipboardToGuacd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		data := bytes.Repeat([]byte("0123456789"), 1500) // three blobs
		if err := h.b.Input(backend.Clipboard{MIME: "text/plain", Data: data}); err != nil {
			t.Fatal(err)
		}
		h.expect("clipboard", "0", "text/plain")
		var got []byte
		for {
			ins := h.next()
			if ins.Opcode() == "end" {
				if ins.Args()[0] != "0" {
					t.Errorf("end of stream %s", ins.Args()[0])
				}
				break
			}
			if ins.Opcode() != "blob" || ins.Args()[0] != "0" {
				t.Fatalf("the bridge sent %s inside the clipboard stream", ins)
			}
			if n := len(ins.Args()[1]); n > guacamole.MaxInstructionLength {
				t.Errorf("blob of %d characters", n)
			}
			b, err := base64.StdEncoding.DecodeString(ins.Args()[1])
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, b...)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("clipboard stream carried %d bytes, want %d", len(got), len(data))
		}

		// Without a MIME type, the clipboard is text; empty data has no
		// blob.
		if err := h.b.Input(backend.Clipboard{}); err != nil {
			t.Fatal(err)
		}
		h.expect("clipboard", "0", "text/plain")
		h.expect("end", "0")
		h.end()
	})
}

func TestPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
			return backend.PromptReply{Answers: []string{"alice", "s3cret"}}, nil
		}}
		h := start(t, sink)
		h.send("required", "username", "password", "domain")
		for _, a := range []struct{ name, value string }{{"username", "alice"}, {"password", "s3cret"}, {"domain", ""}} {
			h.expect("argv", "1", "text/plain", a.name)
			if a.value != "" {
				h.expect("blob", "1", base64.StdEncoding.EncodeToString([]byte(a.value)))
			}
			h.expect("end", "1")
			h.send("ack", "1", "Ready for updated parameter.", "0")
		}
		want := backend.Prompt{Name: "vnc", Fields: []backend.PromptField{
			{Label: "Username"}, {Label: "Password", Secret: true}, {Label: "Domain"},
		}}
		if got := sink.Prompts(); len(got) != 1 || got[0].Name != want.Name || !slices.Equal(got[0].Fields, want.Fields) {
			t.Errorf("prompts = %+v, want %+v", got, want)
		}

		// A refusal is logged.
		h.send("ack", "1", "Not allowed.", "771")
		h.frame(1)
		h.end()
		if !strings.Contains(h.logs.String(), "Not allowed.") {
			t.Errorf("refusal not logged:\n%s", h.logs)
		}
	})
}

func TestPromptFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{}) // no Answer
		h.send("required", "password")
		if err := h.wait(); !errors.Is(err, backendtest.ErrNoAnswer) {
			t.Errorf("Run = %v, want %v", err, backendtest.ErrNoAnswer)
		}
	})
}

func TestError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		h.send("error", "Aborted. See logs.", "519")
		err := h.wait()
		var e *guacd.Error
		if !errors.As(err, &e) || e.Status != 519 || e.Message != "Aborted. See logs." {
			t.Errorf("Run = %v, want guacd's error with status 519", err)
		}
	})
}

func TestDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		h.end()
		if err := h.b.Input(backend.Key{Down: true, Keysym: 'a'}); !errors.Is(err, bridge.ErrClosed) {
			t.Errorf("Input after Run = %v, want ErrClosed", err)
		}
		if err := h.b.Run(t.Context(), &backendtest.Sink{}); err == nil {
			t.Error("second Run: no error")
		}
	})
}

func TestClosedWithoutDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		h.frame(1) // the handshake is over
		h.conn.Close()
		if err := h.wait(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("Run = %v, want %v", err, io.ErrUnexpectedEOF)
		}
	})
}

func TestCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		h.cancel()
		if err := h.wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	})
}

func TestCancelHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cli, srv := net.Pipe()
		defer srv.Close()
		b := bridge.New(addr, config, bridge.Options{
			Dial: func(context.Context, string, string) (net.Conn, error) { return cli, nil },
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- b.Run(ctx, &backendtest.Sink{}) }()
		// guacd reads select and never answers.
		if _, err := guacamole.NewInstructionIO(srv).Read(); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	})
}

func TestDialFails(t *testing.T) {
	dialErr := errors.New("no route")
	b := bridge.New(addr, config, bridge.Options{
		Dial: func(context.Context, string, string) (net.Conn, error) { return nil, dialErr },
	})
	if err := b.Run(t.Context(), &backendtest.Sink{}); !errors.Is(err, dialErr) {
		t.Errorf("Run = %v, want %v", err, dialErr)
	}
}

func TestInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		for _, tc := range []struct {
			ev   backend.InputEvent
			want [][]string
		}{
			{backend.Key{Down: true, Keysym: 0xff0d, Code: 0x28}, [][]string{{"key", "65293", "1"}}},
			{backend.Key{Keysym: 'a'}, [][]string{{"key", "97", "0"}}},
			// Back and Forward have no bits in guacd's mask.
			{backend.Pointer{X: 10, Y: 20, Buttons: backend.ButtonLeft | backend.ButtonRight | backend.ButtonBack},
				[][]string{{"mouse", "10", "20", "5"}}},
			{backend.Pointer{X: -3, Y: 7, Buttons: backend.ButtonMiddle | backend.ButtonForward},
				[][]string{{"mouse", "0", "7", "2"}}},
			// A notch as Firefox reports it, 3 lines, presses once at the
			// pointer with the buttons held.
			{backend.Wheel{DY: 3, Mode: backend.WheelLines},
				[][]string{{"mouse", "0", "7", "18"}, {"mouse", "0", "7", "2"}}},
			// A notch as Chrome reports it, 100 pixels, also presses once.
			{backend.Wheel{DY: -100, Mode: backend.WheelPixels},
				[][]string{{"mouse", "0", "7", "10"}, {"mouse", "0", "7", "2"}}},
			{backend.Wheel{DY: -106}, [][]string{
				{"mouse", "0", "7", "10"}, {"mouse", "0", "7", "2"},
				{"mouse", "0", "7", "10"}, {"mouse", "0", "7", "2"},
			}},
			// Text, through keysyms.
			{backend.Text{Text: "aé€\n\t"}, [][]string{
				{"key", "97", "1"}, {"key", "97", "0"},
				{"key", "233", "1"}, {"key", "233", "0"},
				{"key", "16785580", "1"}, {"key", "16785580", "0"},
				{"key", "65293", "1"}, {"key", "65293", "0"},
				{"key", "65289", "1"}, {"key", "65289", "0"},
			}},
			{backend.Resize{Width: 800, Height: 600}, [][]string{{"size", "800", "600"}}},
		} {
			if err := h.b.Input(tc.ev); err != nil {
				t.Fatalf("Input(%#v) = %v", tc.ev, err)
			}
			for _, w := range tc.want {
				h.expect(w...)
			}
		}

		// Small scrolls add up, horizontal ones and control characters
		// other than C0 send nothing, and neither does Demand.
		for _, ev := range []backend.InputEvent{
			backend.Wheel{DY: 20}, backend.Wheel{DX: 500}, backend.Wheel{DY: 1, Mode: backend.WheelLines},
			backend.Text{Text: "\x7f\u0085"},
		} {
			if err := h.b.Input(ev); err != nil {
				t.Fatalf("Input(%#v) = %v", ev, err)
			}
		}
		h.b.Demand(backend.Demand{Visible: false})
		if err := h.b.Input(backend.Wheel{DY: 15}); err != nil {
			t.Fatal(err)
		}
		h.expect("mouse", "0", "7", "18")
		h.expect("mouse", "0", "7", "2")

		for _, ev := range []backend.InputEvent{
			backend.TermInput{Data: []byte("ls\r")},
			backend.Resize{Width: 0, Height: 600},
		} {
			if err := h.b.Input(ev); err == nil {
				t.Errorf("Input(%#v): no error", ev)
			}
		}
		if err := h.b.Input(backend.TermInput{}); !errors.Is(err, bridge.ErrUnsupportedInput) {
			t.Errorf("Input(TermInput) = %v, want ErrUnsupportedInput", err)
		}
		h.end()
	})
}

func TestInputQueue(t *testing.T) {
	// Inputs before Run wait for the handshake, up to the queue's size.
	b := bridge.New(addr, config, bridge.Options{})
	var err error
	for range 1000 {
		if err = b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
			break
		}
	}
	if !errors.Is(err, bridge.ErrQueueFull) {
		t.Errorf("Input with a full queue = %v, want ErrQueueFull", err)
	}

	synctest.Test(t, func(t *testing.T) {
		cli, srv := net.Pipe()
		b := bridge.New(addr, config, bridge.Options{
			Dial: func(context.Context, string, string) (net.Conn, error) { return cli, nil },
		})
		if err := b.Input(backend.Key{Down: true, Keysym: 'q'}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- b.Run(ctx, &backendtest.Sink{}) }()
		conn := guacamole.NewInstructionIO(srv)
		defer conn.Close()
		for {
			ins, err := conn.Read()
			if err != nil {
				t.Fatal(err)
			}
			switch ins.Opcode() {
			case "select":
				conn.Write(guacamole.NewInstruction([]string{"args", "VERSION_1_5_0"}))
				continue
			case "connect":
				conn.Write(guacamole.NewInstruction([]string{"ready", "$conn"}))
				continue
			case "size", "audio", "video", "image":
				continue
			}
			if got, want := ins.String(), instruction("key", "113", "1"); got != want {
				t.Fatalf("after the handshake: %s, want %s", got, want)
			}
			break
		}
		cancel()
		<-done
	})
}

func TestKeepAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		t0 := time.Now() // the handshake's last write

		at := <-h.nops
		if d := at.Sub(t0); d != 5*time.Second {
			t.Errorf("first nop after %v, want 5s", d)
		}
		// Any write restarts the wait, and nops keep going while a prompt
		// holds the reader.
		time.Sleep(2 * time.Second)
		if err := h.b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
			t.Fatal(err)
		}
		h.expect("key", "97", "1")
		t1 := time.Now()
		if d := (<-h.nops).Sub(t1); d != 5*time.Second {
			t.Errorf("nop %v after a key, want 5s", d)
		}
		h.end()
	})
}

func TestPromptKeepsAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		answer := make(chan struct{})
		sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
			<-answer
			return backend.PromptReply{Answers: []string{"pw"}}, nil
		}}
		h := start(t, sink)
		h.send("required", "password")
		for range 4 {
			<-h.nops
		}
		close(answer)
		h.expect("argv", "1", "text/plain", "password")
		h.expect("blob", "1", base64.StdEncoding.EncodeToString([]byte("pw")))
		h.expect("end", "1")
		h.end()
	})
}

func TestUnsupported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, &backendtest.Sink{})
		h.send("size", "0", "4", "4")
		h.send("lfill", "14", "0", "-1")
		h.send("lfill", "14", "0", "-1")
		h.send("frobnicate", "1")
		h.send("audio", "4", "audio/L16")
		h.expect("ack", "4", "BAD TYPE", "783")
		h.send("file", "6", "text/plain", "a.txt")
		h.expect("ack", "6", "File transfer unsupported", "256")
		h.send("blob", "6", "aGk=")
		h.send("end", "6")
		h.send("argv", "8", "text/plain", "password")
		h.expect("ack", "8", "Receiving argument values unsupported", "256")
		h.send("img", "2", "14", "0", "image/gif", "0", "0")
		h.send("end", "2")
		h.send("img", "3", "14", "0", "image/png", "0", "0")
		h.send("blob", "3", base64.StdEncoding.EncodeToString([]byte("not a png")))
		h.send("end", "3")
		// Things guacd sends that need nothing.
		h.send("nop")
		h.send("set", "0", "multi-touch", "10")
		h.send("msg", "1", "u1", "bob")
		h.frame(1)

		want := map[string]int{"lfill": 2, "frobnicate": 1, "audio": 1, "file": 1, "img image/gif": 1, "end failed": 1}
		got := h.b.Unsupported()
		if len(got) != len(want) {
			t.Errorf("Unsupported = %v, want %v", got, want)
		}
		for op, n := range want {
			if got[op] != n {
				t.Errorf("Unsupported[%q] = %d, want %d", op, got[op], n)
			}
		}
		h.end()
		logs := h.logs.String()
		for op := range want {
			if op == "end failed" {
				continue
			}
			value := op
			if strings.Contains(op, " ") {
				value = strconv.Quote(op)
			}
			if n := strings.Count(logs, "opcode="+value+"\n"); n != 1 {
				t.Errorf("%q logged %d times, want once:\n%s", op, n, logs)
			}
		}
		if strings.Count(logs, "instruction failed") != 1 {
			t.Errorf("failed instruction not logged once:\n%s", logs)
		}
	})
}
