// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

func TestNew(t *testing.T) {
	ok := Config{Addr: "host", TLS: rdp.TLS{InsecureSkipVerify: true}}
	b, err := New(ok)
	if err != nil {
		t.Fatal(err)
	}
	if b.addr != "host:3389" || b.cfg.Width != 1024 || b.cfg.Height != 768 ||
		b.cfg.Protocols != rdp.ProtocolHybrid|rdp.ProtocolHybridEx || b.cfg.TLS.ServerName != "host" ||
		b.dialTimeout != DefaultDialTimeout || b.Class() != backend.Graphical {
		t.Errorf("defaults: %s %d×%d protocols %#x server name %q timeout %v",
			b.addr, b.cfg.Width, b.cfg.Height, b.cfg.Protocols, b.cfg.TLS.ServerName, b.dialTimeout)
	}
	for _, tc := range []struct {
		mode string
		want uint32
	}{
		{SecurityNLA, rdp.ProtocolHybrid | rdp.ProtocolHybridEx},
		{SecurityNLAExt, rdp.ProtocolHybridEx},
		{SecurityTLS, rdp.ProtocolSSL},
		{SecurityAny, rdp.ProtocolSSL | rdp.ProtocolHybrid | rdp.ProtocolHybridEx},
	} {
		c := ok
		c.Security = tc.mode
		if b, err := New(c); err != nil || b.cfg.Protocols != tc.want {
			t.Errorf("security %q: %v, protocols %#x, want %#x", tc.mode, err, b.cfg.Protocols, tc.want)
		}
	}
	if b, _ := New(Config{Addr: "[::1]:3390", TLS: rdp.TLS{ServerName: "rdp.test"}}); b == nil || b.addr != "[::1]:3390" || b.cfg.TLS.ServerName != "rdp.test" {
		t.Errorf("an IPv6 address and a server name: %+v", b)
	}
	if b, _ := New(Config{Addr: "[::1]", TLS: rdp.TLS{InsecureSkipVerify: true}}); b == nil || b.addr != "[::1]:3389" || b.cfg.TLS.ServerName != "::1" {
		t.Errorf("an IPv6 address without a port: %+v", b)
	}
	if b.cfg.RoutingCookie {
		t.Error("the routing cookie is on by default")
	}
	cookie := ok
	cookie.RoutingCookie = true
	if b, err := New(cookie); err != nil || !b.cfg.RoutingCookie {
		t.Errorf("the routing cookie asked for: %v, %+v", err, b)
	}
	for name, cfg := range map[string]Config{
		"no address":       {},
		"no host":          {Addr: ":3389"},
		"negative timeout": {Addr: "host", DialTimeout: -1},
		"too narrow":       {Addr: "host", Width: 199, Height: 600},
		"too wide":         {Addr: "host", Width: 8193, Height: 600},
		"too large":        {Addr: "host", Width: 8192, Height: 8192},
		"colour depth":     {Addr: "host", ColorDepth: 8},
		"standard RDP":     {Addr: "host", Security: "rdp"},
		"pins and roots":   {Addr: "host", TLS: rdp.TLS{Pins: []string{"zz"}}},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The backend connects with the TLS policy, takes the desktop the server
// chose, and draws its bitmaps; cancelling ends Run with no goroutine
// left.
func TestRunDraws(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{Width: 640, Height: 480})
	cfg := s.config(t)
	cfg.Width, cfg.Height = 800, 600
	sink := &backendtest.Sink{}
	r := start(t, newBackend(t, cfg), sink)
	c := s.accept(t)
	if c.Size != image.Pt(800, 600) || c.Info.Username != "occamy" || c.Info.Password != "s3cret" || !c.Info.AutoLogon {
		t.Errorf("the client asked for %v as %+v", c.Size, c.Info)
	}
	waitFor(t, "the resize", func() bool { return sink.Size() == image.Pt(640, 480) })

	red := color.RGBA{0xff, 0, 0, 0xff}
	blue := color.RGBA{0, 0, 0xff, 0xff}
	if err := c.Write(
		rdptest.FastPath(rdptest.Update(rdptest.FPBitmap, rdptest.BitmapUpdate(rdptest.RawBitmap(image.Pt(10, 20), solid(64, 64, red))))),
		rdptest.SlowPathUpdate(rdptest.BitmapUpdate(rdptest.RawBitmap(image.Pt(600, 400), solid(40, 80, blue)))),
	); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the bitmaps", func() bool { return sinkAt(sink, image.Pt(639, 479)) == blue })
	for p, want := range map[image.Point]color.RGBA{
		{10, 20}: red, {73, 83}: red, {74, 83}: {A: 0xff}, {9, 20}: {A: 0xff}, {600, 400}: blue,
	} {
		if got := sinkAt(sink, p); got != want {
			t.Errorf("pixel %v is %v, want %v", p, got, want)
		}
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// A large bitmap, in an update fragmented over PDUs, is drawn in strips
// of at most 64 Ki pixels, each under a lock of its own.
func TestRunDrawsInStrips(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{Width: 1024, Height: 768})
	sink := &backendtest.Sink{}
	start(t, newBackend(t, s.config(t)), sink)
	c := s.accept(t)
	waitFor(t, "the resize", func() bool { return sink.Size() == image.Pt(1024, 768) })
	sink.TakeDamage()

	orange := color.RGBA{0xff, 0x80, 0x10, 0xff}
	update := rdptest.BitmapUpdate(rdptest.FillBitmap(image.Rect(0, 0, 1000, 300), orange))
	if err := c.Write(rdptest.Fragments(rdptest.FPBitmap, update, 8)...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the bitmap", func() bool { return sinkAt(sink, image.Pt(999, 299)) == orange })
	if got := sinkAt(sink, image.Pt(0, 0)); got != orange {
		t.Errorf("pixel (0, 0) is %v, want %v", got, orange)
	}
	damage := sink.TakeDamage()
	area := 0
	for _, d := range damage {
		if n := d.Dx() * d.Dy(); n > drawStripPixels {
			t.Errorf("a strip of %v, %d pixels", d, n)
		}
		area += d.Dx() * d.Dy()
	}
	if area != 1000*300 || len(damage) != (300+64)/65 {
		t.Errorf("%d strips of %d pixels in all: %v", len(damage), area, damage)
	}
}

// drawStripPixels is internal/rdp's strip: 64 Ki pixels.
const drawStripPixels = 64 << 10

// Pointer shapes, the cache, the hidden and default pointers and moves
// reach the session's cursor.
func TestRunPointer(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	sink := &backendtest.Sink{}
	start(t, newBackend(t, s.config(t)), sink)
	c := s.accept(t)
	waitFor(t, "the resize", func() bool { return sink.Size() != image.Point{} })

	shape := solid(8, 6, color.RGBA{0x10, 0x20, 0x30, 0xff})
	shape.SetRGBA(7, 5, color.RGBA{})
	hasShape := func() bool {
		img, hot := sink.CursorImage()
		return img != nil && img.Rect.Size() == image.Pt(8, 6) && hot == image.Pt(2, 3) &&
			img.RGBAAt(0, 0) == shape.RGBAAt(0, 0) && img.RGBAAt(7, 5) == color.RGBA{}
	}
	steps := []struct {
		name   string
		update []byte
		check  func() bool
	}{
		{"shape", rdptest.Update(rdptest.FPPointer, rdptest.Pointer(3, image.Pt(2, 3), shape)), hasShape},
		{"hidden", rdptest.Update(rdptest.FPPtrNull, nil), func() bool { img, _ := sink.CursorImage(); return img == nil }},
		{"cached", rdptest.Update(rdptest.FPCachedPtr, rdptest.CachedPointer(3)), hasShape},
		{"default", rdptest.Update(rdptest.FPPtrDefault, nil), func() bool {
			img, _ := sink.CursorImage()
			return img != nil && img.Rect.Size() != image.Pt(8, 6)
		}},
		{"moved", rdptest.Update(rdptest.FPPtrPosition, rdptest.PointerPosition(image.Pt(33, 44))), func() bool { return sink.CursorPos() == image.Pt(33, 44) }},
	}
	for _, step := range steps {
		if err := c.Write(rdptest.FastPath(step.update)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, step.name, step.check)
	}
}

// The xrdp recordings of the client's tests, played back after the fake
// server's connection sequence, draw the scene's wallpaper through the
// backend.
func TestRunReplaysXRDP(t *testing.T) {
	for _, tc := range []struct {
		bpp int
		tol int
	}{
		{32, 0},
		{16, 8},
	} {
		t.Run(fmt.Sprintf("%dbpp", tc.bpp), func(t *testing.T) {
			checkGoroutines(t)
			frames := loadTrace(t, fmt.Sprintf("../../rdp/testdata/xrdp-%dbpp.trace.gz", tc.bpp))
			s := listen(t, rdptest.Server{Width: 1024, Height: 768})
			cfg := s.config(t)
			cfg.ColorDepth = tc.bpp
			sink := &backendtest.Sink{}
			start(t, newBackend(t, cfg), sink)
			c := s.accept(t)
			// The recordings' updates follow the connection sequence and
			// the four PDUs of the finalization.
			if err := c.Write(frames[11:]...); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the wallpaper", func() bool {
				bad, _ := matchesWallpaper(sink.Pixels(), tc.tol)
				return bad == 0
			})
		})
	}
}

// Keys, text, the pointer and the wheel reach the server as RDP input
// events, by fast-path or by slow-path as the server allows.
func TestRunInput(t *testing.T) {
	for _, fastPath := range []bool{true, false} {
		name := "slow-path"
		flags := rdptest.InputScancodes | rdptest.InputMouseX | rdptest.InputUnicode
		if fastPath {
			name, flags = "fast-path", rdptest.DefaultInputFlags
		}
		t.Run(name, func(t *testing.T) {
			checkGoroutines(t)
			s := listen(t, rdptest.Server{InputFlags: flags})
			sink := &backendtest.Sink{}
			b := newBackend(t, s.config(t))
			b.Demand(backend.Demand{Visible: true})
			start(t, b, sink)
			c := s.accept(t)
			for _, tc := range []struct {
				name string
				in   []backend.InputEvent
				want []rdptest.Event
			}{
				{"key by code", []backend.InputEvent{backend.Key{Down: true, Keysym: 'a', Code: 0x04}, backend.Key{Keysym: 'a', Code: 0x04}},
					[]rdptest.Event{{Kind: rdptest.Scancode, Code: 0x1e}, {Kind: rdptest.Scancode, Code: 0x1e, Release: true}}},
				{"extended key", []backend.InputEvent{backend.Key{Down: true, Keysym: 0xff52, Code: 0x52}},
					[]rdptest.Event{{Kind: rdptest.Scancode, Code: 0x48, Extended: true}}},
				{"a function key by keysym", []backend.InputEvent{backend.Key{Down: true, Keysym: 0xffbe}},
					[]rdptest.Event{{Kind: rdptest.Scancode, Code: 0x3b}}},
				{"a character by keysym", []backend.InputEvent{backend.Key{Down: true, Keysym: 0xe9}},
					[]rdptest.Event{{Kind: rdptest.Unicode, Code: 0xe9}}},
				{"Pause", []backend.InputEvent{backend.Key{Down: true, Keysym: 0xff13, Code: 0x48}, backend.Key{Keysym: 0xff13, Code: 0x48}},
					[]rdptest.Event{
						{Kind: rdptest.Scancode, Code: 0x1d, Extended1: true}, {Kind: rdptest.Scancode, Code: 0x45},
						{Kind: rdptest.Scancode, Code: 0x1d, Extended1: true, Release: true}, {Kind: rdptest.Scancode, Code: 0x45, Release: true},
					}},
				{"text", []backend.InputEvent{backend.Text{Text: "é\n"}},
					[]rdptest.Event{
						{Kind: rdptest.Unicode, Code: 0xe9}, {Kind: rdptest.Unicode, Code: 0xe9, Release: true},
						{Kind: rdptest.Scancode, Code: 0x1c}, {Kind: rdptest.Scancode, Code: 0x1c, Release: true},
					}},
				{"pointer", []backend.InputEvent{
					backend.Pointer{X: 10, Y: 20},
					backend.Pointer{X: 11, Y: 21, Buttons: backend.ButtonLeft | backend.ButtonBack},
					backend.Pointer{X: 11, Y: 21, Buttons: backend.ButtonBack},
				}, []rdptest.Event{
					{Kind: rdptest.Mouse, Flags: 0x0800, X: 10, Y: 20},
					{Kind: rdptest.Mouse, Flags: 0x9000, X: 11, Y: 21},
					{Kind: rdptest.MouseX, Flags: 0x8001, X: 11, Y: 21},
					{Kind: rdptest.Mouse, Flags: 0x1000, X: 11, Y: 21},
				}},
				{"wheel", []backend.InputEvent{backend.Wheel{DY: 3, Mode: backend.WheelLines}, backend.Wheel{DX: -53}},
					[]rdptest.Event{
						{Kind: rdptest.Mouse, Flags: 0x0200 | 0x0100 | 0x88, X: 11, Y: 21},
						{Kind: rdptest.Mouse, Flags: 0x0400 | 0x0100 | 0x88, X: 11, Y: 21},
					}},
				{"pointer clamped", []backend.InputEvent{backend.Pointer{X: 5000, Y: -7, Buttons: backend.ButtonBack}},
					[]rdptest.Event{{Kind: rdptest.Mouse, Flags: 0x0800, X: 1023, Y: 0}}},
			} {
				for _, ev := range tc.in {
					if err := b.Input(ev); err != nil {
						t.Fatalf("%s: %v", tc.name, err)
					}
				}
				got := events(t, c, len(tc.want))
				for i := range got {
					if got[i] != tc.want[i] {
						t.Errorf("%s: event %d is %+v, want %+v", tc.name, i, got[i], tc.want[i])
					}
				}
			}
			quietFor(t, c)
		})
	}
}

// Without Unicode keyboard events, text and characters go out as the
// keys of a US layout, with Shift where needed, and characters off it
// are dropped; without the extended mouse, the back and forward buttons
// are.
func TestRunInputWithoutUnicode(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{InputFlags: rdptest.InputScancodes | rdptest.InputFastPath})
	b := newBackend(t, s.config(t))
	b.Demand(backend.Demand{Visible: true})
	start(t, b, &backendtest.Sink{})
	c := s.accept(t)
	for _, ev := range []backend.InputEvent{
		backend.Text{Text: "a€B"},
		backend.Key{Down: true, Keysym: 0xe9},
		backend.Key{Down: true, Keysym: 'q'},
		backend.Pointer{X: 1, Y: 1, Buttons: backend.ButtonForward},
	} {
		if err := b.Input(ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []rdptest.Event{
		{Kind: rdptest.Scancode, Code: 0x1e}, {Kind: rdptest.Scancode, Code: 0x1e, Release: true},
		{Kind: rdptest.Scancode, Code: 0x2a}, {Kind: rdptest.Scancode, Code: 0x30}, {Kind: rdptest.Scancode, Code: 0x30, Release: true}, {Kind: rdptest.Scancode, Code: 0x2a, Release: true},
		{Kind: rdptest.Scancode, Code: 0x10},
		{Kind: rdptest.Mouse, Flags: 0x0800, X: 1, Y: 1},
	}
	got := events(t, c, len(want))
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("event %d is %+v, want %+v", i, got[i], want[i])
		}
	}
	quietFor(t, c)
}

// Input that comes while the backend connects is queued, and written
// once the connection is up, in PDUs of 255 events.
func TestRunInputBatched(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	cfg := s.config(t)
	cfg.Security, cfg.Password = SecurityAny, ""
	b := newBackend(t, cfg)
	b.Demand(backend.Demand{Visible: true})
	asked := make(chan struct{})
	answer := make(chan struct{})
	sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		close(asked)
		<-answer
		return backend.PromptReply{Answers: []string{"pw"}}, nil
	}}
	start(t, b, sink)
	<-asked
	text := strings.Repeat("x", 150) // 300 events
	if err := b.Input(backend.Text{Text: text}); err != nil {
		t.Fatal(err)
	}
	if err := b.Input(backend.Text{Text: text}); err != nil {
		t.Fatal(err)
	}
	close(answer)
	c := s.accept(t)
	if c.Info.Password != "pw" {
		t.Errorf("password %q, want the answer", c.Info.Password)
	}
	var sizes []int
	for n := 0; n < 600; {
		m := next(t, c)
		if len(m.Events) > 0 {
			sizes = append(sizes, len(m.Events))
			n += len(m.Events)
		}
	}
	if len(sizes) != 3 || sizes[0] != 255 || sizes[1] != 255 || sizes[2] != 90 {
		t.Errorf("PDUs of %v events, want 255, 255 and 90", sizes)
	}
}

// The queue's budget of events is returned as the writer takes inputs,
// so that a session can type more than the budget over its life.
func TestRunInputBudgetReturned(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	b := newBackend(t, s.config(t))
	b.Demand(backend.Demand{Visible: true})
	start(t, b, &backendtest.Sink{})
	c := s.accept(t)
	text := backend.Text{Text: strings.Repeat("x", inputEvents/4/8)} // an eighth of the budget
	for i := range 20 {
		if err := b.Input(text); err != nil {
			t.Fatalf("Text %d: %v", i, err)
		}
		// Each character is pressed and released.
		events(t, c, 2*len(text.Text))
	}
}

// While nobody can see, the server is told to suppress its output, and
// told to resume when a viewer can see again.
func TestRunDemand(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	b := newBackend(t, s.config(t))
	start(t, b, &backendtest.Sink{})
	c := s.accept(t)
	suppress := func(want bool) {
		t.Helper()
		m := next(t, c)
		allow, err := rdptest.SuppressOutput(m.Body)
		if m.Type2 != rdptest.PDU2SuppressOutput || err != nil || allow == want {
			t.Fatalf("got PDU %#x (allow %v, %v), want Suppress Output suppressing %v", m.Type2, allow, err, want)
		}
	}
	suppress(true) // nobody could see from the start
	b.Demand(backend.Demand{Visible: true})
	suppress(false)
	b.Demand(backend.Demand{Visible: true})
	quietFor(t, c)
	b.Demand(backend.Demand{Visible: false})
	suppress(true)
}

// NLA needs a username and a password: the owner is asked for those the
// configuration lacks before the connection, and the answers go to the
// server, the username in the routing cookie when it is asked for and
// the credentials to CredSSP.
func TestRunPromptsForNLA(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{Protocol: rdptest.ProtocolHybrid})
	cfg := s.config(t)
	cfg.Security, cfg.Username, cfg.Password = SecurityNLA, "", ""
	cfg.RoutingCookie = true
	var asked backend.Prompt
	sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		asked = p
		return backend.PromptReply{Answers: []string{"alice", "CORP", "pw"}}, nil
	}}
	r := start(t, newBackend(t, cfg), sink)
	a := s.nextOf(t, r)
	if !errors.Is(a.err, rdptest.ErrCredSSP) {
		t.Fatalf("the server got %v, want CredSSP", a.err)
	}
	a.c.Close()
	if a.c.Cookie != "alice" || a.c.Requested != rdptest.ProtocolHybrid|rdptest.ProtocolHybridEx ||
		!bytes.Contains(a.c.TSRequest, []byte("NTLMSSP\x00")) {
		t.Errorf("cookie %q, protocols %#x, TSRequest % x", a.c.Cookie, a.c.Requested, a.c.TSRequest)
	}
	want := []backend.PromptField{{Label: "Username"}, {Label: "Domain"}, {Label: "Password", Secret: true}}
	if len(asked.Fields) != 3 || asked.Fields[0] != want[0] || asked.Fields[1] != want[1] || asked.Fields[2] != want[2] ||
		!strings.Contains(asked.Instruction, "username, domain and password") {
		t.Errorf("prompt %+v", asked)
	}
	if err := r.wait(t); err == nil {
		t.Error("Run returned nil after the server hung up in CredSSP")
	}
}

// Without the routing cookie asked for, the connection request names
// no user: nothing about the user goes before TLS.
func TestRunNoRoutingCookie(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	start(t, newBackend(t, s.config(t)), &backendtest.Sink{})
	if c := s.accept(t); c.Cookie != "" || c.Info.Username != "occamy" {
		t.Errorf("cookie %q, user %q: want no cookie, the user in the Client Info PDU", c.Cookie, c.Info.Username)
	}
}

// A server that refuses the credentials in CredSSP fails Run with
// ErrAuthFailed.
func TestRunAuthFailed(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{Protocol: rdptest.ProtocolHybrid})
	cfg := s.config(t)
	cfg.Security = SecurityNLA
	r := start(t, newBackend(t, cfg), &backendtest.Sink{})
	a := s.nextOf(t, r)
	if !errors.Is(a.err, rdptest.ErrCredSSP) {
		t.Fatalf("the server got %v, want CredSSP", a.err)
	}
	defer a.c.Close()
	if err := a.c.Write(rdptest.TSRequestError(0xc000006d)); err != nil {
		t.Fatal(err)
	}
	err := r.wait(t)
	var ce *rdp.CredSSPError
	if !errors.Is(err, ErrAuthFailed) || !errors.As(err, &ce) || ce.Code != 0xc000006d {
		t.Errorf("Run returned %v, want ErrAuthFailed with the server's status", err)
	}
}

// Cancelling while the server stalls in CredSSP ends Run at once.
func TestRunCancelDuringNLA(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{Protocol: rdptest.ProtocolHybrid})
	cfg := s.config(t)
	cfg.Security = SecurityNLA
	r := start(t, newBackend(t, cfg), &backendtest.Sink{})
	a := s.nextOf(t, r)
	if !errors.Is(a.err, rdptest.ErrCredSSP) {
		t.Fatalf("the server got %v, want CredSSP", a.err)
	}
	defer a.c.Close()
	time.Sleep(silence)
	begin := time.Now()
	if err := r.stop(t); !errors.Is(err, context.Canceled) || time.Since(begin) > time.Second {
		t.Errorf("Run returned %v after %v", err, time.Since(begin))
	}
}

// With "any", a man in the middle who strips NLA from the offer gets a
// server that selects TLS and repeats the stripped offer: the backend
// refuses before the Client Info PDU, which carries the password.
func TestRunAnyDowngraded(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	cfg := s.config(t)
	cfg.Security = SecurityAny
	cfg.Addr = stripNLA(t, s.ln.Addr().String())
	r := start(t, newBackend(t, cfg), &backendtest.Sink{})
	a := s.next(t)
	if a.err == nil {
		t.Errorf("the server got the Client Info PDU: %+v", a.c.Info)
	}
	if err := r.wait(t); err == nil || !strings.Contains(err.Error(), "was offered security protocols") {
		t.Errorf("Run returned %v, want the downgrade refused", err)
	}
}

// By default, a server that selects TLS without NLA is refused before
// anything but the connection request is sent.
func TestRunDefaultRefusesTLS(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{}) // selects TLS
	cfg := s.config(t)
	cfg.Security = ""
	r := start(t, newBackend(t, cfg), &backendtest.Sink{})
	a := s.next(t)
	if err := r.wait(t); a.err == nil || err == nil || !strings.Contains(err.Error(), "did not offer") {
		t.Errorf("the server got %v, Run returned %v, want TLS refused", a.err, err)
	}
}

// stripNLA is a man in the middle in front of upstream: it rewrites the
// protocols the client offers in its connection request to TLS alone,
// and relays the rest untouched. It returns its address.
func stripNLA(t *testing.T, upstream string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	wg.Go(func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		u, err := net.Dial("tcp", upstream)
		if err != nil {
			return
		}
		defer u.Close()
		var h [4]byte
		if _, err := io.ReadFull(c, h[:]); err != nil {
			return
		}
		p := make([]byte, int(binary.BigEndian.Uint16(h[2:]))-4)
		if _, err := io.ReadFull(c, p); err != nil {
			return
		}
		binary.LittleEndian.PutUint32(p[len(p)-4:], rdp.ProtocolSSL) // the RDP_NEG_REQ ends the request
		if _, err := u.Write(append(h[:], p...)); err != nil {
			return
		}
		wg.Go(func() {
			io.Copy(u, c)
			u.Close()
		})
		io.Copy(c, u)
	})
	return ln.Addr().String()
}

// Without NLA nothing is asked: the server shows its logon screen.
// With a domain configured, the domain is not asked for either.
func TestRunPromptsOnlyForNLA(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	cfg := s.config(t)
	cfg.Username, cfg.Password = "", ""
	sink := &backendtest.Sink{}
	start(t, newBackend(t, cfg), sink)
	c := s.accept(t)
	if len(sink.Prompts()) != 0 || c.Info.AutoLogon || c.Info.Username != "" {
		t.Errorf("TLS: prompts %v, info %+v", sink.Prompts(), c.Info)
	}

	cfg.Security, cfg.Domain = SecurityAny, "CORP"
	sink = &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: []string{"bob", "pw"}}, nil
	}}
	start(t, newBackend(t, cfg), sink)
	c = s.accept(t)
	if p := sink.Prompts(); len(p) != 1 || len(p[0].Fields) != 2 || p[0].Fields[0].Label != "Username" {
		t.Errorf("any: prompts %+v", p)
	}
	if c.Info != (rdptest.Credentials{Domain: "CORP", Username: "bob", Password: "pw", AutoLogon: true}) {
		t.Errorf("any: info %+v", c.Info)
	}
}

func TestRunPromptFails(t *testing.T) {
	checkGoroutines(t)
	failed := errors.New("no")
	for name, tc := range map[string]struct {
		answer func(backend.Prompt) (backend.PromptReply, error)
		want   error
	}{
		"refused": {func(backend.Prompt) (backend.PromptReply, error) { return backend.PromptReply{}, failed }, failed},
		"answers short": {func(backend.Prompt) (backend.PromptReply, error) {
			return backend.PromptReply{Answers: []string{"x"}}, nil
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s := listen(t, rdptest.Server{})
			cfg := s.config(t)
			cfg.Security, cfg.Password = SecurityNLA, ""
			cfg.Username = ""
			r := start(t, newBackend(t, cfg), &backendtest.Sink{Answer: tc.answer})
			err := r.wait(t)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "prompt") {
				t.Errorf("Run returned %v", err)
			}
		})
	}
	// Cancelling while the owner is asked ends Run with the context's
	// error.
	s := listen(t, rdptest.Server{})
	cfg := s.config(t)
	cfg.Security, cfg.Password = SecurityNLA, ""
	asked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- newBackend(t, cfg).Run(ctx, &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
			close(asked)
			<-ctx.Done()
			return backend.PromptReply{}, ctx.Err()
		}})
	}()
	<-asked
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// How the server ends the session decides Run's result: nil when it
// ends on purpose, an error when it fails or hangs up without a reason.
// A server may hang up with a reset rather than the end of the stream.
func TestRunEnd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]byte
		reset  bool
		ok     bool
		want   error
	}{
		{"no reason", [][]byte{rdptest.DisconnectUltimatum()}, false, true, nil},
		{"user logged off", [][]byte{rdptest.SetErrorInfo(0xc), rdptest.DisconnectUltimatum()}, false, true, nil},
		{"administrator disconnected", [][]byte{rdptest.SetErrorInfo(0x1), rdptest.DisconnectUltimatum()}, false, true, nil},
		{"replaced", [][]byte{rdptest.SetErrorInfo(0x5), rdptest.DisconnectUltimatum()}, false, true, nil},
		{"logged off, then hung up", [][]byte{rdptest.SetErrorInfo(0x2)}, false, true, nil},
		{"out of memory", [][]byte{rdptest.SetErrorInfo(0x6), rdptest.DisconnectUltimatum()}, false, false, rdp.ErrorInfo(0x6)},
		{"insufficient privileges", [][]byte{rdptest.SetErrorInfo(0x9), rdptest.DisconnectUltimatum()}, false, false, rdp.ErrorInfo(0x9)},
		{"deactivated, then the ultimatum", [][]byte{rdptest.SetErrorInfo(0x9), rdptest.DeactivateAll(), rdptest.DisconnectUltimatum()}, false, false, rdp.ErrorInfo(0x9)},
		{"insufficient privileges, then hung up", [][]byte{rdptest.SetErrorInfo(0x9)}, false, false, rdp.ErrorInfo(0x9)},
		{"hung up", nil, false, false, ErrConnectionClosed},
		{"reset", nil, true, false, ErrConnectionClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := listen(t, rdptest.Server{})
			b := newBackend(t, s.config(t))
			// Visible from the start, the backend sends nothing after the
			// connection sequence, so that closing the connection ends
			// the stream rather than resetting it.
			b.Demand(backend.Demand{Visible: true})
			r := start(t, b, &backendtest.Sink{})
			c := s.accept(t)
			if err := c.Write(tc.frames...); err != nil {
				t.Fatal(err)
			}
			if tc.reset {
				c.Reset()
			} else {
				c.Close()
			}
			err := r.wait(t)
			if tc.ok != (err == nil) || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("Run returned %v, want %v", err, tc.want)
			}
		})
	}
}

// A reason followed by a reset is the end the reason gives: a server's
// kernel resets the connection when the server closes with input
// unread.
func TestRunReasonThenReset(t *testing.T) {
	checkGoroutines(t)
	for i := range 10 {
		s := listen(t, rdptest.Server{})
		b := newBackend(t, s.config(t))
		b.Demand(backend.Demand{Visible: true})
		r := start(t, b, &backendtest.Sink{})
		c := s.accept(t)
		if err := c.Write(rdptest.SetErrorInfo(0x2)); err != nil {
			t.Fatal(err)
		}
		// Let the reason leave the server's kernel, whose reset drops
		// what it has not sent.
		time.Sleep(10 * time.Millisecond)
		c.Reset()
		if err := r.wait(t); err != nil {
			t.Errorf("run %d: Run returned %v, want nil for an administrator's logoff", i, err)
		}
	}
}

// A reason that the session went on after is not the end of a later
// hang-up.
func TestRunEndAfterStaleReason(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	b := newBackend(t, s.config(t))
	b.Demand(backend.Demand{Visible: true})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	c := s.accept(t)
	red := color.RGBA{0xff, 0, 0, 0xff}
	if err := c.Write(rdptest.SetErrorInfo(0x1),
		rdptest.FastPath(rdptest.Update(rdptest.FPBitmap, rdptest.BitmapUpdate(rdptest.RawBitmap(image.Point{}, solid(64, 64, red)))))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the bitmap after the reason", func() bool { return sinkAt(sink, image.Pt(1, 1)) == red })
	c.Reset()
	if err := r.wait(t); !errors.Is(err, ErrConnectionClosed) {
		t.Errorf("Run returned %v, want ErrConnectionClosed", err)
	}
}

// A certificate that is not pinned is refused, with its fingerprint.
func TestRunCertificate(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	cfg := s.config(t)
	cfg.TLS.Pins = []string{strings.Repeat("00", 32)}
	r := start(t, newBackend(t, cfg), &backendtest.Sink{})
	var ce *rdp.CertificateError
	if err := r.wait(t); !errors.As(err, &ce) {
		t.Errorf("Run returned %v, want a certificate error", err)
	}
}

// Cancelling while the server has not answered ends Run at once.
func TestRunCancelWhileConnecting(t *testing.T) {
	checkGoroutines(t)
	b := newBackend(t, Config{Addr: "rdp.test", TLS: rdp.TLS{InsecureSkipVerify: true}, Security: SecurityTLS, Logger: quiet})
	stalled := make(chan struct{})
	b.dial = func(ctx context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			<-stalled
			server.Close()
		}()
		return client, nil
	}
	r := start(t, b, &backendtest.Sink{})
	time.Sleep(silence)
	begin := time.Now()
	if err := r.stop(t); !errors.Is(err, context.Canceled) || time.Since(begin) > time.Second {
		t.Errorf("Run returned %v after %v", err, time.Since(begin))
	}
	close(stalled)
}

func TestInputErrors(t *testing.T) {
	checkGoroutines(t)
	b := newBackend(t, Config{Addr: "rdp.test", TLS: rdp.TLS{InsecureSkipVerify: true}, Logger: quiet})
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Input before Run: %v", err)
	}
	// Hold Run in its prompt, with the queue up.
	asked := make(chan struct{})
	release := make(chan struct{})
	sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		close(asked)
		<-release
		return backend.PromptReply{}, errors.New("no")
	}}
	b.cfg.Password = ""
	r := start(t, b, sink)
	<-asked
	if err := b.Input(backend.TermInput{Data: []byte("ls")}); !errors.Is(err, ErrUnsupportedInput) {
		t.Errorf("TermInput: %v", err)
	}
	for _, ev := range []backend.InputEvent{backend.Clipboard{MIME: "image/png", Data: []byte("x")}, backend.Resize{Width: 800, Height: 600}, backend.Text{}} {
		if err := b.Input(ev); err != nil {
			t.Errorf("%T: %v", ev, err)
		}
	}
	if err := b.Input(backend.Text{Text: strings.Repeat("x", inputEvents/4+1)}); !errors.Is(err, ErrInputFull) {
		t.Errorf("a Text over the budget: %v", err)
	}
	// Clipboard text is queued, charged by its bytes, of which the queue
	// holds two clipboards of the most text; beyond the most, text is
	// cut.
	big := backend.Clipboard{MIME: "text/plain", Data: bytes.Repeat([]byte("x"), rdp.MaxClipboard+1)}
	for i := range 3 {
		if err := b.Input(big); (err == nil) != (i < 2) || err != nil && !errors.Is(err, ErrInputFull) {
			t.Errorf("clipboard %d of %d bytes: %v", i, len(big.Data), err)
		}
	}
	n := 2
	for ; n < 1000; n++ {
		if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
			if !errors.Is(err, ErrInputFull) {
				t.Fatal(err)
			}
			break
		}
	}
	if n != inputQueue {
		t.Errorf("the queue took %d inputs, want %d", n, inputQueue)
	}
	close(release)
	r.wait(t)
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Input after Run: %v", err)
	}
}

func TestRunTwice(t *testing.T) {
	checkGoroutines(t)
	b := newBackend(t, Config{Addr: "rdp.test", TLS: rdp.TLS{InsecureSkipVerify: true}, Logger: quiet})
	asked := make(chan struct{})
	release := make(chan struct{})
	b.cfg.Password = ""
	r := start(t, b, &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		close(asked)
		<-release
		return backend.PromptReply{}, errors.New("no")
	}})
	<-asked
	if err := b.Run(context.Background(), &backendtest.Sink{}); err == nil {
		t.Error("a second Run while running returned nil")
	}
	close(release)
	r.wait(t)
}
