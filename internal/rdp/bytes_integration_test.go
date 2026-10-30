// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build rdp_integration

package rdp

// TestBytesAgainstGuacd is step 3 of the RDP spike: the bytes per second
// that xrdp sends on the RDP link, to this client and to guacd's FreeRDP,
// for the bench scenes. Both are read from the xrdp container's network
// counter, so both include TCP and TLS. It needs a guacd that reaches the
// same xrdp:
//
//	OCCAMY_RDP_ADDR       xrdp for this client, host:port
//	OCCAMY_RDP_CONTAINER  xrdp's container, for its counter
//	OCCAMY_GUACD_ADDR     guacd, host:port
//	OCCAMY_GUACD_RDP      xrdp as guacd reaches it, host:port
//	OCCAMY_BYTES_RUNS     runs of each scene, 3 by default
//	OCCAMY_BYTES_SCENES   the scenes to measure, as "G3,G5"; all by default

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

// scenes are the bench scenes measured, with their function keys
// (bench/scene) and how long each is measured.
var scenes = []struct {
	id     string
	keysym uint32
	d      time.Duration
}{
	{"G1 idle", 0xffbe, 10 * time.Second},
	{"G3 scroll", 0xffc0, 10 * time.Second},
	{"G4 drag", 0xffc1, 10 * time.Second},
	{"G5 motion", 0xffc2, 10 * time.Second},
	{"G6 photo", 0xffc3, 5 * time.Second},
}

// txBytes reads the bytes the xrdp container has sent.
func txBytes(t *testing.T) int64 {
	t.Helper()
	out, err := exec.Command(engine, "exec", rdpContainer, "cat", "/sys/class/net/eth0/statistics/tx_bytes").Output()
	if err != nil {
		t.Fatalf("reading xrdp's counter: %v", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// driver presses keys in a session, through this client or through guacd.
type driver interface {
	key(keysym uint32) error
	close()
}

type nativeDriver struct{ s *session }

func (d nativeDriver) key(keysym uint32) error {
	select {
	case err := <-d.s.done:
		return fmt.Errorf("the session ended: %w", err)
	default:
	}
	sc, ok := ScancodeForKeysym(keysym)
	if !ok {
		return fmt.Errorf("no scancode for %#x", keysym)
	}
	if err := d.s.c.Key(sc, true); err != nil {
		return err
	}
	return d.s.c.Key(sc, false)
}

func (nativeDriver) close() {}

// guacdDriver is a guacd session whose client applies the drawing and
// answers every sync once the frame is applied, as the bench harness's
// client does, so that guacd paces as it does for a browser.
type guacdDriver struct {
	c    *guacd.Conn
	done chan struct{}
}

func dialGuacdRDP(t *testing.T, addr, rdp string, depth int) *guacdDriver {
	host, port, err := net.SplitHostPort(rdp)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := guacd.Dial(ctx, addr, guacd.Config{
		Protocol: "rdp",
		Params: map[string]string{
			"hostname": host, "port": port, "username": "occamy", "password": "occamy",
			"ignore-cert": "true", "color-depth": strconv.Itoa(depth),
		},
		Width: 1024, Height: 768, DPI: 96,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &guacdDriver{c: c, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		display := guacamole.NewDisplay()
		for {
			raw, err := c.ReadRaw()
			if err != nil {
				return
			}
			ins, err := guacamole.ParseInstruction(raw)
			if err != nil {
				return
			}
			if ins.Opcode() == "sync" {
				display.Image()
				if _, err := c.WriteRaw(raw); err != nil {
					return
				}
				continue
			}
			display.Apply(ins)
		}
	}()
	return d
}

func (d *guacdDriver) key(keysym uint32) error {
	select {
	case <-d.done:
		return errors.New("the guacd session ended")
	default:
	}
	k := strconv.FormatUint(uint64(keysym), 10)
	b := guacamole.NewInstruction([]string{"key", k, "1"}).String() +
		guacamole.NewInstruction([]string{"key", k, "0"}).String()
	_, err := d.c.WriteRaw([]byte(b))
	return err
}

func (d *guacdDriver) close() {
	d.c.Close()
	<-d.done
}

func TestBytesAgainstGuacd(t *testing.T) {
	guacdAddr, guacdRDP := os.Getenv("OCCAMY_GUACD_ADDR"), os.Getenv("OCCAMY_GUACD_RDP")
	if guacdAddr == "" || guacdRDP == "" || rdpContainer == "" {
		t.Skip("needs OCCAMY_GUACD_ADDR, OCCAMY_GUACD_RDP and the xrdp container")
	}
	runs := 3
	if n, err := strconv.Atoi(os.Getenv("OCCAMY_BYTES_RUNS")); err == nil && n > 0 {
		runs = n
	}
	series := []struct {
		name string
		open func(t *testing.T) driver
	}{
		{"guacd (FreeRDP, GFX), 32 bpp", func(t *testing.T) driver { return dialGuacdRDP(t, guacdAddr, guacdRDP, 32) }},
		{"native, 32 bpp (planar)", func(t *testing.T) driver { return nativeDriver{dial(t, Config{ColorDepth: 32})} }},
		{"guacd (FreeRDP, GFX), 16 bpp", func(t *testing.T) driver { return dialGuacdRDP(t, guacdAddr, guacdRDP, 16) }},
		{"native, 16 bpp (RLE)", func(t *testing.T) driver { return nativeDriver{dial(t, Config{ColorDepth: 16})} }},
	}
	rates := map[string]map[string][]float64{}
	for run := range runs {
		for _, sr := range series {
			t.Run(fmt.Sprintf("%s/run%d", sr.name, run), func(t *testing.T) {
				d := sr.open(t)
				defer d.close()
				// The session may start now; F1 and a letter show the idle
				// scene whatever a previous session left.
				time.Sleep(5 * time.Second)
				for _, k := range []uint32{0xffbe, 'z'} {
					if err := d.key(k); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(2 * time.Second)
				for _, sc := range scenes {
					if only := os.Getenv("OCCAMY_BYTES_SCENES"); only != "" && !strings.Contains(only, sc.id[:2]) {
						continue
					}
					// The photograph is measured from before its key, since
					// xrdp sends it within milliseconds; the animations
					// after a second of settling.
					photo := sc.id == "G6 photo"
					var before int64
					if photo {
						before = txBytes(t)
					}
					if err := d.key(sc.keysym); err != nil {
						t.Fatal(err)
					}
					if !photo {
						time.Sleep(time.Second)
						before = txBytes(t)
					}
					time.Sleep(sc.d)
					rate := float64(txBytes(t)-before) / sc.d.Seconds()
					if rates[sr.name] == nil {
						rates[sr.name] = map[string][]float64{}
					}
					rates[sr.name][sc.id] = append(rates[sr.name][sc.id], rate)
					if err := d.key(0xff1b); err != nil { // Escape
						t.Fatal(err)
					}
					time.Sleep(time.Second)
				}
			})
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nxrdp's sent KB/s, median of %d runs:\n%-30s", runs, "")
	for _, sc := range scenes {
		fmt.Fprintf(&b, "%12s", sc.id)
	}
	for _, sr := range series {
		fmt.Fprintf(&b, "\n%-30s", sr.name)
		for _, sc := range scenes {
			v := slices.Clone(rates[sr.name][sc.id])
			slices.Sort(v)
			if len(v) == 0 {
				fmt.Fprintf(&b, "%12s", "-")
				continue
			}
			fmt.Fprintf(&b, "%12.1f", v[len(v)/2]/1000)
		}
	}
	t.Log(b.String())
}
