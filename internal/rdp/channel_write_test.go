// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of what the client owes on the clipboard channel and how it
// writes it.

// clipClient returns a client whose clipboard channel is open, writing
// to conn, for tests that call its reader's and writer's steps in turn.
func clipClient(conn net.Conn) *Client {
	c, clock := testClock(1024, 768)
	clock.err = errPaced
	c.t = &transport{conn: conn}
	c.cfg.Clipboard = bothWays.Clipboard
	c.chio.kick = make(chan struct{}, 1)
	c.clipCh = &vchannel{name: "cliprdr", id: 1004, limit: maxCliprdrMessage}
	return c
}

// listResponses counts the Format List Responses that accept and that
// refuse in b, frames of one-chunk channel messages.
func listResponses(t *testing.T, b []byte) (ok, fail int) {
	t.Helper()
	for len(b) > 0 {
		frame := b[:binary.BigEndian.Uint16(b[2:])]
		b = b[len(frame):]
		p := frame[7+6:] // the TPKT, X.224 and MCS headers, then a PER length
		if p[0]&0x80 != 0 {
			p = p[2:]
		} else {
			p = p[1:]
		}
		m, err := rdptest.ParseCliprdr(p[8:]) // after the channel PDU header
		if err != nil || m.Type != rdptest.CBFormatListResponse {
			t.Fatalf("a message of type %d, %v; want Format List Responses", m.Type, err)
		}
		if m.Flags == rdptest.CBResponseOK {
			ok++
		} else {
			fail++
		}
	}
	return ok, fail
}

// owed waits until the client owes the server n Format List Responses.
func (l *live) owed(t *testing.T, n int64) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		l.c.chio.mu.Lock()
		owed := l.c.clip.listOK + l.c.clip.listFail
		l.c.chio.mu.Unlock()
		if owed == n {
			return
		}
		select {
		case err := <-l.done:
			l.done <- err
			t.Fatalf("Run returned %v with %d responses owed", err, owed)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d responses owed, want %d", owed, n)
		}
	}
}

// formatLists returns n Format Lists from the server on l's clipboard
// channel, each a frame of its own.
func (l *live) formatLists(n int) [][]byte {
	one := rdptest.ChannelChunks(l.conn.Channels["cliprdr"], rdptest.FormatList(true), rdptest.ChunkSize)[0]
	return slices.Repeat([][]byte{one}, n)
}

// The Format List Responses the client owes are a count, which no number
// of lists ends the session for: each pass of the writer sends
// listResponsesPerPass of them, in writes of chunksPerWrite chunks, and
// has the writer look again for the rest.
func TestClipboardListResponsesPaced(t *testing.T) {
	conn := &countConn{}
	c := clipClient(conn)
	bad := cliprdrPDU(cbFormatList, 0, []byte{1, 2, 3}) // refused: not a list of short names
	const lists = 1000
	for n := range lists {
		list := rdptest.FormatList(false)
		if n%2 == 1 {
			list = bad
		}
		if err := c.cliprdrMessage(nil, list, false); err != nil {
			t.Fatalf("list %d: %v", n, err)
		}
	}
	if c.clip.listOK != lists/2 || c.clip.listFail != lists/2 {
		t.Fatalf("%d and %d responses owed, want %d of each", c.clip.listOK, c.clip.listFail, lists/2)
	}
	<-c.chio.kick // the reader's
	if err := c.writeDue(); err != nil {
		t.Fatal(err)
	}
	if owed := c.clip.listOK + c.clip.listFail; owed != lists-listResponsesPerPass || conn.writes != listResponsesPerPass/chunksPerWrite || len(c.chio.kick) != 1 {
		t.Fatalf("after a pass, %d owed, %d writes, %d wake-ups; want %d, %d and 1", owed, conn.writes, len(c.chio.kick), lists-listResponsesPerPass, listResponsesPerPass/chunksPerWrite)
	}
	passes := 1
	for ; len(c.chio.kick) == 1; passes++ {
		<-c.chio.kick
		if err := c.writeDue(); err != nil {
			t.Fatal(err)
		}
	}
	ok, fail := listResponses(t, conn.buf.Bytes())
	if want := (lists + listResponsesPerPass - 1) / listResponsesPerPass; passes != want || ok != lists/2 || fail != lists/2 {
		t.Errorf("%d passes, %d responses that accept and %d that refuse; want %d passes, %d of each", passes, ok, fail, want, lists/2)
	}
}

// What a pass of the writer allocates does not grow with the responses
// owed.
func TestClipboardListResponsesAllocs(t *testing.T) {
	c := clipClient(discardConn{})
	pass := func(owed int64) func() {
		return func() {
			c.clip.listOK = owed
			if err := c.writeDue(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-c.chio.kick:
			default:
			}
		}
	}
	few := testing.AllocsPerRun(5, pass(listResponsesPerPass+1))
	if many := testing.AllocsPerRun(5, pass(1_000_000)); many != few {
		t.Errorf("a pass allocates %v times with a million responses owed, %v with %d", many, few, listResponsesPerPass+1)
	}
}

// liveHeap returns the bytes of the heap in use after a full collection.
// The second collection frees what the first left in sync.Pool's victim
// caches, such as crypto/tls's record buffers.
func liveHeap() int64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

// A server that floods Format Lists and does not read keeps its session:
// the client owes it a count, which takes no memory for each list, and
// the writer waits for the server to read.
//
// The heap is measured from after a first batch of lists, so that what
// the connection allocates once is not counted: the TLS read buffer,
// which grows to a full record's size the first time one arrives, and
// the batch itself.
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
	batch := l.formatLists(1000)
	flood := func(n int) {
		for range n / len(batch) {
			if err := l.conn.Write(batch...); err != nil {
				t.Fatal(err)
			}
		}
	}
	flood(len(batch))
	l.owed(t, int64(len(batch)))
	before := liveHeap()
	const lists = 100_000
	flood(lists)
	l.owed(t, int64(len(batch)+lists))
	if grew := liveHeap() - before; grew > lists {
		t.Errorf("the heap grew by %d bytes with %d more responses owed", grew, lists)
	}
	l.c.chio.wmu.Unlock()
	held = false
	select {
	case err := <-l.done:
		l.done <- err
		t.Fatalf("Run returned %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

// A server that reads gets a response to each list of a burst in one
// write, however long the burst: the writer sends them a pass at a time.
// The writer is held while the burst comes, so that the reader's
// wake-ups come to one, and only the writer's own have it look again.
func TestClipboardListBurst(t *testing.T) {
	for _, lists := range []int{listResponsesPerPass + 1, 1000} {
		t.Run(fmt.Sprint(lists), func(t *testing.T) {
			l := runLive(t, rdptest.Server{}, bothWays)
			l.ready(t, true)
			l.c.chio.wmu.Lock()
			held := true
			defer func() {
				if held {
					l.c.chio.wmu.Unlock()
				}
			}()
			if err := l.conn.Write(l.formatLists(lists)...); err != nil {
				t.Fatal(err)
			}
			l.owed(t, int64(lists))
			l.c.chio.wmu.Unlock()
			held = false
			for range lists {
				want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
			}
			select {
			case err := <-l.done:
				l.done <- err
				t.Fatalf("Run returned %v", err)
			default:
			}
		})
	}
}

// A paste held up as the server reads it slowly, while the server's user
// copies, has the client owe a response to each copy, which go once the
// paste is written, a pass at a time.
func TestClipboardPasteHeldUp(t *testing.T) {
	g := &gateConn{held: make(chan struct{}), gate: make(chan struct{})}
	c := clipClient(g)
	c.clip.ready = true
	c.clip.text = bytes.Repeat([]byte("x"), 60<<10) // 77 chunks of UTF-16: three writes
	c.clip.provide = []uint32{cfUnicodeText}
	paste := make(chan error, 1)
	go func() { paste <- c.writeDue() }()
	<-g.held
	const copies = 1000
	for range copies {
		if err := c.cliprdrMessage(nil, rdptest.FormatList(false), false); err != nil {
			t.Fatal(err)
		}
	}
	close(g.gate)
	if err := <-paste; err != nil {
		t.Fatal(err)
	}
	passes := 0
	for ; len(c.chio.kick) == 1; passes++ {
		<-c.chio.kick
		if err := c.writeDue(); err != nil {
			t.Fatal(err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// Each pass of 64 responses takes two writes, as does the last of 40.
	if want := (copies + listResponsesPerPass - 1) / listResponsesPerPass; passes != want || len(g.sizes) != 3+2*want || c.clip.listOK != 0 {
		t.Errorf("%d passes, %d writes, %d owed; want %d passes and %d writes", passes, len(g.sizes), c.clip.listOK, want, 3+2*want)
	}
}

// A server that reads a paste slowly while its user copies keeps its
// session, and gets the paste and a response to each copy.
func TestClipboardSlowPaste(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	if err := l.c.SetClipboard(bytes.Repeat([]byte("x"), 1<<20)); err != nil {
		t.Fatal(err)
	}
	want(t, l.next(t), rdptest.CBFormatList, 0)
	l.send(t, rdptest.FormatDataRequest(cfUnicodeText))
	var pasted atomic.Bool
	var responses atomic.Int64
	readErr := make(chan error, 1)
	go func() {
		l.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		for {
			m, err := l.conn.Next()
			if err == nil && m.Channel == "cliprdr" {
				var c rdptest.Cliprdr
				if c, err = rdptest.ParseCliprdr(m.Data); c.Type == rdptest.CBFormatDataResponse {
					pasted.Store(true)
				} else if c.Type == rdptest.CBFormatListResponse {
					responses.Add(1)
				}
			}
			if err != nil {
				readErr <- err
				return
			}
			time.Sleep(100 * time.Microsecond) // a chunk at a time, slowly
		}
	}()
	copies, most := 0, int64(0)
	for !pasted.Load() {
		select {
		case err := <-readErr:
			t.Fatalf("reading the paste: %v", err)
		default:
		}
		l.send(t, rdptest.FormatList(true))
		copies++
		time.Sleep(2 * time.Millisecond)
		l.c.chio.mu.Lock()
		most = max(most, l.c.clip.listOK+l.c.clip.listFail)
		l.c.chio.mu.Unlock()
	}
	for deadline := time.Now().Add(30 * time.Second); responses.Load() < int64(copies); time.Sleep(5 * time.Millisecond) {
		select {
		case err := <-readErr:
			t.Fatalf("reading the responses: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d responses to %d copies", responses.Load(), copies)
		}
	}
	select {
	case err := <-l.done:
		l.done <- err
		t.Fatalf("Run returned %v", err)
	default:
	}
	t.Logf("%d copies during the paste, at most %d responses owed at once", copies, most)
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

// brokenConn fails every write, counting them.
type brokenConn struct {
	net.Conn
	writes int
}

var errBroken = errors.New("the connection is broken")

func (b *brokenConn) Write([]byte) (int, error) {
	b.writes++
	return 0, errBroken
}

// After a write fails, writeDue writes nothing more and returns that
// failure: whether the write was the last of what was due or one within
// a message of more than chunksPerWrite chunks.
func TestChannelWriteFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		text int
	}{
		{"the last write", 0},
		{"a write within a message", 60 << 10},
	} {
		conn := &brokenConn{}
		c := clipClient(conn)
		c.clip.ready, c.clip.listDue = true, true
		if tc.text > 0 {
			c.clip.text = bytes.Repeat([]byte("x"), tc.text)
			c.clip.provide = []uint32{cfUnicodeText}
		}
		if err := c.writeDue(); !errors.Is(err, errBroken) || conn.writes != 1 {
			t.Fatalf("%s: writeDue returned %v after %d writes, want the failure after 1", tc.name, err, conn.writes)
		}
		c.clip.listDue = true // more is due
		if err := c.writeDue(); !errors.Is(err, errBroken) || conn.writes != 1 {
			t.Errorf("%s: then writeDue returned %v after %d writes, want the failure and no more", tc.name, err, conn.writes)
		}
	}
}

// Once a write failed, what the client owes the server waits for nothing:
// what it owes reaching a bound reports the failure, not the bound.
func TestOwedAfterWriteFailed(t *testing.T) {
	c := clipClient(&brokenConn{})
	c.cfg.Resize = true
	c.dynCh = &vchannel{name: "drdynvc", id: 1005}
	if err := c.drdynvcMessage(nil, rdptest.DVCCreateRequest(1, "nobody"), false); err != nil {
		t.Fatal(err)
	}
	if err := c.writeDue(); !errors.Is(err, errBroken) {
		t.Fatalf("writeDue: %v, want the write's failure", err)
	}
	request := cliprdrPDU(cbFormatDataRequest, 0, binary.LittleEndian.AppendUint32(nil, cfUnicodeText))
	for _, tc := range []struct {
		name  string
		bound int
		msg   func(n int) error
	}{
		{"dynamic channel answers", maxDynvcAnswers, func(n int) error {
			return c.drdynvcMessage(nil, rdptest.DVCCreateRequest(uint32(n), "nobody"), false)
		}},
		{"data requests", maxDataRequests, func(int) error {
			return c.cliprdrMessage(nil, request, false)
		}},
	} {
		var err error
		n := 0
		for ; n <= tc.bound && err == nil; n++ {
			err = tc.msg(n)
		}
		if n != tc.bound+1 || !errors.Is(err, errBroken) {
			t.Errorf("%s: after %d, %v, want the write's failure", tc.name, n, err)
		}
	}
}
