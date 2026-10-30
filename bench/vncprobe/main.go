// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command vncprobe measures how long the bench desktop's VNC server takes
// from a key to the update that shows it, with no guacd and no network in
// between, and how many updates a second it sends. It is the remote
// server's share of the desktop numbers in docs/benchmarks.md. Run it
// inside the VNC container:
//
//	GOOS=linux GOARCH=arm64 go build -o vncprobe ./bench/vncprobe
//	podman cp vncprobe docker-vnc-1:/tmp/vncprobe
//	podman exec docker-vnc-1 /tmp/vncprobe
//
// -scene picks the scene the keys are pressed in, by its function key: 2,
// the default, is a still desktop, and 3 to 5 animate (bench/scene). The
// probe goes back to the idle scene when it is done.
//
// It speaks just enough RFB 3.8 for that: VNC authentication, raw
// encoding, and key events.
package main

import (
	"bufio"
	"crypto/des" // #nosec G502 -- VNC authentication is DES by definition
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"io"
	"log"
	"math/bits"
	"net"
	"slices"
	"sync/atomic"
	"time"

	"changkun.de/x/occamy/bench/internal/probe"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5900", "VNC server")
	password := flag.String("password", "vncpassword", "VNC password")
	n := flag.Int("n", 200, "probe keys, 10 per second")
	scene := flag.Int("scene", 2, "scene to probe in, by function key: 2 is still, 3 to 5 animate")
	flag.Parse()
	log.SetFlags(0)
	if *scene < 1 || *scene > 6 {
		log.Fatalf("no scene F%d", *scene)
	}

	c, err := dial(*addr, *password)
	if err != nil {
		log.Fatal(err)
	}
	const keyF1 = 0xffbe
	lat, rate, err := c.measure(uint32(keyF1+*scene-1), *n) // #nosec G115 -- checked above
	if err != nil {
		log.Fatal(err)
	}
	slices.Sort(lat)
	q := func(p float64) time.Duration { return lat[min(len(lat)-1, int(p*float64(len(lat))))] }
	fmt.Printf("n=%d p50=%v p95=%v p99=%v max=%v updates/s=%.1f\n", len(lat),
		q(0.50).Round(10*time.Microsecond), q(0.95).Round(10*time.Microsecond),
		q(0.99).Round(10*time.Microsecond), lat[len(lat)-1].Round(10*time.Microsecond), rate)
}

// conn is a VNC session with a framebuffer of 32-bit pixels, B, G, R and
// padding.
type conn struct {
	nc      net.Conn
	r       *bufio.Reader
	w, h    int
	size    []byte // w and h as the server sent them
	fb      []byte
	updates chan update
	count   atomic.Int64 // updates received
	err     error        // set before updates closes
}

// update is a completed framebuffer update and the swatch's colour after
// it.
type update struct {
	at     time.Time
	swatch color.RGBA
}

func dial(addr, password string) (*conn, error) {
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &conn{nc: nc, r: bufio.NewReaderSize(nc, 1<<20), updates: make(chan update, 1024)}
	if err := c.handshake(password); err != nil {
		nc.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	return c, nil
}

func (c *conn) read(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(c.r, b)
	return b, err
}

func (c *conn) handshake(password string) error {
	if _, err := c.read(12); err != nil { // the server's version
		return err
	}
	if _, err := c.nc.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	count, err := c.read(1)
	if err != nil {
		return err
	}
	types, err := c.read(int(count[0]))
	if err != nil {
		return err
	}
	if !slices.Contains(types, 2) {
		return fmt.Errorf("no VNC authentication among %v", types)
	}
	if _, err := c.nc.Write([]byte{2}); err != nil {
		return err
	}
	challenge, err := c.read(16)
	if err != nil {
		return err
	}
	// The key is the password's first 8 bytes, each with its bits
	// reversed.
	key := make([]byte, 8)
	copy(key, password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key) // #nosec G405 -- see the import
	if err != nil {
		return err
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	if _, err := c.nc.Write(response); err != nil {
		return err
	}
	result, err := c.read(4)
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result) != 0 {
		return errors.New("authentication failed")
	}
	if _, err := c.nc.Write([]byte{1}); err != nil { // shared
		return err
	}
	init, err := c.read(24)
	if err != nil {
		return err
	}
	c.size = init[0:4]
	c.w, c.h = int(binary.BigEndian.Uint16(init[0:])), int(binary.BigEndian.Uint16(init[2:]))
	if _, err := c.read(int(binary.BigEndian.Uint32(init[20:]))); err != nil { // the name
		return err
	}
	c.fb = make([]byte, c.w*c.h*4)
	// 32 bits per pixel, depth 24, little endian, true colour, 255 per
	// channel, red at bit 16, green at 8 and blue at 0; then raw encoding
	// only.
	_, err = c.nc.Write([]byte{
		0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0,
		2, 0, 0, 1, 0, 0, 0, 0,
	})
	return err
}

// request asks for an update of the whole screen.
func (c *conn) request(incremental bool) error {
	b := []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if incremental {
		b[1] = 1
	}
	copy(b[6:], c.size)
	_, err := c.nc.Write(b)
	return err
}

func (c *conn) key(keysym uint32) error {
	b := []byte{4, 1, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[4:], keysym)
	binary.BigEndian.PutUint32(b[12:], keysym)
	_, err := c.nc.Write(b)
	return err
}

// receive applies updates until the connection fails, and reports when
// each completed. It asks for the next update after each, as a client
// does.
func (c *conn) receive() {
	defer close(c.updates)
	c.err = c.receiveErr()
}

func (c *conn) receiveErr() error {
	for {
		t, err := c.read(1)
		if err != nil {
			return err
		}
		switch t[0] {
		case 0: // FramebufferUpdate
			h, err := c.read(3)
			if err != nil {
				return err
			}
			for range binary.BigEndian.Uint16(h[1:]) {
				if err := c.rect(); err != nil {
					return err
				}
			}
			c.count.Add(1)
			c.updates <- update{at: time.Now(), swatch: c.swatch()}
			if err := c.request(true); err != nil {
				return err
			}
		case 2: // Bell
		case 3: // ServerCutText
			h, err := c.read(7)
			if err != nil {
				return err
			}
			if _, err := c.read(int(binary.BigEndian.Uint32(h[3:]))); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected message %d", t[0])
		}
	}
}

func (c *conn) rect() error {
	h, err := c.read(12)
	if err != nil {
		return err
	}
	x, y := int(binary.BigEndian.Uint16(h[0:])), int(binary.BigEndian.Uint16(h[2:]))
	w, ht := int(binary.BigEndian.Uint16(h[4:])), int(binary.BigEndian.Uint16(h[6:]))
	if enc := binary.BigEndian.Uint32(h[8:]); enc != 0 {
		return fmt.Errorf("encoding %d, not raw", enc)
	}
	if x+w > c.w || y+ht > c.h {
		return fmt.Errorf("rectangle %d×%d at (%d, %d) is off the screen", w, ht, x, y)
	}
	for row := y; row < y+ht; row++ {
		if _, err := io.ReadFull(c.r, c.fb[(row*c.w+x)*4:(row*c.w+x+w)*4]); err != nil {
			return err
		}
	}
	return nil
}

func (c *conn) swatch() color.RGBA {
	i := (probe.Centre.Y*c.w + probe.Centre.X) * 4
	return color.RGBA{R: c.fb[i+2], G: c.fb[i+1], B: c.fb[i], A: 0xff}
}

// measure shows the scene for key and presses n letters in it, ten per
// second. It returns the time from each letter until an update shows its
// colour, and the updates per second meanwhile.
func (c *conn) measure(key uint32, n int) ([]time.Duration, float64, error) {
	go c.receive()
	if err := c.request(false); err != nil {
		return nil, 0, err
	}
	if err := c.key(key); err != nil {
		return nil, 0, err
	}
	const keyEscape = 0xff1b
	defer c.key(keyEscape) //nolint:errcheck // best effort, on the way out
	time.Sleep(500 * time.Millisecond)
	var lat []time.Duration
	start, count := time.Now(), c.count.Load()
	next := start
	for i := range n {
		next = next.Add(100 * time.Millisecond)
		k := 'a' + rune(i%26)
		t0 := time.Now()
		if err := c.key(uint32(k)); err != nil {
			return nil, 0, err
		}
		timeout := time.After(5 * time.Second)
	wait:
		for {
			select {
			case u, ok := <-c.updates:
				if !ok {
					return nil, 0, c.err
				}
				if u.at.After(t0) && probe.Match(u.swatch, k) {
					lat = append(lat, u.at.Sub(t0))
					break wait
				}
			case <-timeout:
				return nil, 0, fmt.Errorf("key %d: no update showed it in 5 s", i)
			}
		}
		time.Sleep(time.Until(next))
	}
	rate := float64(c.count.Load()-count) / time.Since(start).Seconds()
	return lat, rate, nil
}
