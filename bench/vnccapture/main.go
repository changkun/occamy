// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command vnccapture records the codec corpus in bench/testdata/corpus:
// frames of the bench desktop captured over VNC, the measurement of
// specs/008-native-architecture/graphics-pipeline.md works on. It starts
// the bench desktop image with a terminal and a browser added, shows each
// scene, saves what x11vnc sends as PNG, and removes the container:
//
//	make corpus
//
// The scenes are those of bench/scene, selected by their function keys,
// plus xterm with its bitmap font, xterm with an antialiased font, and
// dillo showing page.html, each started on the display over the idle
// scene. Animated scenes keep consecutive updates as x11vnc sends them.
//
// The pixels are asked for as red, green, blue and padding, and the
// cursor's shape apart from them, so that x11vnc does not paint the
// pointer into the frames. For the photograph it also asks a second
// connection for x11vnc's own order, blue first, and checks that both
// carry the same image.
package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"changkun.de/x/occamy/bench/internal/rfb"
)

//go:embed page.html
var page []byte

// Keysyms.
const (
	keyEscape = 0xff1b
	keyF3     = 0xffc0
	keyF4     = 0xffc1
	keyF5     = 0xffc2
	keyF6     = 0xffc3
)

// shot is one scene of the corpus.
type shot struct {
	name   string
	key    uint32   // selects the scene, or zero
	run    []string // a program started on the display for the shot
	settle time.Duration
	frames int // consecutive updates kept, the first one complete
}

var shots = []shot{
	{name: "desktop", key: keyEscape, settle: time.Second, frames: 1},
	{
		name: "terminal", key: keyEscape, settle: 3 * time.Second, frames: 1,
		run: []string{"xterm", "-geometry", "120x40+0+0", "-e", "sh", "-c",
			"ls -l --color=always /usr/bin | head -n 39; exec sleep infinity"},
	},
	{
		name: "terminal-aa", key: keyEscape, settle: 3 * time.Second, frames: 1,
		run: []string{"xterm", "-fa", "DejaVu Sans Mono", "-fs", "10",
			"-bg", "#1e1e2e", "-fg", "#cdd6f4", "-geometry", "100x36+96+48", "-e", "sh", "-c",
			"cat -n /usr/local/bin/start.sh; exec sleep infinity"},
	},
	{
		name: "browser", key: keyEscape, settle: 6 * time.Second, frames: 1,
		run: []string{"dillo", "-g", "1024x768+0+0", "file:///tmp/page.html"},
	},
	{name: "photo", key: keyF6, settle: 2 * time.Second, frames: 1},
	{name: "drag", key: keyF4, settle: time.Second, frames: 3},
	{name: "scroll", key: keyF3, settle: time.Second, frames: 3},
	{name: "motion", key: keyF5, settle: time.Second, frames: 2},
}

func main() {
	engine := flag.String("engine", "podman", "container engine")
	img := flag.String("image", "localhost/occamy-bench-corpus", "the bench desktop image with xterm, dillo and procps")
	name := flag.String("name", "occamy-corpus", "container name")
	port := flag.Int("port", 5959, "loopback port for the container's VNC server")
	password := flag.String("password", "vncpassword", "VNC password")
	out := flag.String("out", "bench/testdata/corpus", "output directory")
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("vnccapture: ")
	if err := os.MkdirAll(*out, 0o750); err != nil {
		log.Fatal(err)
	}

	r := &recorder{engine: *engine, name: *name, out: *out}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	if err := r.engineRun("run", "-d", "--rm", "--name", *name,
		"-p", addr+":5900", *img, "vnc"); err != nil {
		log.Fatal(err)
	}
	err := r.record(addr, *password)
	if rmErr := r.engineRun("rm", "-f", *name); rmErr != nil {
		log.Print(rmErr)
	}
	if err != nil {
		log.Fatal(err)
	}
}

type recorder struct {
	engine, name, out string
}

func (r *recorder) engineRun(args ...string) error {
	cmd := exec.Command(r.engine, args...) // #nosec G204 -- the engine and its arguments are the operator's
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", r.engine, args, err)
	}
	return nil
}

func (r *recorder) record(addr, password string) error {
	c, err := dialRetry(addr, password, rfb.Options{Format: rfb.RGBX, CursorShape: true})
	if err != nil {
		return err
	}
	defer c.Close()
	f, err := os.CreateTemp("", "page-*.html")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(page); err != nil {
		return err
	}
	f.Close()
	if err := r.engineRun("cp", f.Name(), r.name+":/tmp/page.html"); err != nil {
		return err
	}
	for _, s := range shots {
		if err := r.shot(c, s); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		if s.name == "photo" {
			if err := sameInBGRX(addr, password, c); err != nil {
				return err
			}
		}
	}
	return c.Key(keyEscape)
}

func dialRetry(addr, password string, o rfb.Options) (*rfb.Conn, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := rfb.Dial(addr, password, o)
		if err == nil || time.Now().After(deadline) {
			return c, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (r *recorder) shot(c *rfb.Conn, s shot) error {
	if s.key != 0 {
		if err := c.Key(s.key); err != nil {
			return err
		}
	}
	if s.run != nil {
		args := append([]string{"exec", "-d", "-e", "DISPLAY=:0", r.name}, s.run...)
		if err := r.engineRun(args...); err != nil {
			return err
		}
		defer r.engineRun("exec", r.name, "pkill", "-x", s.run[0]) //nolint:errcheck // reported on stderr
	}
	time.Sleep(s.settle)
	// Keep the frames in memory until the sequence is complete, so that
	// saving one does not delay the request for the next.
	frames := make([]*image.RGBA, s.frames)
	start := time.Now()
	for i := range s.frames {
		if err := c.Request(i > 0); err != nil {
			return err
		}
		if err := c.ReadUpdate(); err != nil {
			return err
		}
		frames[i] = snapshot(c)
	}
	elapsed := time.Since(start)
	for i, img := range frames {
		file := s.name + ".png"
		if s.frames > 1 {
			file = fmt.Sprintf("%s-%d.png", s.name, i)
		}
		if err := save(filepath.Join(r.out, file), img); err != nil {
			return err
		}
	}
	log.Printf("%s: %d frames in %v", s.name, s.frames, elapsed.Round(time.Millisecond))
	return nil
}

// snapshot copies the screen into an opaque image.
func snapshot(c *rfb.Conn) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, c.W, c.H))
	for i := 0; i < len(c.FB); i += 4 {
		copy(img.Pix[i:i+3], c.FB[i:i+3])
		img.Pix[i+3] = 0xff
	}
	return img
}

// save writes the screen as PNG, with a palette when it has 256 colours
// or fewer, which makes the file smaller and changes no pixel.
func save(path string, img *image.RGBA) error {
	var m image.Image = img
	if p := paletted(img); p != nil {
		m = p
	}
	f, err := os.Create(path) // #nosec G304 -- the operator's output directory
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, m); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func paletted(img *image.RGBA) *image.Paletted {
	index := map[[3]byte]uint8{}
	p := image.NewPaletted(img.Rect, nil)
	for i := 0; i < len(img.Pix); i += 4 {
		k := [3]byte{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
		v, ok := index[k]
		if !ok {
			if len(p.Palette) == 256 {
				return nil
			}
			v = uint8(len(p.Palette)) // #nosec G115 -- fewer than 256 entries
			index[k] = v
			p.Palette = append(p.Palette, color.RGBA{k[0], k[1], k[2], 0xff})
		}
		p.Pix[i/4] = v
	}
	return p
}

// sameInBGRX checks that x11vnc sends the screen c holds in RGBX
// identically in its own order, so that asking for RGBX costs the
// receiver no conversion.
func sameInBGRX(addr, password string, c *rfb.Conn) error {
	b, err := rfb.Dial(addr, password, rfb.Options{Format: rfb.BGRX, CursorShape: true})
	if err != nil {
		return err
	}
	defer b.Close()
	if err := b.Request(false); err != nil {
		return err
	}
	if err := b.ReadUpdate(); err != nil {
		return err
	}
	for i := 0; i < len(c.FB); i += 4 {
		if c.FB[i] != b.FB[i+2] || c.FB[i+1] != b.FB[i+1] || c.FB[i+2] != b.FB[i] {
			return errors.New("the screen differs between RGBX and BGRX")
		}
	}
	log.Print("x11vnc sent the photograph identically as RGBX and as BGRX")
	return nil
}
