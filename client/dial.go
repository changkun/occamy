// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/protocol"
)

// Dial opens a session on rawURL, a stream route of occamyd such as
// wss://host/api/v1/connections/lab/stream, and returns once the server
// welcomed the client. It performs the handshake of docs/protocol.md:
// the hello of opts travels in the upgrade request's hello parameter,
// and token, an access or share token, in Sec-WebSocket-Protocol beside
// occamy.v1, never in the URL. An empty token offers occamy.v1 alone,
// for a client that authenticates through Options.HTTPHeader.
//
// If the server refuses the client after the upgrade, such as for its
// token, Dial returns a *CloseError with the server's code. ctx bounds
// the handshake; once Dial returns, the session lasts until Close or the
// server ends it.
func Dial(ctx context.Context, rawURL, token string, opts Options) (*Session, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}
	q, err := protocol.EncodeHelloQuery(opts.Hello())
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	v := u.Query()
	v.Set(protocol.HelloParameter, q)
	u.RawQuery = v.Encode()
	subprotocols := []string{protocol.Subprotocol}
	if token != "" {
		subprotocols = append(subprotocols, protocol.TokenSubprotocol(token))
	}
	c, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient:   opts.HTTPClient,
		HTTPHeader:   opts.HTTPHeader,
		Subprotocols: subprotocols,
	})
	if err != nil {
		return nil, fmt.Errorf("client: dial: %w", err)
	}
	if p := c.Subprotocol(); p != protocol.Subprotocol {
		_ = c.Close(websocket.StatusCode(protocol.CloseUnsupported), "")
		return nil, fmt.Errorf("client: the server answered subprotocol %q, want %s: %w", p, protocol.Subprotocol, protocol.ErrUnsupported)
	}
	return New(ctx, wsConn{c}, opts)
}

// wsConn is a WebSocket that carries one protocol message per binary
// message.
type wsConn struct{ c *websocket.Conn }

// errText reports a text message, which the protocol does not allow.
var errText = fmt.Errorf("%w: a text message", protocol.ErrUnsupported)

func (w wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	typ, p, err := w.c.Read(ctx)
	if err != nil {
		var ce websocket.CloseError
		if errors.As(err, &ce) {
			return nil, &CloseError{Code: protocol.CloseCode(ce.Code), Reason: ce.Reason} // #nosec G115 -- a close status is a u16 on the wire
		}
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, errText
	}
	return p, nil
}

func (w wsConn) WriteMessage(ctx context.Context, p []byte) error {
	return w.c.Write(ctx, websocket.MessageBinary, p)
}

func (w wsConn) Close(code protocol.CloseCode, reason string) error {
	return w.c.Close(websocket.StatusCode(code), reason)
}

func (w wsConn) SetReadLimit(n int64) { w.c.SetReadLimit(n) }
