// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"encoding/binary"
	"errors"
	"image"
	"net"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of DRDYNVC and Display Control, against rdptest's server as
// the clipboard's are.

var withResize = Config{Resize: true}

// dvc returns the client's next DRDYNVC PDU, skipping its other
// messages.
func (l *live) dvc(t *testing.T) rdptest.DVC {
	t.Helper()
	l.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		m, err := l.conn.Next()
		if err != nil {
			t.Fatalf("reading from the client: %v", err)
		}
		if m.Channel != "drdynvc" {
			continue
		}
		d, err := rdptest.ParseDVC(m.Data)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
}

// sendDVC sends DRDYNVC PDUs from the server.
func (l *live) sendDVC(t *testing.T, pdus ...[]byte) {
	t.Helper()
	if err := l.conn.SendDVC(pdus...); err != nil {
		t.Fatal(err)
	}
}

// quietDVC checks that the client sends nothing on DRDYNVC for a while.
func (l *live) quietDVC(t *testing.T) {
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
		if m.Channel == "drdynvc" {
			d, _ := rdptest.ParseDVC(m.Data)
			t.Fatalf("the client sent %+v", d)
		}
	}
}

// display opens the Display Control channel as id after the
// capabilities, and sends its capabilities.
func (l *live) display(t *testing.T, id uint32, monitors, a, b uint32) {
	t.Helper()
	l.sendDVC(t, rdptest.DVCCapsPDU(3))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCaps || d.Version != 2 {
		t.Fatalf("%+v, want capabilities of version 2", d)
	}
	l.sendDVC(t, rdptest.DVCCreateRequest(id, rdptest.DisplayControl))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCreate || d.Channel != id || d.Status != 0 {
		t.Fatalf("%+v, want the channel accepted", d)
	}
	l.sendDVC(t, rdptest.DVCDataPDU(id, rdptest.DisplayCaps(monitors, a, b)))
}

// layout returns the size of the client's next monitor layout on the
// channel id.
func (l *live) layout(t *testing.T, id uint32) image.Point {
	t.Helper()
	d := l.dvc(t)
	if d.Cmd != rdptest.DVCData || d.Channel != id {
		t.Fatalf("%+v, want data on channel %d", d, id)
	}
	ms, err := rdptest.ParseMonitorLayout(d.Data)
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	if len(ms) != 1 || m.Flags != 1 || m.Left != 0 || m.Top != 0 || m.DesktopScaleFactor != 100 || m.DeviceScaleFactor != 100 {
		t.Fatalf("layout %+v, want one primary monitor at the origin at 100%%", ms)
	}
	return image.Pt(int(m.Width), int(m.Height))
}

// reactivate has the server reactivate the session at size, as a server
// does to resize, and waits until the client has drawn it.
func (l *live) reactivate(t *testing.T, size image.Point) {
	t.Helper()
	frames := append([][]byte{rdptest.DeactivateAll(), rdptest.DemandActive(size.X, size.Y, rdptest.DefaultInputFlags)}, rdptest.Finalization()...)
	if err := l.conn.Write(frames...); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if w, h := l.c.Size(); w == size.X && h == size.Y {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client did not take the size %v", size)
		}
	}
}

// setLayoutEvery sets the pacing of monitor layouts, and has the client
// look at them again.
func (l *live) setLayoutEvery(every, retry time.Duration) {
	l.c.chio.mu.Lock()
	l.c.layoutEvery, l.c.layoutRetry = every, retry
	l.c.chio.mu.Unlock()
	l.c.due()
}

// The client asks for DRDYNVC only with Resize, answers the server's
// capabilities with version 2 at most, accepts Display Control and
// refuses other channels, a channel id that is open, and a second
// Display Control channel.
func TestDynvcChannels(t *testing.T) {
	if l := runLive(t, rdptest.Server{}, Config{}); len(l.conn.Channels) != 0 {
		t.Errorf("channels %v without Resize", l.conn.Channels)
	}
	for v, want := range map[int]int{1: 1, 2: 2, 3: 2, 4: 2} {
		l := runLive(t, rdptest.Server{}, withResize)
		l.sendDVC(t, rdptest.DVCCapsPDU(v))
		if d := l.dvc(t); d.Cmd != rdptest.DVCCaps || d.Version != want {
			t.Errorf("server version %d: %+v, want version %d", v, d, want)
		}
	}

	l := runLive(t, rdptest.Server{}, withResize)
	l.sendDVC(t, rdptest.DVCCapsPDU(2))
	l.dvc(t)
	for _, tc := range []struct {
		id     uint32
		name   string
		status uint32
	}{
		{1, "Microsoft::Windows::RDS::Graphics", 0xc0000001},
		{300, rdptest.DisplayControl, 0},
		{300, "rdpsnd", 0xc0000001},                 // the id is open
		{70000, rdptest.DisplayControl, 0xc0000001}, // one at a time
	} {
		l.sendDVC(t, rdptest.DVCCreateRequest(tc.id, tc.name))
		if d := l.dvc(t); d.Cmd != rdptest.DVCCreate || d.Channel != tc.id || d.Status != tc.status {
			t.Errorf("create %d %q: %+v, want status %#x", tc.id, tc.name, d, tc.status)
		}
	}
	if n := l.c.Stats().Ignored["refused dynamic channels"]; n != 3 {
		t.Errorf("%d channels refused, want 3", n)
	}
	// A layout goes on the channel's id, in the size the id needs.
	l.setLayoutEvery(0, time.Hour)
	l.sendDVC(t, rdptest.DVCDataPDU(300, rdptest.DisplayCaps(16, 8192, 8192)))
	l.c.RequestSize(1280, 720)
	if got := l.layout(t, 300); got != image.Pt(1280, 720) {
		t.Errorf("layout %v, want 1280×720", got)
	}
	// A close is answered with a close, and the channel may open again.
	l.sendDVC(t, rdptest.DVCClosePDU(300))
	if d := l.dvc(t); d.Cmd != rdptest.DVCClose || d.Channel != 300 {
		t.Errorf("%+v, want the close answered", d)
	}
	l.sendDVC(t, rdptest.DVCClosePDU(300)) // not open: ignored
	l.sendDVC(t, rdptest.DVCCreateRequest(70000, rdptest.DisplayControl))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCreate || d.Channel != 70000 || d.Status != 0 {
		t.Errorf("%+v, want Display Control open again", d)
	}
	// The size the desktop does not have yet goes on the new channel once
	// it has the server's capabilities.
	l.sendDVC(t, rdptest.DVCDataPDU(70000, rdptest.DisplayCaps(16, 8192, 8192)))
	if got := l.layout(t, 70000); got != image.Pt(1280, 720) {
		t.Errorf("layout %v, want 1280×720 again", got)
	}
}

// The client asks for the newest size once the server has sent its
// capabilities, at most one layout every layoutEvery, with the width
// made even and the sides clamped; not for the desktop's size, the last
// size asked for while the desktop has not changed, or a size beyond
// the server's area.
func TestDisplayControl(t *testing.T) {
	l := runLive(t, rdptest.Server{}, withResize)
	l.setLayoutEvery(300*time.Millisecond, layoutRetry)
	l.c.RequestSize(1280, 720) // before the channel: waits for it
	const id = 9
	l.display(t, id, 16, 8192, 8192)
	if got := l.layout(t, id); got != image.Pt(1280, 720) {
		t.Errorf("layout %v, want 1280×720", got)
	}

	// Paced: the newest of three wins, and goes after layoutEvery.
	begin := time.Now()
	l.c.RequestSize(1000, 700)
	l.c.RequestSize(1001, 9000)
	if got := l.layout(t, id); got != image.Pt(1000, 8192) {
		t.Errorf("layout %v, want the newest size, 1000×8192", got)
	}
	// The retry of 1280×720, due after layoutRetry, does not hold it back.
	if d := time.Since(begin); d < 200*time.Millisecond || d > 1500*time.Millisecond {
		t.Errorf("the second layout went after %v, want about 300 ms", d)
	}

	// While the desktop keeps its size, the size goes again after
	// layoutRetry, maxLayoutTries times in all. The desktop's own size
	// goes no more.
	l.setLayoutEvery(0, 20*time.Millisecond)
	for range maxLayoutTries - 1 {
		if got := l.layout(t, id); got != image.Pt(1000, 8192) {
			t.Fatalf("layout %v, want 1000×8192 again", got)
		}
	}
	l.quietDVC(t)
	if n := l.c.Stats().Ignored["sizes the server did not apply"]; n != 1 {
		t.Errorf("%d sizes given up, want 1", n)
	}
	// Asked for anew, the size goes again; the desktop's own size, no
	// more.
	l.setLayoutEvery(0, time.Second)
	l.c.RequestSize(1001, 9000)
	if got := l.layout(t, id); got != image.Pt(1000, 8192) {
		t.Fatalf("layout %v, want 1000×8192 asked for anew", got)
	}
	l.c.RequestSize(1024, 768)
	l.quietDVC(t)
	l.reactivate(t, image.Pt(800, 600))
	l.c.RequestSize(800, 600)
	l.quietDVC(t)
	// Once the desktop has the size, it goes no more.
	l.c.RequestSize(10, 10)
	if got := l.layout(t, id); got != image.Pt(200, 200) {
		t.Errorf("layout %v, want the least, 200×200", got)
	}
	l.setLayoutEvery(0, time.Hour) // no retry while the server reactivates
	l.reactivate(t, image.Pt(200, 200))
	l.setLayoutEvery(0, 0)
	l.quietDVC(t)

	// Beyond the server's area: one monitor of at most 640×480.
	l = runLive(t, rdptest.Server{}, withResize)
	l.display(t, id, 1, 640, 480)
	l.c.RequestSize(1280, 720)
	l.quietDVC(t)
	l.c.RequestSize(640, 480)
	if got := l.layout(t, id); got != image.Pt(640, 480) {
		t.Errorf("layout %v, want 640×480", got)
	}
	if n := l.c.Stats().Ignored["sizes beyond the server's area"]; n != 1 {
		t.Errorf("%d sizes refused, want 1", n)
	}

	// Without the channel, RequestSize does nothing.
	plain := runLive(t, rdptest.Server{}, Config{})
	plain.c.RequestSize(800, 600)
}

// A message on a dynamic channel comes in one Data PDU or in a Data
// First and Data PDUs, reassembled up to the channel's bound; a message
// over it is dropped, one over the length it declared ends the session,
// as does compressed data. Data on a channel that is not open is
// ignored.
func TestDynvcData(t *testing.T) {
	const id = 5
	l := runLive(t, rdptest.Server{}, withResize)
	l.sendDVC(t, rdptest.DVCCapsPDU(2))
	l.dvc(t)
	l.sendDVC(t, rdptest.DVCCreateRequest(id, rdptest.DisplayControl))
	l.dvc(t)
	l.setLayoutEvery(0, layoutRetry)
	// Over the bound, in one PDU or in several: dropped, so no
	// capabilities yet.
	big := append(rdptest.DisplayCaps(16, 8192, 8192), make([]byte, maxDisplayMessage)...)
	l.sendDVC(t, rdptest.DVCDataPDU(id, big))
	l.sendDVC(t, rdptest.DVCMessage(id, big, 1000)...)
	l.sendDVC(t, rdptest.DVCDataPDU(77, rdptest.DisplayCaps(16, 8192, 8192)))
	l.c.RequestSize(1280, 720)
	l.quietDVC(t)
	// In three PDUs: whole.
	l.sendDVC(t, rdptest.DVCMessage(id, rdptest.DisplayCaps(16, 8192, 8192), 7)...)
	if got := l.layout(t, id); got != image.Pt(1280, 720) {
		t.Errorf("layout %v, want 1280×720", got)
	}
	st := l.c.Stats().Ignored
	if st["dynamic channel messages over their bound"] != 2 || st["dynamic channel PDUs"] != 1 {
		t.Errorf("ignored %v", st)
	}

	for _, tc := range []struct {
		name string
		pdus [][]byte
		err  string
	}{
		{"longer than declared", [][]byte{rdptest.DVCDataFirstPDU(id, 4, []byte("12345"))}, "longer than the 4 bytes"},
		{"compressed", [][]byte{rdptest.DVCPDU(rdptest.DVCDataZ, 0, id, []byte{0xe0, 0})}, "compressed"},
		{"a name without its NUL", [][]byte{rdptest.DVCPDU(rdptest.DVCCreate, 0, 3, []byte("abc"))}, "without its NUL"},
		{"a name too long", [][]byte{rdptest.DVCCreateRequest(3, strings.Repeat("x", maxDynChannelName))}, "without its NUL"},
		{"version 0", [][]byte{{rdptest.DVCCaps << 4, 0, 0, 0}}, "version 0"},
		{"a length of size code 3", [][]byte{rdptest.DVCPDU(rdptest.DVCDataFirst, 3, id, []byte{1, 0, 0, 0, 0})}, "size code 3"},
	} {
		l := runLive(t, rdptest.Server{}, withResize)
		l.sendDVC(t, rdptest.DVCCapsPDU(2))
		l.dvc(t)
		l.sendDVC(t, rdptest.DVCCreateRequest(id, rdptest.DisplayControl))
		l.dvc(t)
		l.sendDVC(t, tc.pdus...)
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

// A server that opens channels faster than it reads the answers ends
// the session, rather than make the client hold them.
func TestDynvcAnswersBounded(t *testing.T) {
	c := testClient(64, 64)
	c.cfg.Resize = true
	c.chio.kick = make(chan struct{}, 1)
	c.dynCh = &vchannel{name: "drdynvc", id: 1005}
	var err error
	n := 0
	for ; n <= maxDynvcAnswers && err == nil; n++ {
		err = c.drdynvcMessage(nil, rdptest.DVCCreateRequest(uint32(n), "nobody"), false)
	}
	if err == nil || n != maxDynvcAnswers+1 || len(c.dvc.answers) != maxDynvcAnswers {
		t.Errorf("after %d requests, %d answers held, %v", n, len(c.dvc.answers), err)
	}
	// What is due goes, and makes room.
	if err := c.writeDue(); err != nil || len(c.dvc.answers) != 0 {
		t.Errorf("writeDue: %v, %d answers left", err, len(c.dvc.answers))
	}
	// A truncated static message is dropped.
	if err := c.drdynvcMessage(nil, rdptest.DVCCapsPDU(2), true); err != nil || c.dvc.version != 0 {
		t.Errorf("a truncated message: %v, version %d", err, c.dvc.version)
	}
}

// The server's capabilities bound the area of a layout: their product,
// saturated at maxPixels.
func TestDisplayCapsArea(t *testing.T) {
	for _, tc := range []struct {
		monitors, a, b uint32
		want           int64
	}{
		{16, 8192, 8192, maxPixels},
		{1, 640, 480, 640 * 480},
		{2, 640, 480, 2 * 640 * 480},
		{0xffffffff, 0xffffffff, 0xffffffff, maxPixels},
		{0, 8192, 8192, 0},
		{1 << 30, 1, 1, maxPixels},
	} {
		c := testClient(64, 64)
		c.chio.kick = make(chan struct{}, 1)
		if err := c.displayMessage(nil, rdptest.DisplayCaps(tc.monitors, tc.a, tc.b)); err != nil {
			t.Fatal(err)
		}
		if !c.disp.caps || c.disp.maxArea != tc.want {
			t.Errorf("caps %d×%d×%d: area %d, want %d", tc.monitors, tc.a, tc.b, c.disp.maxArea, tc.want)
		}
	}
	c := testClient(64, 64)
	if err := c.displayMessage(nil, rdptest.DisplayCaps(1, 2, 3)[:16]); err == nil {
		t.Error("a short capabilities PDU")
	}
	other := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 9), 8)
	if err := c.displayMessage(nil, other); err != nil || c.Stats().Ignored["Display Control PDUs"] != 1 {
		t.Errorf("another PDU: %v, %v", err, c.Stats().Ignored)
	}
}

// After a reactivation the client tells the server again that nobody
// can see, since the server's output state after one is its own.
func TestSuppressOutputAfterReactivation(t *testing.T) {
	l := runLive(t, rdptest.Server{}, Config{})
	if err := l.c.SuppressOutput(true); err != nil {
		t.Fatal(err)
	}
	suppressions := func(n int) {
		t.Helper()
		l.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for got := 0; got < n; {
			m, err := l.conn.Next()
			if err != nil {
				t.Fatalf("after %d of %d Suppress Output PDUs: %v", got, n, err)
			}
			if m.Type2 != rdptest.PDU2SuppressOutput {
				continue
			}
			if allow, err := rdptest.SuppressOutput(m.Body); err != nil || allow {
				t.Fatalf("Suppress Output allowing %v, %v", allow, err)
			}
			got++
		}
	}
	suppressions(1)
	l.reactivate(t, image.Pt(800, 600))
	suppressions(1)
}

// FuzzDynvc feeds DRDYNVC PDUs, each after its length, to a client that
// takes Display Control, and writes what is due after each: no panic,
// and the answers within their bound.
func FuzzDynvc(f *testing.F) {
	seed := func(pdus ...[]byte) []byte {
		var b []byte
		for _, p := range pdus {
			b = binary.LittleEndian.AppendUint16(b, uint16(len(p)))
			b = append(b, p...)
		}
		return b
	}
	f.Add(seed(rdptest.DVCCapsPDU(3), rdptest.DVCCreateRequest(7, rdptest.DisplayControl),
		rdptest.DVCDataPDU(7, rdptest.DisplayCaps(16, 8192, 8192))))
	f.Add(seed(append(rdptest.DVCMessage(7, rdptest.DisplayCaps(1, 640, 480), 5),
		rdptest.DVCCreateRequest(7, rdptest.DisplayControl), rdptest.DVCClosePDU(7))...))
	f.Fuzz(func(t *testing.T, data []byte) {
		c := testClient(1024, 768)
		c.cfg.Resize = true
		c.chio.kick = make(chan struct{}, 1)
		c.dynCh = &vchannel{name: "drdynvc", id: 1005}
		c.RequestSize(1280, 720)
		for len(data) >= 2 {
			n := min(int(binary.LittleEndian.Uint16(data)), len(data)-2)
			p := data[2 : 2+n]
			data = data[2+n:]
			if err := c.drdynvcMessage(nil, p, false); err != nil {
				return
			}
			if len(c.dvc.answers) > maxDynvcAnswers || len(c.dvc.open) > 1 {
				t.Fatalf("%d answers, %d channels", len(c.dvc.answers), len(c.dvc.open))
			}
			for _, dc := range c.dvc.open {
				if len(dc.buf) > dc.limit {
					t.Fatalf("kept %d bytes, limit %d", len(dc.buf), dc.limit)
				}
			}
			if err := c.writeDue(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
