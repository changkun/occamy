// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of the static channels and the clipboard. They run the
// client against rdptest's server on the loopback interface, which joins
// the channels the client asks for and reassembles what it sends on
// them.

// live is a client running against rdptest's server.
type live struct {
	c    *Client
	conn *rdptest.Conn
	out  *screen
	done chan error
}

// connectLive connects a client with cfg to srv, TLS without NLA unless
// cfg asks otherwise, and returns it and the server's connection.
func connectLive(t *testing.T, srv rdptest.Server, cfg Config) (*Client, *rdptest.Conn, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type accepted struct {
		c   *rdptest.Conn
		err error
	}
	conns := make(chan accepted, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			conns <- accepted{nil, err}
			return
		}
		c, err := srv.Accept(nc)
		if err != nil {
			nc.Close()
		}
		conns <- accepted{c, err}
	}()
	_, fp, err := rdptest.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protocols == 0 {
		cfg.Protocols = ProtocolSSL
	}
	cfg.TLS = TLS{Pins: []string{fp}}
	if cfg.Logger == nil {
		cfg.Logger = quiet
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, cerr := Connect(ctx, nc, cfg)
	a := <-conns
	if a.c != nil {
		t.Cleanup(func() { a.c.Close() })
	}
	if cerr != nil {
		return nil, a.c, cerr
	}
	if a.err != nil {
		c.Close()
		t.Fatalf("the server: %v", a.err)
	}
	return c, a.c, nil
}

// runLive connects as connectLive does and runs the client into a
// screen until the test ends.
func runLive(t *testing.T, srv rdptest.Server, cfg Config) *live {
	t.Helper()
	c, conn, err := connectLive(t, srv, cfg)
	if err != nil {
		t.Fatal(err)
	}
	l := &live{c: c, conn: conn, out: &screen{}, done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { l.done <- c.Run(ctx, l.out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-l.done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})
	return l
}

// send sends a CLIPRDR message from the server.
func (l *live) send(t *testing.T, msgs ...[]byte) {
	t.Helper()
	for _, m := range msgs {
		if err := l.conn.SendChannel("cliprdr", m); err != nil {
			t.Fatal(err)
		}
	}
}

// next returns the client's next CLIPRDR message.
func (l *live) next(t *testing.T) rdptest.Cliprdr {
	t.Helper()
	l.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		m, err := l.conn.Next()
		if err != nil {
			t.Fatalf("reading from the client: %v", err)
		}
		if m.Channel == "" {
			continue
		}
		if m.Channel != "cliprdr" {
			t.Fatalf("a message on %q", m.Channel)
		}
		msg, err := rdptest.ParseCliprdr(m.Data)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
}

// quiet checks that the client sends nothing for a while.
func (l *live) quiet(t *testing.T) {
	t.Helper()
	l.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for {
		m, err := l.conn.Next()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if !m.Empty() {
			t.Fatalf("the client sent %+v", m)
		}
	}
}

// want checks a CLIPRDR message's type and flags.
func want(t *testing.T, m rdptest.Cliprdr, typ, flags uint16) {
	t.Helper()
	if m.Type != typ || m.Flags != flags {
		t.Fatalf("CLIPRDR message %d with flags %#x (% x), want %d with %#x", m.Type, m.Flags, m.Data, typ, flags)
	}
}

// formats checks a Format List's format IDs.
func formats(t *testing.T, m rdptest.Cliprdr, long bool, ids ...uint32) {
	t.Helper()
	want(t, m, rdptest.CBFormatList, 0)
	got, err := rdptest.FormatIDs(m.Data, long)
	if err != nil || !slices.Equal(got, ids) {
		t.Fatalf("formats %v, %v; want %v", got, err, ids)
	}
}

// waitClipboard waits until the screen has n clipboards and returns
// them.
func (l *live) waitClipboard(t *testing.T, n int) []string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if got := l.out.clipboards(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("clipboards %q, want %d", l.out.clipboards(), n)
		}
	}
}

// ready runs the initialization: the server's capabilities and Monitor
// Ready, the client's capabilities and first Format List, which it
// returns.
func (l *live) ready(t *testing.T, long bool) rdptest.Cliprdr {
	t.Helper()
	l.send(t, rdptest.ClipCaps(long), rdptest.MonitorReady())
	caps := l.next(t)
	want(t, caps, rdptest.CBClipCaps, 0)
	if got, err := rdptest.LongNames(caps.Data); err != nil || !got {
		t.Fatalf("client capabilities: long names %v, %v", got, err)
	}
	return l.next(t)
}

var bothWays = Config{Clipboard: ClipboardFromServer | ClipboardToServer}

// The client names the clipboard channel, joins the MCS channel the
// server gives it, and does not ask for it unless configured to.
func TestChannelJoined(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	id, ok := l.conn.Channels["cliprdr"]
	if !ok || len(l.conn.Channels) != 1 || !slices.Contains(l.conn.Joined, id) {
		t.Errorf("channels %v, joined %v", l.conn.Channels, l.conn.Joined)
	}
	if want := []uint16{rdptest.User, rdptest.IOChannel, id}; !slices.Equal(l.conn.Joined, want) {
		t.Errorf("joined %v, want %v", l.conn.Joined, want)
	}
	plain := runLive(t, rdptest.Server{}, Config{})
	if len(plain.conn.Channels) != 0 || len(plain.conn.Joined) != 2 {
		t.Errorf("without the clipboard: channels %v, joined %v", plain.conn.Channels, plain.conn.Joined)
	}
}

// The MCS channels a server gives the static channels are checked: no
// more than asked for, none taken twice or by the I/O channel. One the
// server gives 0 is not joined.
func TestChannelAssigned(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []uint16
		err  string
	}{
		{"more than asked", []uint16{1004, 1005}, "listed 2 static channels"},
		{"the I/O channel", []uint16{rdptest.IOChannel}, "taken"},
		{"none", []uint16{}, ""},
		{"zero", []uint16{0}, ""},
	} {
		c, conn, err := connectLive(t, rdptest.Server{ChannelIDs: tc.ids}, bothWays)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: %v, want %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(conn.Joined) != 2 || c.clipCh.id != 0 {
			t.Errorf("%s: joined %v", tc.name, conn.Joined)
		}
		if err := c.SetClipboard([]byte("x")); err != nil {
			t.Errorf("%s: SetClipboard: %v", tc.name, err)
		}
		c.Close()
	}
	sd := []byte{0x03, 0x0c, 0, 0, 0xeb, 0x03, 32, 0}
	binary.LittleEndian.PutUint16(sd[2:], uint16(len(sd)+64))
	sd = append(sd, make([]byte, 64)...)
	if _, err := parseServerData(sd); err == nil || !strings.Contains(err.Error(), "32 static channels") {
		t.Errorf("32 channels in the server's network data: %v", err)
	}
	c := &Client{channels: []*vchannel{{name: "a"}, {name: "b"}}, ioChannel: 1003}
	if err := c.assignChannels([]uint16{1004, 1004}); err == nil {
		t.Error("two channels on one MCS channel")
	}
}

// The clipboard's initialization: the client answers the server's
// capabilities and Monitor Ready with its own, long format names, and an
// empty Format List; the owner's text that came before goes in it.
func TestClipboardInit(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.quiet(t) // nothing before Monitor Ready
	formats(t, l.ready(t, true), true)
	l.quiet(t)

	l = runLive(t, rdptest.Server{}, bothWays)
	if err := l.c.SetClipboard([]byte("early")); err != nil {
		t.Fatal(err)
	}
	l.quiet(t)
	formats(t, l.ready(t, false), false, rdptest.CFUnicodeText, rdptest.CFText)
}

// The owner's text: a Format List of CF_UNICODETEXT and CF_TEXT, then
// the text in the format the server asks for, with CR LF line ends, and
// a failure for other formats.
func TestClipboardToServer(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	text := "héllo\nwörld €\r\nend\r"
	if err := l.c.SetClipboard([]byte(text)); err != nil {
		t.Fatal(err)
	}
	formats(t, l.next(t), true, rdptest.CFUnicodeText, rdptest.CFText)
	l.send(t, rdptest.CliprdrPDU(rdptest.CBFormatListResponse, rdptest.CBResponseOK, nil))

	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	m := l.next(t)
	want(t, m, rdptest.CBFormatDataResponse, rdptest.CBResponseOK)
	if got, err := rdptest.FromUnicodeText(m.Data); err != nil || got != "héllo\r\nwörld €\r\nend\r\n" {
		t.Errorf("CF_UNICODETEXT %q, %v", got, err)
	}
	l.send(t, rdptest.FormatDataRequest(rdptest.CFText))
	m = l.next(t)
	want(t, m, rdptest.CBFormatDataResponse, rdptest.CBResponseOK)
	if want := "h\xe9llo\r\nw\xf6rld ?\r\nend\r\n\x00"; string(m.Data) != want {
		t.Errorf("CF_TEXT %q, want %q", m.Data, want)
	}
	l.send(t, rdptest.FormatDataRequest(99))
	want(t, l.next(t), rdptest.CBFormatDataResponse, rdptest.CBResponseFail)

	// The same text again is not announced again while the server's
	// clipboard is the client's; after the server's own, it is.
	if err := l.c.SetClipboard([]byte(text)); err != nil {
		t.Fatal(err)
	}
	l.quiet(t)
	l.send(t, rdptest.FormatList(true))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	if err := l.c.SetClipboard([]byte(text)); err != nil {
		t.Fatal(err)
	}
	formats(t, l.next(t), true, rdptest.CFUnicodeText, rdptest.CFText)
	// No text, no formats; a request then fails.
	if err := l.c.SetClipboard(nil); err != nil {
		t.Fatal(err)
	}
	formats(t, l.next(t), true)
	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatDataResponse, rdptest.CBResponseFail)
}

// The server's text: the client acknowledges its Format List, asks for
// CF_UNICODETEXT, or CF_TEXT where the list has only that, and gives the
// text to its Output with LF line ends. The locks of the clipboard's
// data, which xrdp sends, and other PDUs for files are ignored.
func TestClipboardFromServer(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	lock := []byte{1, 0, 0, 0} // clipDataId
	l.send(t, rdptest.CliprdrPDU(10, 0, lock), rdptest.CliprdrPDU(11, 0, lock))
	l.send(t, rdptest.FormatList(true, 49161, rdptest.CFText, rdptest.CFUnicodeText, 16))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	m := l.next(t)
	want(t, m, rdptest.CBFormatDataRequest, 0)
	if binary.LittleEndian.Uint32(m.Data) != rdptest.CFUnicodeText {
		t.Fatalf("request for format % x", m.Data)
	}
	l.send(t, rdptest.FormatDataResponse(true, rdptest.UnicodeText("one\r\ntwo\rthree\n😀")))
	if got := l.waitClipboard(t, 1); got[0] != "one\ntwo\nthree\n😀" {
		t.Errorf("clipboard %q", got[0])
	}
	if n := l.c.Stats().Ignored["clipboard PDUs"]; n != 2 {
		t.Errorf("%d clipboard PDUs ignored, want the 2 locks", n)
	}

	// Short format names, and CF_TEXT alone, as Latin-1.
	l = runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, false)
	l.send(t, rdptest.FormatList(false, rdptest.CFText))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	if m := l.next(t); m.Type != rdptest.CBFormatDataRequest || binary.LittleEndian.Uint32(m.Data) != rdptest.CFText {
		t.Fatalf("%+v, want a request for CF_TEXT", m)
	}
	l.send(t, rdptest.FormatDataResponse(true, []byte("caf\xe9\r\n\x00junk")))
	if got := l.waitClipboard(t, 1); got[0] != "café\n" {
		t.Errorf("clipboard %q", got[0])
	}

	// A list without text is acknowledged and asked nothing of; one that
	// does not parse is refused.
	l.send(t, rdptest.FormatList(false, 49161))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	l.send(t, rdptest.CliprdrPDU(rdptest.CBFormatList, 0, []byte{13, 0, 0}))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseFail)
	l.quiet(t)
}

// One request awaits its answer at a time. An answer to a request made
// for an older announcement is dropped, and the newest announcement is
// asked for once it comes; answers nobody asked for, and failures, are
// dropped.
func TestClipboardStale(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	l.send(t, rdptest.FormatDataResponse(true, rdptest.UnicodeText("unasked")))
	l.send(t, rdptest.FormatList(true, rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	want(t, l.next(t), rdptest.CBFormatDataRequest, 0)
	l.send(t, rdptest.FormatList(true, rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	l.quiet(t) // no second request while the first awaits its answer
	l.send(t, rdptest.FormatDataResponse(true, rdptest.UnicodeText("old")))
	want(t, l.next(t), rdptest.CBFormatDataRequest, 0)
	l.send(t, rdptest.FormatDataResponse(false, nil))
	l.send(t, rdptest.FormatList(true, rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	want(t, l.next(t), rdptest.CBFormatDataRequest, 0)
	l.send(t, rdptest.FormatDataResponse(true, rdptest.UnicodeText("new")))
	if got := l.waitClipboard(t, 1); !slices.Equal(got, []string{"new"}) {
		t.Errorf("clipboards %q, want only the newest", got)
	}
	if n := l.c.Stats().Ignored["clipboard answers"]; n != 3 {
		t.Errorf("%d answers dropped, want 3", n)
	}
}

// Each direction is its own: without ClipboardFromServer the client asks
// nothing of the server's announcements, and without ClipboardToServer it
// announces nothing and fails the server's requests.
func TestClipboardOneWay(t *testing.T) {
	l := runLive(t, rdptest.Server{}, Config{Clipboard: ClipboardToServer})
	l.ready(t, true)
	l.send(t, rdptest.FormatList(true, rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
	l.quiet(t)

	l = runLive(t, rdptest.Server{}, Config{Clipboard: ClipboardFromServer})
	if err := l.c.SetClipboard([]byte("secret")); err != nil {
		t.Fatal(err)
	}
	formats(t, l.ready(t, true), true)
	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatDataResponse, rdptest.CBResponseFail)
}

// Text beyond MaxClipboard is cut at a character either way, and a
// message on the channel beyond its bound is kept up to it.
func TestClipboardBounds(t *testing.T) {
	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	big := strings.Repeat("é", MaxClipboard/2) + "x" // MaxClipboard+1 bytes
	if err := l.c.SetClipboard([]byte(big)); err != nil {
		t.Fatal(err)
	}
	formats(t, l.next(t), true, rdptest.CFUnicodeText, rdptest.CFText)
	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	m := l.next(t)
	if got, err := rdptest.FromUnicodeText(m.Data); err != nil || got != big[:MaxClipboard] {
		t.Errorf("%d bytes sent of %d, %v; want %d", len(got), len(big), err, MaxClipboard)
	}

	// The server's text: 2^22+1 characters of UTF-16, of which the client
	// keeps those that fit MaxClipboard bytes of UTF-8.
	l.send(t, rdptest.FormatList(true, rdptest.CFUnicodeText))
	l.next(t)
	l.next(t)
	l.send(t, rdptest.FormatDataResponse(true, rdptest.UnicodeText(strings.Repeat("a", MaxClipboard+1))))
	if got := l.waitClipboard(t, 1); len(got[0]) != MaxClipboard {
		t.Errorf("a clipboard of %d bytes, want %d", len(got[0]), MaxClipboard)
	}
	// More than the channel keeps: the client keeps the first
	// maxCliprdrMessage bytes and cuts the text there, at a character.
	l.send(t, rdptest.FormatList(true, rdptest.CFUnicodeText))
	l.next(t)
	l.next(t)
	l.send(t, rdptest.FormatDataResponse(true, rdptest.UnicodeText(strings.Repeat("€", MaxClipboard+10))))
	got := l.waitClipboard(t, 2)[1]
	if n := utf8.RuneCountInString(got); n != MaxClipboard/3 || len(got) > MaxClipboard {
		t.Errorf("a clipboard of %d characters in %d bytes", n, len(got))
	}
}

// The chunks of a message on a channel: the client reassembles them
// whatever their size, drops a chunk without its first, and fails on
// chunks that do not add up to the length they declared or that are
// compressed. It sends its own in chunks of at most 1,600 bytes.
func TestChannelChunks(t *testing.T) {
	for _, size := range []int{1, 7, rdptest.ChunkSize, 1 << 15} {
		l := runLive(t, rdptest.Server{}, bothWays)
		id := l.conn.Channels["cliprdr"]
		var chunks [][]byte
		chunks = append(chunks, rdptest.ChannelChunks(id, rdptest.ClipCaps(true), size)...)
		chunks = append(chunks, rdptest.ChannelChunks(id, rdptest.MonitorReady(), size)...)
		if err := l.conn.Write(chunks...); err != nil {
			t.Fatal(err)
		}
		want(t, l.next(t), rdptest.CBClipCaps, 0)
		formats(t, l.next(t), true)
	}

	l := runLive(t, rdptest.Server{}, bothWays)
	l.ready(t, true)
	if err := l.c.SetClipboard(bytes.Repeat([]byte("x"), 100_000)); err != nil {
		t.Fatal(err)
	}
	l.next(t)
	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	l.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		m, err := l.conn.Next() // rdptest fails on a chunk over 1,600 bytes
		if err != nil {
			t.Fatal(err)
		}
		if m.Channel != "" {
			if want := (200_002 + 8 + rdptest.ChunkSize - 1) / rdptest.ChunkSize; m.Chunks != want {
				t.Errorf("%d chunks, want %d", m.Chunks, want)
			}
			break
		}
	}

	id := uint16(0)
	for _, tc := range []struct {
		name   string
		chunks func() [][]byte
		err    string
	}{
		{"orphan", func() [][]byte {
			return [][]byte{rdptest.ChannelChunk(id, 8, rdptest.ChannelLast, rdptest.MonitorReady())}
		}, ""},
		{"longer than declared", func() [][]byte {
			return [][]byte{rdptest.ChannelChunk(id, 4, rdptest.ChannelFirst|rdptest.ChannelLast, rdptest.MonitorReady())}
		}, "longer than the 4 bytes"},
		{"shorter than declared", func() [][]byte {
			return [][]byte{rdptest.ChannelChunk(id, 9, rdptest.ChannelFirst|rdptest.ChannelLast, rdptest.MonitorReady())}
		}, "declared 9"},
		{"compressed", func() [][]byte {
			return [][]byte{rdptest.ChannelChunk(id, 8, rdptest.ChannelFirst|rdptest.ChannelLast|rdptest.ChannelCompressed, rdptest.MonitorReady())}
		}, "compressed"},
	} {
		l := runLive(t, rdptest.Server{}, bothWays)
		id = l.conn.Channels["cliprdr"]
		if err := l.conn.Write(tc.chunks()...); err != nil {
			t.Fatal(err)
		}
		if tc.err == "" {
			l.quiet(t)
			if n := l.c.Stats().Ignored["orphan channel chunks"]; n != 1 {
				t.Errorf("%s: ignored %v", tc.name, l.c.Stats().Ignored)
			}
			continue
		}
		select {
		case err := <-l.done:
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: Run returned %v, want %q", tc.name, err, tc.err)
			}
			l.done <- err
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Run did not return", tc.name)
		}
	}
}

// A server that asks for the clipboard again and again without reading
// the answers ends the session, rather than make the client hold them.
func TestClipboardRequestsBounded(t *testing.T) {
	c, clock := testClock(1024, 768)
	clock.err = errPaced
	c.cfg.Clipboard = bothWays.Clipboard
	c.chio.kick = make(chan struct{}, 1)
	c.clipCh = &vchannel{name: "cliprdr", limit: maxCliprdrMessage}
	var err error
	for i := 0; i <= maxDataRequests && err == nil; i++ {
		err = c.cliprdrMessage(nil, rdptest.FormatDataRequest(rdptest.CFUnicodeText), false)
	}
	if err == nil || len(c.clip.provide) != maxDataRequests {
		t.Errorf("%d requests held, %v", len(c.clip.provide), err)
	}
}

// The server's text is charged against the decoding rate, a tile at
// least, so that announcing it again and again makes the client wait.
func TestClipboardPaced(t *testing.T) {
	c, clock := testClock(64, 64)
	c.cfg.Clipboard = bothWays.Clipboard
	c.chio.kick = make(chan struct{}, 1)
	c.clipCh = &vchannel{name: "cliprdr", limit: maxCliprdrMessage}
	out := &screen{}
	burst := 2*64*64 + (bitmapPad+1)*(bitmapPad+1)
	n := 0
	for ; clock.slept == 0 && n < 100; n++ {
		c.clip.requested = cfUnicodeText
		if err := c.cliprdrMessage(out, rdptest.FormatDataResponse(true, rdptest.UnicodeText("x")), false); err != nil {
			t.Fatal(err)
		}
	}
	if want := burst/pointerCost + 1; n != want {
		t.Errorf("waited after %d clipboards, want %d", n, want)
	}
}

// What the server says on the channel is bounded, whatever it says: no
// panic, and clipboards of valid UTF-8 within MaxClipboard (screen
// checks).
func FuzzCliprdr(f *testing.F) {
	for _, m := range [][]byte{
		rdptest.ClipCaps(true), rdptest.ClipCaps(false), rdptest.MonitorReady(),
		rdptest.FormatList(true, 13, 1), rdptest.FormatList(false, 1),
		rdptest.FormatDataRequest(13), rdptest.FormatDataRequest(1),
		rdptest.FormatDataResponse(true, rdptest.UnicodeText("a\r\nb😀")),
		rdptest.FormatDataResponse(true, []byte{0x3d, 0xd8, 'a', 0}),
		rdptest.FormatDataResponse(false, nil),
	} {
		f.Add(m, uint32(cfUnicodeText), true)
	}
	f.Fuzz(func(t *testing.T, msg []byte, requested uint32, truncated bool) {
		c := testClient(64, 64)
		c.cfg.Clipboard = bothWays.Clipboard
		c.chio.kick = make(chan struct{}, 1)
		c.clipCh = &vchannel{name: "cliprdr", id: 1004, limit: maxCliprdrMessage}
		c.clip.requested = requested
		out := &screen{}
		if err := c.cliprdrMessage(out, msg, truncated); err != nil && !errors.Is(err, errPaced) {
			return
		}
		if err := c.SetClipboard(msg); err != nil {
			t.Fatal(err)
		}
		c.clip.provide = append(c.clip.provide, cfUnicodeText, cfText, requested)
		if err := c.writeDue(); err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzChannel feeds the static channel chunks of what fuzz data says,
// each a header and data, to the client's reassembly, which must keep
// within its bound whatever comes.
func FuzzChannel(f *testing.F) {
	seed := func(msgs ...[]byte) []byte {
		var b []byte
		for _, m := range msgs {
			for _, ch := range rdptest.ChannelChunks(1004, m, 5) {
				p := ch[7+7:] // after the TPKT, X.224 and Send Data Indication
				if ch[13]&0x80 != 0 {
					p = p[1:]
				}
				b = binary.LittleEndian.AppendUint16(b, uint16(len(p)))
				b = append(b, p...)
			}
		}
		return b
	}
	f.Add(seed(rdptest.ClipCaps(true), rdptest.MonitorReady(), rdptest.FormatList(true, 13)))
	f.Add(seed(rdptest.FormatDataResponse(true, rdptest.UnicodeText("hello"))))
	f.Fuzz(func(t *testing.T, data []byte) {
		c := testClient(64, 64)
		c.cfg.Clipboard = bothWays.Clipboard
		c.chio.kick = make(chan struct{}, 1)
		vc := &vchannel{name: "cliprdr", id: 1004, limit: 64, handle: (*Client).cliprdrMessage}
		c.channels = []*vchannel{vc}
		c.clipCh = vc
		c.clip.requested = cfUnicodeText
		out := &screen{}
		for len(data) >= 2 {
			n := min(int(binary.LittleEndian.Uint16(data)), len(data)-2)
			p := data[2 : 2+n]
			data = data[2+n:]
			if err := c.otherChannel(out, 1004, p); err != nil {
				return
			}
			if len(vc.buf) > vc.limit || vc.got > vc.total && vc.partial {
				t.Fatalf("kept %d bytes of %d, limit %d", len(vc.buf), vc.got, vc.limit)
			}
		}
	})
}

// A big clipboard goes in writes of chunksPerWrite chunks, so that input
// waits for one of them, not for all.
func TestChannelWrites(t *testing.T) {
	conn := &countConn{}
	c := &Client{t: &transport{conn: conn}, userID: rdptest.User}
	vc := &vchannel{name: "cliprdr", id: 1004}
	msg := bytes.Repeat([]byte("x"), 100*chunkSize+1)
	if err := c.sendChannel(vc, msg); err != nil {
		t.Fatal(err)
	}
	if want := (101 + chunksPerWrite - 1) / chunksPerWrite; conn.writes != want {
		t.Errorf("%d writes, want %d", conn.writes, want)
	}
	// Reassembled, the chunks are the message: each a TPKT, an X.224
	// Data TPDU and an MCS Send Data Request, whose PER length counts the
	// channel PDU header and data that follow.
	var got []byte
	chunks := 0
	for b := conn.buf.Bytes(); len(b) > 0; chunks++ {
		frame := b[:binary.BigEndian.Uint16(b[2:])]
		b = b[len(frame):]
		p := frame[7+6:]
		n := int(p[0])
		if n&0x80 != 0 {
			n, p = int(binary.BigEndian.Uint16(p)&0x7fff), p[2:]
		} else {
			p = p[1:]
		}
		if n != len(p) || n > 8+chunkSize {
			t.Fatalf("a PDU of %d bytes declares %d", len(p), n)
		}
		flags := binary.LittleEndian.Uint32(p[4:])
		if first, last := chunks == 0, len(b) == 0; (flags&chanFlagFirst != 0) != first || (flags&chanFlagLast != 0) != last {
			t.Errorf("chunk %d: flags %#x", chunks, flags)
		}
		if total := binary.LittleEndian.Uint32(p); int(total) != len(msg) {
			t.Errorf("chunk %d: length %d", chunks, total)
		}
		got = append(got, p[8:]...)
	}
	if !bytes.Equal(got, msg) || chunks != 101 {
		t.Errorf("reassembled %d bytes in %d chunks, want %d in 101", len(got), chunks, len(msg))
	}
	if err := c.sendChannel(&vchannel{name: "none"}, msg); err != nil || conn.writes != (101+chunksPerWrite-1)/chunksPerWrite {
		t.Errorf("a channel the server gave no MCS channel: %v", err)
	}
}

// The conversions of text, either way.
func TestClipboardText(t *testing.T) {
	for _, tc := range []struct{ in, utf16 string }{
		{"a\nb", "a\r\nb"},
		{"a\r\nb", "a\r\nb"},
		{"a\rb", "a\r\nb"},
		{"\xff", "\ufffd"},
		{"😀", "😀"},
	} {
		got, err := rdptest.FromUnicodeText(unicodeText([]byte(tc.in)))
		if err != nil || got != tc.utf16 {
			t.Errorf("unicodeText(%q) = %q, %v; want %q", tc.in, got, err, tc.utf16)
		}
	}
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{rdptest.UnicodeText("a\r\nb\rc\n"), "a\nb\nc\n"},
		{[]byte{0x3d, 0xd8}, ""},                           // a high surrogate cut off
		{[]byte{0x3d, 0xd8, 'a', 0}, "\ufffda"},            // unpaired
		{[]byte{0x00, 0xdc, 'a', 0}, "\ufffda"},            // a low surrogate alone
		{[]byte{'a', 0, 0, 0, 'b', 0}, "a"},                // up to the NUL
		{[]byte{'a', 0, 'b'}, "a"},                         // an odd byte
		{[]byte{0x3d, 0xd8, 0x00, 0xde}, "😀"},              // a pair
		{[]byte{0x3d, 0xd8, 0x00, 0x00, 'b', 0}, "\ufffd"}, // a pair cut by the NUL
	} {
		if got := string(fromUnicodeText(tc.in)); got != tc.want {
			t.Errorf("fromUnicodeText(% x) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := string(fromANSIText([]byte("a\r\nb\xe9\r\x00c"))); got != "a\nbé\n" {
		t.Errorf("fromANSIText: %q", got)
	}
	if got := ansiText([]byte("€é\n")); string(got) != "?\xe9\r\n\x00" {
		t.Errorf("ansiText: %q", got)
	}
	if got := clipboardText([]byte(strings.Repeat("€", MaxClipboard/3) + "€")); len(got) != MaxClipboard-MaxClipboard%3 {
		t.Errorf("clipboardText kept %d bytes", len(got))
	}
	if clipboardText([]byte{}) != nil {
		t.Error("empty text is not none")
	}
	if got := fromUnicodeText(rdptest.UnicodeText(strings.Repeat("€", MaxClipboard))); len(got) > MaxClipboard || !utf8.Valid(got) {
		t.Errorf("fromUnicodeText kept %d bytes", len(got))
	}
	if got := fromANSIText(bytes.Repeat([]byte{0xe9}, MaxClipboard)); len(got) != MaxClipboard {
		t.Errorf("fromANSIText kept %d bytes", len(got))
	}
}

// Format lists, long and short.
func TestTextFormat(t *testing.T) {
	for _, tc := range []struct {
		list []byte
		long bool
		want uint32
		ok   bool
	}{
		{rdptest.FormatList(true, 1, 13)[8:], true, 13, true},
		{rdptest.FormatList(true, 1, 7)[8:], true, 1, true},
		{rdptest.FormatList(false, 13, 1)[8:], false, 13, true},
		{rdptest.FormatList(false, 49161)[8:], false, 0, true},
		{nil, true, 0, true},
		{[]byte{13, 0, 0, 0, 'a', 0}, true, 0, false},
		{make([]byte, 35), false, 0, false},
		{[]byte{13, 0, 0}, true, 0, false},
	} {
		if got, ok := textFormat(tc.list, tc.long); got != tc.want || ok != tc.ok {
			t.Errorf("textFormat(% x, %v) = %d, %v; want %d, %v", tc.list, tc.long, got, ok, tc.want, tc.ok)
		}
	}
	if _, err := parseClipCaps([]byte{1, 0, 0, 0, 1, 0, 2, 0}); err == nil {
		t.Error("a capability set of length 2")
	}
	if long, err := parseClipCaps(rdptest.ClipCaps(true)[8:]); !long || err != nil {
		t.Errorf("parseClipCaps: %v, %v", long, err)
	}
}

// What is due goes out in the protocol's order, whichever goroutine
// writes it: the capabilities before the Format List, though the owner's
// text comes while Monitor Ready is being answered.
func TestClipboardOrder(t *testing.T) {
	for range 20 {
		l := runLive(t, rdptest.Server{}, bothWays)
		l.send(t, rdptest.ClipCaps(true), rdptest.MonitorReady())
		if err := l.c.SetClipboard([]byte("race")); err != nil {
			t.Fatal(err)
		}
		want(t, l.next(t), rdptest.CBClipCaps, 0)
		m := l.next(t)
		want(t, m, rdptest.CBFormatList, 0)
		if ids, _ := rdptest.FormatIDs(m.Data, true); len(ids) == 0 {
			// The first list went before the text, and the second has it.
			formats(t, l.next(t), true, rdptest.CFUnicodeText, rdptest.CFText)
		}
	}
}
