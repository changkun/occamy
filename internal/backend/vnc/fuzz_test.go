// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"maps"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

// updateCorpus rewrites the seed corpus in testdata/fuzz from the seeds
// below: go test -run TestFuzzCorpus ./internal/backend/vnc -args
// -update-corpus.
var updateCorpus = flag.Bool("update-corpus", false, "rewrite the fuzz seed corpus in testdata/fuzz")

// The fuzzed decoders draw on a small screen with small limits, so that
// resizes stay cheap.
var (
	fuzzScreen = image.Pt(16, 12)
	fuzzLimits = limits{side: 256, pixels: 1 << 14}
)

// messageSeeds are server messages after the handshake, by name.
func messageSeeds() map[string][]byte {
	pf := wantPixelFormat
	img := pattern(20, 16, 5)
	cursor := image.NewRGBA(image.Rect(0, 0, 9, 3))
	for x := range 9 {
		cursor.SetRGBA(x, x%3, color.RGBA{R: 0xff, A: 0xff})
	}
	seeds := map[string][]byte{
		"raw":          putUpdate(putRaw(pf, img, image.Rect(0, 0, 16, 12))),
		"raw-partial":  putUpdate(putRaw(pf, img, image.Rect(3, 2, 9, 7)), putRaw(pf, img, image.Rect(0, 0, 0, 4))),
		"copy-rect":    putUpdate(putCopy(image.Rect(4, 4, 12, 10), image.Pt(0, 0))),
		"desktop-size": putUpdate(putDesktopSize(20, 16), putRaw(pf, img, image.Rect(0, 0, 20, 16))),
		"cursor":       putUpdate(putCursor(pf, cursor, image.Pt(4, 1))),
		"cursor-empty": putUpdate(rectHeader(image.Rectangle{}, encCursor)),
		"bell":         putBell(),
		"cut-text":     putCutText("caf\xe9\nline"),
		"colour-map":   putColourMap(3),

		"ext-desktop-size": putUpdate(putExtDesktopSize(0, 0, 20, 16, screenMsg{id: 1, r: image.Rect(0, 0, 20, 16)}),
			putRaw(pf, img, image.Rect(0, 0, 20, 16))),
		"ext-desktop-size-answer": putUpdate(putExtDesktopSize(reasonClient, 3, 0, 0)),
		"last-rect":               putUncounted(putRaw(pf, img, image.Rect(0, 0, 4, 4)), putLastRect()),
		"qemu-keys":               putUpdate(putQEMUKeys()),
		"continuous":              slices.Concat(putFence(fenceRequest|fenceBlockBefore, "abc"), putEndOfContinuous()),
		"clipboard-caps":          putClipCaps(clipText|1<<1|clipRequest|clipPeek|clipNotify|clipProvide, 1<<20, 0),
		"clipboard-actions":       slices.Concat(putClip(clipNotify|clipText, nil), putClip(clipRequest|clipText, nil), putClip(clipPeek, nil)),
		"clipboard-provide":       putClipProvide("café\r\nline\x00"),
	}
	maps.Copy(seeds, encodingSeeds())
	var all []byte
	for _, name := range []string{
		"raw", "copy-rect", "cursor", "bell", "cut-text", "colour-map", "desktop-size",
		"continuous", "clipboard-caps", "clipboard-provide", "qemu-keys", "ext-desktop-size",
	} {
		all = append(all, seeds[name]...)
	}
	seeds["all"] = all
	return seeds
}

// handshakeSeeds are servers' sides of handshakes, by name.
func handshakeSeeds() map[string][]byte {
	challenge := make([]byte, 16)
	vc := func(subs ...uint32) script {
		s := script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2, 0, byte(len(subs)))
		for _, st := range subs {
			s = s.u32(st)
		}
		return s
	}
	return map[string][]byte{
		"vencrypt-x509none":  vc(subX509None).u8(1).u32(0).init(16, 12, ""),
		"vencrypt-x509vnc":   vc(subX509Vnc).u8(1).u8(challenge...).u32(0).init(16, 12, ""),
		"vencrypt-x509plain": vc(subX509Plain).u8(1).u32(0).init(16, 12, ""),
		"vencrypt-tigervnc":  vc(258, subX509Vnc, secVNCAuth, secNone).u8(1).u8(challenge...).u32(0).init(16, 12, ""),
		"vencrypt-failed":    vc(subX509Plain).u8(1).u32(1).str("Authentication failed"),
		"vencrypt-0.1":       script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 1, 2).u32(19),

		"3.3-none":    script{}.version("003.003").u32(secNone).init(16, 12, "desktop"),
		"3.3-auth":    script{}.version("003.003").u32(secVNCAuth).u8(challenge...).u32(0).init(16, 12, ""),
		"3.7-auth":    script{}.version("003.007").u8(2, secNone, secVNCAuth).u8(challenge...).u32(0).init(16, 12, "x"),
		"3.8-none":    none38().init(16, 12, "caf\xe9"),
		"3.8-failed":  script{}.version("003.008").u8(1, secVNCAuth).u8(challenge...).u32(1).str("Authentication failed"),
		"3.8-refused": script{}.version("003.008").u8(0).str("Too many security failures"),
		"3.889":       script{}.version("003.889").u8(2, 30, secVNCAuth).u8(challenge...).u32(0).init(16, 12, ""),

		"ard":             script{}.version("003.889").u8(4, 30, 33, 36, 35).u8(ardScriptKey()...).u32(0).init(16, 12, "Mac"),
		"ard-failed":      script{}.version("003.008").u8(1, secARD).u8(ardScriptKey()...).u32(1).str("Authentication failed"),
		"ard-macos-fails": script{}.version("003.889").u8(1, secARD).u8(ardScriptKey()...).u8(1, 0, 0, 0).str("Authentication failed"),
		"ard-3.3":         script{}.version("003.003").u32(secARD).u8(ardScriptKey()...).u32(0).init(16, 12, ""),
		"ard-small-prime": script{}.version("003.008").u8(1, secARD).ardKey(3, minARDKey, big.NewInt(7), big.NewInt(2)).u32(0).init(16, 12, ""),
	}
}

func FuzzServerMessages(f *testing.F) {
	for _, seed := range messageSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// The sink panics when the decoder breaks its contract: a
		// rectangle outside the screen, an empty one, or a lock left
		// held.
		sink := &backendtest.Sink{}
		sink.Resize(fuzzScreen.X, fuzzScreen.Y)
		d := newDecoder(context.Background(), bytes.NewReader(data), sink, fuzzScreen, fuzzLimits, discardLogger)
		d.resizeEvery = 0
		// The extensions' state takes what the decoder tells it, and the
		// writer's side runs after each message, as Run would.
		r := newRun()
		r.size = fuzzScreen
		d.peer = r
		for i := 0; ; i++ {
			typ, err := d.next()
			if err != nil {
				break
			}
			if typ == msgFramebufferUpdate {
				r.updated(d.size, d.resized)
			}
			for len(r.control) > 0 {
				<-r.control
			}
			r.due(io.Discard, i%3 != 2)
		}
		if s := sink.Size(); s.X > fuzzLimits.side || s.Y > fuzzLimits.side || s.X*s.Y > fuzzLimits.pixels || s != d.size {
			t.Errorf("screen %v, decoder's %v, beyond the limits %+v", s, d.size, fuzzLimits)
		}
		if img, _ := sink.CursorImage(); img != nil && (img.Rect.Dx() > maxCursor || img.Rect.Dy() > maxCursor) {
			t.Errorf("cursor %v beyond %d", img.Rect, maxCursor)
		}
		if n := cap(d.enc.band); n > 4*fuzzLimits.side*zrleTile {
			t.Errorf("a band of %d bytes for a screen of at most %d pixels wide", n, fuzzLimits.side)
		}
		if n := cap(d.enc.jpeg); n > 2*(1<<22) {
			t.Errorf("a JPEG buffer of %d bytes", n)
		}
	})
}

func FuzzHandshake(f *testing.F) {
	for _, seed := range handshakeSeeds() {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, data []byte, preferAuth bool) {
		// TLS is pretended: the handshake goes on in the clear, so that
		// the fuzzer reaches VeNCrypt's inner authentication. Apple Remote
		// Desktop is allowed, with keys from a fixed seed, so that each
		// input runs the same way again.
		a := auth{
			password:    func() (string, error) { return "hunter2", nil },
			plain:       func() (string, string, error) { return "alice", "hunter2", nil },
			hasUser:     preferAuth,
			hasPassword: preferAuth,
			startTLS:    func() error { return nil },
			ard:         true,
			rand:        mrand.NewChaCha8([32]byte{}),
		}
		init, err := handshake(bytes.NewReader(data), io.Discard, a, fuzzLimits)
		if err != nil {
			return
		}
		if fuzzLimits.check(init.size.X, init.size.Y) != nil {
			t.Errorf("screen %v beyond the limits %+v", init.size, fuzzLimits)
		}
		if len(init.name) > 2*maxName {
			t.Errorf("name of %d bytes", len(init.name))
		}
	})
}

// TestFuzzCorpus checks that the seed corpus in testdata/fuzz holds the
// seeds above, which -update-corpus writes. The corpus also keeps
// inputs that fuzzing found, under names of its own.
func TestFuzzCorpus(t *testing.T) {
	for target, seeds := range map[string]map[string][]byte{
		"FuzzServerMessages": messageSeeds(),
		"FuzzHandshake":      handshakeSeeds(),
	} {
		dir := filepath.Join("testdata", "fuzz", target)
		for name, seed := range seeds {
			var want string
			if target == "FuzzHandshake" {
				want = fmt.Sprintf("go test fuzz v1\n[]byte(%q)\nbool(false)\n", seed)
			} else {
				want = fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", seed)
			}
			path := filepath.Join(dir, "seed-"+name)
			if *updateCorpus {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Errorf("%s is not the seed %q (err %v); run go test -run TestFuzzCorpus -args -update-corpus", path, name, err)
			}
		}
	}
}
