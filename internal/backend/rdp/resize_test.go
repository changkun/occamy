// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"image"
	"testing"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// Resize through Display Control, against rdptest's server.

// dvc returns the backend's next DRDYNVC PDU, failing on anything else
// but Suppress Output and the PDUs of a reactivation.
func dvc(t *testing.T, c *rdptest.Conn) rdptest.DVC {
	t.Helper()
	for {
		m := next(t, c)
		if m.Channel == "" && len(m.Events) == 0 {
			continue // Suppress Output, or the finalization of a reactivation
		}
		if m.Channel != "drdynvc" {
			t.Fatalf("%+v, want a DRDYNVC PDU", m)
		}
		d, err := rdptest.ParseDVC(m.Data)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
}

// openDisplay opens Display Control as the channel id, and sends its
// capabilities.
func openDisplay(t *testing.T, c *rdptest.Conn, id uint32) {
	t.Helper()
	if err := c.SendDVC(rdptest.DVCCapsPDU(3)); err != nil {
		t.Fatal(err)
	}
	if d := dvc(t, c); d.Cmd != rdptest.DVCCaps {
		t.Fatalf("%+v, want the capabilities", d)
	}
	if err := c.SendDVC(rdptest.DVCCreateRequest(id, rdptest.DisplayControl)); err != nil {
		t.Fatal(err)
	}
	if d := dvc(t, c); d.Cmd != rdptest.DVCCreate || d.Status != 0 {
		t.Fatalf("%+v, want Display Control accepted", d)
	}
	if err := c.SendDVC(rdptest.DVCDataPDU(id, rdptest.DisplayCaps(16, 8192, 8192))); err != nil {
		t.Fatal(err)
	}
}

// A viewer's Resize goes to the server as a monitor layout, and the
// server's reactivation at that size resizes the session; while nobody
// can see, the backend tells the server again after it reactivates.
func TestRunResize(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	sink := &backendtest.Sink{}
	b := newBackend(t, s.config(t))
	b.Demand(backend.Demand{Visible: true})
	start(t, b, sink)
	c := s.accept(t)
	const id = 3
	openDisplay(t, c, id)
	// A side of zero or less asks for nothing.
	for _, ev := range []backend.InputEvent{backend.Resize{}, backend.Resize{Width: 1280}, backend.Resize{Height: 720}, backend.Resize{Width: -1, Height: 720}} {
		if err := b.Input(ev); err != nil {
			t.Fatal(err)
		}
	}
	quietFor(t, c)
	if err := b.Input(backend.Resize{Width: 1280, Height: 720}); err != nil {
		t.Fatal(err)
	}
	d := dvc(t, c)
	ms, err := rdptest.ParseMonitorLayout(d.Data)
	if d.Cmd != rdptest.DVCData || d.Channel != id || err != nil || len(ms) != 1 || ms[0].Width != 1280 || ms[0].Height != 720 {
		t.Fatalf("%+v (%+v, %v), want a layout of 1280×720", d, ms, err)
	}
	frames := append([][]byte{rdptest.DeactivateAll(), rdptest.DemandActive(1280, 720, rdptest.DefaultInputFlags)}, rdptest.Finalization()...)
	if err := c.Write(frames...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session at 1280×720", func() bool { return sink.Size() == image.Pt(1280, 720) })

	b.Demand(backend.Demand{Visible: false})
	suppress := func() {
		t.Helper()
		for {
			m := next(t, c)
			if m.Type2 != rdptest.PDU2SuppressOutput {
				continue
			}
			if allow, err := rdptest.SuppressOutput(m.Body); err != nil || allow {
				t.Fatalf("Suppress Output allowing %v, %v; want it suppressing", allow, err)
			}
			return
		}
	}
	suppress()
	frames = append([][]byte{rdptest.DeactivateAll(), rdptest.DemandActive(1024, 768, rdptest.DefaultInputFlags)}, rdptest.Finalization()...)
	if err := c.Write(frames...); err != nil {
		t.Fatal(err)
	}
	suppress()
	quietFor(t, c)
}
