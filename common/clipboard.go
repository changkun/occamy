// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package common

import (
	"sync"

	"github.com/changkun/occamy/lib"
	"github.com/sirupsen/logrus"
)

// ClipboardBlockSize is the maximum number of bytes to send in an
// individual blob when transmitting the clipboard contents to a
// connected client.
const ClipboardBlockSize = 4096

// Clipboard defines a generic clipboard structure
type Clipboard struct {
	// The mimetype of the contained clipboard data, length 256.
	Mimetype string
	// Buffer gives arbitrary clipboard data.
	Buffer []byte
	// MaxSize specifies the maximum size of the buffer
	MaxSize int
	// Lock which restricts simultaneous access to the clipboard,
	// guaranteeing ordered modifications to the clipboard and that
	// changes to the clipboard are not allowed while the clipboard is
	// being broadcast to all users.
	mu sync.Mutex
}

// NewClipboard creates a new clipboard having the given initial size.
func NewClipboard(size int) *Clipboard {
	return &Clipboard{MaxSize: size}
}

// Send sends the contents of the clipboard along the given client,
// splitting the contents as necessary.
func (c *Clipboard) Send(client *lib.Client) {
	c.mu.Lock()
	logrus.Debug("Broadcasting clipboard to all connected users.")
	client.ForEachUser(sendUserClipboard, c)
	logrus.Debug("Broadcast of clipboard complete.")
	c.mu.Unlock()
}

// Reset clears the clipboard contents and assigns a new mimetype for
// future data.
func (c *Clipboard) Reset(mimetype string) {
	c.mu.Lock()
	c.Buffer = []byte{}
	c.Mimetype = mimetype
	c.mu.Unlock()
}

// Append appends the given data to the current clipboard contents. The
// data must match the mimetype chosen for the clipboard data by c.Reset().
func (c *Clipboard) Append(data []byte) {
	c.mu.Lock()
	if len(c.Buffer) < c.MaxSize {
		c.Buffer = append(c.Buffer, data[:c.MaxSize-len(c.Buffer)]...)
	}
	c.mu.Unlock()
}

// Callback for u.ForEach() which sends clipboard data to each connected
// client.
func sendUserClipboard(u *lib.User, data interface{}) {
	// TODO: need implement GuacStream
	// clipboard := data.(*Clipboard)
	// current := clipboard.Buffer
}
