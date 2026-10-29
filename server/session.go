// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"changkun.de/x/occamy/internal/guacd"
	"changkun.de/x/occamy/internal/protocol"
)

// Session is a guacd connection shared by every client with the same
// claims. The first client, the owner, creates the connection in guacd;
// later clients join it.
type Session struct {
	Key    string // jwt.GenerateID(), the sharing rule
	ConnID string // guacd connection ID, valid once ready is closed and err is nil

	ready chan struct{} // closed when ConnID or err is set
	err   error         // why the owner's handshake failed
	users int           // guarded by proxy.mu
}

// Guacamole status codes that occamy.js reads from the WebSocket close
// reason.
const (
	statusSuccess          = 0x0000
	statusServerError      = 0x0200
	statusUpstreamTimeout  = 0x0202
	statusClientBadRequest = 0x0300
)

// errBadRequest marks a connection rejected for its own parameters.
var errBadRequest = errors.New("bad request")

// closeStatus maps the error a connection ended with to a WebSocket close
// code and a Guacamole status code.
func closeStatus(err error) (code, status int) {
	var ge *guacd.Error
	switch {
	case err == nil:
		return websocket.CloseNormalClosure, statusSuccess
	case errors.As(err, &ge) && ge.Status != 0:
		return websocket.CloseInternalServerErr, ge.Status
	case errors.Is(err, errBadRequest):
		return websocket.ClosePolicyViolation, statusClientBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return websocket.CloseInternalServerErr, statusUpstreamTimeout
	default:
		return websocket.CloseInternalServerErr, statusServerError
	}
}

// display is the client's screen, announced to guacd in the handshake.
type display struct {
	width, height, dpi int
	timezone           string
}

// timezonePattern matches IANA time zone names such as Europe/Berlin or
// Etc/GMT+1.
var timezonePattern = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)

// displayHints reads the optional width, height, dpi and timezone query
// parameters. Sizes are clamped to 1-8192 and the DPI to 48-600; anything
// missing or malformed falls back to 1024x768 at 96 DPI and no time zone.
func displayHints(q url.Values) display {
	d := display{
		width:  intParam(q, "width", guacd.DefaultWidth, 1, 8192),
		height: intParam(q, "height", guacd.DefaultHeight, 1, 8192),
		dpi:    intParam(q, "dpi", guacd.DefaultDPI, 48, 600),
	}
	if tz := q.Get("timezone"); len(tz) <= 64 && timezonePattern.MatchString(tz) {
		d.timezone = tz
	}
	return d
}

func intParam(q url.Values, name string, fallback, lo, hi int) int {
	v, err := strconv.Atoi(q.Get(name))
	if err != nil {
		return fallback
	}
	return min(max(v, lo), hi)
}

// aLongTimeAgo is a deadline in the past, used to abort blocked reads.
var aLongTimeAgo = time.Unix(1, 0)

// serveIO proxies instructions between guacd and the client until either
// side ends, and returns why, or nil if a side closed normally.
func serveIO(conn *guacd.Conn, ws *websocket.Conn) error {
	var wmu sync.Mutex // gorilla/websocket allows one writer at a time
	send := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return ws.WriteMessage(websocket.TextMessage, b)
	}

	// Each side reports nil when it ends normally: guacd closes after a
	// disconnect, and a client leaves however it likes.
	exit := make(chan error, 2)
	go func() {
		// Announce the connection ID as an internal-opcode instruction,
		// the way guacamole-client's tunnel announces its UUID.
		id := protocol.NewInstruction([]string{"", conn.ID}).String()
		if err := send([]byte(id)); err != nil {
			exit <- clientError(err)
			return
		}
		for {
			raw, err := conn.ReadRaw()
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				} else {
					err = fmt.Errorf("from guacd: %w", err)
				}
				exit <- err
				return
			}
			if err := send(raw); err != nil {
				exit <- clientError(err)
				return
			}
		}
	}()
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				exit <- clientError(err)
				return
			}
			if err := forward(conn, msg, send); err != nil {
				exit <- err
				return
			}
		}
	}()

	err := <-exit
	// Unblock the other side: closing guacd ends its reader, and a read
	// deadline ends the WebSocket reader without closing the socket, so
	// that the caller can still send a close frame.
	conn.Close()
	_ = ws.NetConn().SetReadDeadline(aLongTimeAgo)
	<-exit //nolint:errcheck // a consequence of the first error
	return err
}

// clientError classifies an error on the client's WebSocket. A client
// that closes, with or without a close frame, or whose connection drops,
// has left; only a close for a protocol violation is a failure.
func clientError(err error) error {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway,
			websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure:
			return nil
		}
		return fmt.Errorf("from client: %w", err)
	}
	return nil
}

// internalOpcode prefixes instructions with the empty opcode, which the
// tunnel uses for its own messages.
var internalOpcode = []byte("0.")

// forward sends the instructions in one client message to guacd. Internal
// instructions stay in the proxy: a ping is answered with itself, as
// guacamole-client's tunnel does, and the rest are dropped.
func forward(conn *guacd.Conn, msg []byte, reply func([]byte) error) error {
	r := bufio.NewReader(bytes.NewReader(msg))
	out := make([]byte, 0, len(msg))
	for {
		raw, err := protocol.ReadRaw(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", errBadRequest, err)
		}
		if !bytes.HasPrefix(raw, internalOpcode) {
			out = append(out, raw...)
			continue
		}
		ins, err := protocol.ParseInstruction(raw)
		if err != nil {
			return fmt.Errorf("%w: %w", errBadRequest, err)
		}
		if args := ins.Args(); len(args) > 0 && args[0] == "ping" {
			if err := reply(raw); err != nil {
				return err
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	_, err := conn.WriteRaw(out)
	return err
}
