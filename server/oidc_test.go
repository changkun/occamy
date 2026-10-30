// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

const (
	testClientID   = "occamy"
	testReturnURL  = "https://app.example.com/signed-in"
	otherReturnURL = "http://localhost:3000/"
)

// testRSAKey is the identity provider's signing key, shared by the tests
// since an RSA key takes a while to make.
var testRSAKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// testIdP is an OpenID Connect provider in the test process: discovery,
// a key set, an authorization endpoint that signs the user in at once,
// and a token endpoint that checks the grant, the client and PKCE, and
// mints ID tokens that a test can spoil.
type testIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu          sync.Mutex
	redirectURI string              // occamyd's callback
	secret      string              // the client secret, or "" for a public client
	grants      map[string]idpGrant // by code
	discovered  int                 // discovery requests
	exchanges   int                 // token requests
	clientAuth  string              // how the last token request authenticated: basic, post or none
	authorized  url.Values          // the last authorization request

	// What the next sign-ins get, set by tests before they start.
	down        bool          // discovery answers 503
	hang        chan struct{} // discovery waits for it when not nil
	doc         func(map[string]any)
	authMethods []string            // token_endpoint_auth_methods_supported
	subject     string              // sub
	extra       jwt.MapClaims       // more claims
	spoil       func(jwt.MapClaims) // edits the claims last
	sign        func(jwt.MapClaims) string
	authError   string // the authorization endpoint answers with this error
	issParam    string // the authorization endpoint adds iss (RFC 9207)
	tokenStatus int    // the token endpoint fails with this status
	tokenError  string // and this error code, invalid_grant if empty
	noIDToken   bool
	oct         []byte // a symmetric key that the key set also lists
}

type idpGrant struct {
	redirectURI, nonce, challenge string
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	p := &testIdP{key: testRSAKey(), grants: map[string]idpGrant{}, subject: "carol"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *testIdP) set(f func(p *testIdP)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *testIdP) counts() (discovered, exchanges int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discovered, p.exchanges
}

func (p *testIdP) discovery(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.discovered++
	down, hang, edit, methods := p.down, p.hang, p.doc, p.authMethods
	p.mu.Unlock()
	if hang != nil {
		select {
		case <-hang:
		case <-r.Context().Done():
			return
		}
	}
	if down {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
		return
	}
	doc := map[string]any{
		"issuer":                 p.srv.URL,
		"authorization_endpoint": p.srv.URL + "/authorize",
		"token_endpoint":         p.srv.URL + "/token",
		"jwks_uri":               p.srv.URL + "/jwks",
		// The provider's say does not widen what occamyd accepts.
		"id_token_signing_alg_values_supported": []string{"RS256", "HS256", "none"},
	}
	if methods != nil {
		doc["token_endpoint_auth_methods_supported"] = methods
	}
	if edit != nil {
		edit(doc)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc)
}

func (p *testIdP) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := p.key.PublicKey
	b64 := base64.RawURLEncoding.EncodeToString
	keys := []map[string]string{{
		"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}
	p.mu.Lock()
	if p.oct != nil {
		keys = append(keys, map[string]string{"kty": "oct", "kid": "k1", "k": b64(p.oct)})
	}
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

// authorize signs the user in at once, after checking the request as a
// provider that requires PKCE with S256 would.
func (p *testIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authorized = q
	switch {
	case q.Get("response_type") != "code", q.Get("client_id") != testClientID, q.Get("redirect_uri") != p.redirectURI:
		http.Error(w, "bad client", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256", q.Get("code_challenge") == "", q.Get("nonce") == "", q.Get("state") == "":
		http.Error(w, "PKCE with S256, a nonce and a state are required", http.StatusBadRequest)
		return
	case !slices.Contains(strings.Fields(q.Get("scope")), "openid"):
		http.Error(w, "not an OpenID request", http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {q.Get("state")}}
	if p.issParam != "" {
		back.Set("iss", p.issParam)
	}
	if p.authError != "" {
		back.Set("error", p.authError)
		back.Set("error_description", "the description is not passed on")
	} else {
		code := rand.Text()
		p.grants[code] = idpGrant{redirectURI: q.Get("redirect_uri"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
		back.Set("code", code)
	}
	http.Redirect(w, r, p.redirectURI+"?"+back.Encode(), http.StatusFound)
}

// token exchanges a code for an ID token: the code once, for the
// redirect URI it was issued to, from the client, with the verifier of
// its challenge.
func (p *testIdP) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exchanges++
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "secret details"})
	}
	if err := r.ParseForm(); err != nil {
		fail(http.StatusBadRequest, "invalid_request")
		return
	}
	if p.tokenStatus != 0 {
		code := p.tokenError
		if code == "" {
			code = "invalid_grant"
		}
		fail(p.tokenStatus, code)
		return
	}
	id, secret, basic := r.BasicAuth()
	switch {
	case basic:
		p.clientAuth = "basic"
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	case r.PostForm.Has("client_secret"):
		p.clientAuth = "post"
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	default:
		p.clientAuth = "none"
		id = r.PostForm.Get("client_id")
	}
	if id != testClientID || secret != p.secret {
		fail(http.StatusUnauthorized, "invalid_client")
		return
	}
	code := r.PostForm.Get("code")
	g, ok := p.grants[code]
	delete(p.grants, code)
	switch {
	case r.PostForm.Get("grant_type") != "authorization_code", !ok, r.PostForm.Get("redirect_uri") != g.redirectURI:
		fail(http.StatusBadRequest, "invalid_grant")
		return
	case oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != g.challenge:
		fail(http.StatusBadRequest, "invalid_grant") // PKCE
		return
	}
	now := time.Now()
	c := jwt.MapClaims{
		"iss":   p.srv.URL,
		"sub":   p.subject,
		"aud":   testClientID,
		"iat":   now.Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
		"nonce": g.nonce,
	}
	for k, v := range p.extra {
		c[k] = v
	}
	if p.spoil != nil {
		p.spoil(c)
	}
	sign := p.sign
	if sign == nil {
		sign = p.signRS256
	}
	resp := map[string]any{"access_token": "provider-access-token", "token_type": "Bearer", "expires_in": 300}
	if !p.noIDToken {
		resp["id_token"] = sign(c)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

func (p *testIdP) signRS256(c jwt.MapClaims) string {
	return signWith(jwt.SigningMethodRS256, p.key, c)
}

func signWith(m jwt.SigningMethod, key any, c jwt.MapClaims) string {
	tok := jwt.NewWithClaims(m, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(key)
	if err != nil {
		panic(err)
	}
	return s
}

// testClock is the time of an OIDC sign-in, which tests move on.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// oidcTest is occamyd with OIDC sign-in through a testIdP.
type oidcTest struct {
	idp   *testIdP
	s     *server
	url   string // occamyd's
	clock *testClock
}

// newOIDCTest serves a file with alice in ops, bob in dev, a connection
// that allows carol and the group admins, and sign-in through a fresh
// provider, as public client occamy, with groups from the groups claim.
func newOIDCTest(t *testing.T, edit func(*config.File)) *oidcTest {
	t.Helper()
	idp := newTestIdP(t)
	ln := localListener(t)
	base := "http://" + ln.Addr().String()
	f := testFile(t)
	f.Auth.OIDC = &config.OIDC{
		Issuer:       idp.srv.URL,
		ClientID:     testClientID,
		RedirectURL:  base + "/api/v1/auth/oidc/callback",
		ReturnURLs:   []string{testReturnURL, otherReturnURL},
		Scopes:       config.DefaultOIDCScopes,
		SubjectClaim: "sub",
		GroupsClaim:  "groups",
	}
	f.Connections = []config.Connection{{
		ID: "box", Name: "box", Protocol: "ssh", Target: "box.internal:22",
		Credentials: config.Credentials{Prompt: true},
		Allow:       config.Allow{Users: []string{"carol"}, Groups: []string{"admins"}},
	}}
	if edit != nil {
		edit(f)
	}
	idp.set(func(p *testIdP) { p.redirectURI = f.Auth.OIDC.RedirectURL })
	s, err := newServer(f)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	clock := &testClock{t: time.Now()}
	s.oidc.now = clock.now
	srv := httptest.NewUnstartedServer(s.routes())
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &oidcTest{idp: idp, s: s, url: base, clock: clock}
}

// browser follows no redirects, so that a test sees each hop, and keeps
// cookies.
type browser struct {
	c   *http.Client
	jar http.CookieJar
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{jar: jar, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) get(t *testing.T, u string) reply {
	t.Helper()
	resp, err := b.c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, body}
}

// loginURL is occamyd's login with a return URL and a client's
// challenge.
func (ot *oidcTest) loginURL(returnURL, challenge string) string {
	q := url.Values{"code_challenge": {challenge}}
	if returnURL != "" {
		q.Set("return_url", returnURL)
	}
	return ot.url + "/api/v1/auth/oidc/login?" + q.Encode()
}

// toProvider sends the browser to login and returns the provider's
// authorization URL that occamyd redirected it to, and the client's
// verifier.
func (ot *oidcTest) toProvider(t *testing.T, b *browser) (string, string) {
	t.Helper()
	verifier := oauth2.GenerateVerifier()
	r := b.get(t, ot.loginURL(testReturnURL, oauth2.S256ChallengeFromVerifier(verifier)))
	loc := r.header.Get("Location")
	if r.code != http.StatusFound || !strings.HasPrefix(loc, ot.idp.srv.URL+"/authorize?") {
		t.Fatalf("login: %d to %q: %s", r.code, loc, r.body)
	}
	return loc, verifier
}

// toCallback takes the browser through the provider and returns the
// callback URL that the provider sent it to.
func (ot *oidcTest) toCallback(t *testing.T, b *browser, authURL string) string {
	t.Helper()
	r := b.get(t, authURL)
	loc := r.header.Get("Location")
	if r.code != http.StatusFound || !strings.HasPrefix(loc, ot.url+"/api/v1/auth/oidc/callback?") {
		t.Fatalf("the provider: %d to %q: %s", r.code, loc, r.body)
	}
	return loc
}

// signIn takes a browser from login through the provider to the
// callback, and returns the callback's reply and the client's verifier.
func (ot *oidcTest) signIn(t *testing.T, b *browser) (reply, string) {
	t.Helper()
	authURL, verifier := ot.toProvider(t, b)
	return b.get(t, ot.toCallback(t, b, authURL)), verifier
}

// sentBack checks that r sends the browser back to testReturnURL, and
// returns the fragment it carries.
func sentBack(t *testing.T, r reply) url.Values {
	t.Helper()
	loc := r.header.Get("Location")
	frag, ok := strings.CutPrefix(loc, testReturnURL+"#")
	if r.code != http.StatusFound || !ok {
		t.Fatalf("callback: %d to %q, want a redirect to %s: %s", r.code, loc, testReturnURL, r.body)
	}
	if r.header.Get("Cache-Control") != "no-store" || r.header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("callback headers: %v", r.header)
	}
	v, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// clears reports whether r clears the sign-in cookie.
func clears(r reply) bool {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == flowCookieName && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

// wantError checks that r sends the browser back with error code and no
// one-time code.
func wantError(t *testing.T, r reply, code string) {
	t.Helper()
	v := sentBack(t, r)
	if v.Get("error") != code || v.Has("code") || len(v) != 1 {
		t.Errorf("sent back with %v, want error %s", v, code)
	}
}

// exchange posts a one-time code and a verifier to occamyd.
func (ot *oidcTest) exchange(t *testing.T, code, verifier string) reply {
	t.Helper()
	b, err := json.Marshal(codeRequest{Code: code, CodeVerifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	return do(t, http.MethodPost, ot.url+"/api/v1/auth/oidc/token", http.Header{"Content-Type": {"application/json"}}, string(b))
}

// signedIn runs a whole sign-in and returns the access token's subject
// and groups.
func (ot *oidcTest) signedIn(t *testing.T) (string, []string) {
	t.Helper()
	cb, verifier := ot.signIn(t, newBrowser(t))
	v := sentBack(t, cb)
	r := ot.exchange(t, v.Get("code"), verifier)
	if r.code != http.StatusOK {
		t.Fatalf("exchange: %d %s", r.code, r.body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(r.body, &tr); err != nil {
		t.Fatal(err)
	}
	c, err := ot.s.verifier.VerifyAccess(tr.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return c.Subject, c.Groups
}

// TestOIDCSignIn runs a sign-in from login to access token.
func TestOIDCSignIn(t *testing.T) {
	logged := captureLog(t)
	ot := newOIDCTest(t, nil)
	ot.idp.set(func(p *testIdP) { p.extra = jwt.MapClaims{"groups": []string{"admins", "marketing"}} })
	b := newBrowser(t)

	authURL, verifier := ot.toProvider(t, b)
	u, _ := url.Parse(authURL)
	q := u.Query()
	for k, want := range map[string]string{
		"response_type":         "code",
		"client_id":             testClientID,
		"redirect_uri":          ot.url + "/api/v1/auth/oidc/callback",
		"scope":                 "openid profile email",
		"code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("authorization request %s = %q, want %q", k, q.Get(k), want)
		}
	}
	// The challenge is occamyd's own, not the client's: the client's
	// verifier never leaves the client before the exchange.
	if q.Get("code_challenge") == oauth2.S256ChallengeFromVerifier(verifier) {
		t.Error("occamyd sent the client's challenge to the provider")
	}
	callbackURL := ot.toCallback(t, b, authURL)
	state := q.Get("state")
	cb := b.get(t, callbackURL)
	v := sentBack(t, cb)
	if len(v) != 1 || v.Get("code") == "" {
		t.Fatalf("sent back with %v, want a code alone", v)
	}
	if !clears(cb) {
		t.Errorf("the callback did not clear the sign-in cookie: %q", cb.header.Values("Set-Cookie"))
	}

	r := ot.exchange(t, v.Get("code"), verifier)
	if r.code != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange: %d %v %s", r.code, r.header, r.body)
	}
	var tr tokenResponse
	dec := json.NewDecoder(strings.NewReader(string(r.body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tr); err != nil || tr.TokenType != "Bearer" || tr.ExpiresIn != 900 {
		t.Fatalf("exchange answered %s: %v", r.body, err)
	}
	c, err := ot.s.verifier.VerifyAccess(tr.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	// marketing is named nowhere in the file, so it is dropped.
	if c.Subject != "carol" || !slices.Equal(c.Groups, []string{"admins"}) || c.IdP != auth.IdPOIDC {
		t.Errorf("access token for %q in %v from idp %q, want carol in [admins] from oidc", c.Subject, c.Groups, c.IdP)
	}

	out := logged()
	if !strings.Contains(out, `user "carol" signed in, idp oidc`) {
		t.Errorf("no sign-in in the log:\n%s", out)
	}
	for name, secret := range map[string]string{"state": state, "one-time code": v.Get("code"), "verifier": verifier, "access token": tr.AccessToken} {
		if strings.Contains(out, secret) {
			t.Errorf("the log holds the %s:\n%s", name, out)
		}
	}
}

// TestOIDCLoginCookie checks the sign-in cookie: sealed, HttpOnly,
// SameSite=Lax, for the callback's path only, and Secure when the
// browser reaches the callback over https.
func TestOIDCLoginCookie(t *testing.T) {
	for _, tt := range []struct {
		name, redirect string
		cookie         string
		path           string
		secure         bool
	}{
		{"plain http on localhost", "", "occamy_oidc", "/api/v1/auth/oidc/callback", false},
		{"https through a proxy with a prefix", "https://occamy.example.com/occamy/api/v1/auth/oidc/callback", "__Secure-occamy_oidc", "/occamy/api/v1/auth/oidc/callback", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, func(f *config.File) {
				if tt.redirect != "" {
					f.Auth.OIDC.RedirectURL = tt.redirect
				}
			})
			r := newBrowser(t).get(t, ot.loginURL(testReturnURL, oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())))
			cookies := (&http.Response{Header: r.header}).Cookies()
			if r.code != http.StatusFound || len(cookies) != 1 {
				t.Fatalf("login: %d, cookies %q", r.code, r.header.Values("Set-Cookie"))
			}
			c := cookies[0]
			if c.Name != tt.cookie || c.Path != tt.path || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure != tt.secure || c.MaxAge != 600 {
				t.Errorf("cookie %q, want %s with path %s, HttpOnly, SameSite=Lax, Max-Age=600 and Secure %v", r.header.Get("Set-Cookie"), tt.cookie, tt.path, tt.secure)
			}
			f, ok := ot.s.oidc.flows.open(c.Value)
			if !ok || f.ReturnURL != testReturnURL || f.State == "" || f.Nonce == "" || f.Verifier == "" {
				t.Fatalf("the cookie holds %+v", f)
			}
			for _, secret := range []string{f.State, f.Nonce, f.Verifier} {
				if strings.Contains(c.Value, secret) {
					t.Errorf("the cookie shows %q", secret)
				}
			}
			u, _ := url.Parse(r.header.Get("Location"))
			if u.Query().Get("code_challenge") != oauth2.S256ChallengeFromVerifier(f.Verifier) || u.Query().Get("state") != f.State || u.Query().Get("nonce") != f.Nonce {
				t.Errorf("the authorization request does not carry the cookie's sign-in: %v", u.Query())
			}
		})
	}
	// occamyd serving TLS makes the cookie Secure whatever the URL says.
	fc, err := newFlowCookies("http://127.0.0.1:5636/api/v1/auth/oidc/callback", true)
	if err != nil || !fc.secure || fc.name != "__Secure-occamy_oidc" {
		t.Errorf("serving TLS: %s, secure %v, %v", fc.name, fc.secure, err)
	}
	// A sealed value under one name does not open under the other.
	plain, err := newFlowCookies("http://127.0.0.1:5636/api/v1/auth/oidc/callback", false)
	if err != nil {
		t.Fatal(err)
	}
	plain.aead = fc.aead
	w := httptest.NewRecorder()
	if err := fc.set(w, flow{State: "s", Expires: time.Now().Add(time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	value := w.Result().Cookies()[0].Value
	if _, ok := plain.open(value); ok {
		t.Error("a __Secure- cookie's value opens as a plain one")
	}
	// The callback reads the cookie of its name only, so that one that
	// another origin may set, such as occamy_oidc beside
	// __Secure-occamy_oidc, cannot carry a sealed sign-in, even one
	// sealed for the prefixed name.
	for name, want := range map[string]bool{fc.name: true, flowCookieName: false, "other": false} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback", nil)
		req.AddCookie(&http.Cookie{Name: name, Value: value})
		if f, _ := fc.match(req, []string{"s"}, time.Now()); (f != nil) != want {
			t.Errorf("the sign-in in a cookie named %s matched: %v, want %v", name, f != nil, want)
		}
	}
}

// TestOIDCLoginReplacesFlow checks what a second login in one browser
// does, such as a top-level navigation to login from another site: it
// replaces the sign-in cookie, so the sign-in under way fails at the
// callback with invalid_state, a nuisance the docs record. The sign-in
// that the other site started reaches the return URL with a code that
// the browser's client cannot redeem, since it holds no verifier of that
// challenge.
func TestOIDCLoginReplacesFlow(t *testing.T) {
	ot := newOIDCTest(t, nil)
	b := newBrowser(t)
	authURL, verifier := ot.toProvider(t, b)

	other := oauth2.GenerateVerifier()
	r := b.get(t, ot.loginURL(testReturnURL, oauth2.S256ChallengeFromVerifier(other)))
	if r.code != http.StatusFound || len(r.header.Values("Set-Cookie")) != 1 {
		t.Fatalf("second login: %d %q", r.code, r.header.Values("Set-Cookie"))
	}
	otherAuthURL := r.header.Get("Location")

	cb := b.get(t, ot.toCallback(t, b, authURL))
	if cb.code != http.StatusBadRequest || decodeError(t, cb.body).Code != codeInvalidState {
		t.Fatalf("the first sign-in's callback: %d %s", cb.code, cb.body)
	}
	v := sentBack(t, b.get(t, ot.toCallback(t, b, otherAuthURL)))
	if r := ot.exchange(t, v.Get("code"), verifier); r.code != http.StatusUnauthorized {
		t.Errorf("the client's verifier redeemed the other site's code: %d %s", r.code, r.body)
	}
}

// TestOIDCOpenRedirect checks that login sends nobody anywhere but to a
// configured return URL, exactly as written.
func TestOIDCOpenRedirect(t *testing.T) {
	ot := newOIDCTest(t, nil)
	challenge := oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())
	for _, bad := range []string{
		"https://evil.example.com/",
		"https://app.example.com/signed-in/",
		"https://app.example.com/signed-in?next=https://evil.example.com/",
		"https://app.example.com/signed-in#x",
		"HTTPS://app.example.com/signed-in",
		"https://app.example.com.evil.example.com/signed-in",
		"//evil.example.com/signed-in",
		"/signed-in",
		"javascript:alert(1)",
		// Near misses, which a parser or a normalization might take for
		// the configured URL.
		"https://app.example.com./signed-in",
		"https://app.example.com:443/signed-in",
		"https://APP.example.com/signed-in",
		"https://app.example.com/signed%2Din",
		"https://app.example.com/Signed-in",
		"https://app.example.com/signed-in.",
		"https://app.example.com/signed-in;x",
		"https://app.example.com/signed-in?",
		"https://app.example.com/signed-in%23x",
		"https://app.example.com/signed-in%0d%0aSet-Cookie:x=y",
		"https://app.example.com/signed-in\r\nSet-Cookie: x=y",
		`https://app.example.com\@evil.example.com/signed-in`,
		`https://evil.example.com\@app.example.com/signed-in`,
		"https://app.example.com@evil.example.com/signed-in",
		"https://user@app.example.com/signed-in",
		"https://xn--pp-ula.example.com/signed-in",
		"https://\u0430pp.example.com/signed-in", // a Cyrillic a
		`\\evil.example.com/signed-in`,
		`https:/\evil.example.com/signed-in`,
		"///evil.example.com",
		" https://app.example.com/signed-in",
		"https://app.example.com/signed-in ",
		"\thttps://app.example.com/signed-in",
		"https://app.example.com/signed-in/..",
		"https://app.example.com/./signed-in",
		"https://app.example.com/signed-in#",
	} {
		r := newBrowser(t).get(t, ot.loginURL(bad, challenge))
		if r.code != http.StatusBadRequest || r.header.Get("Location") != "" || len(r.header.Values("Set-Cookie")) != 0 {
			t.Errorf("return_url %q: %d to %q, cookies %q", bad, r.code, r.header.Get("Location"), r.header.Values("Set-Cookie"))
			continue
		}
		if e := decodeError(t, r.body); e.Code != codeBadRequest {
			t.Errorf("return_url %q: %+v", bad, e)
		}
	}
	// Two return URLs, of which one is allowed.
	q := url.Values{"return_url": {testReturnURL, "https://evil.example.com/"}, "code_challenge": {challenge}}
	if r := newBrowser(t).get(t, ot.url+"/api/v1/auth/oidc/login?"+q.Encode()); r.code != http.StatusBadRequest {
		t.Errorf("two return URLs: %d", r.code)
	}
	// With two configured, a sign-in must name one.
	if r := newBrowser(t).get(t, ot.loginURL("", challenge)); r.code != http.StatusBadRequest {
		t.Errorf("no return URL of two: %d", r.code)
	}
	for _, good := range []string{testReturnURL, otherReturnURL} {
		if r := newBrowser(t).get(t, ot.loginURL(good, challenge)); r.code != http.StatusFound {
			t.Errorf("return_url %q: %d %s", good, r.code, r.body)
		}
	}

	// A return URL percent-encoded byte by byte decodes to the configured
	// one, as any other encoding of the query does.
	var encoded strings.Builder
	for _, b := range []byte(testReturnURL) {
		fmt.Fprintf(&encoded, "%%%02X", b)
	}
	if r := newBrowser(t).get(t, ot.url+"/api/v1/auth/oidc/login?code_challenge="+challenge+"&return_url="+encoded.String()); r.code != http.StatusFound || !strings.HasPrefix(r.header.Get("Location"), ot.idp.srv.URL+"/authorize?") {
		t.Errorf("the configured return URL, percent-encoded: %d to %q", r.code, r.header.Get("Location"))
	}
	// After the state matched, an error goes back to the sign-in's own
	// return URL.
	ot.idp.set(func(p *testIdP) { p.authError = "server_error" })
	b := newBrowser(t)
	r := b.get(t, ot.loginURL(otherReturnURL, challenge))
	if loc := b.get(t, ot.toCallback(t, b, r.header.Get("Location"))).header.Get("Location"); loc != otherReturnURL+"#error=sign_in_failed" {
		t.Errorf("an error went back to %q, want %s", loc, otherReturnURL)
	}

	// With one configured, a sign-in may leave it out.
	one := newOIDCTest(t, func(f *config.File) { f.Auth.OIDC.ReturnURLs = []string{testReturnURL} })
	authURL, _ := one.toProvider(t, newBrowser(t))
	if r := newBrowser(t).get(t, one.loginURL("", challenge)); r.code != http.StatusFound || authURL == "" {
		t.Errorf("the one return URL left out: %d %s", r.code, r.body)
	}
}

// TestOIDCLoginChallenge checks that login requires the client's S256
// challenge.
func TestOIDCLoginChallenge(t *testing.T) {
	ot := newOIDCTest(t, nil)
	verifier := oauth2.GenerateVerifier()
	good := oauth2.S256ChallengeFromVerifier(verifier)
	for name, q := range map[string]url.Values{
		"none":          {"return_url": {testReturnURL}},
		"empty":         {"return_url": {testReturnURL}, "code_challenge": {""}},
		"plain":         {"return_url": {testReturnURL}, "code_challenge": {verifier}, "code_challenge_method": {"plain"}},
		"short":         {"return_url": {testReturnURL}, "code_challenge": {good[:42]}},
		"padded":        {"return_url": {testReturnURL}, "code_challenge": {base64.URLEncoding.EncodeToString(make([]byte, 32))}},
		"not base64url": {"return_url": {testReturnURL}, "code_challenge": {strings.Repeat("+", 43)}},
		"twice":         {"return_url": {testReturnURL}, "code_challenge": {good, good}},
	} {
		r := newBrowser(t).get(t, ot.url+"/api/v1/auth/oidc/login?"+q.Encode())
		if r.code != http.StatusBadRequest || len(r.header.Values("Set-Cookie")) != 0 {
			t.Errorf("challenge %s: %d %s", name, r.code, r.body)
		}
	}
	q := url.Values{"return_url": {testReturnURL}, "code_challenge": {good}, "code_challenge_method": {"S256"}}
	if r := newBrowser(t).get(t, ot.url+"/api/v1/auth/oidc/login?"+q.Encode()); r.code != http.StatusFound {
		t.Errorf("S256 named: %d %s", r.code, r.body)
	}
}

// TestOIDCCallbackState checks that the callback refuses, with API v1's
// error and no exchange, a state that matches no sign-in cookie of the
// browser.
func TestOIDCCallbackState(t *testing.T) {
	refused := func(t *testing.T, ot *oidcTest, r reply) {
		t.Helper()
		if r.code != http.StatusBadRequest || r.header.Get("Location") != "" {
			t.Fatalf("callback: %d to %q: %s", r.code, r.header.Get("Location"), r.body)
		}
		if e := decodeError(t, r.body); e.Code != codeInvalidState {
			t.Errorf("callback: %+v", e)
		}
		if _, n := ot.idp.counts(); n != 0 {
			t.Errorf("the callback exchanged the code %d times", n)
		}
	}
	callbackOf := func(t *testing.T, ot *oidcTest, b *browser) (string, *url.URL) {
		t.Helper()
		authURL, _ := ot.toProvider(t, b)
		cb := ot.toCallback(t, b, authURL)
		u, err := url.Parse(cb)
		if err != nil {
			t.Fatal(err)
		}
		return cb, u
	}

	t.Run("state mismatch", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		_, u := callbackOf(t, ot, b)
		q := u.Query()
		q.Set("state", rand.Text())
		u.RawQuery = q.Encode()
		refused(t, ot, b.get(t, u.String()))
	})
	t.Run("state twice", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		_, u := callbackOf(t, ot, b)
		q := u.Query()
		q.Add("state", q.Get("state"))
		u.RawQuery = q.Encode()
		refused(t, ot, b.get(t, u.String()))
	})
	t.Run("no state", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		_, u := callbackOf(t, ot, b)
		q := u.Query()
		q.Del("state")
		u.RawQuery = q.Encode()
		refused(t, ot, b.get(t, u.String()))
	})
	t.Run("missing cookie", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		cb, _ := callbackOf(t, ot, newBrowser(t))
		refused(t, ot, newBrowser(t).get(t, cb))
	})
	t.Run("expired cookie", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		cb, _ := callbackOf(t, ot, b)
		ot.clock.add(flowLifetime)
		r := b.get(t, cb)
		refused(t, ot, r)
		if !clears(r) {
			t.Errorf("the callback kept an expired sign-in cookie: %q", r.header.Values("Set-Cookie"))
		}
	})
	t.Run("another sign-in's state leaves this one alone", func(t *testing.T) {
		// A link from another site sends the browser to the callback,
		// with the attacker's state, while its own sign-in is under way.
		ot := newOIDCTest(t, nil)
		victim := newBrowser(t)
		authURL, verifier := ot.toProvider(t, victim)
		attackers, _ := callbackOf(t, ot, newBrowser(t))
		r := victim.get(t, attackers)
		refused(t, ot, r)
		if len(r.header.Values("Set-Cookie")) != 0 {
			t.Errorf("the callback touched the sign-in under way: %q", r.header.Values("Set-Cookie"))
		}
		v := sentBack(t, victim.get(t, ot.toCallback(t, victim, authURL)))
		if r := ot.exchange(t, v.Get("code"), verifier); r.code != http.StatusOK {
			t.Errorf("the sign-in under way failed: %v, %d %s", v, r.code, r.body)
		}
	})
	t.Run("tampered cookie", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		cb, u := callbackOf(t, ot, b)
		c := b.jar.Cookies(u)[0]
		v := []byte(c.Value)
		v[len(v)/2] ^= 1
		b.jar.SetCookies(u, []*http.Cookie{{Name: c.Name, Value: string(v), Path: "/api/v1/auth/oidc/callback"}})
		refused(t, ot, b.get(t, cb))
	})
	t.Run("another browser's sign-in", func(t *testing.T) {
		// A callback in a browser whose own sign-in cookie is for
		// another sign-in, as when one is forged or planted.
		ot := newOIDCTest(t, nil)
		victim, attacker := newBrowser(t), newBrowser(t)
		ot.toProvider(t, victim)
		cb, _ := callbackOf(t, ot, attacker)
		refused(t, ot, victim.get(t, cb))
	})
	t.Run("cookie cleared after use", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		cb, _ := callbackOf(t, ot, b)
		sentBack(t, b.get(t, cb))
		// The same callback again: the cookie is gone.
		r := b.get(t, cb)
		if r.code != http.StatusBadRequest || decodeError(t, r.body).Code != codeInvalidState {
			t.Errorf("callback replayed: %d %s", r.code, r.body)
		}
	})
}

// TestOIDCIDTokenRefused checks that the callback sends the browser back
// with sign_in_failed, and no code, for each way an ID token can fail.
func TestOIDCIDTokenRefused(t *testing.T) {
	pub, err := x509.MarshalPKIXPublicKey(&testRSAKey().PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		spoil func(jwt.MapClaims)
		sign  func(jwt.MapClaims) string
		oct   []byte
	}{
		{name: "nonce mismatch", spoil: func(c jwt.MapClaims) { c["nonce"] = rand.Text() }},
		{name: "no nonce", spoil: func(c jwt.MapClaims) { delete(c, "nonce") }},
		{name: "wrong audience", spoil: func(c jwt.MapClaims) { c["aud"] = "someone-else" }},
		{name: "no audience", spoil: func(c jwt.MapClaims) { delete(c, "aud") }},
		{name: "audiences without azp", spoil: func(c jwt.MapClaims) { c["aud"] = []string{testClientID, "someone-else"} }},
		{name: "azp of another party", spoil: func(c jwt.MapClaims) { c["azp"] = "someone-else" }},
		{name: "wrong issuer", spoil: func(c jwt.MapClaims) { c["iss"] = "https://evil.example.com" }},
		{name: "expired", spoil: func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{name: "no expiry", spoil: func(c jwt.MapClaims) { delete(c, "exp") }},
		{name: "alg none", sign: func(c jwt.MapClaims) string {
			return signWith(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, c)
		}},
		{name: "HMAC with the public key's PEM", sign: func(c jwt.MapClaims) string {
			return signWith(jwt.SigningMethodHS256, pubPEM, c)
		}},
		{name: "HMAC with the public key's DER", sign: func(c jwt.MapClaims) string {
			return signWith(jwt.SigningMethodHS256, pub, c)
		}},
		{
			// go-jose will not take an RSA key as an HMAC secret, so
			// only a key set that lists a symmetric key shows that the
			// algorithm list, not the key's type, refuses HMAC.
			name: "HMAC with a symmetric key in the key set", oct: pubPEM,
			sign: func(c jwt.MapClaims) string { return signWith(jwt.SigningMethodHS256, pubPEM, c) },
		},
		{name: "another key", sign: func(c jwt.MapClaims) string {
			return signWith(jwt.SigningMethodRS256, other, c)
		}},
		{name: "groups of the wrong type", spoil: func(c jwt.MapClaims) { c["groups"] = map[string]any{"admins": true} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, nil)
			ot.idp.set(func(p *testIdP) { p.spoil, p.sign, p.oct = tt.spoil, tt.sign, tt.oct })
			cb, _ := ot.signIn(t, newBrowser(t))
			wantError(t, cb, codeSignInFailed)
		})
	}

	// Several audiences are fine when occamyd is the authorized party.
	ot := newOIDCTest(t, nil)
	ot.idp.set(func(p *testIdP) {
		p.spoil = func(c jwt.MapClaims) { c["aud"], c["azp"] = []string{"someone-else", testClientID}, testClientID }
	})
	if user, _ := ot.signedIn(t); user != "carol" {
		t.Errorf("signed in as %q", user)
	}
}

// TestOIDCAccessDenied checks the users that the callback refuses, with
// access_denied: whom the provider refused, a token without a usable
// user name, a static user's name unless link_static_users links them,
// and with require_known_users, a user whom no allow list names, by name
// or by group.
func TestOIDCAccessDenied(t *testing.T) {
	for _, tt := range []struct {
		name  string
		edit  func(*config.File)
		idp   func(*testIdP)
		allow bool
	}{
		{name: "the provider refused", idp: func(p *testIdP) { p.authError = "access_denied" }},
		{name: "the provider refused, naming itself as promised", idp: func(p *testIdP) {
			p.authError, p.issParam = "access_denied", p.srv.URL
			p.doc = func(d map[string]any) { d["authorization_response_iss_parameter_supported"] = true }
		}},
		{name: "no subject", idp: func(p *testIdP) { p.spoil = func(c jwt.MapClaims) { delete(c, "sub") } }},
		{name: "empty subject", idp: func(p *testIdP) { p.subject = "" }},
		{
			// go-oidc itself refuses a sub that is not a string.
			name: "subject claim not a string", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "preferred_username" },
			idp: func(p *testIdP) { p.extra = jwt.MapClaims{"preferred_username": 42} },
		},
		{
			name: "subject claim missing", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "preferred_username" },
			idp: func(p *testIdP) { p.extra = nil },
		},
		{
			name: "subject claim", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "preferred_username" },
			idp: func(p *testIdP) { p.subject, p.extra = "a1b2c3", jwt.MapClaims{"preferred_username": "carol"} }, allow: true,
		},
		{name: "subject with a control character", idp: func(p *testIdP) { p.subject = "carol\nuser \"alice\" signed in" }},
		{name: "subject too long", idp: func(p *testIdP) { p.subject = strings.Repeat("c", maxSubject+1) }},
		{
			name: "unknown user", edit: func(f *config.File) { f.Auth.OIDC.RequireKnownUsers = true },
			idp: func(p *testIdP) { p.subject = "mallory" },
		},
		{
			name: "user named in an allow list", edit: func(f *config.File) { f.Auth.OIDC.RequireKnownUsers = true },
			idp: func(p *testIdP) { p.subject = "carol" }, allow: true,
		},
		{
			name: "user in an allowed group", edit: func(f *config.File) { f.Auth.OIDC.RequireKnownUsers = true },
			idp: func(p *testIdP) {
				p.subject, p.extra = "dave", jwt.MapClaims{"groups": []string{"marketing", "admins"}}
			}, allow: true,
		},
		{
			// ops is a static user's group, which no allow list names.
			name: "user in no allowed group", edit: func(f *config.File) { f.Auth.OIDC.RequireKnownUsers = true },
			idp: func(p *testIdP) { p.subject, p.extra = "dave", jwt.MapClaims{"groups": []string{"ops"}} },
		},
		{
			name: "any user without require_known_users",
			idp:  func(p *testIdP) { p.subject = "mallory" }, allow: true,
		},
		// A name belongs to one source unless the file links them.
		{name: "static user", idp: func(p *testIdP) { p.subject = "alice" }},
		{
			name: "static user, require_known_users", edit: func(f *config.File) { f.Auth.OIDC.RequireKnownUsers = true },
			idp: func(p *testIdP) { p.subject = "alice" },
		},
		{
			name: "static user named in an allow list",
			edit: func(f *config.File) { f.Connections[0].Allow.Users = append(f.Connections[0].Allow.Users, "alice") },
			idp:  func(p *testIdP) { p.subject = "alice" },
		},
		{
			// An account at the provider that picks its own
			// preferred_username.
			name: "static user by a claim the user chose", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "preferred_username" },
			idp: func(p *testIdP) {
				p.subject, p.extra = "opaque-123", jwt.MapClaims{"preferred_username": "alice", "groups": []string{"admins"}}
			},
		},
		{
			name: "static user, linked", edit: func(f *config.File) { f.Auth.OIDC.LinkStaticUsers = true },
			idp: func(p *testIdP) { p.subject = "alice" }, allow: true,
		},
		// A static user's name in another case or another normal form is
		// the static user's too, and one with an invisible character is
		// no name at all.
		{name: "static user in another case", idp: func(p *testIdP) { p.subject = "Alice" }},
		{name: "static user in upper case", idp: func(p *testIdP) { p.subject = "ALICE" }},
		{
			name: "static user, decomposed", edit: func(f *config.File) { f.Auth.Users[1].Name = "zo\u00eb" },
			idp: func(p *testIdP) { p.subject = "Zoe\u0308" },
		},
		{name: "static user with an invisible character", idp: func(p *testIdP) { p.subject = "alice\u034f" }},
		{
			// Linked, Alice is a user apart from alice, as access compares
			// names exactly.
			name: "static user in another case, linked", edit: func(f *config.File) { f.Auth.OIDC.LinkStaticUsers = true },
			idp: func(p *testIdP) { p.subject = "Alice" }, allow: true,
		},
		{
			name: "user named in an allow list in another case", edit: func(f *config.File) { f.Auth.OIDC.RequireKnownUsers = true },
			idp: func(p *testIdP) { p.subject = "Carol" },
		},
		{
			name: "static user, linked, require_known_users",
			edit: func(f *config.File) { f.Auth.OIDC.LinkStaticUsers, f.Auth.OIDC.RequireKnownUsers = true, true },
			idp:  func(p *testIdP) { p.subject = "alice" }, allow: true,
		},
		{
			name: "email not verified", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "email" },
			idp: func(p *testIdP) { p.extra = jwt.MapClaims{"email": "carol@example.com", "email_verified": false} },
		},
		{
			name: "email verification unknown", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "email" },
			idp: func(p *testIdP) { p.extra = jwt.MapClaims{"email": "carol@example.com"} },
		},
		{
			name: "email verification a string", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "email" },
			idp: func(p *testIdP) { p.extra = jwt.MapClaims{"email": "carol@example.com", "email_verified": "true"} },
		},
		{
			name: "email verified", edit: func(f *config.File) { f.Auth.OIDC.SubjectClaim = "email" },
			idp:   func(p *testIdP) { p.extra = jwt.MapClaims{"email": "carol@example.com", "email_verified": true} },
			allow: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, tt.edit)
			ot.idp.set(tt.idp)
			if tt.allow {
				ot.signedIn(t)
				return
			}
			cb, _ := ot.signIn(t, newBrowser(t))
			wantError(t, cb, codeAccessDenied)
		})
	}
}

// TestOIDCProviderErrors checks that the provider's failures send the
// browser back with sign_in_failed.
func TestOIDCProviderErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		idp  func(*testIdP)
	}{
		{"authorization error", func(p *testIdP) { p.authError = "server_error" }},
		{"token endpoint refuses", func(p *testIdP) { p.tokenStatus = http.StatusBadRequest }},
		{"token endpoint fails", func(p *testIdP) { p.tokenStatus = http.StatusInternalServerError }},
		{"no ID token", func(p *testIdP) { p.noIDToken = true }},
		{"another issuer in the answer", func(p *testIdP) { p.issParam = "https://evil.example.com" }},
		{"no issuer in the answer of a provider that promises one", func(p *testIdP) {
			p.doc = func(d map[string]any) { d["authorization_response_iss_parameter_supported"] = true }
		}},
		// RFC 9207 compares the issuer exactly.
		{"the issuer with a trailing slash", func(p *testIdP) { p.issParam = p.srv.URL + "/" }},
		{"the issuer in upper case", func(p *testIdP) { p.issParam = "HTTP" + strings.TrimPrefix(p.srv.URL, "http") }},
		{"the issuer under another host name", func(p *testIdP) { p.issParam = strings.Replace(p.srv.URL, "127.0.0.1", "localhost", 1) }},
		{"the issuer with a dot segment", func(p *testIdP) { p.issParam = p.srv.URL + "/." }},
		// An error response is checked too (RFC 9207, 2.4), and its
		// error is not passed on as the provider's.
		{"an error naming another issuer", func(p *testIdP) { p.authError, p.issParam = "access_denied", "https://evil.example.com" }},
		{"an error naming no issuer, from a provider that promises one", func(p *testIdP) {
			p.authError = "access_denied"
			p.doc = func(d map[string]any) { d["authorization_response_iss_parameter_supported"] = true }
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logged := captureLog(t)
			ot := newOIDCTest(t, nil)
			ot.idp.set(tt.idp)
			cb, _ := ot.signIn(t, newBrowser(t))
			wantError(t, cb, codeSignInFailed)
			if out := logged(); strings.Contains(out, "secret details") || strings.Contains(out, "is not passed on") {
				t.Errorf("the log holds the provider's description:\n%s", out)
			}
		})
	}

	t.Run("no code", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		b := newBrowser(t)
		authURL, _ := ot.toProvider(t, b)
		u, _ := url.Parse(ot.toCallback(t, b, authURL))
		q := u.Query()
		q.Del("code")
		u.RawQuery = q.Encode()
		wantError(t, b.get(t, u.String()), codeSignInFailed)
	})
	t.Run("the issuer named twice", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.issParam = p.srv.URL })
		b := newBrowser(t)
		authURL, _ := ot.toProvider(t, b)
		u, _ := url.Parse(ot.toCallback(t, b, authURL))
		q := u.Query()
		q.Add("iss", ot.idp.srv.URL)
		u.RawQuery = q.Encode()
		wantError(t, b.get(t, u.String()), codeSignInFailed)
	})
	t.Run("the issuer named", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.issParam = p.srv.URL })
		ot.signedIn(t)
	})
	t.Run("the issuer named, as promised", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) {
			p.issParam = p.srv.URL
			p.doc = func(d map[string]any) { d["authorization_response_iss_parameter_supported"] = true }
		})
		ot.signedIn(t)
	})
}

// TestOIDCEndpoints checks which endpoints of a discovery document
// occamyd talks to: https, or http on the loopback interface when the
// issuer is there too.
func TestOIDCEndpoints(t *testing.T) {
	for _, tt := range []struct {
		issuer, endpoint string
		ok               bool
	}{
		{"https://id.example.com", "https://id.example.com/token", true},
		{"https://id.example.com", "https://elsewhere.example.com/token", true},
		// The discovery document, from the issuer occamyd trusts, picks
		// the hosts: occamyd restricts the scheme only.
		{"https://id.example.com", "https://127.0.0.1:6443/token", true},
		{"https://id.example.com", "https://169.254.169.254/latest", true},
		{"https://id.example.com", "http://id.example.com/token", false},
		{"https://id.example.com", "http://localhost:8080/token", false},
		{"https://id.example.com", "http://127.0.0.1:8080/token", false},
		{"https://id.example.com", "http://[::1]:8080/token", false},
		{"https://id.example.com", "ftp://id.example.com/token", false},
		{"https://id.example.com", "/token", false},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080/token", true},
		{"http://localhost:8080", "http://localhost:9090/token", true},
		{"http://127.0.0.1:8080", "https://id.example.com/token", true},
		{"http://127.0.0.1:8080", "http://id.example.com/token", false},
	} {
		if got := endpointProblem(tt.issuer, tt.endpoint) == ""; got != tt.ok {
			t.Errorf("endpoint %s of issuer %s: accepted %v, want %v", tt.endpoint, tt.issuer, got, tt.ok)
		}
	}
}

// TestOIDCPKCE checks that occamyd proves the code is its own with the
// verifier of the challenge it sent: a challenge changed on the way to
// the provider fails the exchange.
func TestOIDCPKCE(t *testing.T) {
	ot := newOIDCTest(t, nil)
	b := newBrowser(t)
	authURL, _ := ot.toProvider(t, b)
	u, _ := url.Parse(authURL)
	q := u.Query()
	q.Set("code_challenge", oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier()))
	u.RawQuery = q.Encode()
	wantError(t, b.get(t, ot.toCallback(t, b, u.String())), codeSignInFailed)
	if _, n := ot.idp.counts(); n != 1 {
		t.Errorf("%d exchanges, want 1", n)
	}
}

// TestOIDCGroups checks how the groups claim maps to groups: only the
// groups the file names are kept.
func TestOIDCGroups(t *testing.T) {
	for _, tt := range []struct {
		name   string
		claim  string // groups_claim
		extra  jwt.MapClaims
		groups []string
	}{
		{"mapped and unmapped", "groups", jwt.MapClaims{"groups": []string{"marketing", "admins", "ops", "admins", "Admins"}}, []string{"admins", "ops"}},
		{"none mapped", "groups", jwt.MapClaims{"groups": []string{"marketing"}}, nil},
		{"one as a string", "groups", jwt.MapClaims{"groups": "admins"}, []string{"admins"}},
		{"claim missing", "groups", nil, nil},
		{"claim null", "groups", jwt.MapClaims{"groups": nil}, nil},
		{"claim not configured", "", jwt.MapClaims{"groups": []string{"admins"}}, nil},
		{"another claim", "roles", jwt.MapClaims{"roles": []string{"dev"}, "groups": []string{"admins"}}, []string{"dev"}},
		{"a claim name with dots", "https://occamy.example.com/groups", jwt.MapClaims{"https://occamy.example.com/groups": []string{"ops"}}, []string{"ops"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, func(f *config.File) { f.Auth.OIDC.GroupsClaim = tt.claim })
			ot.idp.set(func(p *testIdP) { p.extra = tt.extra })
			if user, groups := ot.signedIn(t); user != "carol" || !slices.Equal(groups, tt.groups) {
				t.Errorf("signed in as %q in %q, want carol in %q", user, groups, tt.groups)
			}
		})
	}
}

// TestOIDCLinkedGroups checks the groups of an OIDC user that
// link_static_users links to a static user: those of the groups claim,
// not the static user's.
func TestOIDCLinkedGroups(t *testing.T) {
	for _, tt := range []struct {
		name   string
		extra  jwt.MapClaims
		groups []string
	}{
		{"no groups claim", nil, nil},
		{"groups claim", jwt.MapClaims{"groups": []string{"admins"}}, []string{"admins"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, func(f *config.File) { f.Auth.OIDC.LinkStaticUsers = true })
			ot.idp.set(func(p *testIdP) { p.subject, p.extra = "alice", tt.extra }) // static alice is in ops
			if user, groups := ot.signedIn(t); user != "alice" || !slices.Equal(groups, tt.groups) {
				t.Errorf("signed in as %q in %q, want alice in %q", user, groups, tt.groups)
			}
		})
	}
}

// TestOIDCCode checks the one-time code: once, for a minute, with the
// verifier of the login's challenge.
func TestOIDCCode(t *testing.T) {
	codeOf := func(t *testing.T, ot *oidcTest) (string, string) {
		t.Helper()
		cb, verifier := ot.signIn(t, newBrowser(t))
		return sentBack(t, cb).Get("code"), verifier
	}
	unauthorized := func(t *testing.T, r reply) {
		t.Helper()
		if r.code != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != bearerRealm {
			t.Fatalf("exchange: %d %v %s", r.code, r.header, r.body)
		}
		if e := decodeError(t, r.body); e.Code != codeUnauthorized {
			t.Errorf("exchange: %+v", e)
		}
	}
	ot := newOIDCTest(t, nil)

	t.Run("used twice", func(t *testing.T) {
		code, verifier := codeOf(t, ot)
		if r := ot.exchange(t, code, verifier); r.code != http.StatusOK {
			t.Fatalf("first exchange: %d %s", r.code, r.body)
		}
		unauthorized(t, ot.exchange(t, code, verifier))
	})
	t.Run("wrong verifier", func(t *testing.T) {
		code, verifier := codeOf(t, ot)
		unauthorized(t, ot.exchange(t, code, oauth2.GenerateVerifier()))
		// The code is spent by a wrong try.
		unauthorized(t, ot.exchange(t, code, verifier))
	})
	t.Run("the challenge as verifier", func(t *testing.T) {
		code, verifier := codeOf(t, ot)
		unauthorized(t, ot.exchange(t, code, oauth2.S256ChallengeFromVerifier(verifier)))
	})
	t.Run("malformed verifier", func(t *testing.T) {
		code, verifier := codeOf(t, ot)
		unauthorized(t, ot.exchange(t, code, verifier[:42]))
	})
	t.Run("expired", func(t *testing.T) {
		code, verifier := codeOf(t, ot)
		ot.clock.add(codeLifetime)
		unauthorized(t, ot.exchange(t, code, verifier))
	})
	t.Run("unknown", func(t *testing.T) {
		unauthorized(t, ot.exchange(t, rand.Text(), oauth2.GenerateVerifier()))
	})
	t.Run("bad requests", func(t *testing.T) {
		for _, tt := range []struct {
			contentType, body string
			status            int
		}{
			{"application/json", `{"code":"x"}`, http.StatusBadRequest},
			{"application/json", `{"code_verifier":"x"}`, http.StatusBadRequest},
			{"application/json", `{"code":"x","code_verifier":"y","client_id":"z"}`, http.StatusBadRequest},
			{"application/x-www-form-urlencoded", "code=x&code_verifier=y", http.StatusUnsupportedMediaType},
		} {
			r := do(t, http.MethodPost, ot.url+"/api/v1/auth/oidc/token", http.Header{"Content-Type": {tt.contentType}}, tt.body)
			if r.code != tt.status {
				t.Errorf("%s %s: %d %s", tt.contentType, tt.body, r.code, r.body)
			}
		}
	})
}

// TestOIDCPendingLimit checks that codes waiting for their clients are
// bounded, for each user and in all, and that expired ones make room.
func TestOIDCPendingLimit(t *testing.T) {
	t.Run("per user", func(t *testing.T) {
		o := newOIDCTest(t, nil).s.oidc
		var codes, verifiers []string
		for range maxPendingPerUser + 1 {
			v := oauth2.GenerateVerifier()
			code, err := o.pend("mallory", nil, oauth2.S256ChallengeFromVerifier(v))
			if err != nil {
				t.Fatal(err)
			}
			codes, verifiers = append(codes, code), append(verifiers, v)
		}
		// Another user's code does not count against mallory's.
		if _, err := o.pend("carol", nil, "c"); err != nil {
			t.Fatal(err)
		}
		if _, ok := o.redeem(codes[0], verifiers[0]); ok {
			t.Error("the oldest of more than maxPendingPerUser codes still redeems")
		}
		for i := 1; i < len(codes); i++ {
			if _, ok := o.redeem(codes[i], verifiers[i]); !ok {
				t.Errorf("code %d of %d does not redeem", i+1, len(codes))
			}
		}
	})
	t.Run("names apart", func(t *testing.T) {
		// Codes count by the exact name, so names that pass for
		// mallory's evict none of mallory's.
		o := newOIDCTest(t, nil).s.oidc
		var codes, verifiers []string
		for range maxPendingPerUser {
			v := oauth2.GenerateVerifier()
			code, err := o.pend("mallory", nil, oauth2.S256ChallengeFromVerifier(v))
			if err != nil {
				t.Fatal(err)
			}
			codes, verifiers = append(codes, code), append(verifiers, v)
		}
		for _, other := range []string{"Mallory", "mallory ", "m\u0430llory"} {
			for range maxPendingPerUser + 2 {
				if _, err := o.pend(other, nil, "c"); err != nil {
					t.Fatal(err)
				}
			}
		}
		for i := range codes {
			if _, ok := o.redeem(codes[i], verifiers[i]); !ok {
				t.Errorf("mallory's code %d was evicted by another name", i+1)
			}
		}
	})
	t.Run("one user signing in over and over", func(t *testing.T) {
		// An account at the provider that signs in without end, and
		// never exchanges its codes, keeps others from no sign-in.
		ot := newOIDCTest(t, nil)
		for range maxPendingCodes {
			if _, err := ot.s.oidc.pend("mallory", nil, "c"); err != nil {
				t.Fatal(err)
			}
		}
		ot.s.oidc.codesMu.Lock()
		n := len(ot.s.oidc.codes)
		ot.s.oidc.codesMu.Unlock()
		if n != maxPendingPerUser {
			t.Errorf("%d codes wait for one user, want %d", n, maxPendingPerUser)
		}
		if user, _ := ot.signedIn(t); user != "carol" {
			t.Errorf("signed in as %q", user)
		}
	})
	t.Run("in all", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		o := ot.s.oidc
		for i := range maxPendingCodes {
			if _, err := o.pend(fmt.Sprintf("user%d", i), nil, "c"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := o.pend("carol", nil, "c"); err == nil {
			t.Fatal("no limit on waiting codes")
		}
		cb, _ := ot.signIn(t, newBrowser(t))
		wantError(t, cb, codeUnavailable)
		ot.clock.add(codeLifetime)
		if _, err := o.pend("carol", nil, "c"); err != nil {
			t.Fatalf("expired codes kept: %v", err)
		}
	})
}

// TestOIDCCodeRace checks that a code redeems once, however many
// exchanges race for it. It takes many rounds, since a look-up and a
// delete under two locks, one right after the other, leave a window of
// nanoseconds.
func TestOIDCCodeRace(t *testing.T) {
	o := newOIDCTest(t, nil).s.oidc
	for range 500 {
		v := oauth2.GenerateVerifier()
		code, err := o.pend("carol", nil, oauth2.S256ChallengeFromVerifier(v))
		if err != nil {
			t.Fatal(err)
		}
		var won atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 32 {
			wg.Go(func() {
				<-start
				if _, ok := o.redeem(code, v); ok {
					won.Add(1)
				}
			})
		}
		close(start)
		wg.Wait()
		if n := won.Load(); n != 1 {
			t.Fatalf("%d of 32 racing exchanges of one code succeeded, want 1", n)
		}
	}
}

// TestOIDCClientAuth checks how occamyd authenticates to the token
// endpoint: a public client by its ID, a confidential one with its
// secret in a Basic header, or in the body for a provider that takes
// only that.
func TestOIDCClientAuth(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("s3cret:/+\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		secret  bool
		methods []string
		want    string
	}{
		{"public", false, nil, "none"},
		{"public, whatever the provider takes", false, []string{"client_secret_basic"}, "none"},
		{"confidential", true, nil, "basic"},
		{"confidential, basic or post", true, []string{"client_secret_post", "client_secret_basic"}, "basic"},
		{"confidential, post only", true, []string{"client_secret_post"}, "post"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, func(f *config.File) {
				if tt.secret {
					f.Auth.OIDC.ClientSecretFile = secretFile
				}
			})
			ot.idp.set(func(p *testIdP) {
				p.authMethods = tt.methods
				if tt.secret {
					p.secret = "s3cret:/+"
				}
			})
			ot.signedIn(t)
			ot.idp.set(func(p *testIdP) {
				if p.clientAuth != tt.want || p.exchanges != 1 {
					t.Errorf("client authentication %s in %d exchanges, want %s in 1", p.clientAuth, p.exchanges, tt.want)
				}
			})
		})
	}
}

// TestOIDCDiscovery checks that the provider is discovered on the first
// sign-in, once, and that a provider that is down, hangs or misdescribes
// itself fails sign-ins, not occamyd's start.
func TestOIDCDiscovery(t *testing.T) {
	challenge := oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())
	unavailable := func(t *testing.T, ot *oidcTest) {
		t.Helper()
		r := newBrowser(t).get(t, ot.loginURL(testReturnURL, challenge))
		wantError(t, r, codeUnavailable)
		if len(r.header.Values("Set-Cookie")) != 0 {
			t.Errorf("a cookie for a sign-in that cannot start: %q", r.header.Values("Set-Cookie"))
		}
	}

	t.Run("lazily and once", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		if n, _ := ot.idp.counts(); n != 0 {
			t.Fatalf("%d discoveries before the first sign-in", n)
		}
		ot.signedIn(t)
		ot.signedIn(t)
		if n, _ := ot.idp.counts(); n != 1 {
			t.Errorf("%d discoveries, want 1", n)
		}
	})
	t.Run("provider down at start", func(t *testing.T) {
		probe := localListener(t)
		closed := "http://" + probe.Addr().String()
		probe.Close()
		ot := newOIDCTest(t, func(f *config.File) { f.Auth.OIDC.Issuer = closed })
		body := `{"username":"alice","password":"` + alicePassword + `"}`
		if r := do(t, http.MethodPost, ot.url+"/api/v1/auth/token", http.Header{"Content-Type": {"application/json"}}, body); r.code != http.StatusOK {
			t.Errorf("static sign-in with the provider down: %d %s", r.code, r.body)
		}
		start := time.Now()
		unavailable(t, ot)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("login took %v", d)
		}
	})
	t.Run("provider back up", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.down = true })
		unavailable(t, ot)
		unavailable(t, ot)
		if n, _ := ot.idp.counts(); n != 1 {
			t.Errorf("%d discoveries in the retry interval, want 1", n)
		}
		ot.idp.set(func(p *testIdP) { p.down = false })
		ot.clock.add(oidcRetry)
		ot.signedIn(t)
		if n, _ := ot.idp.counts(); n != 2 {
			t.Errorf("%d discoveries, want 2", n)
		}
	})
	t.Run("provider hangs", func(t *testing.T) {
		defer func(d time.Duration) { oidcTimeout = d }(oidcTimeout)
		oidcTimeout = 300 * time.Millisecond
		ot := newOIDCTest(t, nil)
		hang := make(chan struct{})
		defer close(hang)
		ot.idp.set(func(p *testIdP) { p.hang = hang })
		start := time.Now()
		unavailable(t, ot)
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("login took %v with a %v timeout", d, oidcTimeout)
		}
	})
	for _, tt := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"another issuer", func(d map[string]any) { d["issuer"] = "https://evil.example.com" }},
		{"token endpoint over http", func(d map[string]any) { d["token_endpoint"] = "http://id.example.com/token" }},
		{"authorization endpoint over http", func(d map[string]any) { d["authorization_endpoint"] = "http://id.example.com/authorize" }},
		{"no key set", func(d map[string]any) { delete(d, "jwks_uri") }},
		// The flag is read as a JSON bool, strictly.
		{"iss flag as a string", func(d map[string]any) { d["authorization_response_iss_parameter_supported"] = "true" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ot := newOIDCTest(t, nil)
			ot.idp.set(func(p *testIdP) { p.doc = tt.edit })
			unavailable(t, ot)
		})
	}
	t.Run("https issuer", func(t *testing.T) {
		// An https issuer's endpoints must be https, also on the loopback
		// interface, where http passes for an http issuer.
		var tokenURL atomic.Value
		var srv *httptest.Server
		srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 srv.URL,
				"authorization_endpoint": srv.URL + "/authorize",
				"token_endpoint":         tokenURL.Load(),
				"jwks_uri":               srv.URL + "/jwks",
			})
		}))
		srv.StartTLS()
		t.Cleanup(srv.Close)
		for _, tt := range []struct {
			name, token string
			ok          bool
		}{
			{"token endpoint over https", srv.URL + "/token", true},
			{"token endpoint over http on loopback", "http" + strings.TrimPrefix(srv.URL, "https") + "/token", false},
		} {
			t.Run(tt.name, func(t *testing.T) {
				tokenURL.Store(tt.token)
				ot := newOIDCTest(t, func(f *config.File) { f.Auth.OIDC.Issuer = srv.URL })
				ot.s.oidc.client.Transport = limitBody{srv.Client().Transport, maxProviderBody}
				if !tt.ok {
					unavailable(t, ot)
					return
				}
				r := newBrowser(t).get(t, ot.loginURL(testReturnURL, challenge))
				if loc := r.header.Get("Location"); r.code != http.StatusFound || !strings.HasPrefix(loc, srv.URL+"/authorize?") {
					t.Errorf("login: %d to %q: %s", r.code, loc, r.body)
				}
			})
		}
	})
	t.Run("document too large", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) {
			p.doc = func(d map[string]any) { d["padding"] = strings.Repeat("x", maxProviderBody) }
		})
		unavailable(t, ot)
	})
}

// TestRunOIDCProviderDown runs occamyd, through Run, with a provider that
// nothing serves: it starts, says that discovery failed, signs static
// users in, and sends OIDC sign-ins back with unavailable.
func TestRunOIDCProviderDown(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "v1.yaml")
	raw := `version: 1
listen: 127.0.0.1:0
debug: {address: 127.0.0.1:0}
auth:
  signing_key_file: ` + filepath.Join(dir, "signing.key") + `
  users: [{name: alice, password_hash: "` + hashPassword(t, alicePassword, config.MinBcryptCost) + `"}]
  oidc:
    issuer: http://127.0.0.1:1
    client_id: occamy
    redirect_url: http://127.0.0.1:5636/api/v1/auth/oidc/callback
    return_urls: [` + testReturnURL + `]
`
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c := startChild(t, file, time.Second)
	_, addr, ok := strings.Cut(c.output(), "serving API v1 at ")
	if !ok {
		t.Fatalf("occamyd did not serve:\n%s", c.output())
	}
	base, _, _ := strings.Cut(addr, "\n")
	if !strings.Contains(c.output(), "OIDC discovery at http://127.0.0.1:1 failed") {
		c.waitLine(t, "OIDC discovery at http://127.0.0.1:1 failed")
	}

	body := `{"username":"alice","password":"` + alicePassword + `"}`
	if r := do(t, http.MethodPost, base+"/api/v1/auth/token", http.Header{"Content-Type": {"application/json"}}, body); r.code != http.StatusOK {
		t.Errorf("static sign-in with the provider down: %d %s", r.code, r.body)
	}
	r := newBrowser(t).get(t, base+"/api/v1/auth/oidc/login?code_challenge="+oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier()))
	wantError(t, r, codeUnavailable)
}

// TestOIDCRoutesOff checks that without auth.oidc there are no OIDC
// routes.
func TestOIDCRoutesOff(t *testing.T) {
	ts := newTestServer(t, nil)
	for _, path := range []string{"/api/v1/auth/oidc/login", "/api/v1/auth/oidc/callback"} {
		r := do(t, http.MethodGet, ts.srv.URL+path, nil, "")
		if r.code != http.StatusNotFound || decodeError(t, r.body).Code != codeNotFound {
			t.Errorf("GET %s: %d %s", path, r.code, r.body)
		}
	}
	if ts.s.oidc != nil {
		t.Error("an OIDC sign-in without auth.oidc")
	}
}

// TestOIDCValidators checks the forms of PKCE challenges and verifiers
// and of user names.
func TestOIDCValidators(t *testing.T) {
	v := oauth2.GenerateVerifier()
	for s, want := range map[string]bool{
		oauth2.S256ChallengeFromVerifier(v): true,
		v[:42]:                              false,
		base64.RawURLEncoding.EncodeToString(make([]byte, 31)):                   false,
		base64.RawURLEncoding.EncodeToString(make([]byte, 33)):                   false,
		base64.URLEncoding.EncodeToString(make([]byte, 32)):                      false,
		base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("\xff", 32))): false,
	} {
		if validChallenge(s) != want {
			t.Errorf("validChallenge(%q) = %v", s, !want)
		}
	}
	for s, want := range map[string]bool{
		v:                             true,
		strings.Repeat("a", 43):       true,
		strings.Repeat("a", 128):      true,
		strings.Repeat("a", 42):       false,
		strings.Repeat("a", 129):      false,
		strings.Repeat("a", 42) + "+": false,
		strings.Repeat("-._~", 11):    true,
	} {
		if validVerifier(s) != want {
			t.Errorf("validVerifier(%q) = %v", s, !want)
		}
	}
	for s, want := range map[string]bool{
		"carol":                  true,
		"carol@example.com":      true,
		"Carol Smith":            true,
		"Zo\u00eb \u00c5berg":    true,
		"\u5c71\u7530":           true,
		"":                       false,
		"carol\x00":              false,
		"carol\t":                false,
		"carol\u0085":            false, // next line, a control character
		"\xff":                   false,
		strings.Repeat("c", 255): true,
		strings.Repeat("c", 256): false,
		// Format characters, which render as nothing or reorder text,
		// separators, and spaces that look like U+0020.
		"car\u200bol":  false, // zero-width space
		"car\u200dol":  false, // zero-width joiner
		"\u202elorac":  false, // right-to-left override
		"carol\u00ad":  false, // soft hyphen
		"\ufeffcarol":  false, // byte order mark
		"carol\u2060":  false, // word joiner
		"carol\u2028":  false, // line separator
		"carol\u2029":  false, // paragraph separator
		"carol\u00a0x": false, // no-break space
		"carol\u3000x": false, // ideographic space
		// Default-ignorable code points, which render as nothing, and the
		// blank Braille pattern.
		"carol\u034f":     false, // combining grapheme joiner
		"carol\ufe0f":     false, // variation selector 16
		"carol\U000e0100": false, // variation selector 17
		"carol\u180b":     false, // Mongolian free variation selector
		"carol\u115f":     false, // Hangul choseong filler
		"carol\u1160":     false, // Hangul jungseong filler
		"carol\u3164":     false, // Hangul filler
		"carol\uffa0":     false, // halfwidth Hangul filler
		"carol\u17b4":     false, // Khmer inherent vowel
		"carol\u2800":     false, // Braille pattern blank
		"carol\u2801":     true,  // a Braille pattern that shows
		"care\u0301":      true,  // a combining accent, which shows
		// Look-alikes show, and pass: access compares names exactly.
		"al\u0456ce": true, // Cyrillic i
		"\uff41lice": true, // fullwidth a
	} {
		if validSubject(s) != want {
			t.Errorf("validSubject(%q) = %v", s, !want)
		}
	}
}

// redirector answers every request with a redirect to the same path on
// target, with status, and returns its URL.
func redirector(t *testing.T, target string, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.Path, status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestOIDCProviderRedirects checks that occamyd follows no redirect from
// the provider, at discovery, the key set or the token endpoint, so that
// nothing, least of all the code, the verifier and the client secret,
// goes to a URL that was not checked.
func TestOIDCProviderRedirects(t *testing.T) {
	t.Run("discovery", func(t *testing.T) {
		ot := newOIDCTest(t, func(f *config.File) {
			f.Auth.OIDC.Issuer = redirector(t, f.Auth.OIDC.Issuer, http.StatusTemporaryRedirect)
		})
		r := newBrowser(t).get(t, ot.loginURL(testReturnURL, oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())))
		wantError(t, r, codeUnavailable)
		if n, _ := ot.idp.counts(); n != 0 {
			t.Errorf("discovery followed a redirect %d times", n)
		}
	})
	t.Run("key set", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		keys := redirector(t, ot.idp.srv.URL, http.StatusFound) + "/jwks"
		ot.idp.set(func(p *testIdP) { p.doc = func(d map[string]any) { d["jwks_uri"] = keys } })
		cb, _ := ot.signIn(t, newBrowser(t))
		wantError(t, cb, codeSignInFailed)
	})
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		status int
		secret bool
	}{
		{"token endpoint", http.StatusTemporaryRedirect, false},
		{"token endpoint, client secret in the body", http.StatusPermanentRedirect, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got atomic.Value
			elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				got.Store(r.PostForm.Encode() + " " + r.Header.Get("Authorization"))
				http.Error(w, "no", http.StatusBadRequest)
			}))
			defer elsewhere.Close()
			tokenURL := redirector(t, elsewhere.URL, tt.status) + "/token"
			ot := newOIDCTest(t, func(f *config.File) {
				if tt.secret {
					f.Auth.OIDC.ClientSecretFile = secretFile
				}
			})
			ot.idp.set(func(p *testIdP) {
				if tt.secret {
					p.secret, p.authMethods = "s3cret", []string{"client_secret_post"}
				}
				p.doc = func(d map[string]any) { d["token_endpoint"] = tokenURL }
			})
			cb, _ := ot.signIn(t, newBrowser(t))
			wantError(t, cb, codeSignInFailed)
			if v, _ := got.Load().(string); v != "" {
				t.Errorf("the exchange followed a redirect, carrying %s", v)
			}
		})
	}
}

// TestOIDCProviderTextLogged checks that text from the provider reaches
// the log quoted and truncated: a body that go-oidc puts in an error, or
// an error code in the callback, forges no log line and floods nothing.
func TestOIDCProviderTextLogged(t *testing.T) {
	forged := "\n2026/10/30 12:00:00 user \"alice\" signed in"
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "maintenance"+forged+strings.Repeat("x", 4096), http.StatusServiceUnavailable)
	}))
	t.Cleanup(evil.Close)
	check := func(t *testing.T, out string) {
		t.Helper()
		if strings.Contains(out, forged) {
			t.Errorf("the provider forged a log line:\n%s", out)
		}
		if strings.Contains(out, strings.Repeat("x", maxLogged)) {
			t.Errorf("the provider's text is not truncated:\n%s", out)
		}
		if !strings.Contains(out, `\n2026/10/30 12:00:00 user \"alice\" signed in`) {
			t.Errorf("the provider's text is not in the log, quoted:\n%s", out)
		}
	}
	t.Run("discovery", func(t *testing.T) {
		logged := captureLog(t)
		ot := newOIDCTest(t, func(f *config.File) { f.Auth.OIDC.Issuer = evil.URL })
		wantError(t, newBrowser(t).get(t, ot.loginURL(testReturnURL, oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier()))), codeUnavailable)
		check(t, logged())
	})
	t.Run("key set", func(t *testing.T) {
		logged := captureLog(t)
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.doc = func(d map[string]any) { d["jwks_uri"] = evil.URL } })
		cb, _ := ot.signIn(t, newBrowser(t))
		wantError(t, cb, codeSignInFailed)
		check(t, logged())
	})
	t.Run("the provider's error", func(t *testing.T) {
		logged := captureLog(t)
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.authError = "server_error" + forged + strings.Repeat("x", 4096) })
		cb, _ := ot.signIn(t, newBrowser(t))
		wantError(t, cb, codeSignInFailed)
		check(t, logged())
	})
	t.Run("the token endpoint's error", func(t *testing.T) {
		logged := captureLog(t)
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) {
			p.tokenStatus, p.tokenError = http.StatusBadRequest, "invalid_grant"+forged+strings.Repeat("x", 4096)
		})
		cb, _ := ot.signIn(t, newBrowser(t))
		wantError(t, cb, codeSignInFailed)
		check(t, logged())
	})
	t.Run("the ID token's azp", func(t *testing.T) {
		logged := captureLog(t)
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.extra = jwt.MapClaims{"azp": "other" + forged + strings.Repeat("x", 4096)} })
		cb, _ := ot.signIn(t, newBrowser(t))
		wantError(t, cb, codeSignInFailed)
		check(t, logged())
	})
	t.Run("quote", func(t *testing.T) {
		long := strings.Repeat("x", maxLogged)
		for in, want := range map[string]string{
			"":            `""`,
			"a\nb":        `"a\nb"`,
			"\x1b[31mred": `"\x1b[31mred"`,
			"\u202eevil":  `"\u202eevil"`,
			long:          `"` + long + `"`,
			long + "y":    `"` + long + `"...`,
		} {
			if got := quote(in); got != want {
				t.Errorf("quote(%q) = %s, want %s", in, got, want)
			}
		}
	})
}

// started takes a fresh browser through login and the provider, and
// returns the callback URL, the sign-in cookie the browser holds, and the
// client's verifier.
func (ot *oidcTest) started(t *testing.T) (cb, cookie, verifier string) {
	t.Helper()
	b := newBrowser(t)
	authURL, verifier := ot.toProvider(t, b)
	cb = ot.toCallback(t, b, authURL)
	u, err := url.Parse(cb)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.jar.Cookies(u) {
		if c.Name == flowCookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no sign-in cookie")
	}
	return cb, cookie, verifier
}

// callbackWith sends a browser to the callback with the sign-in cookies
// given, in order, as a browser that holds one for the callback's host
// and one for a parent domain sends both.
func callbackWith(t *testing.T, cb string, cookies ...string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, cb, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pairs []string
	for _, c := range cookies {
		pairs = append(pairs, flowCookieName+"="+c)
	}
	req.Header.Set("Cookie", strings.Join(pairs, "; "))
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, body}
}

// TestOIDCPlantedCookie checks the callback in a browser that holds,
// beside its own sign-in cookie, one that a sibling subdomain planted for
// the parent domain: an attacker's sealed cookie, which login hands to
// anyone.
func TestOIDCPlantedCookie(t *testing.T) {
	t.Run("does not shadow the sign-in's own", func(t *testing.T) {
		ot := newOIDCTest(t, nil)
		_, planted, _ := ot.started(t)
		cb, own, verifier := ot.started(t)
		code := sentBack(t, callbackWith(t, cb, planted, own)).Get("code")
		if r := ot.exchange(t, code, verifier); r.code != http.StatusOK {
			t.Errorf("exchange: %d %s", r.code, r.body)
		}
	})
	t.Run("signs no one in as the attacker", func(t *testing.T) {
		// The attacker signs in at the provider as mallory, plants the
		// cookie, and sends the victim to the attacker's callback.
		ot := newOIDCTest(t, nil)
		ot.idp.set(func(p *testIdP) { p.subject = "mallory" })
		cb, planted, _ := ot.started(t)
		_, own, verifier := ot.started(t)
		code := sentBack(t, callbackWith(t, cb, planted, own)).Get("code")
		if r := ot.exchange(t, code, verifier); r.code != http.StatusUnauthorized {
			t.Errorf("the victim's client exchanged the attacker's code: %d %s", r.code, r.body)
		}
	})
}
