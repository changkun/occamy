// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package guacd connects to an upstream guacd and performs the Guacamole
// handshake for a new connection or for joining an existing one.
package guacd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"changkun.de/x/occamy/internal/guacamole"
)

// ProtocolVersion is the Guacamole protocol version sent in connect.
const ProtocolVersion = "VERSION_1_5_0"

// DefaultTimeout bounds a handshake whose context has no deadline, matching
// guacd's own GUACD_USEC_TIMEOUT.
const DefaultTimeout = 15 * time.Second

// Default display settings, used where Config leaves them zero.
const (
	DefaultWidth  = 1024
	DefaultHeight = 768
	DefaultDPI    = 96
)

// DefaultImageMimetypes are the image formats announced when
// Config.ImageMimetypes is nil.
var DefaultImageMimetypes = []string{"image/png", "image/jpeg", "image/webp"}

// Config describes the connection to create or join.
type Config struct {
	Protocol     string            // used when ConnectionID is empty
	ConnectionID string            // "$..." to join an existing connection
	Params       map[string]string // values for the names guacd sends in args

	Width, Height, DPI int

	// ImageMimetypes defaults to DefaultImageMimetypes when nil; an empty,
	// non-nil slice announces none. Audio and video default to none.
	ImageMimetypes, AudioMimetypes, VideoMimetypes []string

	Timezone, Name string // sent only when set
}

// Conn is a guacd connection after a successful handshake.
type Conn struct {
	ID      string // connection ID from ready
	Version string // protocol version guacd sent in args

	io *guacamole.InstructionIO
}

// Error is an error instruction received from guacd.
type Error struct {
	Message string
	Status  int
}

func (e *Error) Error() string {
	return fmt.Sprintf("guacd: %s (status %d)", e.Message, e.Status)
}

// aLongTimeAgo is a deadline in the past, used to abort blocked I/O.
var aLongTimeAgo = time.Unix(1, 0)

// Dial connects to guacd at addr and performs the handshake.
func Dial(ctx context.Context, addr string, c Config) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("guacd: %w", err)
	}
	conn, err := Handshake(ctx, nc, c)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return conn, nil
}

// Handshake performs the Guacamole handshake on nc. It returns once guacd
// has sent ready, which happens before guacd connects to the remote desktop,
// so later failures arrive as error and disconnect instructions on the
// returned connection. The caller closes nc if Handshake fails.
func Handshake(ctx context.Context, nc net.Conn, c Config) (*Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		if err := nc.SetDeadline(time.Now().Add(DefaultTimeout)); err != nil {
			return nil, fmt.Errorf("guacd: %w", err)
		}
	}
	// Unblocks the handshake on cancellation. If setting the deadline
	// fails, nc is already closed and the handshake fails anyway.
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(aLongTimeAgo) })
	defer stop()

	conn := &Conn{io: guacamole.NewInstructionIO(nc)}
	if err := conn.handshake(c); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("guacd: handshake: %w", ctxErr)
		}
		return nil, err
	}
	if !stop() {
		return nil, fmt.Errorf("guacd: handshake: %w", ctx.Err())
	}
	if err := nc.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("guacd: %w", err)
	}
	return conn, nil
}

func (c *Conn) handshake(cfg Config) error {
	selected := cfg.Protocol
	if cfg.ConnectionID != "" {
		selected = cfg.ConnectionID
	}
	if err := c.send("select", selected); err != nil {
		return err
	}

	args, err := c.expect("args")
	if err != nil {
		return err
	}
	if len(args) == 0 || !strings.HasPrefix(args[0], "VERSION_") {
		return errors.New("guacd: args without a protocol version, guacd before 1.1 is not supported")
	}
	c.Version = args[0]

	width, height, dpi := cfg.Width, cfg.Height, cfg.DPI
	if width == 0 {
		width = DefaultWidth
	}
	if height == 0 {
		height = DefaultHeight
	}
	if dpi == 0 {
		dpi = DefaultDPI
	}
	images := cfg.ImageMimetypes
	if images == nil {
		images = DefaultImageMimetypes
	}
	err = c.send("size", strconv.Itoa(width), strconv.Itoa(height), strconv.Itoa(dpi))
	if err == nil {
		err = c.send("audio", cfg.AudioMimetypes...)
	}
	if err == nil {
		err = c.send("video", cfg.VideoMimetypes...)
	}
	if err == nil {
		err = c.send("image", images...)
	}
	if err == nil && cfg.Timezone != "" {
		err = c.send("timezone", cfg.Timezone)
	}
	if err == nil && cfg.Name != "" {
		err = c.send("name", cfg.Name)
	}
	if err != nil {
		return err
	}

	values := make([]string, 0, len(args))
	values = append(values, ProtocolVersion)
	for _, name := range args[1:] {
		values = append(values, cfg.Params[name])
	}
	if err := c.send("connect", values...); err != nil {
		return err
	}

	ready, err := c.expect("ready")
	if err != nil {
		return err
	}
	if len(ready) == 0 || ready[0] == "" {
		return errors.New("guacd: ready without a connection ID")
	}
	c.ID = ready[0]
	return nil
}

func (c *Conn) send(opcode string, args ...string) error {
	_, err := c.io.Write(guacamole.NewInstruction(append([]string{opcode}, args...)))
	if err != nil {
		return fmt.Errorf("guacd: send %s: %w", opcode, err)
	}
	return nil
}

// expect reads the next instruction and returns its arguments if it has the
// given opcode. An error instruction becomes *Error.
func (c *Conn) expect(opcode string) ([]string, error) {
	ins, err := c.io.Read()
	if err != nil {
		return nil, fmt.Errorf("guacd: read %s: %w", opcode, err)
	}
	switch ins.Opcode() {
	case opcode:
		return ins.Args(), nil
	case "error":
		e := &Error{}
		if args := ins.Args(); len(args) > 0 {
			e.Message = args[0]
			if len(args) > 1 {
				e.Status, _ = strconv.Atoi(args[1])
			}
		}
		return nil, e
	default:
		return nil, fmt.Errorf("guacd: unexpected %q instruction during handshake, want %q", ins.Opcode(), opcode)
	}
}

// ReadRaw reads the next raw instruction from guacd.
func (c *Conn) ReadRaw() ([]byte, error) {
	return c.io.ReadRaw()
}

// WriteRaw writes raw instructions to guacd.
func (c *Conn) WriteRaw(b []byte) (int, error) {
	return c.io.WriteRaw(b)
}

// Close closes the connection to guacd.
func (c *Conn) Close() error {
	return c.io.Close()
}
