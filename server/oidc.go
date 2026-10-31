// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

// Sign-in through an OpenID Connect provider takes four steps:
//
//  1. The client sends the browser to GET /api/v1/auth/oidc/login with
//     return_url, one of auth.oidc.return_urls, and code_challenge, the
//     S256 challenge (RFC 7636) of a verifier that the client keeps.
//  2. occamyd seals the state, the nonce, its own PKCE verifier for the
//     provider, the return URL and the client's challenge into a cookie,
//     and sends the browser to the provider.
//  3. The provider sends the browser to the callback, which matches the
//     state to the cookie, exchanges the provider's code with occamyd's
//     verifier, verifies the ID token and its nonce, maps its claims to
//     a user and groups, and sends the browser back to the return URL
//     with a one-time code in the fragment: #code=…, or #error=… if the
//     sign-in failed.
//  4. The client posts that code with its verifier to POST
//     /api/v1/auth/oidc/token and gets an access token, in the shape
//     POST /api/v1/auth/token answers.
//
// The access token never appears in a URL, so it reaches no log, Referer
// or history. The one-time code does sit in the browser's history, but it
// lives a minute, works once, and only with the verifier that the client
// that started the sign-in holds, so a code planted in another browser
// signs no one in.

// Error codes of OIDC sign-in, besides API v1's. invalid_state is a JSON
// error of the callback; the others reach the client in the fragment of
// its return URL.
const (
	codeInvalidState = "invalid_state"  // the callback matches no sign-in started in this browser
	codeAccessDenied = "access_denied"  // the provider or occamyd refused the user
	codeSignInFailed = "sign_in_failed" // the provider failed, or its answer did not verify
	codeUnavailable  = "unavailable"    // the provider cannot be reached
)

// Limits of OIDC sign-in.
const (
	// flowLifetime bounds the time from login to callback, the time a
	// person has to sign in at the provider.
	flowLifetime = 10 * time.Minute
	// codeLifetime bounds the time from callback to the client's
	// exchange of the one-time code, which the client makes at once.
	codeLifetime = time.Minute
	// maxPendingCodes bounds the one-time codes waiting for exchange.
	maxPendingCodes = 4096
	// maxPendingPerUser bounds the codes of one user waiting for
	// exchange, so that one account at the provider, signing in over and
	// over, cannot fill the table for everyone: the user's oldest code
	// makes room for a new one.
	maxPendingPerUser = 3
	// maxProviderBody bounds a response from the provider.
	maxProviderBody = 1 << 20
	// maxSubject bounds the length of a user name from the provider; an
	// OIDC sub is at most 255 ASCII characters.
	maxSubject = config.MaxNameBytes
	// maxLogged bounds what of the provider's or a client's text reaches
	// the log.
	maxLogged = 256
)

// Variables, so that tests can shorten them.
var (
	// oidcTimeout bounds each request to the provider: discovery, the
	// key set and the code exchange.
	oidcTimeout = 10 * time.Second
	// oidcRetry is how long a failed discovery is remembered before the
	// next sign-in tries again.
	oidcRetry = 10 * time.Second
)

// idTokenAlgs are the algorithms an ID token may be signed with: the
// asymmetric ones that go-oidc supports. Neither none nor an HMAC is
// accepted, whatever the provider advertises, so a token signed with the
// provider's public key as an HMAC secret is refused before any key is
// tried.
var idTokenAlgs = []string{
	oidc.RS256, oidc.RS384, oidc.RS512,
	oidc.ES256, oidc.ES384, oidc.ES512,
	oidc.PS256, oidc.PS384, oidc.PS512,
	oidc.EdDSA,
}

// oidcSignIn signs in users through the OIDC provider of auth.oidc.
type oidcSignIn struct {
	conf   config.OIDC
	signer *auth.Signer
	ttl    time.Duration
	client *http.Client // to the provider: oidcTimeout, maxProviderBody, no redirects
	flows  *flowCookies
	// static are the static users' names, which an OIDC user may not
	// take, by config.SameName, and nil with link_static_users.
	// admitUsers and admitGroups are the users and groups that
	// require_known_users admits, and nil without it. knownGroups are the
	// groups the file names; other groups of a user are dropped.
	static      []string
	admitUsers  map[string]bool
	admitGroups map[string]bool
	knownGroups map[string]bool
	now         func() time.Time

	mu       sync.Mutex // guards provider, tried and err
	provider *oidcProvider
	tried    time.Time
	err      error

	codesMu sync.Mutex
	codes   map[[sha256.Size]byte]pendingSignIn // by the hash of the code
	pended  uint64                              // codes pended so far
}

// oidcProvider is what discovery found out about the provider.
type oidcProvider struct {
	endpoint oauth2.Endpoint
	verifier *oidc.IDTokenVerifier
	// issParam is whether the provider names itself in every answer to
	// the callback (RFC 9207).
	issParam bool
}

// pendingSignIn is a user signed in at the callback whose client has yet
// to exchange its one-time code.
type pendingSignIn struct {
	user      string
	groups    []string
	challenge string // the client's S256 code challenge
	expires   time.Time
	seq       uint64 // the order of pending, oldest first
}

// newOIDC prepares sign-in through f's OIDC provider, without contacting
// it: discovery happens on the first sign-in, or when warm runs, so a
// provider that is down does not keep occamyd from starting. servesTLS
// tells whether occamyd terminates TLS itself.
func newOIDC(f *config.File, signer *auth.Signer, servesTLS bool) (*oidcSignIn, error) {
	conf := *f.Auth.OIDC
	flows, err := newFlowCookies(conf.RedirectURL, servesTLS)
	if err != nil {
		return nil, err
	}
	o := &oidcSignIn{
		conf:   conf,
		signer: signer,
		ttl:    f.Auth.AccessTokenLifetime,
		client: &http.Client{
			Timeout:   oidcTimeout,
			Transport: limitBody{http.DefaultTransport, maxProviderBody},
			// A redirect is answered, not followed, so that the code,
			// the verifier and the client secret go to the discovered
			// token endpoint only, and every hop is one whose URL was
			// checked.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		flows:       flows,
		knownGroups: f.KnownGroups(),
		now:         time.Now,
		codes:       map[[sha256.Size]byte]pendingSignIn{},
	}
	if !conf.LinkStaticUsers {
		for _, u := range f.Auth.Users {
			o.static = append(o.static, u.Name)
		}
	}
	if conf.RequireKnownUsers {
		o.admitUsers, o.admitGroups = f.Admitted()
	}
	return o, nil
}

// register adds the OIDC routes to mux.
func (o *oidcSignIn) register(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/auth/oidc/login", o.login)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", o.callback)
	mux.HandleFunc("POST /api/v1/auth/oidc/token", o.token)
}

// warm runs discovery once, so that the log tells at start whether the
// provider can be reached. A failure is retried by the next sign-in.
func (o *oidcSignIn) warm() {
	_, _ = o.discover()
}

// discover returns the provider, fetching its discovery document the
// first time. A failure is remembered for oidcRetry, so that sign-ins
// while the provider is down do not each wait for it; concurrent
// sign-ins wait for one fetch.
func (o *oidcSignIn) discover() (*oidcProvider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	if o.err != nil && o.now().Sub(o.tried) < oidcRetry {
		return nil, o.err
	}
	o.tried = o.now()
	p, err := o.fetchProvider()
	if err != nil {
		o.err = err
		// #nosec G706 -- the issuer is the operator's configuration, and the error, which may hold the provider's answer, is quoted.
		log.Printf("warning: OIDC discovery at %s failed, retried on the next sign-in after %v: %s", o.conf.Issuer, oidcRetry, quote(err.Error()))
		return nil, err
	}
	// #nosec G706 -- the operator's configuration, not a client's input.
	log.Printf("OIDC provider %s discovered", o.conf.Issuer)
	o.provider, o.err = p, nil
	return p, nil
}

// fetchProvider reads the provider's discovery document. go-oidc checks
// that it names the configured issuer; the endpoints must pass
// endpointProblem.
func (o *oidcSignIn) fetchProvider() (*oidcProvider, error) {
	ctx := oidc.ClientContext(context.Background(), o.client)
	p, err := oidc.NewProvider(ctx, o.conf.Issuer)
	if err != nil {
		return nil, err
	}
	var meta struct {
		JWKS        string   `json:"jwks_uri"`
		AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
		IssParam    bool     `json:"authorization_response_iss_parameter_supported"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, err
	}
	endpoint := p.Endpoint()
	for _, u := range []struct{ name, url string }{
		{"authorization_endpoint", endpoint.AuthURL},
		{"token_endpoint", endpoint.TokenURL},
		{"jwks_uri", meta.JWKS},
	} {
		if problem := endpointProblem(o.conf.Issuer, u.url); problem != "" {
			return nil, fmt.Errorf("the discovery document's %s: %s", u.name, problem)
		}
	}
	endpoint.AuthStyle = o.authStyle(meta.AuthMethods)
	return &oidcProvider{
		endpoint: endpoint,
		verifier: p.Verifier(&oidc.Config{
			ClientID:             o.conf.ClientID,
			SupportedSigningAlgs: idTokenAlgs,
			Now:                  func() time.Time { return o.now() },
		}),
		issParam: meta.IssParam,
	}, nil
}

// endpointProblem describes what keeps an endpoint that issuer's
// discovery document names from being one occamyd talks to: it must be
// https, or http on the loopback interface when the issuer is too, so
// that a provider elsewhere cannot point occamyd at a port of its own
// host in the clear. It returns "" for such an endpoint.
func endpointProblem(issuer, endpoint string) string {
	if p := config.SecureURLProblem(endpoint); p != "" {
		return p
	}
	iu, err := url.Parse(issuer)
	if eu, _ := url.Parse(endpoint); eu.Scheme != "https" && (err != nil || iu.Scheme != "http") {
		return fmt.Sprintf("%q needs https, as the issuer has", endpoint)
	}
	return ""
}

// authStyle picks how occamyd authenticates to the token endpoint: a
// public client sends its ID in the body, and a confidential one its
// secret in a Basic header, the default of OIDC discovery, unless the
// provider takes only client_secret_post. Probing both, as oauth2 can,
// would send the code twice.
func (o *oidcSignIn) authStyle(methods []string) oauth2.AuthStyle {
	if o.conf.ClientSecretFile == "" && o.conf.ClientSecretEnv == "" {
		return oauth2.AuthStyleInParams
	}
	if len(methods) > 0 && !slices.Contains(methods, "client_secret_basic") && slices.Contains(methods, "client_secret_post") {
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

// oauth2Config returns the OAuth 2.0 client of provider p, with secret,
// which may be empty.
func (o *oidcSignIn) oauth2Config(p *oidcProvider, secret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.conf.ClientID,
		ClientSecret: secret,
		Endpoint:     p.endpoint,
		RedirectURL:  o.conf.RedirectURL,
		Scopes:       o.conf.Scopes,
	}
}

// login implements GET /api/v1/auth/oidc/login: it starts a sign-in and
// sends the browser to the provider. A return URL that is not one of
// auth.oidc.return_urls, or a missing challenge, is refused with API
// v1's error, before any cookie is set; a provider that cannot be
// reached sends the browser back with #error=unavailable.
func (o *oidcSignIn) login(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	returnURL, ok := o.returnURL(q["return_url"])
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "return_url must be one of auth.oidc.return_urls, exactly")
		return
	}
	challenge := q["code_challenge"]
	method := q.Get("code_challenge_method")
	if len(challenge) != 1 || !validChallenge(challenge[0]) || len(q["code_challenge_method"]) > 1 || method != "" && method != "S256" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "code_challenge must be the S256 challenge of the client's code verifier (RFC 7636)")
		return
	}
	p, err := o.discover()
	if err != nil {
		sendBack(w, returnURL, url.Values{"error": {codeUnavailable}})
		return
	}
	f := flow{
		State:     rand.Text(),
		Nonce:     rand.Text(),
		Verifier:  oauth2.GenerateVerifier(),
		ReturnURL: returnURL,
		Challenge: challenge[0],
		Expires:   o.now().Add(flowLifetime).Unix(),
	}
	if err := o.flows.set(w, f); err != nil {
		log.Printf("sealing an OIDC sign-in: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	redirect(w, o.oauth2Config(p, "").AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)))
}

// returnURL returns the return URL a sign-in names, which must be one of
// auth.oidc.return_urls exactly. With only one configured, a sign-in may
// leave it out.
func (o *oidcSignIn) returnURL(v []string) (string, bool) {
	switch {
	case len(v) == 0 && len(o.conf.ReturnURLs) == 1:
		return o.conf.ReturnURLs[0], true
	case len(v) == 1 && slices.Contains(o.conf.ReturnURLs, v[0]):
		return v[0], true
	}
	return "", false
}

// callback implements GET /api/v1/auth/oidc/callback, where the provider
// sends the browser back. Until the state matches a sign-in cookie, it
// answers with API v1's error, since it knows no return URL it can
// trust; after, it sends the browser back to the sign-in's return URL
// with a one-time code or an error. It clears the cookie when the state
// matches, or when the browser holds no sign-in in progress, so that a
// link to the callback with another state, from another site, cannot end
// a sign-in that is under way.
func (o *oidcSignIn) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, pending := o.flows.match(r, q["state"], o.now())
	if f != nil || !pending {
		o.flows.clear(w)
	}
	if f == nil {
		// #nosec G706 -- the remote address comes from net/http, not the client's data.
		log.Printf("OIDC sign-in failed from %s: no sign-in cookie of this browser matches the state", r.RemoteAddr)
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusBadRequest, codeInvalidState, "the sign-in did not start in this browser, or took longer than 10 minutes: start it again")
		return
	}
	user, groups, err := o.identify(r, q, f)
	if err != nil {
		var se *signInError
		if !errors.As(err, &se) {
			se = &signInError{codeSignInFailed, quote(err.Error())}
		}
		// #nosec G706 -- a reason quotes whatever came from the provider or the client, with quote, or with %q once checked by validSubject.
		log.Printf("OIDC sign-in failed from %s: %s", r.RemoteAddr, se.reason)
		sendBack(w, f.ReturnURL, url.Values{"error": {se.code}})
		return
	}
	code, err := o.pend(user, groups, f.Challenge)
	if err != nil {
		// #nosec G706 -- the user name is quoted.
		log.Printf("OIDC sign-in of user %q failed: %v", user, err)
		sendBack(w, f.ReturnURL, url.Values{"error": {codeUnavailable}})
		return
	}
	sendBack(w, f.ReturnURL, url.Values{"code": {code}})
}

// signInError is a failed sign-in: code goes to the client, and reason,
// which names no secret, to the log. A reason quotes any text from the
// provider or the client with quote, or, for a user name that
// validSubject passed, with %q.
type signInError struct {
	code   string
	reason string
}

func (e *signInError) Error() string { return e.reason }

func failed(code, format string, args ...any) error {
	return &signInError{code, fmt.Sprintf(format, args...)}
}

// identify finishes a sign-in whose state matched: it checks the
// provider's answer in q, exchanges the code with the verifier of f,
// verifies the ID token, and maps its claims to a user and groups.
func (o *oidcSignIn) identify(r *http.Request, q url.Values, f *flow) (user string, groups []string, err error) {
	p, err := o.discover()
	if err != nil {
		return "", nil, failed(codeUnavailable, "the provider cannot be reached: %s", quote(err.Error()))
	}
	// RFC 9207: a provider that names itself must be ours, and one that
	// says it names itself must do so, in an error response too (2.4),
	// so the check comes before the provider's error is taken as its.
	// The issuer is compared exactly, once.
	switch iss, ok := q["iss"]; {
	case ok && (len(iss) != 1 || iss[0] != o.conf.Issuer):
		return "", nil, failed(codeSignInFailed, "the provider's answer names another issuer, %s", quote(strings.Join(iss, " ")))
	case !ok && p.issParam:
		return "", nil, failed(codeSignInFailed, "the provider's answer names no issuer, though its discovery document says it does (RFC 9207)")
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return "", nil, failed(codeAccessDenied, "the provider denied access")
		}
		return "", nil, failed(codeSignInFailed, "the provider answered %s", quote(e))
	}
	code := q["code"]
	if len(code) != 1 || code[0] == "" {
		return "", nil, failed(codeSignInFailed, "the provider's answer has no code")
	}
	secret, err := o.conf.ClientSecret()
	if err != nil && !errors.Is(err, config.ErrNoSecret) {
		return "", nil, failed(codeSignInFailed, "reading the client secret: %s", quote(err.Error()))
	}
	ctx := oidc.ClientContext(r.Context(), o.client)
	tok, err := o.oauth2Config(p, secret).Exchange(ctx, code[0], oauth2.VerifierOption(f.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil {
			// Neither the body nor the description: only the provider's
			// error code and status.
			return "", nil, failed(codeSignInFailed, "the token endpoint refused the code: %s, status %d", quote(re.ErrorCode), re.Response.StatusCode)
		}
		return "", nil, failed(codeSignInFailed, "exchanging the code: %s", quote(err.Error()))
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", nil, failed(codeSignInFailed, "the token endpoint sent no ID token")
	}
	// Issuer, audience, expiry, algorithm and signature.
	id, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return "", nil, failed(codeSignInFailed, "the ID token does not verify: %s", quote(err.Error()))
	}
	// go-oidc leaves the nonce to the caller.
	if subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(f.Nonce)) != 1 {
		return "", nil, failed(codeSignInFailed, "the ID token's nonce is not this sign-in's")
	}
	var claims map[string]json.RawMessage
	if err := id.Claims(&claims); err != nil {
		return "", nil, failed(codeSignInFailed, "the ID token's claims: %s", quote(err.Error()))
	}
	return o.mapClaims(id, claims)
}

// mapClaims maps the claims of a verified ID token to a user and groups:
// the user from subject_claim, which may not name a static user, by
// config.SameName, unless link_static_users links them, and the groups
// from groups_claim, keeping the groups the file names; a linked user
// gets these, not the static user's. With require_known_users, an allow
// list must name the user, exactly, or one of the groups.
func (o *oidcSignIn) mapClaims(id *oidc.IDToken, claims map[string]json.RawMessage) (user string, groups []string, err error) {
	// OpenID Connect Core 1.0, 3.1.3.7: with several audiences, the
	// authorized party must be occamyd, and so must any that is named.
	var azp string
	if raw, ok := claims["azp"]; ok && json.Unmarshal(raw, &azp) != nil {
		return "", nil, failed(codeSignInFailed, "the ID token's azp is not a string")
	}
	if (len(id.Audience) > 1 || azp != "") && azp != o.conf.ClientID {
		return "", nil, failed(codeSignInFailed, "the ID token was issued to another party, %s", quote(azp))
	}

	if err := json.Unmarshal(claims[o.conf.SubjectClaim], &user); err != nil || !validSubject(user) {
		return "", nil, failed(codeAccessDenied, "the ID token's %s claim is missing or not a user name", o.conf.SubjectClaim)
	}
	if o.conf.SubjectClaim == "email" {
		// An address the provider has not verified may be anyone's.
		var verified bool
		if err := json.Unmarshal(claims["email_verified"], &verified); err != nil || !verified {
			return "", nil, failed(codeAccessDenied, "user %q: the provider has not verified the email address", user)
		}
	}
	// A name belongs to one source unless the file links them.
	if i := slices.IndexFunc(o.static, func(name string) bool { return config.SameName(user, name) }); i >= 0 {
		return "", nil, failed(codeAccessDenied, "user %q takes the name of static user %q, and auth.oidc.link_static_users is off", user, o.static[i])
	}
	if groups, err = o.groupsOf(user, claims); err != nil {
		return "", nil, err
	}
	if o.admitUsers != nil && !o.admitUsers[user] && !slices.ContainsFunc(groups, func(g string) bool { return o.admitGroups[g] }) {
		return "", nil, failed(codeAccessDenied, "user %q is not admitted by require_known_users: no allow list names the user or the user's groups", user)
	}
	return user, groups, nil
}

// groupsOf returns the groups that groups_claim names, keeping those the
// file names, sorted and without duplicates.
func (o *oidcSignIn) groupsOf(user string, claims map[string]json.RawMessage) ([]string, error) {
	if o.conf.GroupsClaim == "" {
		return nil, nil
	}
	raw, ok := claims[o.conf.GroupsClaim]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var all []string
	if err := json.Unmarshal(raw, &all); err != nil {
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return nil, failed(codeSignInFailed, "user %q: the ID token's %s claim is neither a list of strings nor a string", user, o.conf.GroupsClaim)
		}
		all = []string{one}
	}
	var groups []string
	for _, g := range all {
		if o.knownGroups[g] {
			groups = append(groups, g)
		}
	}
	slices.Sort(groups)
	return slices.Compact(groups), nil
}

// validSubject reports whether s can name a user, by config.UsableName:
// the one rule for the names of static users, OIDC users and trusted
// issuers' users.
func validSubject(s string) bool { return config.UsableName(s) }

// pend keeps a signed-in user for its client to collect, and returns the
// one-time code to collect it with. It sweeps expired codes, and drops
// the user's oldest code when maxPendingPerUser of the user's wait.
func (o *oidcSignIn) pend(user string, groups []string, challenge string) (string, error) {
	code := rand.Text()
	o.codesMu.Lock()
	defer o.codesMu.Unlock()
	now := o.now()
	var oldest [sha256.Size]byte
	mine := 0
	for k, p := range o.codes {
		switch {
		case !now.Before(p.expires):
			delete(o.codes, k)
		case p.user == user:
			if mine == 0 || p.seq < o.codes[oldest].seq {
				oldest = k
			}
			mine++
		}
	}
	if mine >= maxPendingPerUser {
		delete(o.codes, oldest)
	}
	if len(o.codes) >= maxPendingCodes {
		return "", errors.New("too many sign-ins wait for their clients")
	}
	o.pended++
	o.codes[sha256.Sum256([]byte(code))] = pendingSignIn{user, groups, challenge, now.Add(codeLifetime), o.pended}
	return code, nil
}

// redeem returns the sign-in that code collects, if any, and forgets it:
// a code is tried once, whether or not its verifier matches.
func (o *oidcSignIn) redeem(code, verifier string) (pendingSignIn, bool) {
	key := sha256.Sum256([]byte(code))
	o.codesMu.Lock()
	p, ok := o.codes[key]
	delete(o.codes, key)
	o.codesMu.Unlock()
	if !ok || !o.now().Before(p.expires) || !validVerifier(verifier) {
		return pendingSignIn{}, false
	}
	return p, subtle.ConstantTimeCompare([]byte(oauth2.S256ChallengeFromVerifier(verifier)), []byte(p.challenge)) == 1
}

// codeRequest is the body of POST /api/v1/auth/oidc/token.
type codeRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

// token implements POST /api/v1/auth/oidc/token: the one-time code from
// the callback and the verifier of the login's challenge in, an access
// token out, as from POST /api/v1/auth/token. A code used before,
// expired or unknown, and a wrong verifier, get the same 401.
func (o *oidcSignIn) token(w http.ResponseWriter, r *http.Request) {
	var req codeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Code == "" || req.CodeVerifier == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "code and code_verifier are required")
		return
	}
	p, ok := o.redeem(req.Code, req.CodeVerifier)
	if !ok {
		// #nosec G706 -- the remote address comes from net/http, not the client's data.
		log.Printf("OIDC code exchange failed from %s", r.RemoteAddr)
		w.Header().Set("WWW-Authenticate", bearerRealm)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "the code is unknown, used or expired, or the verifier does not match")
		return
	}
	token, err := o.signer.AccessFrom(auth.IdPOIDC, p.user, p.groups, o.ttl)
	if err != nil {
		log.Printf("signing an access token for %q: %v", p.user, err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	log.Printf("user %q signed in, idp %s", p.user, auth.IdPOIDC)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(o.ttl / time.Second),
	})
}

// validChallenge reports whether s is an S256 code challenge: the
// unpadded base64url encoding of a SHA-256 hash.
func validChallenge(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == sha256.Size
}

// validVerifier reports whether s is a PKCE code verifier: 43 to 128
// characters of A-Z, a-z, 0-9, '-', '.', '_' and '~' (RFC 7636, 4.1).
func validVerifier(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("-._~", r)
	})
}

// redirect sends the browser to u, a URL with sign-in data in it, which
// no cache may keep and no Referer may carry on.
func redirect(w http.ResponseWriter, u string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", u)
	w.WriteHeader(http.StatusFound)
}

// sendBack sends the browser to a sign-in's return URL with v in the
// fragment, which the browser keeps to itself: it reaches no server's
// log and no Referer.
func sendBack(w http.ResponseWriter, returnURL string, v url.Values) {
	redirect(w, returnURL+"#"+v.Encode())
}

// quote returns s for the log, truncated to maxLogged bytes and quoted,
// so that text from the provider or a client, such as a response body
// that go-oidc puts in its errors, can neither forge a log line nor flood
// the log.
func quote(s string) string {
	if len(s) > maxLogged {
		return strconv.Quote(s[:maxLogged]) + "..."
	}
	return strconv.Quote(s)
}

// limitBody bounds the bodies of the responses its transport receives
// to n bytes; reading past them is an error.
type limitBody struct {
	rt http.RoundTripper
	n  int64
}

func (l limitBody) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := l.rt.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, l.n)
	return resp, nil
}
