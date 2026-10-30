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

	"github.com/coder/websocket"

	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
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
	statusResourceNotFound = 0x0204 // guacd's answer to a join to a removed connection
	statusClientBadRequest = 0x0300
)

// errBadRequest marks a connection rejected for its own parameters.
var errBadRequest = errors.New("bad request")

// closeStatus maps the error a connection ended with to a WebSocket close
// code and a Guacamole status code.
func closeStatus(err error) (code websocket.StatusCode, status int) {
	var ge *guacd.Error
	switch {
	case err == nil:
		return websocket.StatusNormalClosure, statusSuccess
	case errors.As(err, &ge) && ge.Status != 0:
		return websocket.StatusInternalError, ge.Status
	case errors.Is(err, errBadRequest):
		return websocket.StatusPolicyViolation, statusClientBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return websocket.StatusInternalError, statusUpstreamTimeout
	default:
		return websocket.StatusInternalError, statusServerError
	}
}

// closeWith closes the WebSocket with the close code for err and its
// Guacamole status code as the reason. The close handshake waits up to
// 5 s each for the close frame to go out and for the client's to come
// back. Only the first call on a connection does anything.
func closeWith(ws *websocket.Conn, err error) {
	code, status := closeStatus(err)
	// Best effort: the client may already be gone.
	_ = ws.Close(code, strconv.Itoa(status))
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

// serveIO proxies instructions between guacd and the client until either
// side ends, and returns why, or nil if a side closed normally. It closes
// the WebSocket with the status for that reason.
func serveIO(conn *guacd.Conn, ws *websocket.Conn) error {
	// coder/websocket closes the connection when the context of a read or
	// a write ends, so session I/O runs without one, as it ran without
	// deadlines on gorilla/websocket. Both goroutines write, which
	// coder/websocket allows.
	ctx := context.Background()
	send := func(b []byte) error {
		return ws.Write(ctx, websocket.MessageText, b)
	}

	// Each side reports nil when it ends normally: guacd closes after a
	// disconnect, and a client leaves however it likes.
	exit := make(chan error, 2)
	go func() {
		// Announce the connection ID as an internal-opcode instruction,
		// the way guacamole-client's tunnel announces its UUID.
		id := guacamole.NewInstruction([]string{"", conn.ID}).String()
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
			_, msg, err := ws.Read(ctx)
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
	// Unblock the other side: closing guacd ends its reader, and closing
	// the WebSocket ends the client's reader and any blocked write. A
	// cancelled read would close the WebSocket without a close frame, so
	// the close carries the status here.
	conn.Close()
	closeWith(ws, err)
	<-exit //nolint:errcheck // a consequence of the first error
	return err
}

// clientError classifies an error on the client's WebSocket. A client
// that closes normally, with or without a close frame, or whose
// connection drops, has left. A close for any other reason, such as a
// protocol violation, and a message over the read limit are failures.
// coder/websocket reports a dropped connection as a plain error, not as
// close code 1006 as gorilla/websocket did, and every plain error but the
// read limit counts as leaving, as before.
func clientError(err error) error {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd:
		return nil
	case -1: // no close frame from the client
		if errors.Is(err, websocket.ErrMessageTooBig) {
			return fmt.Errorf("from client: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("from client: %w", err)
	}
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
		raw, err := guacamole.ReadRaw(r)
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
		ins, err := guacamole.ParseInstruction(raw)
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
