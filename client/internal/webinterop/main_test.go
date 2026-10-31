// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

// The server against the Go client, which offers raw only: what the
// browser client's interop test checks, in brief, so that the server is
// known to work before a browser test blames the browser client.
func TestServer(t *testing.T) {
	srv := httptest.NewServer(newServer(slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("graphical", func(t *testing.T) {
		s, err := client.Dial(ctx, ws+"/graphical", token, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		// The first frame may show the screen before the desktop painted
		// all of it; a later one shows the rest.
		frame(ctx, t, s)
		var screen imageJSON
		get(t, srv.URL+"/screen", &screen)
		if screen.Width != deskWidth || screen.Height != deskHeight {
			t.Fatalf("the desktop is %dx%d", screen.Width, screen.Height)
		}
		for !bytes.Equal(s.Screenshot().Pix, screen.Pixels) {
			frame(ctx, t, s)
		}
		if _, err := s.Key(ctx, true, 'x', 0x1b); err != nil {
			t.Fatal(err)
		}
		want := backendtest.SwatchColor('x')
		for s.Screenshot().RGBAAt(0, 0) != want {
			frame(ctx, t, s)
		}
		var inputs []inputJSON
		get(t, srv.URL+"/inputs", &inputs)
		if len(inputs) != 1 || inputs[0] != (inputJSON{Type: "key", Down: true, Keysym: 'x', Code: 0x1b}) {
			t.Fatalf("inputs %+v", inputs)
		}
	})

	t.Run("token", func(t *testing.T) {
		_, err := client.Dial(ctx, ws+"/graphical", "wrong", client.Options{})
		if ce, ok := errors.AsType[*client.CloseError](err); !ok || ce.Code != protocol.CloseUnauthorized {
			t.Fatalf("Dial with a wrong token: %v", err)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		s, err := client.Dial(ctx, ws+"/terminal", token, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if _, err := s.TermInput(ctx, []byte("hi\r")); err != nil {
			t.Fatal(err)
		}
		// The frames are diffs of the screen, which show the echo.
		var out []byte
		for !bytes.Contains(out, []byte("hi")) {
			ev, err := s.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if d, ok := ev.(client.TermData); ok {
				out = append(out, d.Data...)
			}
		}
	})
}

// frame waits for s's next frame.
func frame(ctx context.Context, t *testing.T, s *client.Session) {
	t.Helper()
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch ev := ev.(type) {
		case client.Frame:
			return
		case client.End:
			t.Fatalf("the session ended: %+v", ev)
		}
	}
}

func get(t *testing.T, url string, v any) {
	t.Helper()
	res, err := http.Get(url) // #nosec G107 -- the test server's URL
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
