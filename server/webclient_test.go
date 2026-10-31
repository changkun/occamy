// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

// writeFiles writes files, by their slash-separated names, under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// policyFor is the Content-Security-Policy the web client's files carry
// for a server at serverURL, spelt out rather than built.
func policyFor(t *testing.T, serverURL string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return "default-src 'self'; script-src 'self'; worker-src 'self'; " +
		"connect-src 'self' ws://" + u.Host + " wss://" + u.Host + "; " +
		"img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; object-src 'none'; " +
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// TestWebClient checks that a v1 file with web_client serves the built
// client under /static/, with its headers: the page revalidated, the
// hashed assets cached for a year, and the policy on every response;
// and that one without serves nothing there.
func TestWebClient(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"index.html":       "<!doctype html><title>Occamy</title>",
		"assets/index.js":  "console.log('occamy')",
		"assets/index.css": "body{}",
	}
	writeFiles(t, dir, files)
	ts := newTestServer(t, func(f *config.File) { f.WebClient = dir })
	policy := policyFor(t, ts.srv.URL)

	for _, c := range []struct{ path, body, typ, cache string }{
		{"/static/", files["index.html"], "text/html; charset=utf-8", "no-cache"},
		{"/static/assets/index.js", files["assets/index.js"], "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/static/assets/index.css", files["assets/index.css"], "text/css; charset=utf-8", "public, max-age=31536000, immutable"},
	} {
		r := do(t, http.MethodGet, ts.srv.URL+c.path, nil, "")
		if r.code != http.StatusOK || string(r.body) != c.body {
			t.Errorf("GET %s: %d %q, want 200 %q", c.path, r.code, r.body, c.body)
			continue
		}
		want := map[string]string{
			"Content-Type":            c.typ,
			"Cache-Control":           c.cache,
			"X-Content-Type-Options":  "nosniff",
			"Content-Security-Policy": policy,
			"Referrer-Policy":         "no-referrer",
		}
		for k, v := range want {
			if got := r.header.Get(k); got != v {
				t.Errorf("GET %s: %s %q, want %q", c.path, k, got, v)
			}
		}
	}
	// A missing asset is not cached, so that a later build's is fetched.
	for _, path := range []string{"/static/missing.js", "/static/assets/missing.js"} {
		r := do(t, http.MethodGet, ts.srv.URL+path, nil, "")
		if r.code != http.StatusNotFound || r.header.Get("Cache-Control") != "no-cache" || r.header.Get("Content-Security-Policy") != policy {
			t.Errorf("GET %s: %d, Cache-Control %q, policy %q; want 404, no-cache and the policy", path, r.code, r.header.Get("Cache-Control"), r.header.Get("Content-Security-Policy"))
		}
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

// TestWebClientFilesOnly checks that /static/ serves files and the
// index.html of a directory alone: a directory without one is not
// listed, and a symbolic link that leads out of web_client is not
// followed, to a file or to a directory, while one within it is.
func TestWebClientFilesOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"index.html":      "page",
		"assets/index.js": "script",
		"docs/index.html": "docs",
	})
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{"secret.txt": "secret", "index.html": "outside"})
	for link, to := range map[string]string{
		"secret.txt":     filepath.Join(outside, "secret.txt"),
		"out":            outside,
		"assets/up.txt":  filepath.Join("..", "..", filepath.Base(outside), "secret.txt"),
		"assets/page.js": filepath.Join("..", "index.html"),
	} {
		if err := os.Symlink(to, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
	}
	ts := newTestServer(t, func(f *config.File) { f.WebClient = dir })

	for _, c := range []struct {
		path string
		code int
		body string
	}{
		{"/static/assets/", http.StatusNotFound, ""},
		{"/static/assets", http.StatusNotFound, ""},
		{"/static/docs/", http.StatusOK, "docs"},
		{"/static/secret.txt", http.StatusNotFound, ""},
		{"/static/out/", http.StatusNotFound, ""},
		{"/static/out/secret.txt", http.StatusNotFound, ""},
		{"/static/assets/up.txt", http.StatusNotFound, ""},
		{"/static/assets/page.js", http.StatusOK, "page"},
	} {
		r := do(t, http.MethodGet, ts.srv.URL+c.path, nil, "")
		if r.code != c.code || c.body != "" && string(r.body) != c.body {
			t.Errorf("GET %s: %d %q, want %d %q", c.path, r.code, r.body, c.code, c.body)
		}
		if strings.Contains(string(r.body), "secret") || strings.Contains(string(r.body), "index.js") {
			t.Errorf("GET %s: the body shows what it may not: %q", c.path, r.body)
		}
	}
}

// TestWebClientPolicy checks that the policy names the request's host
// for the WebSocket schemes only when it is a plain host, so that a
// request cannot write directives into it.
func TestWebClientPolicy(t *testing.T) {
	for host, want := range map[string]string{
		"occamy.example":         "connect-src 'self' ws://occamy.example wss://occamy.example;",
		"127.0.0.1:5636":         "connect-src 'self' ws://127.0.0.1:5636 wss://127.0.0.1:5636;",
		"[::1]:5636":             "connect-src 'self' ws://[::1]:5636 wss://[::1]:5636;",
		"":                       "connect-src 'self';",
		"a;script-src *":         "connect-src 'self';",
		"a b":                    "connect-src 'self';",
		"a.example:1/x":          "connect-src 'self';",
		"a.example:5636 'self'*": "connect-src 'self';",
	} {
		got := webClientPolicy(host)
		if !strings.Contains(got, want) || strings.Count(got, ";") != 10 {
			t.Errorf("webClientPolicy(%q) = %q, want it to hold %q and 11 directives", host, got, want)
		}
	}
}

// TestWebClientV0 checks that a v0 file's client: true serves /static/
// through the same handler, with its headers, though nothing is built
// where it looks.
func TestWebClientV0(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	r := rp.get(t, "/static/", nil)
	if r.code != http.StatusNotFound {
		t.Errorf("GET /static/ of an unbuilt client: %d, want 404", r.code)
	}
	if got, want := r.header.Get("Content-Security-Policy"), policyFor(t, rp.srv.URL); got != want {
		t.Errorf("Content-Security-Policy %q, want %q", got, want)
	}
	if got := r.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options %q, want nosniff", got)
	}
}

// TestWebClientNotBuilt checks that occamyd warns at start when
// web_client names a directory without a built client, and serves it
// anyway, so that a client built later is served without a restart, as
// is one whose directory did not exist at start.
func TestWebClientNotBuilt(t *testing.T) {
	logged := captureLog(t)
	dir := t.TempDir()
	ts := newTestServer(t, func(f *config.File) { f.WebClient = dir })
	if !strings.Contains(logged(), "warning: web_client:") {
		t.Errorf("no warning for a web client that is not built; the log:\n%s", logged())
	}
	if r := do(t, http.MethodGet, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusNotFound {
		t.Errorf("GET /static/ before the build: %d, want 404", r.code)
	}
	writeFiles(t, dir, map[string]string{"index.html": "built"})
	if r := do(t, http.MethodGet, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusOK || string(r.body) != "built" {
		t.Errorf("GET /static/ once built: %d %q", r.code, r.body)
	}

	later := filepath.Join(t.TempDir(), "dist")
	ts = newTestServer(t, func(f *config.File) { f.WebClient = later })
	if r := do(t, http.MethodGet, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusNotFound {
		t.Errorf("GET /static/ of a missing directory: %d, want 404", r.code)
	}
	writeFiles(t, later, map[string]string{"index.html": "built later"})
	if r := do(t, http.MethodGet, ts.srv.URL+"/static/", nil, ""); r.code != http.StatusOK || string(r.body) != "built later" {
		t.Errorf("GET /static/ once its directory is built: %d %q", r.code, r.body)
	}
}
