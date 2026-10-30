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
// It speaks RFB through bench/internal/rfb.
package main

import (
	"flag"
	"fmt"
	"image/color"
	"log"
	"slices"
	"sync/atomic"
	"time"

	"changkun.de/x/occamy/bench/internal/probe"
	"changkun.de/x/occamy/bench/internal/rfb"
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

// conn is a VNC session whose updates are reported as they complete.
type conn struct {
	*rfb.Conn
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
	c, err := rfb.Dial(addr, password, rfb.Options{Format: rfb.BGRX})
	if err != nil {
		return nil, err
	}
	return &conn{Conn: c, updates: make(chan update, 1024)}, nil
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
		if err := c.ReadUpdate(); err != nil {
			return err
		}
		c.count.Add(1)
		c.updates <- update{at: time.Now(), swatch: c.swatch()}
		if err := c.Request(true); err != nil {
			return err
		}
	}
}

func (c *conn) swatch() color.RGBA {
	i := (probe.Centre.Y*c.W + probe.Centre.X) * 4
	return color.RGBA{R: c.FB[i+2], G: c.FB[i+1], B: c.FB[i], A: 0xff}
}

// measure shows the scene for key and presses n letters in it, ten per
// second. It returns the time from each letter until an update shows its
// colour, and the updates per second meanwhile.
func (c *conn) measure(key uint32, n int) ([]time.Duration, float64, error) {
	go c.receive()
	if err := c.Request(false); err != nil {
		return nil, 0, err
	}
	if err := c.Key(key); err != nil {
		return nil, 0, err
	}
	const keyEscape = 0xff1b
	defer c.Key(keyEscape) //nolint:errcheck // best effort, on the way out
	time.Sleep(500 * time.Millisecond)
	var lat []time.Duration
	start, count := time.Now(), c.count.Load()
	next := start
	for i := range n {
		next = next.Add(100 * time.Millisecond)
		k := 'a' + rune(i%26)
		t0 := time.Now()
		if err := c.Key(uint32(k)); err != nil {
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
