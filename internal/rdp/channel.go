// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Static virtual channels, [MS-RDPBCGR] 1.3.3 and 2.2.6. The client
// names the channels it wants in its network data (CHANNEL_DEF), the
// server gives each one an MCS channel in the same order, and the client
// joins them. A message on a channel goes in chunks, each after a
// CHANNEL_PDU_HEADER that gives the length of the whole message and
// whether the chunk is its first or its last. Under TLS the chunks carry
// no security header.
//
// The client reassembles a channel's messages up to a bound of its own
// for each channel, keeps the first bytes of a longer message and drops
// the rest, and fails on chunks that do not add up to the length their
// first chunk declared.
//
// What the client sends on its channels is written by one goroutine at a
// time (writeDue): the reader only records what is due, so that it never
// waits for the server to read, and the chunks of one message never
// interleave with another message's on the same channel.

// CHANNEL_PDU_HEADER flags, [MS-RDPBCGR] 2.2.6.1.1.
const (
	chanFlagFirst      = 0x00000001
	chanFlagLast       = 0x00000002
	chanPacketCompress = 0x00200000
)

// CHANNEL_DEF options, [MS-RDPBCGR] 2.2.1.3.4.1, as FreeRDP sets them,
// but without compression, which the client does not offer.
const (
	chanOptionInitialized  = 0x80000000
	chanOptionEncryptRDP   = 0x40000000
	chanOptionShowProtocol = 0x00200000
)

const (
	// chunkSize is CHANNEL_CHUNK_LENGTH, the size of the chunks the client
	// sends and the size it asks the server to send (the virtual channel
	// capability set). A server takes chunks of at least this size
	// ([MS-RDPBCGR] 2.2.7.1.10).
	chunkSize = 1600
	// maxChannelDefs is CHANNEL_MAX_COUNT, the most static channels a
	// client may name and so the most a server's network data may list.
	maxChannelDefs = 31
	// chunksPerWrite bounds the chunks of one message written at once, so
	// that input written between them waits for at most about 50 KiB.
	chunksPerWrite = 32
)

// vchannel is a static virtual channel the client asked for.
type vchannel struct {
	name    string // at most 7 ASCII characters
	options uint32
	// limit is the most bytes of a message the client keeps; the rest is
	// dropped, and the handler told that the message was truncated.
	limit int
	// handle applies a message from the server. out is nil during the
	// connection sequence.
	handle func(c *Client, out Output, msg []byte, truncated bool) error

	id uint16 // the MCS channel the server gave, 0 if none

	// The message being reassembled, owned by the reader.
	buf     []byte
	total   int64 // the length its first chunk declared
	got     int64 // the bytes of its chunks so far, kept or not
	partial bool  // a first chunk came, and its last has not
}

// newChannels returns the static channels the configuration asks for.
func (c *Client) newChannels() []*vchannel {
	var chans []*vchannel
	if c.cfg.Clipboard != 0 {
		chans = append(chans, &vchannel{
			name:    "cliprdr",
			options: chanOptionInitialized | chanOptionEncryptRDP | chanOptionShowProtocol,
			limit:   maxCliprdrMessage,
			handle:  (*Client).cliprdrMessage,
		})
	}
	if c.cfg.Resize {
		chans = append(chans, &vchannel{
			name:    "drdynvc",
			options: chanOptionInitialized | chanOptionEncryptRDP,
			limit:   maxDrdynvcMessage,
			handle:  (*Client).drdynvcMessage,
		})
	}
	return chans
}

// assignChannels gives the client's channels the MCS channels the server
// listed for them, in the order the client named them. A server may list
// fewer, as FreeRDP accepts, but not more; a channel it gives the ID 0,
// or none, is not joined. The IDs must differ from each other and from
// the I/O and message channels, so that a message reaches one handler.
func (c *Client) assignChannels(ids []uint16) error {
	if len(ids) > len(c.channels) {
		return fmt.Errorf("rdp: the server listed %d static channels; the client asked for %d", len(ids), len(c.channels))
	}
	seen := map[uint16]bool{c.ioChannel: true}
	if c.msgChannel != 0 {
		seen[c.msgChannel] = true
	}
	for i, id := range ids {
		if id == 0 {
			continue
		}
		if seen[id] {
			return fmt.Errorf("rdp: the server gave static channel %q the MCS channel %d, which is taken", c.channels[i].name, id)
		}
		seen[id] = true
		c.channels[i].id = id
	}
	return nil
}

// channelByID returns the static channel with MCS channel id, or nil.
func (c *Client) channelByID(id uint16) *vchannel {
	for _, vc := range c.channels {
		if vc.id == id && id != 0 {
			return vc
		}
	}
	return nil
}

// channelChunk applies a chunk of a message on vc: the data of a Send
// Data Indication on its MCS channel.
func (c *Client) channelChunk(out Output, vc *vchannel, p []byte) error {
	r := reader{b: p}
	length := r.u32()
	flags := r.u32()
	if r.err != nil {
		return fmt.Errorf("rdp: %s channel PDU: %w", vc.name, r.err)
	}
	if flags&chanPacketCompress != 0 {
		return fmt.Errorf("rdp: compressed %s channel PDU, though the client offered no compression", vc.name)
	}
	data := r.b
	switch {
	case flags&chanFlagFirst != 0:
		if vc.partial {
			c.count(func(s *Stats) { s.Ignored["unfinished channel messages"]++ })
		}
		vc.buf, vc.total, vc.got, vc.partial = vc.buf[:0], int64(length), 0, true
	case !vc.partial:
		// A chunk without its first, which a server that follows
		// [MS-RDPBCGR] 3.1.5.2.2.1 does not send: dropped.
		c.count(func(s *Stats) { s.Ignored["orphan channel chunks"]++ })
		return nil
	}
	vc.got += int64(len(data))
	if vc.got > vc.total {
		vc.partial = false
		return fmt.Errorf("rdp: %s channel message longer than the %d bytes it declared", vc.name, vc.total)
	}
	if keep := vc.limit - len(vc.buf); keep > 0 {
		vc.buf = append(vc.buf, data[:min(len(data), keep)]...)
	}
	if flags&chanFlagLast == 0 {
		return nil
	}
	vc.partial = false
	if vc.got != vc.total {
		return fmt.Errorf("rdp: %s channel message of %d bytes, though it declared %d", vc.name, vc.got, vc.total)
	}
	err := vc.handle(c, out, vc.buf, int64(len(vc.buf)) < vc.total)
	if cap(vc.buf) > retainFrag {
		vc.buf = nil
	}
	return err
}

// chunk returns the frame of one chunk of a message of total bytes on
// the MCS channel id.
func (c *Client) chunk(id uint16, total int, first, last bool, data []byte) []byte {
	var flags uint32
	if first {
		flags |= chanFlagFirst
	}
	if last {
		flags |= chanFlagLast
	}
	var w writer
	w.u32(uint32(total)) // #nosec G115 -- messages stay below 4 GiB
	w.u32(flags)
	w.bytes(data)
	return tpkt(sendDataRequest(c.userID, id, w.b))
}

// sendChannel sends msg on vc in chunks of chunkSize, chunksPerWrite of
// them at a time (channelBatch).
func (c *Client) sendChannel(vc *vchannel, msg []byte) error {
	b := channelBatch{c: c}
	if err := b.add(vc, msg); err != nil {
		return err
	}
	return b.flush()
}

// channelBatch gathers the chunks of channel messages and writes them
// chunksPerWrite at a time: the small messages that are due share a
// write, and input written meanwhile waits for one write of chunks, not
// for a whole message. writeDue's lock keeps the chunks of other
// messages on the channels out.
type channelBatch struct {
	c      *Client
	frames [][]byte
}

// add appends msg on vc in chunks of chunkSize, and writes every
// chunksPerWrite of them.
func (b *channelBatch) add(vc *vchannel, msg []byte) error {
	if vc.id == 0 {
		return nil
	}
	for i := 0; ; i += chunkSize {
		end := min(i+chunkSize, len(msg))
		b.frames = append(b.frames, b.c.chunk(vc.id, len(msg), i == 0, end == len(msg), msg[i:end]))
		if len(b.frames) == chunksPerWrite {
			if err := b.flush(); err != nil {
				return err
			}
		}
		if end == len(msg) {
			return nil
		}
	}
}

// flush writes the chunks gathered, if any.
func (b *channelBatch) flush() error {
	if len(b.frames) == 0 {
		return nil
	}
	err := b.c.t.writeFrames(b.frames)
	clear(b.frames)
	b.frames = b.frames[:0]
	return err
}

// channelIO is the state of the client's channel output, which the reader
// and the caller of SetClipboard share with the goroutine that writes it.
type channelIO struct {
	mu   sync.Mutex // guards the state of the channels' protocols
	wmu  sync.Mutex // held by writeDue
	kick chan struct{}
	err  error // the error of a failed write, under wmu
}

// due tells the channel writer that output is due.
func (c *Client) due() {
	select {
	case c.chio.kick <- struct{}{}:
	default:
	}
}

// writeDue writes the channel output that is due. One call at a time
// writes, so that what is due goes out in the order the protocols need.
// After a write fails, nothing more is written.
func (c *Client) writeDue() error {
	c.chio.wmu.Lock()
	defer c.chio.wmu.Unlock()
	if c.chio.err != nil {
		return c.chio.err
	}
	now := c.clock()
	c.chio.mu.Lock()
	msgs := c.cliprdrDue(nil)
	msgs = c.dynvcDue(msgs, now)
	c.chio.mu.Unlock()
	b := channelBatch{c: c}
	for _, m := range msgs {
		if err := b.add(m.vc, m.encode()); err != nil {
			c.chio.err = err
			return err
		}
	}
	if err := b.flush(); err != nil {
		c.chio.err = err
		return err
	}
	return nil
}

// dueMessage is a message due on a channel. Its encoding waits until the
// channel state's lock is released.
type dueMessage struct {
	vc     *vchannel
	encode func() []byte
}

// channelWriter writes channel output whenever it is due, until ctx is
// done. A write that fails stops it; the reader then sees the connection
// fail, or Run's caller sees the next write fail.
func (c *Client) channelWriter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.chio.kick:
		}
		if err := c.writeDue(); err != nil {
			if !errors.Is(err, context.Canceled) {
				c.log.Debug("rdp: channel write failed", "err", err)
			}
			return
		}
	}
}
