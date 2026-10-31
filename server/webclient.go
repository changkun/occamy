// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import "net/http"

// webClient serves the built web client in dir under /static/: its
// page, scripts, styles and the Worker that decodes tiles. The page
// calls API v1 on this origin, which answers no CORS preflight, so
// occamyd serves it itself. Every response is revalidated, so that a
// page of an earlier build never asks for scripts that a newer one
// replaced; none is sniffed; and the page may not be framed, so that no
// other page can lay itself over a remote desktop and take its clicks.
func webClient(dir string) http.Handler {
	files := http.StripPrefix("/static", http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		files.ServeHTTP(w, r)
	})
}
