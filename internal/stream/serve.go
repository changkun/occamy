// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"errors"
	"image"
	"io"
	"log/slog"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// Config holds a stream's settings. Zero fields take the defaults.
type Config struct {
	// SessionID names the session in welcome.
	SessionID string
	// Capabilities are what the server offers. Welcome carries their
	// agreement with the client's. Without codecs, the server offers
	// those its encoder sends, encoder.Codecs() by default.
	Capabilities protocol.Capabilities
	// Pacer configures the viewer's pacer. Its Window is the agreed frame
	// window.
	Pacer pacer.Config
	// NewEncoder returns the encoder of a viewer that agreed to codecs,
	// or nil if it sends none of them. If NewEncoder is nil, the viewer's
	// encoder is internal/encoder's.
	NewEncoder func(codecs []protocol.Codec) Encoder
	// Logger receives the stream's log, slog.Default() if nil.
	Logger *slog.Logger
}

// Encoder encodes a viewer's tiles and cursor in the codecs it agreed
// to. Each writer has its own, which it calls from one goroutine.
// internal/encoder's is the one occamyd uses.
type Encoder interface {
	// Tile appends the encoding of a tile's pixels, w by h in RGBA order
	// row by row, to dst and returns it with its codec; or, if the tile
	// goes lossy, returns dst unchanged and lossy set, so that the writer
	// merges it with the lossy tiles beside it and encodes them with
	// Lossy.
	Tile(dst, pix []byte, w, h int) (b []byte, codec protocol.Codec, lossy bool)
	// Lossy appends the encoding of pix, w by h pixels in RGBA order row
	// by row, opaque whatever their alpha, at quality from 1 to 100, and
	// returns it with its codec, or with codec 0 if it cannot. pix is
	// one tile that Tile reported lossy, or several side by side.
	Lossy(dst, pix []byte, w, h, quality int) ([]byte, protocol.Codec)
	// Cursor appends the encoding of a cursor's image, w by h pixels as
	// an image.RGBA holds them, row by row with alpha premultiplied, to
	// dst in a codec that keeps it exact, with straight alpha as the
	// protocol's codecs carry it, and returns it with its codec, or
	// reports false if the viewer decodes none.
	Cursor(dst, pix []byte, w, h int) (b []byte, codec protocol.Codec, ok bool)
}

var _ Encoder = (*encoder.Encoder)(nil)

// newEncoder is internal/encoder's encoder, or nil if it sends none of
// codecs.
func newEncoder(codecs []protocol.Codec) Encoder {
	if e := encoder.New(codecs); e != nil {
		return e
	}
	return nil
}

// errEnded stops the writer once it has sent close for the session's end.
var errEnded = errors.New("stream: session ended")

// Serve serves viewer v over c until the session ends, the client leaves
// or ctx is done; v leaves the session when Serve returns. hello is the
// client's, from the upgrade request. Serve sends welcome at once and the
// first frame as soon as there is something to show, so that the client
// sees the screen one round trip after it asked.
//
// A graphical session's frames are tiles; a terminal session's are the
// diffs between the screens the viewer was sent.
func Serve(ctx context.Context, c Conn, v *session.Viewer, hello protocol.Hello, cfg Config) error {
	defer v.Leave()
	s := v.Session()
	if len(cfg.Capabilities.Codecs) == 0 {
		cfg.Capabilities.Codecs = encoder.Codecs()
	}
	if cfg.Capabilities.Terminal == 0 {
		cfg.Capabilities.Terminal = 1
	}
	if cfg.NewEncoder == nil {
		cfg.NewEncoder = newEncoder
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	agreed := protocol.Agree(hello.Capabilities, cfg.Capabilities)
	limits := agreed.Limits()
	class := protocol.ClassGraphical
	var enc Encoder
	if s.Class() == backend.Terminal {
		class = protocol.ClassTerminal
		if agreed.Terminal == 0 {
			return closeWith(ctx, c, protocol.Close{Code: protocol.CloseUnsupported, Reason: "the client renders no terminal"})
		}
	} else if enc = cfg.NewEncoder(agreed.Codecs); enc == nil {
		return closeWith(ctx, c, protocol.Close{Code: protocol.CloseUnsupported, Reason: "no codec in common"})
	}

	// The client's visibility applies before the first frame, so that a
	// client that starts hidden is sent none.
	if err := v.View(hello.Visible, image.Rectangle{}); err != nil {
		if errors.Is(err, session.ErrEnded) {
			// The session ended before the stream began, as when its
			// backend fails at once: the client learns why, as it would
			// have a moment later.
			return closeEnded(ctx, c, s)
		}
		return err
	}
	welcome := protocol.Welcome{
		ViewerID:     uint32(v.ID()), // #nosec G115 -- viewer IDs count up from 1
		Role:         role(v.Role()),
		Class:        class,
		SessionID:    cfg.SessionID,
		Capabilities: agreed,
	}
	if err := c.WriteMessage(ctx, welcome.Append(nil)); err != nil {
		return err
	}

	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pc := cfg.Pacer
	pc.Window = int(agreed.FrameWindow)
	w := newWriter(c, v, pacer.New(pc), enc, limits, cfg.Logger)
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
	case errors.Is(err, errEnded),
		errors.Is(rerr, errClientClosed), errors.Is(rerr, io.EOF),
		errors.Is(rerr, session.ErrEnded), errors.Is(rerr, session.ErrLeft):
		return nil
	case errors.Is(rerr, context.Canceled):
		return err // the writer ended first
	case errors.As(rerr, new(connError)):
		return rerr // the connection failed, and nobody is left to tell
	}
	// The client broke the protocol; tell it why, if it still listens.
	if cerr := closeWith(parent, c, protocol.Close{Code: protocol.CloseCodeFor(rerr), Reason: "protocol error"}); cerr != nil {
		w.log.Debug("close not sent", "err", cerr)
	}
	return rerr
}

// closeWith sends m, waiting at most closeTimeout for a client that does
// not read.
func closeWith(ctx context.Context, c Conn, m protocol.Close) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	return c.WriteMessage(ctx, m.Append(nil))
}

// closeEnded sends the close for the end of s, which is ending, once its
// end is known.
func closeEnded(ctx context.Context, c Conn, s *session.Session) error {
	select {
	case <-s.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return closeWith(ctx, c, closeFor(s.End()))
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
