// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// webClientPolicy returns the Content-Security-Policy of the web
// client's files, served to a request for host. The page runs its own
// scripts and its tile decoder's Worker, from /static/ alone; it calls
// API v1 and opens its streams on its own origin, named with the
// WebSocket schemes too, which 'self' covers only in browsers that
// follow CSP 3; the remote's cursor is a data: URL; and styles may be
// inline, as React's style attributes and the terminal emulator's
// stylesheet are. Nothing may frame the page, so that no other page can
// lay itself over a remote desktop and take its clicks. A host that is
// not a plain name or address with an optional port is left out, so
// that a request cannot write the policy.
func webClientPolicy(host string) string {
	connect := "'self'"
	if hostPattern.MatchString(host) {
		connect += " ws://" + host + " wss://" + host
	}
	return "default-src 'self'; script-src 'self'; worker-src 'self'; connect-src " + connect + "; " +
		"img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; object-src 'none'; " +
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// hostPattern matches a Host header that is a DNS name, an IPv4 address
// or an IPv6 address in brackets, with an optional port.
var hostPattern = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

// immutable is the Cache-Control of the files under assets/, whose names
// carry a hash of their content, so that a new build names new files.
const immutable = "public, max-age=31536000, immutable"

// webClient serves the built web client in dir under /static/: its
// page, scripts, styles and the Worker that decodes tiles. The page
// calls API v1 on this origin, which answers no CORS preflight, so
// occamyd serves it itself.
//
// It serves files alone: a directory without an index.html, and a
// symbolic link that leads out of dir, are not found. dir is opened for
// each request, so that a client built after occamyd started is served
// without a restart. The page is revalidated on every load, so that a
// page of an earlier build never asks for scripts that a newer one
// replaced, and the files under assets/ are cached for a year. No
// response is sniffed, and every one carries the webClientPolicy.
func webClient(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", webClientPolicy(r.Host))
		h.Set("Referrer-Policy", "no-referrer")
		w = &cacheWriter{ResponseWriter: w, asset: strings.HasPrefix(r.URL.Path, "/static/assets/")}
		root, err := os.OpenRoot(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		http.StripPrefix("/static", http.FileServerFS(filesOnly{root.FS()})).ServeHTTP(w, r)
	})
}

// filesOnly is a file system that holds the files of fsys and the
// directories that have an index.html, which http.FileServerFS serves in
// their place; any other directory, and any name fsys refuses, such as a
// link out of an os.Root, does not exist, so that it is not listed and a
// refusal is not a server error.
type filesOnly struct{ fsys fs.FS }

func (f filesOnly) Open(name string) (fs.File, error) {
	file, err := f.fsys.Open(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	info, err := file.Stat()
	if err == nil && !info.IsDir() {
		return file, nil
	}
	if err == nil {
		index, err := fs.Stat(f.fsys, path.Join(name, "index.html"))
		if err == nil && !index.IsDir() {
			return file, nil
		}
	}
	return nil, errors.Join(&fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}, file.Close())
}

// cacheWriter sets Cache-Control as the response starts, once its
// status is known: a file under assets/ that is served is cached for a
// year, and anything else, a missing asset too, is revalidated, so that
// a later build's file of that name is fetched. It is set this late
// because http.FileServerFS drops the header from its errors.
type cacheWriter struct {
	http.ResponseWriter
	asset bool
	wrote bool
}

func (c *cacheWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		cache := "no-cache"
		switch code {
		case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
			if c.asset {
				cache = immutable
			}
		}
		c.Header().Set("Cache-Control", cache)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *cacheWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (c *cacheWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
