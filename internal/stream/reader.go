// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// errClientClosed ends the reader when the client sends close.
var errClientClosed = errors.New("stream: client closed")

// connError is a failure of the connection rather than of the client's
// messages: there is nobody left to tell.
type connError struct{ err error }

func (e connError) Error() string { return e.err.Error() }
func (e connError) Unwrap() error { return e.err }

// safeRead is read, which returns a session.PanicError if it panics: the
// reader runs on its own goroutine, where net/http's recover does not
// reach, and a client's message must not crash occamyd.
func (w *writer) safeRead(ctx context.Context, l protocol.Limits) (err error) {
	defer func() {
		if perr := session.Recovered(recover()); perr != nil {
			err = perr
		}
	}()
	return w.read(ctx, l)
}

// read applies the client's messages until it closes, breaks the
// protocol, or ctx is done.
func (w *writer) read(ctx context.Context, l protocol.Limits) error {
	reg := protocol.ClientMessages()
	for {
		b, err := w.c.ReadMessage(ctx)
		if err != nil {
			return connError{err}
		}
		m, err := protocol.Decode(b, reg, l)
		if err != nil {
			return err
		}
		if err := w.apply(m); err != nil {
			return err
		}
	}
}

// apply applies one client message. Input the backend cannot take, such
// as terminal input to a graphical session, is dropped; the client hears
// of it through the frames it does not get.
func (w *writer) apply(m protocol.Message) error {
	switch m := m.(type) {
	case nil: // an ignorable message of a later version
		return nil
	case *protocol.Key:
		return w.input(m.Seq, backend.Key{Down: m.Down, Keysym: m.Keysym, Code: m.Code})
	case *protocol.Pointer:
		return w.input(m.Seq, backend.Pointer{X: int(m.X), Y: int(m.Y), Buttons: backend.Buttons(m.Buttons)})
	case *protocol.Wheel:
		return w.input(m.Seq, backend.Wheel{DX: int(m.DX), DY: int(m.DY), Mode: backend.WheelMode(m.Mode)})
	case *protocol.Text:
		return w.input(m.Seq, backend.Text{Text: m.Text})
	case *protocol.TermInput:
		return w.input(m.Seq, backend.TermInput{Data: slices.Clone(m.Data)})
	case *protocol.ResizeRequest:
		return w.input(m.Seq, boundResize(w.s.Class(), int(m.Width), int(m.Height)))
	case *protocol.ClientClipboard:
		return w.input(m.Seq, backend.Clipboard{MIME: m.MIME, Data: slices.Clone(m.Data)})
	case *protocol.View:
		var vp image.Rectangle
		if m.W != 0 && m.H != 0 {
			vp = image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
		}
		if err := w.v.View(m.Visible, vp); err != nil {
			return err
		}
		w.wake()
		return nil
	case *protocol.FrameAck:
		w.acks.put(ack{id: m.FrameID, decode: time.Duration(m.DecodeTime) * time.Microsecond, at: time.Now()})
		return nil
	case *protocol.PromptReply:
		err := w.v.Answer(m.ID, backend.PromptReply{Answers: m.Answers})
		if errors.Is(err, session.ErrNotOwner) {
			return nil // a viewer cannot answer; nothing to do
		}
		return err
	case *protocol.Ping:
		w.mu.Lock()
		if len(w.pings) == maxPings {
			w.pings = append(w.pings[:0], w.pings[1:]...)
		}
		w.pings = append(w.pings, *m)
		w.mu.Unlock()
		w.wake()
		return nil
	case *protocol.Pong:
		return nil
	case *protocol.Close:
		return errClientClosed
	case *protocol.Hello:
		return fmt.Errorf("%w: hello after the handshake", protocol.ErrInvalid)
	default:
		return fmt.Errorf("%w: %v from a client after the handshake", protocol.ErrUnsupported, m.Type())
	}
}

// input gives the session an input. An input the backend rejects is
// dropped; only the session's end stops the reader.
func (w *writer) input(seq uint32, ev backend.InputEvent) error {
	err := w.v.Input(seq, ev)
	if errors.Is(err, session.ErrEnded) || errors.Is(err, session.ErrLeft) {
		return err
	}
	if err != nil {
		w.log.Debug("input dropped", "seq", seq, "err", err)
	}
	return nil
}

// boundResize is the resize a client asks of a session of class, clamped
// to the largest a backend is asked for: a terminal holds every cell of
// its screen, and each viewer keeps the screens it was sent.
func boundResize(class backend.Class, width, height int) backend.Resize {
	if class == backend.Terminal {
		return backend.Resize{Width: min(width, terminal.MaxCols), Height: min(height, terminal.MaxRows)}
	}
	return backend.Resize{Width: min(width, backend.MaxResizeSide), Height: min(height, backend.MaxResizeSide)}
}
