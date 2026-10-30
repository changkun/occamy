// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build integration

package vnc

// TestBenchDesktop runs the backend against x11vnc on the bench desktop
// (docker/bench/desktop), whose scene program draws the same pixels in
// every run. Start one on a port of your choice and point the test at
// it:
//
//	podman run -d --rm --name occamy-vnc-test -p 127.0.0.1:15900:5900 occamy-bench-desktop vnc
//	OCCAMY_VNC_ADDR=127.0.0.1:15900 go test -tags integration -run TestBenchDesktop ./internal/backend/vnc
//
// OCCAMY_VNC_PASSWORD defaults to the image's, vncpassword.

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// The scene's keys and swatch (bench/internal/probe, which is internal to
// bench/): a letter fills the swatch with a colour of the palette.
const (
	keyEscape = 0xff1b
	keyF5     = 0xffc2 // full-screen motion
)

var (
	swatchCentre  = image.Pt(32, 32)
	swatchPalette = []color.RGBA{
		{0xff, 0x00, 0x00, 0xff}, {0x00, 0xff, 0x00, 0xff}, {0x00, 0x00, 0xff, 0xff}, {0xff, 0xff, 0x00, 0xff},
		{0x00, 0xff, 0xff, 0xff}, {0xff, 0x00, 0xff, 0xff}, {0xff, 0x80, 0x00, 0xff}, {0x80, 0x00, 0xff, 0xff},
	}
)

func TestBenchDesktop(t *testing.T) {
	addr := os.Getenv("OCCAMY_VNC_ADDR")
	if addr == "" {
		t.Skip("OCCAMY_VNC_ADDR is not set")
	}
	pw := os.Getenv("OCCAMY_VNC_PASSWORD")
	if pw == "" {
		pw = "vncpassword"
	}
	checkGoroutines(t)
	ref := referenceFrame(t, addr, pw)
	t.Logf("reference frame %v", ref.Rect)

	b := newBackend(t, Config{Addr: addr, Password: pw})
	b.Demand(backend.Demand{Visible: true})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)

	// The idle screen, decoded from the backend's pixel format, matches
	// the reference decoded from x11vnc's own.
	waitFor(t, "the idle screen", func() bool { return same(sink.Pixels(), ref) })
	if img, hot := sink.CursorImage(); img == nil {
		t.Error("x11vnc sent no cursor")
	} else {
		t.Logf("cursor %v, hot spot %v", img.Rect, hot)
	}

	press := func(k uint32) {
		t.Helper()
		for _, down := range []bool{true, false} {
			if err := b.Input(backend.Key{Down: down, Keysym: k}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, k := range "abcdefghz" {
		press(uint32(k))
		want := swatchPalette[int(k)%len(swatchPalette)]
		waitFor(t, "the swatch of "+string(k), func() bool {
			px := sink.Pixels()
			return px != nil && px.RGBAAt(swatchCentre.X, swatchCentre.Y) == want
		})
	}

	// Full-screen motion keeps the update loop busy; then the idle scene
	// comes back as it was.
	press(keyF5)
	time.Sleep(time.Second)
	b.Demand(backend.Demand{Visible: false})
	time.Sleep(200 * time.Millisecond)
	b.Demand(backend.Demand{Visible: true})
	press(keyEscape)
	waitFor(t, "the idle screen again", func() bool { return same(sink.Pixels(), ref) })
	t.Logf("%d locks, %d copies", sink.Locks(), len(sink.Copies()))

	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func same(a, b *image.RGBA) bool {
	if a == nil || a.Rect != b.Rect {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

// referenceFrame reads the whole screen with a client apart from the
// backend: RFB 3.8, Raw only, in the server's own pixel format, which
// it decodes by the format's shifts. It takes the cursor's shape and
// skips it, so that x11vnc does not draw the pointer into the pixels.
func referenceFrame(t *testing.T, addr, pw string) *image.RGBA {
	t.Helper()
	nc, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(timeout))
	r := bufio.NewReader(nc)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("reference client: %v", err)
		}
	}
	readN := func(n int) []byte {
		t.Helper()
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		must(err)
		return b
	}
	readN(12)
	_, err = nc.Write([]byte("RFB 003.008\n"))
	must(err)
	types := readN(int(readN(1)[0]))
	var auth bool
	for _, st := range types {
		auth = auth || st == secVNCAuth
	}
	if !auth {
		t.Fatalf("reference client: no VNC Authentication among %v", types)
	}
	_, err = nc.Write([]byte{secVNCAuth})
	must(err)
	var challenge [16]byte
	copy(challenge[:], readN(16))
	resp := vncAuthResponse(pw, challenge)
	_, err = nc.Write(resp[:])
	must(err)
	if binary.BigEndian.Uint32(readN(4)) != 0 {
		t.Fatal("reference client: authentication failed")
	}
	_, err = nc.Write([]byte{1})
	must(err)
	init := readN(24)
	w, h := int(binary.BigEndian.Uint16(init[0:])), int(binary.BigEndian.Uint16(init[2:]))
	pf := parsePixFormat(init[4:20])
	readN(int(binary.BigEndian.Uint32(init[20:])))
	if pf.bpp != 32 || !pf.trueColour {
		t.Fatalf("reference client: server pixel format %+v", pf)
	}
	// Raw and Cursor, and the whole screen.
	_, err = nc.Write([]byte{msgSetEncodings, 0, 0, 2, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0x11})
	must(err)
	_, err = nc.Write(appendUpdateRequest(nil, false, image.Rect(0, 0, w, h)))
	must(err)

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// seen marks the pixels drawn so far, so that rectangles which overlap
	// count their pixels once: the frame is whole when each is drawn.
	seen := make([]bool, w*h)
	covered := 0
	for covered < w*h {
		hdr := readN(4)
		if hdr[0] != msgFramebufferUpdate {
			t.Fatalf("reference client: message type %d", hdr[0])
		}
		for range binary.BigEndian.Uint16(hdr[2:]) {
			rh := readN(12)
			x, y := int(binary.BigEndian.Uint16(rh[0:])), int(binary.BigEndian.Uint16(rh[2:]))
			rw, rht := int(binary.BigEndian.Uint16(rh[4:])), int(binary.BigEndian.Uint16(rh[6:]))
			switch enc := int32(binary.BigEndian.Uint32(rh[8:])); enc {
			case encCursor:
				readN(4*rw*rht + (rw+7)/8*rht)
				continue
			case encRaw:
			default:
				t.Fatalf("reference client: encoding %d", enc)
			}
			pix := readN(4 * rw * rht)
			for i := range rw * rht {
				var v uint32
				if pf.bigEndian {
					v = binary.BigEndian.Uint32(pix[4*i:])
				} else {
					v = binary.LittleEndian.Uint32(pix[4*i:])
				}
				c := color.RGBA{
					R: uint8(v >> pf.rshift & uint32(pf.rmax) * 255 / uint32(pf.rmax)),
					G: uint8(v >> pf.gshift & uint32(pf.gmax) * 255 / uint32(pf.gmax)),
					B: uint8(v >> pf.bshift & uint32(pf.bmax) * 255 / uint32(pf.bmax)),
					A: 0xff,
				}
				px, py := x+i%rw, y+i/rw
				if px >= w || py >= h {
					t.Fatalf("reference client: a rectangle of %dx%d at (%d,%d) on a screen of %dx%d", rw, rht, x, y, w, h)
				}
				img.SetRGBA(px, py, c)
				if !seen[py*w+px] {
					seen[py*w+px] = true
					covered++
				}
			}
		}
		// TigerVNC may answer with an update that leaves pixels out, the
		// cursor alone; the rest comes with the next request.
		if covered < w*h {
			_, err = nc.Write(appendUpdateRequest(nil, false, image.Rect(0, 0, w, h)))
			must(err)
		}
	}
	return img
}
