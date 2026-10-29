// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

// Remote desktops in the demo stack, as guacd resolves them.
var credentials = map[string]config.JWT{
	"ssh": {Protocol: "ssh", Host: "ssh:22", Username: "occamy", Password: "occamy"},
	"vnc": {Protocol: "vnc", Host: "vnc:5900", Password: "vncpassword"},
	"rdp": {Protocol: "rdp", Host: "rdp:3389", Username: "occamy", Password: "occamy"},
}

// The display every series announces, the web client's default.
const (
	width, height, dpi = 1024, 768, 96
)

// rawConn carries Guacamole instructions, one at a time.
type rawConn interface {
	readRaw() ([]byte, error)
	writeRaw(b []byte) error
	close() error
}

// wsConn is a v0 connection through occamyd's WebSocket.
type wsConn struct {
	ws *websocket.Conn
	r  *bufio.Reader // the rest of the current message
	mu sync.Mutex    // one writer at a time
}

func (c *wsConn) readRaw() ([]byte, error) {
	for {
		if c.r != nil {
			raw, err := guacamole.ReadRaw(c.r)
			if err == nil {
				return raw, nil
			}
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
		}
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return nil, err
		}
		c.r = bufio.NewReader(bytes.NewReader(msg))
	}
}

func (c *wsConn) writeRaw(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

func (c *wsConn) close() error { return c.ws.Close() }

// guacdConn is a v0 connection straight to guacd.
type guacdConn struct {
	c  *guacd.Conn
	mu sync.Mutex
}

func (c *guacdConn) readRaw() ([]byte, error) { return c.c.ReadRaw() }

func (c *guacdConn) writeRaw(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.c.WriteRaw(b)
	return err
}

func (c *guacdConn) close() error { return c.c.Close() }

var httpClient = &http.Client{Timeout: 10 * time.Second}

// dialOccamy logs in to occamyd at addr and opens a session.
func dialOccamy(ctx context.Context, addr string, cred config.JWT) (rawConn, error) {
	body, _ := json.Marshal(map[string]string{
		"protocol": cred.Protocol, "host": cred.Host,
		"username": cred.Username, "password": cred.Password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/v1/login", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login: %s", resp.Status)
	}
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	url := fmt.Sprintf("ws://%s/api/v1/connect?token=%s&width=%d&height=%d&dpi=%d",
		addr, out.Token, width, height, dpi)
	d := websocket.Dialer{Subprotocols: []string{"guacamole"}}
	ws, _, err := d.DialContext(ctx, url, nil) //nolint:bodyclose // gorilla/websocket: the response body need not be closed
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &wsConn{ws: ws}, nil
}

// dialGuacd opens a session straight to guacd at addr, with the
// parameters occamyd would send.
func dialGuacd(ctx context.Context, addr string, cred config.JWT) (rawConn, error) {
	params, err := cred.GuacdParams(map[string]map[string]string{
		"rdp": {"ignore-cert": "true"}, // as in docker/occamy.yaml
	})
	if err != nil {
		return nil, err
	}
	c, err := guacd.Dial(ctx, addr, guacd.Config{
		Protocol: cred.Protocol, Params: params,
		Width: width, Height: height, DPI: dpi,
	})
	if err != nil {
		return nil, err
	}
	return &guacdConn{c: c}, nil
}

// frame is a complete frame that drew something.
type frame struct {
	at     time.Time
	damage image.Rectangle
	marked bool // the session's marker matched in the damaged area
}

// session is a client that applies drawing as a browser does: it decodes
// every image, answers every sync once the frame is applied, and sends a
// nop every five seconds, as guacamole-common-js does.
type session struct {
	conn    rawConn
	display *guacamole.Display
	frames  chan frame

	// marker, if set, reports whether a damaged area shows what the
	// scenario waits for.
	marker atomic.Pointer[func(img *image.RGBA, r image.Rectangle) bool]

	start      time.Time
	firstFrame atomic.Int64 // nanoseconds after start
	drawn      atomic.Int64 // frames that drew
	decode     atomic.Int64 // nanoseconds spent applying drawing
	done       chan struct{}
	err        error // set before done closes
}

func newSession(conn rawConn, start time.Time) *session {
	s := &session{
		conn:    conn,
		display: guacamole.NewDisplay(),
		frames:  make(chan frame, 1<<16),
		start:   start,
		done:    make(chan struct{}),
	}
	go s.read()
	go s.keepAlive()
	return s
}

func (s *session) read() {
	defer close(s.done)
	defer close(s.frames)
	for {
		raw, err := s.conn.readRaw()
		if err != nil {
			if !errClosed(err) {
				s.err = err
			}
			return
		}
		ins, err := guacamole.ParseInstruction(raw)
		if err != nil {
			s.err = err
			return
		}
		switch ins.Opcode() {
		case "sync":
			// Compositing the frame is part of the client's cost.
			t := time.Now()
			img := s.display.Image()
			now := time.Now()
			s.decode.Add(int64(now.Sub(t)))
			damage := s.display.TakeDamage()
			// The reply acknowledges the frame, so that guacd keeps
			// sending.
			if err := s.conn.writeRaw(raw); err != nil {
				s.err = err
				return
			}
			if damage.Empty() {
				continue
			}
			s.drawn.Add(1)
			s.firstFrame.CompareAndSwap(0, int64(now.Sub(s.start)))
			f := frame{at: now, damage: damage}
			if m := s.marker.Load(); m != nil {
				f.marked = (*m)(img, damage)
			}
			s.frames <- f
		case "error":
			args := ins.Args()
			s.err = fmt.Errorf("guacd error: %v", args)
			return
		case "disconnect":
			return
		default:
			t := time.Now()
			if err := s.display.Apply(ins); err != nil {
				s.err = err
				return
			}
			s.decode.Add(int64(time.Since(t)))
		}
	}
}

func (s *session) keepAlive() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if s.conn.writeRaw([]byte("3.nop;")) != nil {
				return
			}
		}
	}
}

func (s *session) close() error {
	s.conn.close()
	<-s.done
	// Drain what is left, so that the reader is never blocked.
	for range s.frames {
	}
	return s.err
}

func (s *session) setMarker(m func(img *image.RGBA, r image.Rectangle) bool) {
	s.marker.Store(&m)
}

// errEnded reports a session that ended while a scenario waited on it.
var errEnded = errors.New("session ended")

// ended returns errEnded, with the reason the session ended if it failed.
// It is called once frames has closed, after s.err is set.
func (s *session) ended() error {
	if s.err != nil {
		return fmt.Errorf("%w: %w", errEnded, s.err)
	}
	return errEnded
}

// wait returns the first frame after t for which ok reports true.
func (s *session) wait(ctx context.Context, t time.Time, ok func(frame) bool) (frame, error) {
	for {
		select {
		case <-ctx.Done():
			return frame{}, ctx.Err()
		case f, open := <-s.frames:
			if !open {
				return frame{}, s.ended()
			}
			if f.at.After(t) && ok(f) {
				return f, nil
			}
		}
	}
}

// settle consumes frames until none has arrived for quiet, or until the
// context ends.
func (s *session) settle(ctx context.Context, quiet time.Duration) error {
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, open := <-s.frames:
			if !open {
				return s.ended()
			}
			timer.Reset(quiet)
		case <-timer.C:
			return nil
		}
	}
}

// drain consumes frames for d.
func (s *session) drain(ctx context.Context, d time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	err := s.settle(ctx, d+time.Second)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// Keysyms for the keys the scenarios press. Printable ASCII characters are
// their own keysyms.
const (
	keyReturn = 0xff0d
	keyEscape = 0xff1b
	keyF1     = 0xffbe
)

// key presses and releases a key.
func (s *session) key(keysym int) error {
	k := strconv.Itoa(keysym)
	b := guacamole.NewInstruction([]string{"key", k, "1"}).String() +
		guacamole.NewInstruction([]string{"key", k, "0"}).String()
	return s.conn.writeRaw([]byte(b))
}

// typeText presses the keys for text.
func (s *session) typeText(text string) error {
	for _, r := range text {
		if err := s.key(int(r)); err != nil {
			return err
		}
	}
	return nil
}
