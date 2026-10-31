// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// vectors are the protocol's message vectors, which every client and
// server implementation checks itself against.
type vectors struct {
	Messages []struct {
		Name, From, Hex string
	}
	Invalid []struct {
		Name, From, Hex, Error string
		Limits                 *struct {
			MaxTextureSize int `json:"max_texture_size"`
		}
	}
}

func loadVectors(t *testing.T) (v vectors, msg func(name string) []byte) {
	t.Helper()
	b, err := os.ReadFile("../protocol/testdata/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	byName := map[string][]byte{}
	for _, m := range v.Messages {
		byName[m.Name] = mustHex(t, m.Hex)
	}
	return v, func(name string) []byte {
		t.Helper()
		b, ok := byName[name]
		if !ok {
			t.Fatalf("no vector %q", name)
		}
		return b
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// recvBytes returns the client's next message as it was sent.
func (sc *scripted) recvBytes() []byte {
	sc.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, err := sc.srv.ReadMessage(ctx)
	if err != nil {
		sc.t.Fatalf("no message from the client: %v", err)
	}
	return b
}

// The client's messages are the vectors' bytes.
func TestGoldenClientMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, vec := loadVectors(t)
		w := graphical
		sc := newScripted(t, w, client.Options{})
		ctx := context.Background()
		inputs := []struct {
			name string
			send func() (uint32, error)
		}{
			{"key down a", func() (uint32, error) { return sc.cl.Key(ctx, true, 'a', 4) }},
			{"key up left shift", func() (uint32, error) { return sc.cl.Key(ctx, false, 0xffe1, 225) }},
			{"text", func() (uint32, error) { return sc.cl.Text(ctx, "日本") }},
			{"term-input", func() (uint32, error) { return sc.cl.TermInput(ctx, []byte("ls\r")) }},
			{"pointer", func() (uint32, error) { return sc.cl.Pointer(ctx, 640, 360, 1) }},
			{"wheel by lines", func() (uint32, error) { return sc.cl.Wheel(ctx, 0, -3, protocol.WheelLines) }},
			{"wheel by pixels", func() (uint32, error) { return sc.cl.Wheel(ctx, 12, 40, protocol.WheelPixels) }},
			{"resize-request", func() (uint32, error) { return sc.cl.Resize(ctx, 1280, 720) }},
			{"clipboard from the client", func() (uint32, error) {
				return sc.cl.Clipboard(ctx, "text/plain;charset=utf-8", []byte("hello"))
			}},
		}
		for i, in := range inputs {
			seq, err := in.send()
			if err != nil || seq != uint32(i+1) { // #nosec G115 -- small
				t.Fatalf("%s: seq %d, %v", in.name, seq, err)
			}
			if got := sc.recvBytes(); !bytes.Equal(got, vec(in.name)) {
				t.Errorf("%s: % x, want % x", in.name, got, vec(in.name))
			}
		}
		others := []struct {
			name string
			send func() error
		}{
			{"view of the whole screen", func() error { return sc.cl.View(ctx, true, image.Rectangle{}) }},
			{"view of a viewport", func() error { return sc.cl.View(ctx, true, image.Rect(0, 200, 800, 800)) }},
			{"view hidden", func() error { return sc.cl.View(ctx, false, image.Rectangle{}) }},
			{"prompt-reply", func() error { return sc.cl.PromptReply(ctx, 1, []string{"hunter2"}) }},
			{"prompt-reply without answers", func() error { return sc.cl.PromptReply(ctx, 2, nil) }},
		}
		for _, o := range others {
			if err := o.send(); err != nil {
				t.Fatalf("%s: %v", o.name, err)
			}
			if got := sc.recvBytes(); !bytes.Equal(got, vec(o.name)) {
				t.Errorf("%s: % x, want % x", o.name, got, vec(o.name))
			}
		}

		// A ping is answered with its pong.
		sc.sendBytes(vec("ping"))
		if got := sc.recvBytes(); !bytes.Equal(got, vec("pong")) {
			t.Errorf("pong: % x, want % x", got, vec("pong"))
		}

		// A frame is acknowledged when the caller comes back for the
		// next event, having shown it, here 1250 µs after its frame-end
		// arrived, with that decode time.
		sc.send(&protocol.FrameEnd{FrameID: 1})
		sc.sync()
		time.Sleep(1000 * time.Microsecond)
		if f, ok := next(t, sc.cl, time.Second).(client.Frame); !ok || f.ID != 1 {
			t.Fatalf("no frame 1: %#v", f)
		}
		time.Sleep(250 * time.Microsecond) // drawing it
		if ev := next(t, sc.cl, time.Millisecond); ev != nil {
			t.Fatalf("event %#v", ev)
		}
		if got := sc.recvBytes(); !bytes.Equal(got, vec("frame-ack")) {
			t.Errorf("frame-ack: % x, want % x", got, vec("frame-ack"))
		}

		// Close says so last.
		sc.cl.Close()
		if got := sc.recvBytes(); !bytes.Equal(got, vec("close normal")) {
			t.Errorf("close: % x, want % x", got, vec("close normal"))
		}
	})
}

// The server's vectors reach the caller as events.
func TestGoldenServerMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, vec := loadVectors(t)
		sc, err := startScripted(t, client.Options{}, rawMessage(vec("welcome")))
		if err != nil {
			t.Fatal(err)
		}
		if sc.cl.ViewerID() != 1 || sc.cl.Role() != protocol.RoleOwner || sc.cl.Class() != protocol.ClassGraphical || sc.cl.SessionID() != "s-7f3a" {
			t.Errorf("welcome: viewer %d, role %v, class %v, session %q", sc.cl.ViewerID(), sc.cl.Role(), sc.cl.Class(), sc.cl.SessionID())
		}
		// The client takes the two codecs it offered of the five the
		// server agreed to: raw, and cached for its tile cache.
		if c := sc.cl.Capabilities().Codecs; !slices.Equal(c, []protocol.Codec{protocol.CodecRaw, protocol.CodecCached}) {
			t.Errorf("codecs %v", c)
		}
		if n := sc.cl.Capabilities().CacheSlots; n != 1024 {
			t.Errorf("cache slots %d", n)
		}
		take := func(names ...string) []client.Event {
			t.Helper()
			for _, n := range names {
				sc.sendBytes(vec(n))
			}
			sc.sync()
			return drain(t, sc.cl, time.Millisecond)
		}

		evs := take("resize", "tile raw", "frame-end")
		if len(evs) != 2 {
			t.Fatalf("events %#v", evs)
		}
		if r, ok := evs[0].(client.Resize); !ok || r.Width != 1920 || r.Height != 1080 {
			t.Errorf("resize %#v", evs[0])
		}
		if f, ok := evs[1].(client.Frame); !ok || f.ID != 1 || f.LastSeq != 5 || f.ServerTime != 16667*time.Microsecond {
			t.Errorf("frame %#v", evs[1])
		}
		payload := mustHex(t, "00254a6f94b9de03284d7297bce1062b")
		sc.cl.Framebuffer(func(img *image.RGBA) {
			if got := img.Pix[img.PixOffset(64, 128):][:8]; !bytes.Equal(got, payload[:8]) {
				t.Errorf("tile's first row % x, want % x", got, payload[:8])
			}
			if got := img.Pix[img.PixOffset(64, 129):][:8]; !bytes.Equal(got, payload[8:]) {
				t.Errorf("tile's second row % x, want % x", got, payload[8:])
			}
		})

		sc.send(&protocol.FrameEnd{FrameID: 2})
		sc.sendBytes(vec("copy-rect"))
		evs = take()
		sc.send(&protocol.FrameEnd{FrameID: 3})
		evs = append(evs, take()...)
		if len(evs) != 2 {
			t.Fatalf("events %#v", evs)
		}
		if f := evs[1].(client.Frame); len(f.Dirty) != 1 || f.Dirty[0] != image.Rect(0, 0, 800, 584) {
			t.Errorf("copy-rect's frame: %#v", f)
		}

		for _, c := range []struct {
			name string
			want func(client.Event) bool
		}{
			{"cursor", func(ev client.Event) bool {
				c, ok := ev.(client.Cursor)
				return ok && c.Hot == image.Pt(1, 0) && bytes.Equal(c.Image.Pix, payload)
			}},
			{"cursor hidden", func(ev client.Event) bool { c, ok := ev.(client.Cursor); return ok && c.Image == nil }},
			{"cursor-move", func(ev client.Event) bool { return ev == client.CursorMove{X: 100, Y: 200} }},
			{"clipboard from the server", func(ev client.Event) bool {
				c, ok := ev.(client.Clipboard)
				return ok && c.MIME == "text/plain;charset=utf-8" && string(c.Data) == "copied"
			}},
			{"bell", func(ev client.Event) bool { return ev == client.Bell{} }},
			{"title", func(ev client.Event) bool { return ev == client.Title{Title: "user@host: ~"} }},
			{"prompt", func(ev client.Event) bool {
				p, ok := ev.(client.Prompt)
				return ok && p.ID == 1 && p.Name == "SSH" && len(p.Fields) == 1 && p.Fields[0] == protocol.PromptField{Label: "Password: ", Secret: true}
			}},
			{"viewer-joined", func(ev client.Event) bool {
				return ev == client.ViewerJoined{ViewerID: 2, Role: protocol.RoleViewer, Name: "alice"}
			}},
			{"viewer-left", func(ev client.Event) bool { return ev == client.ViewerLeft{ViewerID: 2} }},
			{"pong", nil},
		} {
			evs := take(c.name)
			switch {
			case c.want == nil && len(evs) != 0:
				t.Errorf("%s: events %#v", c.name, evs)
			case c.want != nil && (len(evs) != 1 || !c.want(evs[0])):
				t.Errorf("%s: events %#v", c.name, evs)
			}
		}
	})
}

// A terminal's vectors pass through untouched.
func TestGoldenTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, vec := loadVectors(t)
		sc, err := startScripted(t, client.Options{}, rawMessage(vec("welcome to a read-only terminal")))
		if err != nil {
			t.Fatal(err)
		}
		if sc.cl.Role() != protocol.RoleReadOnly || sc.cl.Class() != protocol.ClassTerminal {
			t.Errorf("role %v, class %v", sc.cl.Role(), sc.cl.Class())
		}
		for _, n := range []string{"resize", "title", "term-snapshot", "term-data", "frame-end"} {
			sc.sendBytes(vec(n))
		}
		sc.sync()
		evs := drain(t, sc.cl, time.Millisecond)
		if len(evs) != 5 {
			t.Fatalf("events %#v", evs)
		}
		if evs[0] != (client.Title{Title: "user@host: ~"}) || evs[1] != (client.Resize{Width: 1920, Height: 1080}) {
			t.Errorf("events %#v", evs[:2])
		}
		if s, ok := evs[2].(client.TermSnapshot); !ok || !bytes.Equal(s.Data, mustHex(t, "1b5b481b5b324a6869")) {
			t.Errorf("snapshot %#v", evs[2])
		}
		if d, ok := evs[3].(client.TermData); !ok || !bytes.Equal(d.Data, mustHex(t, "1b5b323b31486869")) {
			t.Errorf("data %#v", evs[3])
		}
		if f, ok := evs[4].(client.Frame); !ok || f.ID != 1 || f.LastSeq != 5 || f.Dirty != nil {
			t.Errorf("frame %#v", evs[4])
		}
		if _, err := sc.cl.TermInput(context.Background(), []byte("x")); !errors.Is(err, client.ErrReadOnly) {
			t.Errorf("input from a read-only viewer: %v", err)
		}
	})
}

// Each invalid vector from the server ends the session with the close
// code of its error.
func TestGoldenInvalid(t *testing.T) {
	v, _ := loadVectors(t)
	errs := map[string]error{
		"truncated":     protocol.ErrTruncated,
		"trailing-data": protocol.ErrTrailingData,
		"invalid":       protocol.ErrInvalid,
		"invalid-utf8":  protocol.ErrInvalidUTF8,
		"unsupported":   protocol.ErrUnsupported,
		"too-large":     protocol.ErrTooLarge,
	}
	n := 0
	for _, c := range v.Invalid {
		if c.From == "client" {
			continue
		}
		n++
		t.Run(c.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				want, ok := errs[c.Error]
				if !ok {
					t.Fatalf("unknown error %q", c.Error)
				}
				var opts client.Options
				if c.Limits != nil && c.Limits.MaxTextureSize > 0 {
					opts.MaxTextureSize = uint16(c.Limits.MaxTextureSize) // #nosec G115 -- small
				}
				b := mustHex(t, c.Hex)
				code := protocol.CloseCodeFor(want)
				if protocol.Type(uint16(b[0])|uint16(b[1])<<8) == protocol.TypeWelcome {
					_, err := startScripted(t, opts, rawMessage(b))
					if !errors.Is(err, want) {
						t.Errorf("New returned %v, want %v", err, want)
					}
					return
				}
				sc := newScripted(t, graphical, opts)
				sc.send(&protocol.Resize{Width: 256, Height: 256})
				sc.sendBytes(b)
				evs := drain(t, sc.cl, time.Second)
				e, ok := last(evs).(client.End)
				if !ok || e.Code != code || !errors.Is(e.Err, want) {
					t.Errorf("last event %#v, want End %v with %v", last(evs), code, want)
				}
			})
		})
	}
	if n < 25 {
		t.Errorf("%d server vectors", n)
	}
}

// rawMessage sends bytes as they are.
type rawMessage []byte

func (m rawMessage) Type() protocol.Type                  { return 0 }
func (m rawMessage) Append(b []byte) []byte               { return append(b, m...) }
func (m rawMessage) Decode([]byte, protocol.Limits) error { return nil }
