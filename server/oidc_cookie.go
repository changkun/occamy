// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// flow is an OIDC sign-in between login and callback. It lives in the
// browser, sealed into a cookie, so that occamyd keeps no state for
// sign-ins that never come back.
type flow struct {
	State     string `json:"s"`
	Nonce     string `json:"n"`
	Verifier  string `json:"v"` // occamyd's PKCE verifier for the provider
	ReturnURL string `json:"r"`
	Challenge string `json:"c"` // the client's S256 challenge
	Expires   int64  `json:"e"` // Unix seconds
}

// flowCookieName names the cookie of a sign-in, with the prefix
// __Secure- when the cookie is Secure, so that the browser takes it only
// from an https response. The name is also the additional data of the
// seal, so that no other sealed value passes for it.
const flowCookieName = "occamy_oidc"

// flowCookies seals sign-ins into cookies with AES-256-GCM under a key
// made for this process: the browser can neither read nor alter one, and
// a sign-in that outlives a restart fails at the callback and starts
// again.
//
// The cookie goes only to the callback, as the browser sees it: its path
// is that of redirect_url, which includes any proxy prefix. It is
// HttpOnly, SameSite=Lax, which the provider's redirect back, a
// cross-site top-level GET, still carries, and Secure when the browser
// reaches the callback over https, which redirect_url tells even when a
// proxy terminates TLS.
type flowCookies struct {
	aead   cipher.AEAD
	name   string
	path   string
	secure bool
}

func newFlowCookies(redirectURL string, servesTLS bool) (*flowCookies, error) {
	u, err := url.Parse(redirectURL)
	if err != nil {
		return nil, fmt.Errorf("auth.oidc.redirect_url: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	c := &flowCookies{aead: aead, name: flowCookieName, path: path, secure: servesTLS || u.Scheme == "https"}
	if c.secure {
		c.name = "__Secure-" + flowCookieName
	}
	return c, nil
}

func (c *flowCookies) cookie(value string, maxAge int) *http.Cookie {
	// #nosec G124 -- Secure whenever the browser reaches the callback over https; a browser on http://localhost would drop a Secure cookie.
	return &http.Cookie{
		Name:     c.name,
		Value:    value,
		Path:     c.path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// set seals f into the response's cookie. A second sign-in in the same
// browser replaces the first.
func (c *flowCookies) set(w http.ResponseWriter, f flow) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	sealed := c.aead.Seal(nil, nil, b, []byte(c.name)) // #nosec G407 -- NewGCMWithRandomNonce makes a random nonce for each seal

	http.SetCookie(w, c.cookie(base64.RawURLEncoding.EncodeToString(sealed), int(flowLifetime/time.Second)))
	return nil
}

// clear removes the cookie from the browser.
func (c *flowCookies) clear(w http.ResponseWriter) {
	http.SetCookie(w, c.cookie("", -1))
}

// match returns the sign-in of the request's cookie whose state is the
// one value of state, if it has not expired by now, or nil. pending
// tells whether the request holds another sign-in that has not expired,
// one that a callback which does not match must leave alone. Every
// cookie of the name is tried, so that one set for a parent domain
// cannot shadow the sign-in's own.
func (c *flowCookies) match(r *http.Request, state []string, now time.Time) (matched *flow, pending bool) {
	for _, ck := range r.CookiesNamed(c.name) {
		f, ok := c.open(ck.Value)
		if !ok || now.Unix() >= f.Expires {
			continue
		}
		if matched == nil && len(state) == 1 && state[0] != "" && subtle.ConstantTimeCompare([]byte(f.State), []byte(state[0])) == 1 {
			matched = f
			continue
		}
		pending = true
	}
	return matched, pending
}

// open unseals a cookie's value.
func (c *flowCookies) open(v string) (*flow, bool) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(v)
	if err != nil {
		return nil, false
	}
	plain, err := c.aead.Open(nil, nil, b, []byte(c.name))
	if err != nil {
		return nil, false
	}
	var f flow
	if err := json.Unmarshal(plain, &f); err != nil {
		return nil, false
	}
	return &f, true
}
