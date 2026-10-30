// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"changkun.de/x/occamy/protocol"
)

func TestHeader(t *testing.T) {
	tests := []struct {
		h         protocol.Header
		raw       []byte
		ignorable bool
	}{
		{protocol.Header{}, []byte{0, 0, 0, 0}, false},
		{protocol.Header{Type: 1}, []byte{1, 0, 0, 0}, false},
		{protocol.Header{Type: 0x1234, Flags: protocol.FlagIgnorable}, []byte{0x34, 0x12, 1, 0}, true},
		{protocol.Header{Type: 0xffff, Flags: 0xffff}, []byte{0xff, 0xff, 0xff, 0xff}, true},
		// Reserved bits do not make a message ignorable.
		{protocol.Header{Type: 2, Flags: 0xfffe}, []byte{2, 0, 0xfe, 0xff}, false},
	}
	for _, tt := range tests {
		raw := protocol.AppendHeader(nil, tt.h)
		if !bytes.Equal(raw, tt.raw) {
			t.Errorf("AppendHeader(%+v) = %x, want %x", tt.h, raw, tt.raw)
		}
		h, body, err := protocol.Split(raw, protocol.Limits{})
		if err != nil || h != tt.h || len(body) != 0 {
			t.Errorf("Split(%x) = %+v, %x, %v, want %+v and an empty body", raw, h, body, err, tt.h)
		}
		if h.Ignorable() != tt.ignorable {
			t.Errorf("%+v.Ignorable() = %v, want %v", h, h.Ignorable(), tt.ignorable)
		}
	}
}

func TestAppendHeaderKeepsPrefix(t *testing.T) {
	b := protocol.AppendHeader([]byte("xy"), protocol.Header{Type: 7})
	if want := []byte{'x', 'y', 7, 0, 0, 0}; !bytes.Equal(b, want) {
		t.Fatalf("AppendHeader = %x, want %x", b, want)
	}
}

func TestSplit(t *testing.T) {
	small := protocol.Limits{MaxMessageSize: 8}
	tests := []struct {
		name string
		msg  []byte
		l    protocol.Limits
		h    protocol.Header
		body []byte
		err  error
	}{
		{"empty", nil, small, protocol.Header{}, nil, protocol.ErrTruncated},
		{"one byte", []byte{1}, small, protocol.Header{}, nil, protocol.ErrTruncated},
		{"three bytes", []byte{1, 0, 0}, small, protocol.Header{}, nil, protocol.ErrTruncated},
		{"header only", []byte{1, 0, 1, 0}, small, protocol.Header{Type: 1, Flags: 1}, []byte{}, nil},
		{"with body", []byte{2, 0, 0, 0, 0xaa, 0xbb}, small, protocol.Header{Type: 2}, []byte{0xaa, 0xbb}, nil},
		{"at the limit", []byte{3, 0, 0, 0, 1, 2, 3, 4}, small, protocol.Header{Type: 3}, []byte{1, 2, 3, 4}, nil},
		{"over the limit", []byte{3, 0, 0, 0, 1, 2, 3, 4, 5}, small, protocol.Header{}, nil, protocol.ErrTooLarge},
		// The size is checked first, so even a message too short for a
		// header is refused for its size.
		{"limit below a header", []byte{1, 0, 0, 0}, protocol.Limits{MaxMessageSize: 3}, protocol.Header{}, nil, protocol.ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, body, err := protocol.Split(tt.msg, tt.l)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Split error = %v, want %v", err, tt.err)
			}
			if h != tt.h || !bytes.Equal(body, tt.body) {
				t.Fatalf("Split = %+v, %x, want %+v, %x", h, body, tt.h, tt.body)
			}
		})
	}
}

func TestSplitAliasesBody(t *testing.T) {
	msg := []byte{1, 0, 0, 0, 'a', 'b'}
	_, body, err := protocol.Split(msg, protocol.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	msg[4] = 'z'
	if body[0] != 'z' {
		t.Fatal("the body is a copy of the message")
	}
}

func TestCheckSize(t *testing.T) {
	tests := []struct {
		l  protocol.Limits
		n  int
		ok bool
	}{
		{protocol.Limits{}, 0, true},
		{protocol.Limits{}, protocol.DefaultMaxMessageSize, true},
		{protocol.Limits{}, protocol.DefaultMaxMessageSize + 1, false},
		{protocol.Limits{MaxMessageSize: -1}, protocol.DefaultMaxMessageSize, true},
		{protocol.Limits{MaxMessageSize: -1}, protocol.DefaultMaxMessageSize + 1, false},
		{protocol.Limits{MaxMessageSize: 16 << 20}, 16 << 20, true},
		{protocol.Limits{MaxMessageSize: 16 << 20}, 16<<20 + 1, false},
		{protocol.Limits{MaxMessageSize: 100}, 101, false},
	}
	for _, tt := range tests {
		err := tt.l.CheckSize(tt.n)
		if tt.ok && err != nil || !tt.ok && !errors.Is(err, protocol.ErrTooLarge) {
			t.Errorf("%+v.CheckSize(%d) = %v, want ok %v", tt.l, tt.n, err, tt.ok)
		}
	}
	if protocol.DefaultMaxMessageSize != 4<<20 {
		t.Errorf("DefaultMaxMessageSize = %d, want 4 MiB", protocol.DefaultMaxMessageSize)
	}
}

func TestSplitDefaultLimit(t *testing.T) {
	msg := make([]byte, protocol.DefaultMaxMessageSize+1)
	if _, _, err := protocol.Split(msg[:protocol.DefaultMaxMessageSize], protocol.Limits{}); err != nil {
		t.Fatalf("Split of a 4 MiB message: %v", err)
	}
	_, _, err := protocol.Split(msg, protocol.Limits{})
	if !errors.Is(err, protocol.ErrTooLarge) {
		t.Fatalf("Split of a message over 4 MiB: %v, want %v", err, protocol.ErrTooLarge)
	}
}

func TestRegistry(t *testing.T) {
	var r protocol.Registry
	if _, ok := r.Lookup(1); ok {
		t.Fatal("an empty registry knows type 1")
	}
	if ok, err := r.Accept(protocol.Header{Type: 1}); ok || !errors.Is(err, protocol.ErrUnsupported) {
		t.Fatalf("empty registry: Accept = %v, %v, want %v", ok, err, protocol.ErrUnsupported)
	}

	r.Register(1, "one")
	r.Register(0xffff, "last")
	if name, ok := r.Lookup(1); !ok || name != "one" {
		t.Fatalf("Lookup(1) = %q, %v", name, ok)
	}
	if name, ok := r.Lookup(0xffff); !ok || name != "last" {
		t.Fatalf("Lookup(0xffff) = %q, %v", name, ok)
	}
	if _, ok := r.Lookup(2); ok {
		t.Fatal("Lookup(2) found an unregistered type")
	}

	tests := []struct {
		name string
		h    protocol.Header
		ok   bool
		err  error
	}{
		{"known", protocol.Header{Type: 1}, true, nil},
		{"known and ignorable", protocol.Header{Type: 1, Flags: protocol.FlagIgnorable}, true, nil},
		{"unknown", protocol.Header{Type: 2}, false, protocol.ErrUnsupported},
		{"unknown and ignorable", protocol.Header{Type: 2, Flags: protocol.FlagIgnorable}, false, nil},
		{"unknown with reserved flags", protocol.Header{Type: 2, Flags: 0xfffe}, false, protocol.ErrUnsupported},
		{"unknown, ignorable with reserved flags", protocol.Header{Type: 2, Flags: 0xffff}, false, nil},
		{"type 0", protocol.Header{}, false, protocol.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := r.Accept(tt.h)
			if ok != tt.ok || !errors.Is(err, tt.err) || (err == nil) != (tt.err == nil) {
				t.Fatalf("Accept(%+v) = %v, %v, want %v, %v", tt.h, ok, err, tt.ok, tt.err)
			}
		})
	}

	_, err := r.Accept(protocol.Header{Type: 0xabc})
	if !strings.Contains(err.Error(), "0x0abc") {
		t.Fatalf("the error does not name the type: %v", err)
	}
}

func TestRegisterPanics(t *testing.T) {
	tests := []struct {
		name  string
		t     protocol.Type
		label string
		want  string
	}{
		{"type 0", 0, "zero", "type 0 is reserved"},
		{"no name", 2, "", "registered without a name"},
		{"twice", 1, "uno", `registered as both "one" and "uno"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r protocol.Registry
			r.Register(1, "one")
			defer func() {
				v := recover()
				s, _ := v.(string)
				if !strings.Contains(s, tt.want) {
					t.Fatalf("Register panicked with %v, want %q", v, tt.want)
				}
			}()
			r.Register(tt.t, tt.label)
		})
	}
}

func TestRegistryConcurrentLookup(t *testing.T) {
	var r protocol.Registry
	for i := 1; i <= 64; i++ {
		r.Register(protocol.Type(i), "t")
	}
	done := make(chan struct{})
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range 1000 {
				r.Lookup(protocol.Type(i % 80))
				r.Accept(protocol.Header{Type: protocol.Type(i % 80), Flags: protocol.FlagIgnorable})
			}
		}()
	}
	for range 4 {
		<-done
	}
}
