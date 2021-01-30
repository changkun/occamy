// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package common

import (
	"sync"

	"changkun.de/x/occamy/lib"
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

// Send sends the contents of the clipboard along the given client,
// splitting the contents as necessary.
func (c *Clipboard) Send(cli *lib.Client) {
	c.mu.Lock()
	logrus.Debug("Broadcasting clipboard to call all connected users.")
	cli.ForeachUser(sendFunc, c)
	logrus.Debug("Broadcast of clipboard complete.")
	c.mu.Unlock()
}

func sendFunc(u *User, data interface{}) interface{} {
	clipboard := data.(*Clipboard)

	buf := clipboard.Buffer
	remaining := len(current)
	stream := NewStreamFromUser(u)

	u.sock.SendClipboard(stream, clipboard.Mimetype)
	logrus.Debugf("Created stream %i for %s clipboard data.", stream.Index, clipboard.Mimetype)

	// split clipboard into chunks
	for remaining > 0 {

		// calculate size of next block
		blockSize := ClipboardBlockSize
		if remaining < blockSize {
			blockSize = remaining
		}

		// send block
		u.sock.SendBlob(stream, buf[:blockSize])
		logrus.Debugf("Sent %i bytes of clipboard data on stream %i.", blockSize, stream.Index)

		remaining -= blockSize
		buf = buf[blockSize:]
	}

	logrus.Debugf("Clipboard stream %i complete.", stream.Index)
	u.sock.SendEnd(stream)
	u.Free(stream)
}
