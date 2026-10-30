// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

// encodingSeeds are seeds of Hextile, ZRLE and Tight on the fuzzed
// screen. Their zlib data is stored, not compressed, so that they do not
// change with Go's compressor; recorded traffic in the corpus has
// compressed data, and JPEG.
func encodingSeeds() map[string][]byte {
	// Each seed is a connection of its own, which starts its zlib streams
	// afresh.
	fresh := func() *encoder {
		e := newEncoder(wantPixelFormat)
		e.level = flate.NoCompression
		return e
	}
	e := fresh()
	// Tiles of 4×4 of every kind, each sent as a rectangle of its own, so
	// that Hextile and ZRLE use their subencodings.
	img := mixed(fuzzScreen.X, fuzzScreen.Y, 4)
	var hextile, zrle [][]byte
	tiles(img.Rect, 4, func(t image.Rectangle) {
		hextile = append(hextile, e.hextile(img, t))
		zrle = append(zrle, e.zrle(img, t))
	})
	// Each subencoding on each of the top row's tiles that have more than
	// one colour.
	var forced [][]byte
	ef := fresh()
	tiles(image.Rect(4, 0, 16, 4), 4, func(t image.Rectangle) {
		for _, f := range []zrleForce{0, 2, 128, 130} {
			forced = append(forced, ef.zrleRect(t, ef.zrleTile(nil, img, t, f)))
		}
	})
	smooth := image.NewRGBA(img.Rect)
	for y := range fuzzScreen.Y {
		for x := range fuzzScreen.X {
			smooth.SetRGBA(x, y, color.RGBA{uint8(x * 9), uint8(y * 13), uint8(x*y + 40), 0xff})
		}
	}
	full := image.Rect(0, 0, fuzzScreen.X, fuzzScreen.Y)
	return map[string][]byte{
		"hextile":     putUpdate(hextile...),
		"zrle":        putUpdate(append(zrle, e.zrle(img, full))...),
		"zrle-forced": putUpdate(forced...),
		"tight-fill":  putUpdate(e.tightFill(image.Rect(2, 3, 9, 11), color.RGBA{0x10, 0x80, 0xf0, 0xff}, 0)),
		"tight-copy": func(e *encoder) []byte {
			return putUpdate(
				e.tightBasic(img, image.Rect(0, 0, 3, 1), tightOptions{}),
				e.tightBasic(img, image.Rect(0, 1, 16, 12), tightOptions{explicit: true, stream: 1}),
			)
		}(fresh()),
		"tight-palette": func(e *encoder) []byte {
			return putUpdate(
				e.tightBasic(img, image.Rect(4, 0, 8, 4), tightOptions{filter: tightFilterPalette, stream: 2}),
				e.tightBasic(img, image.Rect(8, 4, 12, 8), tightOptions{filter: tightFilterPalette, stream: 3}),
				e.tightBasic(img, image.Rect(0, 0, 16, 4), tightOptions{filter: tightFilterPalette, stream: 3}),
			)
		}(fresh()),
		"tight-gradient": putUpdate(fresh().tightBasic(smooth, full, tightOptions{filter: tightFilterGradient})),
		"tight-reset": func(e *encoder) []byte {
			return putUpdate(
				e.tightBasic(img, image.Rect(0, 0, 16, 2), tightOptions{stream: 0}),
				e.tightBasic(img, image.Rect(0, 2, 16, 4), tightOptions{stream: 0, reset: 0b0001}),
				e.tightFill(image.Rect(0, 0, 1, 1), color.RGBA{A: 0xff}, 0b1110),
			)
		}(fresh()),
	}
}

// recordedSessions returns the corpus's recorded sessions by name, from
// files named recorded-<server>-<encoding>.
func recordedSessions(t testing.TB) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "fuzz", "FuzzServerMessages", "recorded-*"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := map[string][]byte{}
	for _, path := range paths {
		data, err := readCorpusFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		sessions[strings.TrimPrefix(filepath.Base(path), "recorded-")] = data
	}
	return sessions
}

// readCorpusFile reads a corpus file of one []byte.
func readCorpusFile(path string) ([]byte, error) {
	f, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := bufio.NewScanner(bytes.NewReader(f))
	s.Buffer(nil, len(f)+1)
	if !s.Scan() || s.Text() != "go test fuzz v1" || !s.Scan() {
		return nil, errors.New("not a corpus file of version 1")
	}
	line := s.Text()
	if !strings.HasPrefix(line, "[]byte(") || !strings.HasSuffix(line, ")") {
		return nil, errors.New("not a []byte")
	}
	v, err := strconv.Unquote(line[len("[]byte(") : len(line)-1])
	return []byte(v), err
}

// decodeSession decodes a seed as the fuzz target does, to the end, and
// returns the decoder.
func decodeSession(data []byte, sink *backendtest.Sink) (*decoder, error) {
	sink.Resize(fuzzScreen.X, fuzzScreen.Y)
	d := newDecoder(context.Background(), bytes.NewReader(data), sink, fuzzScreen, fuzzLimits, discardLogger)
	d.resizeEvery = 0
	for {
		if _, err := d.next(); err != nil {
			if errors.Is(err, io.EOF) {
				return d, nil
			}
			return d, err
		}
	}
}

func TestEncodingSeeds(t *testing.T) {
	// The seeds decode to their ends, so that fuzzing starts from valid
	// rectangles.
	for name, seed := range encodingSeeds() {
		if _, err := decodeSession(seed, &backendtest.Sink{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRecordedSessions(t *testing.T) {
	// Traffic recorded from real servers decodes to its end, and holds
	// the encoding its name gives.
	sessions := recordedSessions(t)
	if len(sessions) == 0 {
		t.Fatal("no recorded sessions in testdata/fuzz/FuzzServerMessages")
	}
	for name, data := range sessions {
		t.Run(name, func(t *testing.T) {
			d, err := decodeSession(data, &backendtest.Sink{})
			if err != nil {
				t.Fatal(err)
			}
			first, last := hextileRawTile, hextileColourTile
			switch {
			case strings.HasSuffix(name, "-tight-jpeg"):
				first, last = tightJPEGRect, tightJPEGRect
			case strings.HasSuffix(name, "-tight"):
				first, last = tightFillRect, tightGradientRect
			case strings.HasSuffix(name, "-zrle"):
				first, last = zrleRawTile, zrlePaletteRLETile
			}
			n := 0
			for k := first; k <= last; k++ {
				n += d.enc.counts[k]
			}
			if n == 0 {
				t.Errorf("no %v to %v in %d bytes", first, last, len(data))
			}
			var kinds []string
			for k, c := range d.enc.counts {
				if c > 0 {
					kinds = append(kinds, kind(k).String()+": "+strconv.Itoa(c))
				}
			}
			t.Logf("%d bytes; %s", len(data), strings.Join(kinds, ", "))
		})
	}
}

// BenchmarkRecorded decodes each recorded session. Its throughput is in
// bytes of pixels drawn, four a pixel.
func BenchmarkRecorded(b *testing.B) {
	for name, data := range recordedSessions(b) {
		b.Run(name, func(b *testing.B) {
			sink := &backendtest.Sink{}
			if _, err := decodeSession(data, sink); err != nil {
				b.Fatal(err)
			}
			drawn := 0
			for _, r := range sink.TakeDamage() {
				drawn += 4 * r.Dx() * r.Dy()
			}
			b.SetBytes(int64(drawn))
			fast := &benchSink{Sink: &backendtest.Sink{}}
			for b.Loop() {
				fast.Resize(fuzzScreen.X, fuzzScreen.Y)
				d := newDecoder(context.Background(), bytes.NewReader(data), fast, fuzzScreen, fuzzLimits, discardLogger)
				d.resizeEvery = 0
				for {
					if _, err := d.next(); err != nil {
						break
					}
				}
			}
		})
	}
}
