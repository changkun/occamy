// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// API v1 answers every error with one JSON shape:
//
//	{"error": {"code": "invalid_token", "message": "the access token is not valid"}}
//
// code is one of the constants below and is what clients branch on;
// message is for people and may change. The v0 routes keep gin's bodies.
const (
	codeBadRequest           = "bad_request"
	codeUnauthorized         = "unauthorized"       // no credentials, or wrong ones
	codeInvalidToken         = "invalid_token"      // a token that fails verification
	codeTokenExpired         = "token_expired"      // a token past its expiry
	codeNotFound             = "not_found"          // no such route or resource
	codeMethodNotAllowed     = "method_not_allowed" // with Allow
	codeTooLarge             = "too_large"          // a body over the limit
	codeUnsupportedMediaType = "unsupported_media_type"
	codeInternal             = "internal"
)

// apiError is the body of an API v1 error.
type apiError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError replies with API v1's error body.
func writeError(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, apiError{errorBody{Code: code, Message: message}})
}

// respond replies with v as JSON. Bodies are compact and end without a
// line break, as the v0 routes' are.
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, status, v)
}

// maxJSONBody bounds the JSON body of an API v1 request. The largest
// today, a sign-in, takes a few hundred bytes.
const maxJSONBody = 64 << 10

// decodeJSON reads the request's body, which must be one JSON object of
// type application/json with no fields that v lacks. On failure it has
// replied with API v1's error and returns false. Requiring the JSON type
// also keeps a cross-site HTML form from posting to the API, since a form
// cannot send it without a CORS preflight.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "the body must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		err = errors.New("the body holds more than one JSON value")
	}
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "the body is too large")
	default:
		// The decoder's messages name fields and types, and a syntax
		// error quotes the character it stopped at: the client's own
		// data, sent back to the client only, never logged.
		writeError(w, http.StatusBadRequest, codeBadRequest, "the body is not a valid request: "+strings.TrimPrefix(err.Error(), "json: "))
	}
	return false
}

// apiFallback answers requests for /api/v1 and under it that no route of
// mux takes with API v1's error, where the ServeMux would answer in plain
// text: 404 for a path without a route, and 405 with Allow for a route
// that takes other methods. Everything else goes to mux.
func apiFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1" && !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			mux.ServeHTTP(w, r)
			return
		}
		// An empty pattern is the mux's own 404 or 405; a redirect to a
		// cleaned path has a pattern.
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		rec := &headerRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		if rec.code == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", rec.header.Get("Allow"))
			writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "the route does not take "+r.Method)
			return
		}
		writeError(w, http.StatusNotFound, codeNotFound, "no such route")
	})
}

// headerRecorder keeps the status and headers of a response and drops its
// body.
type headerRecorder struct {
	header http.Header
	code   int
}

func (h *headerRecorder) Header() http.Header         { return h.header }
func (h *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (h *headerRecorder) WriteHeader(code int) {
	if h.code == 0 {
		h.code = code
	}
}

// healthz implements GET /healthz, the liveness check: occamyd answers.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}
