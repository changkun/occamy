// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

const (
	// ClientMaxStream is the maximum number of inbound or outbound
	// streams supported by any one lib.Client
	ClientMaxStream = 64
	// ClientClosedStreamIndex is the index of a closed stream.
	ClientClosedStreamIndex = -1
)

// Stream represents a single stream within the Occamy protocol.
type Stream struct {
	Index       int
	data        interface{}
	HandlerAck  func()
	HandlerBlob func()
	HandlerEnd  func()
}

// NewStreamFromUser allocates a new stream. An arbitrary index is
// automatically assigned if no previously-allocated stream is available
// for use.
func NewStreamFromUser(u *User) *Stream {
	streamIndex := u.poolStream.Next()
	s := &u.outputStreams[streamIndex]
	s.Index = streamIndex * 2
	s.data = nil
	s.HandlerAck = nil
	s.HandlerBlob = nil
	s.HandlerEnd = nil
	return s
}

// FreeToUser Returns the given stream to the pool of available streams,
// such that it can be reused by any subsequent call to NewStreamFromUser().
func (s *Stream) FreeToUser(u *User) {
	u.poolStream.Free(s.Index / 2)
	s.Index = ClientClosedStreamIndex
}

// NewStreamFromClient allocates a new stream. An arbitrary index is
// automatically assigned if no previously-allocated stream is available
// for use.
func NewStreamFromClient(c *Client) *Stream {
	streamIndex := c.poolStream.Next()
	s := &c.outputStreams[streamIndex]
	s.Index = streamIndex*2 + 1
	s.data = nil
	s.HandlerAck = nil
	s.HandlerBlob = nil
	s.HandlerEnd = nil
	return s
}

// FreeToClient returns the given stream to the pool of available
// streams, such that it can be reused by any subsequent call to
// NewStreamFromClient().
func (s *Stream) FreeToClient(c *Client) {
	c.poolStream.Free((s.Index - 1) / 2)
	s.Index = ClientClosedStreamIndex
}
