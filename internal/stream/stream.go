// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package stream serves one viewer of a session over protocol v1
// (specs/008-native-architecture/session-core.md, Viewer writer): a
// reader turns the client's messages into the viewer's input, view and
// frame acks, and a writer sends the session's state as frames, paced for
// that viewer alone.
package stream

import "context"

// Conn is a viewer's connection, one protocol message per call, such as
// a WebSocket with one message per binary frame.
type Conn interface {
	// ReadMessage returns the client's next message, or io.EOF once the
	// client closed the connection in an orderly way, such as with a
	// WebSocket close frame.
	ReadMessage(ctx context.Context) ([]byte, error)
	// WriteMessage sends p, which the caller may reuse once it returns.
	WriteMessage(ctx context.Context, p []byte) error
}
