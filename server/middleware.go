// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/auth"
)

// The middleware here is plain func(http.Handler) http.Handler. Every
// response writer wrapper has Unwrap, so that coder/websocket and
// http.ResponseController still reach the connection's http.Hijacker and
// http.Flusher through it.

// logRequests logs each request once it has been served: its method,
// path, status, duration and the client's address. It leaves out the
// query string, which carries the token to /api/v0/connect, the headers,
// which carry tokens, and the body, which carries passwords. gin's logger
// wrote the query string, and with it the token.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		// #nosec G706 -- the client's method and path are quoted, so they cannot break the line.
		log.Printf("%q %q %d %v %s", r.Method, r.URL.Path, sw.status(), time.Since(start).Round(time.Microsecond), r.RemoteAddr)
	})
}

// statusWriter records the status code of a response.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// recovery answers a handler's panic with API v1's 500, logging the panic
// and its stack.
func recovery(next http.Handler) http.Handler {
	return recoverWith(next, func(w http.ResponseWriter) {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
	})
}

// recoverV0 answers a v0 route's panic as gin's recovery did: 500 with no
// body.
func recoverV0(next http.Handler) http.Handler {
	return recoverWith(next, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})
}

// recoverWith recovers a panic in next, logs it with its stack, and
// replies with fail unless the handler has already begun its response,
// such as a WebSocket that has been upgraded. http.ErrAbortHandler, the
// panic that aborts a response on purpose, passes through to net/http.
func recoverWith(next http.Handler, fail func(http.ResponseWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			// #nosec G706 -- the client's method and path are quoted, so they cannot break the line.
			log.Printf("panic serving %q %q: %s\n%s", r.Method, r.URL.Path, panicValue(v), debug.Stack())
			if sw.code == 0 {
				fail(w)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// panicValue describes a recovered panic value for the log. Strings and
// errors appear as they are, and anything else by its type only: %v of an
// *http.Request, a URL or a struct holding one would print the query
// string and headers, and with them tokens. So a handler must not panic
// with a string or error that carries a secret.
func panicValue(v any) string {
	switch v := v.(type) {
	case string:
		return strconv.Quote(v)
	case error:
		return strconv.Quote(v.Error())
	default:
		return fmt.Sprintf("a value of type %T", v)
	}
}

// limitBodies bounds every request body to n bytes. A handler reading
// past the limit gets an *http.MaxBytesError. Only when limitBodies gets
// net/http's own response writer does net/http also close the connection
// after the response: http.MaxBytesReader tells the writer through an
// unexported method that no wrapping writer can pass on, so routes puts
// limitBodies outside every wrapper.
func limitBodies(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// bearerRealm is the challenge in API v1's WWW-Authenticate header. v0's
// header keeps gin-jwt's realm, occamy-proxy.
const bearerRealm = `Bearer realm="occamy"` // #nosec G101 -- a challenge, not a credential

// authenticate admits requests that carry a valid access token in an
// Authorization header, and puts the token's claims and the principal
// that ids maps them to in the request's context, where claimsFrom and
// principalFrom find them. It never looks at the query string or
// cookies, so a token never has to appear in a URL. Anything else is
// refused with 401 and a WWW-Authenticate challenge (RFC 6750): no token
// is unauthorized, and a token that fails verification, an expired one,
// one of another kind or from an unknown issuer, or one whose subject
// ids refuses, is invalid_token or token_expired.
func authenticate(v *auth.Verifier, ids identities) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", bearerRealm)
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "an access token is required")
				return
			}
			claims, err := v.VerifyAccess(token)
			if err != nil {
				w.Header().Set("WWW-Authenticate", bearerRealm+`, error="invalid_token"`)
				if errors.Is(err, jwt.ErrTokenExpired) {
					writeError(w, http.StatusUnauthorized, codeTokenExpired, "the access token has expired")
				} else {
					writeError(w, http.StatusUnauthorized, codeInvalidToken, "the access token is not valid")
				}
				return
			}
			p, err := ids.of(claims)
			if err != nil {
				w.Header().Set("WWW-Authenticate", bearerRealm+`, error="invalid_token"`)
				writeError(w, http.StatusUnauthorized, codeInvalidToken, err.Error())
				return
			}
			ctx := context.WithValue(r.Context(), claimsKey{}, claims)
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
		})
	}
}

// bearerToken returns the token of the request's one Authorization
// header with the Bearer scheme. The scheme is case-insensitive, as RFC
// 9110 has it; v0's lookup kept gin-jwt's "Bearer" only.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Values("Authorization")
	if len(h) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(h[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

type claimsKey struct{}

// claimsFrom returns the claims of the access token that authenticate
// admitted the request with: the caller's user name in Subject, groups
// and issuer.
func claimsFrom(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*auth.Claims)
	return c, ok
}

type principalKey struct{}

// principalFrom returns the principal that authenticate admitted the
// request for.
func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

type connKey struct{}

// connContext is the public server's ConnContext: it keeps each
// connection in the context of the requests that arrive on it, where
// connFrom finds it.
func connContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// connFrom returns the connection a request arrived on, below TLS: the
// TCP socket, on which stream.SetLowWater can set TCP_NOTSENT_LOWAT
// before a WebSocket upgrade hides it. A *tls.Conn is no syscall.Conn,
// so SetLowWater would leave it alone.
func connFrom(ctx context.Context) (net.Conn, bool) {
	c, ok := ctx.Value(connKey{}).(net.Conn)
	if tc, isTLS := c.(*tls.Conn); isTLS {
		return tc.NetConn(), true
	}
	return c, ok
}
