// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

// TestWebClient checks that a v1 file with web_client serves the built
// client under /static/, with its headers, and that one without serves
// nothing there.
func TestWebClient(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":       "<!doctype html><title>Occamy</title>",
		"assets/index.js":  "console.log('occamy')",
		"assets/index.css": "body{}",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ts := newTestServer(t, func(f *config.File) { f.WebClient = dir })

	for _, c := range []struct{ path, body, typ string }{
		{"/static/", files["index.html"], "text/html; charset=utf-8"},
		{"/static/assets/index.js", files["assets/index.js"], "text/javascript; charset=utf-8"},
		{"/static/assets/index.css", files["assets/index.css"], "text/css; charset=utf-8"},
	} {
		r := do(t, http.MethodGet, ts.srv.URL+c.path, nil, "")
		if r.code != http.StatusOK || string(r.body) != c.body {
			t.Errorf("GET %s: %d %q, want 200 %q", c.path, r.code, r.body, c.body)
			continue
		}
		want := map[string]string{
			"Content-Type":            c.typ,
			"Cache-Control":           "no-cache",
			"X-Content-Type-Options":  "nosniff",
			"Content-Security-Policy": "frame-ancestors 'none'",
			"Referrer-Policy":         "no-referrer",
		}
		for k, v := range want {
			if got := r.header.Get(k); got != v {
				t.Errorf("GET %s: %s %q, want %q", c.path, k, got, v)
			}
		}
	}
	if r := do(t, http.MethodGet, ts.srv.URL+"/static/missing.js", nil, ""); r.code != http.StatusNotFound {
		t.Errorf("GET /static/missing.js: %d, want 404", r.code)
	}
	if r := do(t, http.MethodHead, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusOK {
		t.Errorf("HEAD /static/: %d, want 200", r.code)
	}
	if r := do(t, http.MethodPost, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusMethodNotAllowed {
		t.Errorf("POST /static/: %d, want 405", r.code)
	}

	without := newTestServer(t, nil)
	if r := do(t, http.MethodGet, without.srv.URL+"/static/", nil, ""); r.code != http.StatusNotFound {
		t.Errorf("GET /static/ without web_client: %d, want 404", r.code)
	}
}

// TestWebClientNotBuilt checks that occamyd warns at start when
// web_client names a directory without a built client, and serves it
// anyway, so that a client built later is served without a restart.
func TestWebClientNotBuilt(t *testing.T) {
	logged := captureLog(t)
	dir := t.TempDir()
	ts := newTestServer(t, func(f *config.File) { f.WebClient = dir })
	if !strings.Contains(logged(), "warning: web_client:") {
		t.Errorf("no warning for a web client that is not built; the log:\n%s", logged())
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("built"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := do(t, http.MethodGet, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusOK || string(r.body) != "built" {
		t.Errorf("GET /static/ once built: %d %q", r.code, r.body)
	}
}
