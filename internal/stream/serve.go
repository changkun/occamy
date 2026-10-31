// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"slices"
	"time"

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
	// NoScroll turns scroll detection off: the writer then keeps no copy
	// of the client's screen and sends no copy-rect.
	NoScroll bool
	// RefineDelay is how long a tile stays unchanged before the writer
	// refines it, DefaultRefineDelay if zero: sends it again exact if it
	// has up to 256 colours, or at the quality its SSIM calls for, when
	// it went at a lower quality while it moved, or while the viewer's
	// link held its quality down. Refinement is background work: it goes
	// only while nothing else is to be sent and no other frame awaits an
	// ack, a little at a time, and gives way to any input or change.
	RefineDelay time.Duration
	// NoRefine turns refinement off, and with it motion: nothing goes
	// at a lower quality than the pacer's for refinement to raise.
	NoRefine bool
	// NoCache turns the tile cache off: the server offers neither the
	// cached codec nor cache slots. Otherwise it offers the slots in
	// Capabilities, DefaultCacheSlots if zero, and keeps a mirror of the
	// slots of each client that agrees to both.
	NoCache bool
	// NoShare turns the encode cache and its workers off: each viewer of
	// a session encodes its tiles alone, on its writer. Otherwise the
	// viewers whose encoders are internal/encoder's share the session's
	// encodings of tiles and runs, and its workers, which encode a
	// frame's rows ahead of the writer.
	NoShare bool
	// EncodeWorkers is how many workers a session has,
	// DefaultEncodeWorkers() if zero, none if negative: the viewers then
	// share the encode cache only.
	EncodeWorkers int
	// EncodeCacheBytes bounds the encodings a session keeps,
	// DefaultEncodeCacheBytes if zero or less.
	EncodeCacheBytes int
	// Logger receives the stream's log, slog.Default() if nil.
	Logger *slog.Logger
	// ViewerTimeout bounds how long a viewer, hidden or not, may leave
	// the frames it was sent unacknowledged, and how long one write to
	// it may block, DefaultViewerTimeout if zero. A viewer that stops
	// reading or acknowledging otherwise keeps the screen it was last
	// sent, a terminal's whole screen, for as long as it stays
	// connected.
	ViewerTimeout time.Duration
}

// DefaultViewerTimeout is Config.ViewerTimeout's default.
const DefaultViewerTimeout = 30 * time.Second

// DefaultRefineDelay is Config.RefineDelay's default: longer than the
// time between the changes of a tile in motion, which changes more than
// once a second, so that a tile is rarely refined between two of them.
const DefaultRefineDelay = 300 * time.Millisecond

// ErrStalled ends the stream of a viewer that acknowledged no frame, or
// took no message, for Config.ViewerTimeout. Serve tells it so with
// close too-slow, if it still reads.
var ErrStalled = errors.New("stream: the viewer stopped taking frames")

// timedConn bounds each write to its Conn by d: a write that blocks
// that long is the viewer's stall, ErrStalled.
type timedConn struct {
	Conn
	d time.Duration
}

func (c timedConn) WriteMessage(ctx context.Context, p []byte) error {
	wctx, cancel := context.WithTimeout(ctx, c.d)
	defer cancel()
	err := c.Conn.WriteMessage(wctx, p)
	if err != nil && ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: a write blocked for %v: %w", ErrStalled, c.d, err)
	}
	return err
}

// Encoder encodes a viewer's tiles and cursor in the codecs it agreed
// to. Each writer has its own, which it calls from one goroutine.
// internal/encoder's is the one occamyd uses.
type Encoder interface {
	// Tile appends the encoding of a tile's pixels, w by h in RGBA order
	// row by row, to dst and returns it with its codec; or, if the tile
	// goes lossy, returns dst unchanged and lossy set, so that the writer
	// merges it with the lossy tiles beside it and encodes them with
	// Lossy or Still. moving reports that the tile is in motion, which
	// may send it lossy where it would go lossless still.
	Tile(dst, pix []byte, w, h int, moving bool) (b []byte, codec protocol.Codec, lossy bool)
	// Lossy appends the encoding of pix, w by h pixels in RGBA order row
	// by row, opaque whatever their alpha, at quality from 1 to 100, and
	// returns it with its codec, or with codec 0 if it cannot. pix is
	// one tile that Tile reported lossy, or several side by side.
	Lossy(dst, pix []byte, w, h, quality int) ([]byte, protocol.Codec)
	// Still encodes pix as Lossy does, for pixels that are not moving,
	// at the lowest quality from lo to hi at which each of its tiles,
	// tile pixels wide side by side, looks like pix by the encoder's
	// perceptual score, and returns the quality, and whether the score
	// was reached; if not, the quality is hi.
	Still(dst, pix []byte, w, h, tile, lo, hi int) (b []byte, codec protocol.Codec, quality int, reached bool)
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
	switch {
	case cfg.NoCache:
		cfg.Capabilities.Codecs = slices.DeleteFunc(slices.Clone(cfg.Capabilities.Codecs),
			func(c protocol.Codec) bool { return c == protocol.CodecCached })
		cfg.Capabilities.CacheSlots = 0
	case cfg.Capabilities.CacheSlots == 0:
		cfg.Capabilities.CacheSlots = DefaultCacheSlots
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
	if cfg.ViewerTimeout <= 0 {
		cfg.ViewerTimeout = DefaultViewerTimeout
	}
	c = timedConn{Conn: c, d: cfg.ViewerTimeout}
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
	if w.fb != nil && !cfg.NoScroll {
		w.sc = &scroller{}
	}
	if w.fb != nil && slices.Contains(agreed.Codecs, protocol.CodecCached) {
		w.cache = newTileCache(int(agreed.CacheSlots))
	}
	switch {
	case cfg.NoRefine:
		w.refineDelay = -1
	case cfg.RefineDelay > 0:
		w.refineDelay = cfg.RefineDelay
	}
	w.stallTimeout = cfg.ViewerTimeout
	if p, ok := profileOf(enc); ok && w.fb != nil && !cfg.NoShare {
		workers := cfg.EncodeWorkers
		if workers == 0 {
			workers = DefaultEncodeWorkers()
		}
		budget := cfg.EncodeCacheBytes
		if budget <= 0 {
			budget = DefaultEncodeCacheBytes
		}
		w.sh, w.prof = share(w.fb, workers, budget), p
		defer w.sh.release(w.fb)
	}
	read := make(chan error, 1)
	go func() {
		err := w.safeRead(ctx, limits)
		// When the session ends, the writer still sends close.
		if !errors.Is(err, session.ErrEnded) {
			cancel()
		}
		read <- err
	}()
	err := w.run(ctx)
	cancel()
	rerr := <-read
	var p session.PanicError
	switch {
	case errors.Is(err, ErrStalled):
		// The viewer kept the stream's state, a terminal's screen among
		// it, without taking frames: it goes, told why if it still reads.
		w.log.Info("viewer dropped", "viewer", v.ID(), "err", err)
		if cerr := closeWith(parent, c, protocol.Close{Code: protocol.CloseTooSlow, Reason: "the viewer did not keep up"}); cerr != nil {
			w.log.Debug("close not sent", "err", cerr)
		}
		return err
	case errors.Is(err, errEnded),
		errors.Is(rerr, errClientClosed), errors.Is(rerr, io.EOF),
		errors.Is(rerr, session.ErrEnded), errors.Is(rerr, session.ErrLeft):
		return nil
	case errors.Is(rerr, context.Canceled):
		return err // the writer ended first
	case errors.As(rerr, new(connError)):
		return rerr // the connection failed, and nobody is left to tell
	case errors.As(rerr, &p):
		// occamyd failed on its own: the client learns that much, and
		// the log the rest.
		w.log.Error("stream reader panicked", "panic", p.Value, "stack", string(p.Stack))
		if cerr := closeWith(parent, c, protocol.Close{Code: protocol.CloseInternalError, Reason: "internal error"}); cerr != nil {
			w.log.Debug("close not sent", "err", cerr)
		}
		return rerr
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
		session.Internal:    protocol.CloseInternalError,
	}[e.Reason]
	if code == 0 {
		code = protocol.CloseInternalError
	}
	return protocol.Close{Code: code, Reason: e.Reason.String()}
}
