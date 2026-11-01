// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

// A viewer that falls behind leaves the frames of another on the same
// session as they were, whether its link is a thousandth as fast or it
// stops acknowledging: the fast viewer sees as much of a screen that
// moves at 100 Hz as it does alone. The clock is synctest's, so this
// shows that nothing waits for the viewer behind, not what encoding for
// it costs in CPU.
func TestSlowViewer(t *testing.T) {
	for name, codecs := range map[string][]protocol.Codec{
		"raw":    rawOnly.Codecs,
		"codecs": {protocol.CodecDeflate, protocol.CodecRaw, protocol.CodecWebP, protocol.CodecJPEG},
	} {
		t.Run(name, func(t *testing.T) {
			hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: codecs}}
			// frames counts what the fast viewer and the joined one get
			// in two seconds, after half a second to settle: less time
			// than the viewer timeout that would drop the one behind.
			frames := func(t *testing.T, join func(*testing.T, *rig) *client) (fast, behind int) {
				t.Helper()
				synctest.Test(t, func(t *testing.T) {
					r := newRig(t, backendtest.NewScreen(320, 256, 10*time.Millisecond), 50e6, time.Millisecond, hello)
					var joined *client
					if join != nil {
						joined = join(t, r)
					}
					time.Sleep(500 * time.Millisecond)
					fast = r.cl.frameCount()
					if joined != nil {
						behind = joined.frameCount()
					}
					time.Sleep(2 * time.Second)
					fast = r.cl.frameCount() - fast
					if joined != nil {
						behind = joined.frameCount() - behind
					}
				})
				return fast, behind
			}
			alone, _ := frames(t, nil)
			if alone < 50 {
				t.Fatalf("a viewer alone got %d frames in two seconds, too few to compare", alone)
			}
			for _, tt := range []struct {
				name string
				join func(*testing.T, *rig) *client
			}{
				{"slow link", func(t *testing.T, r *rig) *client {
					return r.join(t, 50e3, 100*time.Millisecond, hello)
				}},
				{"no acks", func(t *testing.T, r *rig) *client {
					c := r.join(t, 50e6, time.Millisecond, hello)
					c.mu.Lock()
					c.noAcks = true
					c.mu.Unlock()
					return c
				}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					fast, behind := frames(t, tt.join)
					t.Logf("alone %d frames; beside the viewer behind %d, which got %d", alone, fast, behind)
					if behind*2 > fast {
						t.Errorf("the viewer meant to fall behind got %d frames to the fast one's %d", behind, fast)
					}
					if fast*10 < alone*9 {
						t.Errorf("the fast viewer got %d frames beside the one behind, %d alone", fast, alone)
					}
				})
			}
		})
	}
}
