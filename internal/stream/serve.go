// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"errors"
	"image"
	"log/slog"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// Config holds a stream's settings. Zero fields take the defaults.
type Config struct {
	// SessionID names the session in welcome.
	SessionID string
	// Capabilities are what the server offers. Welcome carries their
	// agreement with the client's.
	Capabilities protocol.Capabilities
	// Pacer configures the viewer's pacer. Its Window is the agreed frame
	// window.
	Pacer pacer.Config
	// Encoder encodes tiles, raw pixels if nil.
	Encoder Encoder
	// Logger receives the stream's log, slog.Default() if nil.
	Logger *slog.Logger
}

// Encoder encodes a tile's pixels for a viewer.
type Encoder interface {
	// Encode appends the encoding of pix, w by h pixels in RGBA order row
	// by row, to dst at the given lossy quality, and returns it with its
	// codec.
	Encode(dst, pix []byte, w, h, quality int) ([]byte, protocol.Codec)
}

// raw sends pixels as they are, until the graphics pipeline's codecs land.
type raw struct{}

func (raw) Encode(dst, pix []byte, w, h, _ int) ([]byte, protocol.Codec) {
	return append(dst, pix[:4*w*h]...), protocol.CodecRaw
}

// errEnded stops the writer once it has sent close for the session's end.
var errEnded = errors.New("stream: session ended")

// Serve serves viewer v over c until the session ends, the client leaves
// or ctx is done; v leaves the session when Serve returns. hello is the
// client's, from the upgrade request. Serve sends welcome at once and the
// first frame as soon as there is something to show, so that the client
// sees the screen one round trip after it asked.
//
// Terminal sessions are not served yet: their frames are screen diffs
// from the terminal emulator, which come with it.
func Serve(ctx context.Context, c Conn, v *session.Viewer, hello protocol.Hello, cfg Config) error {
	defer v.Leave()
	s := v.Session()
	if s.Class() != backend.Graphical {
		return errors.New("stream: terminal sessions are not served yet")
	}
	if cfg.Encoder == nil {
		cfg.Encoder = raw{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	agreed := protocol.Agree(hello.Capabilities, cfg.Capabilities)
	limits := agreed.Limits()

	// The client's visibility applies before the first frame, so that a
	// client that starts hidden is sent none.
	if err := v.View(hello.Visible, image.Rectangle{}); err != nil {
		return err
	}
	welcome := protocol.Welcome{
		ViewerID:     uint32(v.ID()), // #nosec G115 -- viewer IDs count up from 1
		Role:         role(v.Role()),
		Class:        protocol.ClassGraphical,
		SessionID:    cfg.SessionID,
		Capabilities: agreed,
	}
	if err := c.WriteMessage(ctx, welcome.Append(nil)); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pc := cfg.Pacer
	pc.Window = int(agreed.FrameWindow)
	w := newWriter(c, v, pacer.New(pc), cfg.Encoder, limits, cfg.Logger)
	read := make(chan error, 1)
	go func() {
		err := w.read(ctx, limits)
		// When the session ends, the writer still sends close.
		if !errors.Is(err, session.ErrEnded) {
			cancel()
		}
		read <- err
	}()
	err := w.run(ctx)
	cancel()
	rerr := <-read
	switch {
	case errors.Is(err, errEnded):
		return nil
	case errors.Is(rerr, errClientClosed), errors.Is(rerr, session.ErrEnded), errors.Is(rerr, session.ErrLeft):
		return nil
	case rerr != nil && !errors.Is(rerr, context.Canceled):
		// The client broke the protocol; tell it why, if it still listens.
		closing := protocol.Close{Code: protocol.CloseCodeFor(rerr), Reason: "protocol error"}
		_ = c.WriteMessage(context.WithoutCancel(ctx), closing.Append(nil))
		return rerr
	}
	return err
}

func role(r session.Role) protocol.Role {
	switch r {
	case session.RoleOwner:
		return protocol.RoleOwner
	case session.RoleViewer:
		return protocol.RoleViewer
	default:
		return protocol.RoleReadOnly
	}
}

// closeFor is the close message for a session's end.
func closeFor(e session.End) protocol.Close {
	code := map[session.Reason]protocol.CloseCode{
		session.Closed:      protocol.CloseSessionClosed,
		session.OwnerLeft:   protocol.CloseOwnerLeft,
		session.Idle:        protocol.CloseIdle,
		session.RemoteEnded: protocol.CloseRemoteEnded,
		session.Failed:      protocol.CloseBackendFailed,
		session.Shutdown:    protocol.CloseServerShutdown,
	}[e.Reason]
	if code == 0 {
		code = protocol.CloseInternalError
	}
	return protocol.Close{Code: code, Reason: e.Reason.String()}
}
