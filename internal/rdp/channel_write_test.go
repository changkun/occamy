// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of what the client owes on the clipboard channel and how it
// writes it.

// The Format List Responses the client owes are bounded: one list past
// maxListResponses ends the session. What is due goes in writes of
// chunksPerWrite chunks, the small messages together, not a write each.
func TestClipboardListResponsesBounded(t *testing.T) {
	conn := &countConn{}
	c, clock := testClock(1024, 768)
	clock.err = errPaced
	c.t = &transport{conn: conn}
	c.cfg.Clipboard = bothWays.Clipboard
	c.chio.kick = make(chan struct{}, 1)
	c.clipCh = &vchannel{name: "cliprdr", id: 1004, limit: maxCliprdrMessage}
	bad := cliprdrPDU(cbFormatList, 0, []byte{1, 2, 3}) // refused: not a list of short names
	var err error
	n := 0
	for ; n <= maxListResponses && err == nil; n++ {
		list := rdptest.FormatList(false)
		if n%2 == 1 {
			list = bad
		}
		err = c.cliprdrMessage(nil, list, false)
	}
	if err == nil || !strings.Contains(err.Error(), "Format List Responses") || c.clip.listOK+c.clip.listFail != maxListResponses {
		t.Fatalf("after %d lists, %d and %d responses owed, %v", n, c.clip.listOK, c.clip.listFail, err)
	}
	if err := c.writeDue(); err != nil {
		t.Fatal(err)
	}
	if want := maxListResponses / chunksPerWrite; conn.writes != want {
		t.Errorf("%d responses in %d writes, want %d", maxListResponses, conn.writes, want)
	}
	// Written, they make room.
	if err := c.cliprdrMessage(nil, rdptest.FormatList(false), false); err != nil {
		t.Errorf("a list after the responses went: %v", err)
	}
}

// A server that floods Format Lists while the client cannot write ends
// the session, rather than having the client owe an answer to each.
func TestClipboardListFlood(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	l.c.chio.wmu.Lock() // the channel writer is held up
	held := true
	defer func() {
		if held {
			l.c.chio.wmu.Unlock()
		}
	}()
	for range maxListResponses + 1 {
		l.send(t, rdptest.FormatList(true))
	}
	// Ending the session, the client closes its side of the connection.
	l.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, err := l.conn.Next()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("the client did not end the session")
		}
		if err != nil {
			break
		}
	}
	l.c.chio.wmu.Unlock()
	held = false
	select {
	case err := <-l.done:
		if err == nil || !strings.Contains(err.Error(), "Format List Responses") {
			t.Errorf("Run returned %v, want the responses' bound", err)
		}
		l.done <- err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// gateConn records the size of each write, and holds the second until
// gate is closed, closing held when it does.
type gateConn struct {
	net.Conn
	mu    sync.Mutex
	sizes []int
	held  chan struct{}
	gate  chan struct{}
}

func (g *gateConn) Write(p []byte) (int, error) {
	g.mu.Lock()
	g.sizes = append(g.sizes, len(p))
	n := len(g.sizes)
	g.mu.Unlock()
	if n == 2 {
		close(g.held)
		<-g.gate
	}
	return len(p), nil
}

// The chunks of one message do not interleave with another's: a Format
// List that SetClipboard writes while the channel writer is halfway
// through a data response of several writes goes after all of it.
func TestChannelMessagesWhole(t *testing.T) {
	g := &gateConn{held: make(chan struct{}), gate: make(chan struct{})}
	c, clock := testClock(64, 64)
	clock.err = errPaced
	c.t = &transport{conn: g}
	c.cfg.Clipboard = bothWays.Clipboard
	c.chio.kick = make(chan struct{}, 1)
	c.clipCh = &vchannel{name: "cliprdr", id: 1004, limit: maxCliprdrMessage}
	c.clip.ready = true
	c.clip.text = bytes.Repeat([]byte("x"), 60<<10) // 77 chunks of UTF-16: three writes
	c.clip.provide = []uint32{cfUnicodeText}
	response := make(chan error, 1)
	go func() { response <- c.writeDue() }()
	<-g.held
	list := make(chan error, 1)
	go func() { list <- c.SetClipboard([]byte("y")) }()
	time.Sleep(50 * time.Millisecond) // time to write the list, were it let
	close(g.gate)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-list; err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sizes) != 4 || g.sizes[3] > 200 || slices.Min(g.sizes[:3]) < 1000 {
		t.Errorf("writes of %v bytes, want the response's three, then the Format List", g.sizes)
	}
}

// A CLIPRDR message whose length is wrong is dropped, as is one the
// channel cut, unless it is text, which is kept as far as it came.
func TestClipboardLength(t *testing.T) {
	c := testClient(64, 64)
	c.cfg.Clipboard = bothWays.Clipboard
	c.chio.kick = make(chan struct{}, 1)
	c.clipCh = &vchannel{name: "cliprdr", limit: maxCliprdrMessage}
	long := rdptest.FormatList(false, rdptest.CFUnicodeText)
	binary.LittleEndian.PutUint32(long[4:], uint32(len(long))) // #nosec G115 -- small; longer than its body
	for _, truncated := range []bool{false, true} {
		if err := c.cliprdrMessage(nil, long, truncated); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.Stats().Ignored["clipboard PDUs"]; n != 2 || c.clip.listOK != 0 || c.clip.request != 0 {
		t.Errorf("%d dropped, %d lists taken, request %d; want both dropped", n, c.clip.listOK, c.clip.request)
	}
	out := &screen{}
	text := rdptest.FormatDataResponse(true, rdptest.UnicodeText("cut"))
	binary.LittleEndian.PutUint32(text[4:], uint32(len(text))) // #nosec G115 -- small; longer than its body
	c.clip.requested = cfUnicodeText
	if err := c.cliprdrMessage(out, text, true); err != nil {
		t.Fatal(err)
	}
	if got := out.clipboards(); len(got) != 1 || got[0] != "cut" {
		t.Errorf("cut text: %q, want \"cut\"", got)
	}
	// Not cut by the channel, the same text's length is wrong.
	c.clip.requested = cfUnicodeText
	if err := c.cliprdrMessage(out, text, false); err != nil {
		t.Fatal(err)
	}
	if n := c.Stats().Ignored["clipboard PDUs"]; n != 3 || len(out.clipboards()) != 1 {
		t.Errorf("%d dropped, clipboards %q; want the text of a wrong length dropped", n, out.clipboards())
	}
}

// The clipboard channel keeps a CLIPRDR message of its header and two
// bytes of UTF-16 for each byte of MaxClipboard, and no more; and a
// channel the server gave no MCS channel has id 0, which no PDU is taken
// to be on.
func TestClipboardChannelBound(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	if got, want := l.c.clipCh.limit, 8+2*(4<<20); got != want || MaxClipboard != 4<<20 {
		t.Errorf("the clipboard channel keeps %d bytes, want %d", got, want)
	}
	c := &Client{channels: []*vchannel{{name: "cliprdr"}}}
	if vc := c.channelByID(0); vc != nil {
		t.Errorf("channel 0 is %q", vc.name)
	}
}
