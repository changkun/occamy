// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// The extensions of the community RFB specification (rfbproto) that the
// backend asks for, beyond RFC 6143: each has a file of its own, and
// this one routes the server's pseudo-rectangles and messages to them.

import (
	"fmt"
	"image"
)

// encLastRect is the LastRect pseudo-encoding: a rectangle of it ends
// the update, whatever its header's count said.
const encLastRect int32 = -224

// extensions are the pseudo-encodings of the extensions, which SetEncodings
// asks for after the encodings.
var extensions = []int32{
	encExtendedDesktopSize,
	encLastRect,
	encContinuousUpdates,
	encFence,
	encQEMUExtKey,
	encExtendedClipboard,
}

// peer is the client's side of a connection, which the decoder tells
// what the server's extensions announce and ask for. *run implements it.
// The decoder's methods are called by the decoder's goroutine, one at a
// time, between reads; only fence may wait.
type peer interface {
	// fence takes a Fence from the server, and answers it if it is a
	// request. It returns an error if the answer cannot be sent.
	fence(flags uint32, payload []byte) error
	// continuousUpdates records that the server supports them.
	continuousUpdates()
	// qemuKeys records that the server takes QEMU Extended Key Events.
	qemuKeys()
	// extendedDesktopSize records an ExtendedDesktopSize: the screen is
	// size, its first screen is first unless nil, and answer is whether
	// it answers this client's SetDesktopSize.
	extendedDesktopSize(size image.Point, first *screen, answer bool)
	// clipboardCaps records the server's Extended Clipboard caps: the
	// formats and actions it takes.
	clipboardCaps(flags uint32)
	// clipboardAction takes the server's request, peek or notify.
	clipboardAction(flags uint32)
}

// nopPeer ignores everything, for a decoder on its own.
type nopPeer struct{}

func (nopPeer) fence(uint32, []byte) error                     { return nil }
func (nopPeer) continuousUpdates()                             {}
func (nopPeer) qemuKeys()                                      {}
func (nopPeer) extendedDesktopSize(image.Point, *screen, bool) {}
func (nopPeer) clipboardCaps(uint32)                           {}
func (nopPeer) clipboardAction(uint32)                         {}

// tell returns the decoder's peer.
func (d *decoder) tell() peer {
	if d.peer == nil {
		return nopPeer{}
	}
	return d.peer
}

// extensionRect applies a rectangle of an extension's pseudo-encoding,
// whose header gave enc and r.
func (d *decoder) extensionRect(enc int32, r image.Rectangle) error {
	switch enc {
	case encExtendedDesktopSize:
		return d.extendedDesktopSize(r)
	case encLastRect:
		d.last = true
		return nil
	case encQEMUExtKey:
		d.tell().qemuKeys()
		return nil
	}
	return fmt.Errorf("unsupported encoding %d", enc)
}

// extensionMessage applies a server message of an extension, whose type
// t has been read.
func (d *decoder) extensionMessage(t byte) error {
	switch t {
	case msgEndOfContinuousUpdates:
		d.tell().continuousUpdates()
		return nil
	case msgServerFence:
		return d.serverFence()
	}
	return fmt.Errorf("vnc: unknown message type %d", t)
}
