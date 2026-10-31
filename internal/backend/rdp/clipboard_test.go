// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The clipboard through the backend, and NLA through rdptest's CredSSP
// acceptor.

// clip returns the next message from the backend on the clipboard
// channel, failing on anything else but Suppress Output.
func clip(t *testing.T, c *rdptest.Conn) rdptest.Cliprdr {
	t.Helper()
	for {
		m := next(t, c)
		if m.Type2 == rdptest.PDU2SuppressOutput {
			continue
		}
		if m.Channel != "cliprdr" {
			t.Fatalf("%+v, want a clipboard message", m)
		}
		msg, err := rdptest.ParseCliprdr(m.Data)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
}

// sendClip sends clipboard messages from the server.
func sendClip(t *testing.T, c *rdptest.Conn, msgs ...[]byte) {
	t.Helper()
	for _, m := range msgs {
		if err := c.SendChannel("cliprdr", m); err != nil {
			t.Fatal(err)
		}
	}
}

// clipReady runs the clipboard's initialization and returns the
// backend's first Format List.
func clipReady(t *testing.T, c *rdptest.Conn) rdptest.Cliprdr {
	t.Helper()
	sendClip(t, c, rdptest.ClipCaps(true), rdptest.MonitorReady())
	if m := clip(t, c); m.Type != rdptest.CBClipCaps {
		t.Fatalf("CLIPRDR message %d, want the capabilities", m.Type)
	}
	return clip(t, c)
}

// isList checks that m is a Format List of ids.
func isList(t *testing.T, m rdptest.Cliprdr, ids ...uint32) {
	t.Helper()
	got, err := rdptest.FormatIDs(m.Data, true)
	if m.Type != rdptest.CBFormatList || err != nil || !slices.Equal(got, ids) {
		t.Fatalf("CLIPRDR message %d of formats %v (%v), want a Format List of %v", m.Type, got, err, ids)
	}
}

// A viewer's clipboard text becomes the server's, announced before the
// input that follows it, as a paste needs; the server's becomes the
// viewers'.
func TestRunClipboard(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	sink := &backendtest.Sink{}
	b := newBackend(t, s.config(t))
	b.Demand(backend.Demand{Visible: true})
	start(t, b, sink)
	c := s.accept(t)
	isList(t, clipReady(t, c))

	ctrlV := []backend.InputEvent{
		backend.Key{Down: true, Keysym: 0xffe3, Code: 0xe0},
		backend.Key{Down: true, Keysym: 'v', Code: 0x19},
	}
	if err := b.Input(backend.Clipboard{MIME: "text/plain;charset=utf-8", Data: []byte("pasted\ntext")}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range ctrlV {
		if err := b.Input(ev); err != nil {
			t.Fatal(err)
		}
	}
	isList(t, clip(t, c), rdptest.CFUnicodeText, rdptest.CFText)
	if evs := events(t, c, 2); evs[1].Code != 0x2f {
		t.Errorf("events %+v, want Control and V after the Format List", evs)
	}
	sendClip(t, c, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	m := clip(t, c)
	if got, err := rdptest.FromUnicodeText(m.Data); m.Type != rdptest.CBFormatDataResponse || err != nil || got != "pasted\r\ntext" {
		t.Errorf("CLIPRDR message %d of %q, %v", m.Type, got, err)
	}

	sendClip(t, c, rdptest.FormatList(true, rdptest.CFUnicodeText))
	if m := clip(t, c); m.Type != rdptest.CBFormatListResponse || m.Flags != rdptest.CBResponseOK {
		t.Fatalf("%+v, want the Format List acknowledged", m)
	}
	if m := clip(t, c); m.Type != rdptest.CBFormatDataRequest || binary.LittleEndian.Uint32(m.Data) != rdptest.CFUnicodeText {
		t.Fatalf("%+v, want CF_UNICODETEXT asked for", m)
	}
	sendClip(t, c, rdptest.FormatDataResponse(true, rdptest.UnicodeText("copied\r\ntext")))
	waitFor(t, "the server's clipboard", func() bool {
		mime, data := sink.ClipboardContent()
		return mime == "text/plain" && string(data) == "copied\ntext"
	})

	// Clipboards of other types are dropped.
	if err := b.Input(backend.Clipboard{MIME: "image/png", Data: []byte("\x89PNG")}); err != nil {
		t.Fatal(err)
	}
	quietFor(t, c)
}

// DisableCopy keeps the server's clipboard from the viewers, and
// DisablePaste the viewers' from the server; with both, the backend does
// not ask for the channel.
func TestRunClipboardDisabled(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		checkGoroutines(t)
		s := listen(t, rdptest.Server{})
		cfg := s.config(t)
		cfg.DisableCopy = true
		sink := &backendtest.Sink{}
		b := newBackend(t, cfg)
		start(t, b, sink)
		c := s.accept(t)
		isList(t, clipReady(t, c))
		sendClip(t, c, rdptest.FormatList(true, rdptest.CFUnicodeText))
		if m := clip(t, c); m.Type != rdptest.CBFormatListResponse {
			t.Fatalf("%+v, want the Format List acknowledged", m)
		}
		sendClip(t, c, rdptest.FormatDataResponse(true, rdptest.UnicodeText("secret")))
		quietFor(t, c)
		if mime, data := sink.ClipboardContent(); mime != "" || data != nil {
			t.Errorf("the viewers got the clipboard %q", data)
		}
		// Pasting still works.
		if err := b.Input(backend.Clipboard{Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		isList(t, clip(t, c), rdptest.CFUnicodeText, rdptest.CFText)
	})
	t.Run("paste", func(t *testing.T) {
		checkGoroutines(t)
		s := listen(t, rdptest.Server{})
		cfg := s.config(t)
		cfg.DisablePaste = true
		b := newBackend(t, cfg)
		start(t, b, &backendtest.Sink{})
		c := s.accept(t)
		if err := b.Input(backend.Clipboard{MIME: "text/plain", Data: []byte("secret")}); err != nil {
			t.Fatal(err)
		}
		isList(t, clipReady(t, c))
		quietFor(t, c)
		sendClip(t, c, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
		if m := clip(t, c); m.Type != rdptest.CBFormatDataResponse || m.Flags != rdptest.CBResponseFail {
			t.Errorf("%+v, want the request failed", m)
		}
	})
	t.Run("both", func(t *testing.T) {
		checkGoroutines(t)
		s := listen(t, rdptest.Server{})
		cfg := s.config(t)
		cfg.DisableCopy, cfg.DisablePaste = true, true
		b := newBackend(t, cfg)
		start(t, b, &backendtest.Sink{})
		if c := s.accept(t); len(c.Channels) != 0 {
			t.Errorf("channels %v, want none", c.Channels)
		}
		if err := b.Input(backend.Clipboard{Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	})
}

// A clipboard of more than rdp.MaxClipboard bytes is cut, and the queue
// holds two of them: one more fills it.
func TestRunClipboardBounded(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	cfg := s.config(t)
	cfg.Security, cfg.Password = SecurityAny, ""
	b := newBackend(t, cfg)
	asked := make(chan struct{})
	answer := make(chan struct{})
	start(t, b, &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		close(asked)
		<-answer // hold the queue until the clipboards are in it
		return backend.PromptReply{Answers: []string{"pw"}}, nil
	}})
	<-asked
	big := backend.Clipboard{Data: []byte(strings.Repeat("é", rdp.MaxClipboard))}
	for i := range 2 {
		if err := b.Input(big); err != nil {
			t.Fatalf("clipboard %d: %v", i, err)
		}
	}
	if err := b.Input(big); !errors.Is(err, ErrInputFull) {
		t.Errorf("a third clipboard: %v, want ErrInputFull", err)
	}
	close(answer)
	c := s.accept(t)
	clipReady(t, c)
	isList(t, clip(t, c), rdptest.CFUnicodeText, rdptest.CFText)
	sendClip(t, c, rdptest.FormatDataRequest(rdptest.CFUnicodeText))
	m := clip(t, c)
	if got, err := rdptest.FromUnicodeText(m.Data); err != nil || len(got) != rdp.MaxClipboard {
		t.Errorf("the server got %d bytes, %v; want %d", len(got), err, rdp.MaxClipboard)
	}
}

// nlaServer is an rdptest server that selects HYBRID_EX and takes
// alice's account through CredSSP.
func nlaServer() rdptest.Server {
	return rdptest.Server{
		Protocol: rdptest.ProtocolHybridEx,
		Account:  &rdptest.Account{Domain: "CORP", Username: "alice", Password: "pässword"},
	}
}

// The credentials go through CredSSP to a server that checks them, the
// owner's answers when the configuration lacks them, and the session
// runs after.
func TestRunNLA(t *testing.T) {
	for _, security := range []string{SecurityNLA, SecurityNLAExt} {
		t.Run(security, func(t *testing.T) {
			checkGoroutines(t)
			srv := nlaServer()
			if security == SecurityNLA {
				srv.Protocol = rdptest.ProtocolHybrid
			}
			s := listen(t, srv)
			cfg := s.config(t)
			cfg.Security, cfg.Username, cfg.Password = security, "", ""
			sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
				return backend.PromptReply{Answers: []string{"alice", "corp", "pässword"}}, nil
			}}
			b := newBackend(t, cfg)
			b.Demand(backend.Demand{Visible: true})
			start(t, b, sink)
			c := s.accept(t)
			want := rdptest.Credentials{Domain: "corp", Username: "alice", Password: "pässword"}
			if c.Delegated != want || c.Authenticates != 1 {
				t.Errorf("delegated %+v after %d authenticates", c.Delegated, c.Authenticates)
			}
			if err := b.Input(backend.Key{Down: true, Keysym: 'a', Code: 0x04}); err != nil {
				t.Fatal(err)
			}
			events(t, c, 1)
		})
	}
}

// What the backend refuses in CredSSP, before it delegates the
// credentials: a wrong password, which fails Run with ErrAuthFailed; a
// CredSSP version before 5, before it answers the challenge; a challenge
// without sealing; and a server that proves another public key.
func TestRunNLARefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		srv    func(*rdptest.Server)
		cfg    func(*Config)
		auths  int
		err    string
		failed bool
	}{
		{"wrong password", func(*rdptest.Server) {}, func(c *Config) { c.Password = "wrong" }, 1, "", true},
		{"version 4", func(s *rdptest.Server) { s.CredSSPVersion = 4 }, nil, 0, "version", false},
		// A Windows server's challenge flags, without sealing.
		{"no sealing", func(s *rdptest.Server) { s.NTLMFlags = 0xe2888235 &^ rdptest.NTLMSeal }, nil, 0, "lacks flags", false},
		{"wrong proof", func(s *rdptest.Server) { s.WrongProof = true }, nil, 1, "different public key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := nlaServer()
			tc.srv(&srv)
			s := listen(t, srv)
			cfg := s.config(t)
			cfg.Security, cfg.Domain, cfg.Username, cfg.Password = SecurityNLAExt, "CORP", "alice", "pässword"
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			r := start(t, newBackend(t, cfg), &backendtest.Sink{})
			err := r.wait(t)
			a := s.next(t)
			if a.err == nil {
				t.Fatal("the server finished the connection sequence")
			}
			if a.c == nil || a.c.Delegated != (rdptest.Credentials{}) || a.c.Authenticates != tc.auths {
				t.Errorf("the server got %v; want no credentials delegated, %d authenticates", a.err, tc.auths)
			}
			var ce *rdp.CredSSPError
			switch {
			case tc.failed:
				if !errors.Is(err, ErrAuthFailed) || !errors.As(err, &ce) || ce.Code != rdptest.StatusLogonFailure {
					t.Errorf("Run returned %v, want ErrAuthFailed with STATUS_LOGON_FAILURE", err)
				}
			case err == nil || !strings.Contains(err.Error(), tc.err) || errors.Is(err, ErrAuthFailed):
				t.Errorf("Run returned %v, want %q", err, tc.err)
			}
		})
	}
}

// Cancelling while the server stalls in CredSSP, after its challenge or
// after the client's answer, ends Run at once.
func TestRunCancelInNLA(t *testing.T) {
	for _, stall := range []int{1, 2} {
		t.Run(fmt.Sprint(stall), func(t *testing.T) {
			checkGoroutines(t)
			srv := nlaServer()
			srv.StallNLA = stall
			s := listen(t, srv)
			cfg := s.config(t)
			cfg.Security, cfg.Domain, cfg.Username, cfg.Password = SecurityNLAExt, "CORP", "alice", "pässword"
			r := start(t, newBackend(t, cfg), &backendtest.Sink{})
			time.Sleep(silence)
			begin := time.Now()
			if err := r.stop(t); !errors.Is(err, context.Canceled) || time.Since(begin) > time.Second {
				t.Errorf("Run returned %v after %v", err, time.Since(begin))
			}
			if a := s.next(t); a.err == nil || a.c.Authenticates != stall-1 || a.c.Delegated != (rdptest.Credentials{}) {
				t.Errorf("the server got %v, delegated %+v", a.err, a.c.Delegated)
			}
		})
	}
}
