// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"slices"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of the wait for the server to take the owner's text. The
// server of the live ones is rdptest's, driven by the test: one whose
// clipboard process has gone is one the test leaves silent, as xrdp
// without xrdp-chansrv is.

// listOK is a server's answer to a Format List.
func listOK() []byte {
	return rdptest.CliprdrPDU(rdptest.CBFormatListResponse, rdptest.CBResponseOK, nil)
}

// open runs the initialization and answers the client's first Format
// List. It returns once the client has read the answer, which it shows
// by acknowledging a list of the server's sent after: an answer still on
// its way would end the wait for the text a test gives the client next.
func (l *live) open(t *testing.T) {
	t.Helper()
	l.ready(t, true)
	l.send(t, listOK(), rdptest.FormatList(true))
	want(t, l.next(t), rdptest.CBFormatListResponse, rdptest.CBResponseOK)
}

// waitStatus waits until the client has told its Output of the clipboard
// as many things as want has, and checks them.
func (l *live) waitStatus(t *testing.T, want ...bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		got := l.out.clipboardStatus()
		if len(got) >= len(want) {
			if !slices.Equal(got, want) {
				t.Fatalf("clipboard status %v, want %v", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clipboard status %v, want %v", got, want)
		}
	}
}

// paste gives the client text, and checks that it announces it.
func (l *live) paste(t *testing.T, text string) {
	t.Helper()
	if err := l.c.SetClipboard([]byte(text)); err != nil {
		t.Fatal(err)
	}
	formats(t, l.next(t), true, rdptest.CFUnicodeText, rdptest.CFText)
}

// gives checks that the client hands over text when the server asks.
func (l *live) gives(t *testing.T, text string) {
	t.Helper()
	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	m := l.next(t)
	want(t, m, rdptest.CBFormatDataResponse, rdptest.CBResponseOK)
	if got, err := rdptest.FromUnicodeText(m.Data); err != nil || got != text {
		t.Fatalf("the client gave %q, %v; want %q", got, err, text)
	}
}

// A server that goes silent, as xrdp does once its clipboard process has
// gone, leaves a Format List unanswered: the text is dropped and the
// Output told, each time. When the server's process comes back, what was
// dropped stays dropped, the Output hears, and the same text then goes.
func TestClipboardUnanswered(t *testing.T) {
	cfg := bothWays
	cfg.ClipboardWait = 50 * time.Millisecond
	l := runLive(t, rdptest.Server{}, cfg)
	l.open(t)

	l.paste(t, "lost")
	l.waitStatus(t, false)
	l.paste(t, "lost too")
	l.waitStatus(t, false, false)

	l.send(t, rdptest.ClipCaps(true), rdptest.MonitorReady())
	want(t, l.next(t), rdptest.CBClipCaps, 0)
	formats(t, l.next(t), true)
	l.waitStatus(t, false, false, true)

	l.paste(t, "lost")
	l.send(t, listOK())
	l.gives(t, "lost")
	time.Sleep(2 * cfg.ClipboardWait)
	l.waitStatus(t, false, false, true)
}

// An answer after the wait says that the server is there again, but the
// text it answers for is gone, as the Output was told.
func TestClipboardAnswerTooLate(t *testing.T) {
	cfg := bothWays
	cfg.ClipboardWait = 50 * time.Millisecond
	l := runLive(t, rdptest.Server{}, cfg)
	l.open(t)
	l.paste(t, "late")
	l.waitStatus(t, false)
	l.send(t, listOK())
	l.waitStatus(t, false, true)
	l.send(t, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	want(t, l.next(t), rdptest.CBFormatDataResponse, rdptest.CBResponseFail)
}

// Without the server's process, Monitor Ready never comes either: text
// that waits for it is dropped, and a Monitor Ready that comes after
// does not bring it back.
func TestClipboardNeverReady(t *testing.T) {
	cfg := bothWays
	cfg.ClipboardWait = 50 * time.Millisecond
	l := runLive(t, rdptest.Server{}, cfg)
	if err := l.c.SetClipboard([]byte("early")); err != nil {
		t.Fatal(err)
	}
	l.waitStatus(t, false)
	formats(t, l.ready(t, true), true)
	l.waitStatus(t, false, true)
}

// A slow server that does answer is not a silent one, whichever of its
// two answers comes first: xrdp asks for the text before it answers the
// Format List.
func TestClipboardSlowAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(t *testing.T, l *live)
	}{
		{"Format List Response", func(t *testing.T, l *live) { l.send(t, listOK()) }},
		{"Format Data Request", func(t *testing.T, l *live) { l.gives(t, "slow") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := bothWays
			cfg.ClipboardWait = 3 * time.Second
			l := runLive(t, rdptest.Server{}, cfg)
			l.open(t)
			l.paste(t, "slow")
			time.Sleep(cfg.ClipboardWait / 3)
			tc.answer(t, l)
			time.Sleep(cfg.ClipboardWait)
			if got := l.out.clipboardStatus(); len(got) != 0 {
				t.Errorf("clipboard status %v after a slow answer", got)
			}
			l.gives(t, "slow")
		})
	}
}

// The wait's bounds, by a clock of the test's.
func TestClipboardWaitBounds(t *testing.T) {
	const wait = time.Minute
	type fixture struct {
		c     *Client
		clock *fakeClock
		out   *screen
	}
	// pass has the channel writer take a look, as the timer has it do.
	pass := func(t *testing.T, f fixture) {
		t.Helper()
		if err := f.c.writeDue(); err != nil {
			t.Fatal(err)
		}
		f.c.tellClipboard(f.out)
	}
	paste := func(t *testing.T, f fixture, text string) {
		t.Helper()
		if err := f.c.SetClipboard([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	message := func(t *testing.T, f fixture, m []byte) {
		t.Helper()
		if err := f.c.cliprdrMessage(f.out, m, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, f fixture)
		want []bool
		text string // the owner's text in the end
	}{
		{name: "a Format List Response at the last moment", text: "text", run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			f.clock.advance(wait - 1)
			pass(t, f)
			message(t, f, listOK())
			f.clock.advance(10 * wait)
			pass(t, f)
		}},
		{name: "a Format Data Request at the last moment", text: "text", run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			f.clock.advance(wait - 1)
			pass(t, f)
			message(t, f, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
			f.clock.advance(10 * wait)
			pass(t, f)
		}},
		{name: "no answer by the end", want: []bool{false}, run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			f.clock.advance(wait)
			pass(t, f)
		}},
		{name: "the server's own Format List is no answer", want: []bool{false}, run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			message(t, f, rdptest.FormatList(true, rdptest.CFUnicodeText))
			f.clock.advance(wait)
			pass(t, f)
		}},
		{name: "newer text does not put off the end", want: []bool{false}, run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			f.clock.advance(wait - 1)
			paste(t, f, "newer")
			f.clock.advance(1)
			pass(t, f)
		}},
		{name: "no text leaves nothing to wait for", run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			paste(t, f, "")
			f.clock.advance(wait)
			pass(t, f)
		}},
		{name: "text told of is told of again", want: []bool{false, false}, run: func(t *testing.T, f fixture) {
			paste(t, f, "text")
			f.clock.advance(wait)
			pass(t, f)
			paste(t, f, "again")
			f.clock.advance(wait)
			pass(t, f)
		}},
		{name: "Monitor Ready starts the wait again", want: []bool{false}, run: func(t *testing.T, f fixture) {
			f.c.clip.ready = false
			paste(t, f, "text")
			f.clock.advance(wait - 1)
			pass(t, f)
			message(t, f, rdptest.MonitorReady())
			pass(t, f) // the Format List goes
			f.clock.advance(wait - 1)
			pass(t, f)
			if got := f.out.clipboardStatus(); len(got) != 0 {
				t.Fatalf("clipboard status %v before the second wait ended", got)
			}
			f.clock.advance(1)
			pass(t, f)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, clock := testClock(64, 64)
			c.cfg.Clipboard = bothWays.Clipboard
			c.cfg.ClipboardWait = wait
			c.chio.kick = make(chan struct{}, 1)
			c.clipCh = &vchannel{name: "cliprdr", id: 1004, limit: maxCliprdrMessage}
			c.clip.ready = true
			t.Cleanup(func() {
				if c.clip.timer != nil {
					c.clip.timer.Stop()
				}
			})
			f := fixture{c, clock, &screen{}}
			tc.run(t, f)
			if got := f.out.clipboardStatus(); !slices.Equal(got, tc.want) {
				t.Errorf("clipboard status %v, want %v", got, tc.want)
			}
			if string(c.clip.text) != tc.text {
				t.Errorf("the owner's text is %q, want %q", c.clip.text, tc.text)
			}
		})
	}
}
