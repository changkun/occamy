// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/config"
)

// tokenLifetime is how long a v0 token is valid: gin-jwt's default
// timeout, which v0 used.
const tokenLifetime = time.Hour

// realm names occamyd in the WWW-Authenticate header of an error.
const realm = "occamy-proxy"

// maxLoginSize bounds a login request's body. gin read bodies of any
// size; four short claims need far less.
const maxLoginSize = 1 << 20

// The messages of v0's authentication errors, as gin-jwt wrote them.
var (
	errFailedAuthentication = errors.New("incorrect Username or Password")
	errFailedTokenCreation  = errors.New("failed to create JWT Token")
	errEmptyAuthHeader      = errors.New("auth header is empty")
	errInvalidAuthHeader    = errors.New("auth header is invalid")
	errEmptyQueryToken      = errors.New("query token is empty")
	errEmptyCookieToken     = errors.New("cookie token is empty")
	errMissingExpField      = errors.New("missing exp field")
	// gin-jwt panicked on a token without these claims.
	errMissingClaims = errors.New("token lacks the connection claims")
)

// hmacMethods are the algorithms that v0 tokens may be signed with. gin-jwt
// also took RS256, RS384 and RS512, but only with key files, which v0's
// configuration cannot name.
var hmacMethods = map[string]*jwt.SigningMethodHMAC{
	"HS256": jwt.SigningMethodHS256,
	"HS384": jwt.SigningMethodHS384,
	"HS512": jwt.SigningMethodHS512,
}

// tokens issues and verifies v0 tokens: JWTs signed with auth.jwt_secret,
// whose claims carry the protocol, host, username and password to connect
// with, in the shape gin-jwt v2 gave them.
type tokens struct {
	key    []byte
	method *jwt.SigningMethodHMAC
	now    func() time.Time
}

// newTokens returns the tokens for auth.jwt_secret and auth.jwt_alg. The
// algorithm defaults to HS256, as in gin-jwt.
func newTokens(secret, alg string) (*tokens, error) {
	if alg == "" {
		alg = jwt.SigningMethodHS256.Alg()
	}
	method, ok := hmacMethods[alg]
	if !ok {
		return nil, fmt.Errorf("auth.jwt_alg %q is not supported, use HS256, HS384 or HS512", alg)
	}
	// gin-jwt accepted an empty secret, with which anyone can sign tokens.
	if secret == "" {
		return nil, errors.New("auth.jwt_secret is empty")
	}
	return &tokens{key: []byte(secret), method: method, now: time.Now}, nil
}

// login implements POST /api/v0/login. It signs whatever protocol, host,
// username and password it receives, and replies with the token and its
// expiry as gin-jwt did.
func (t *tokens) login(w http.ResponseWriter, r *http.Request) {
	c, err := loginClaims(w, r)
	if err != nil {
		log.Printf("login failed: %v", err)
		unauthorized(w, http.StatusUnauthorized, errFailedAuthentication)
		return
	}
	token, expire, err := t.issue(c)
	if err != nil {
		log.Printf("login failed: %v", err)
		unauthorized(w, http.StatusUnauthorized, errFailedTokenCreation)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":   http.StatusOK,
		"token":  token,
		"expire": expire.Format(time.RFC3339),
	})
}

// issue signs a token for the claims and returns it with its expiry.
func (t *tokens) issue(c *config.JWT) (string, time.Time, error) {
	now := t.now()
	expire := now.Add(tokenLifetime)
	token, err := jwt.NewWithClaims(t.method, jwt.MapClaims{
		"protocol": c.Protocol,
		"host":     c.Host,
		"username": c.Username,
		"password": c.Password,
		"exp":      expire.Unix(),
		"orig_iat": now.Unix(),
	}).SignedString(t.key)
	return token, expire, err
}

// loginClaims reads the claims to sign from a JSON body, or else from a
// form, as gin's binding did, and requires a protocol, a host and a
// password. The errors name fields, never their values.
func loginClaims(w http.ResponseWriter, r *http.Request) (*config.JWT, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginSize)
	var c config.JWT
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			return nil, fmt.Errorf("reading the JSON body: %w", err)
		}
	} else {
		err := r.ParseMultipartForm(maxLoginSize) // #nosec G120 -- MaxBytesReader bounds the body above
		if err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return nil, fmt.Errorf("reading the form: %w", err)
		}
		c = config.JWT{
			Protocol: r.Form.Get("protocol"),
			Host:     r.Form.Get("host"),
			Username: r.Form.Get("username"),
			Password: r.Form.Get("password"),
		}
	}
	switch "" {
	case c.Protocol:
		return nil, errors.New("no protocol")
	case c.Host:
		return nil, errors.New("no host")
	case c.Password:
		return nil, errors.New("no password")
	}
	return &c, nil
}

// authenticate returns the claims of the request's token. If there is no
// valid token, it replies with an error as gin-jwt did and returns false.
func (t *tokens) authenticate(w http.ResponseWriter, r *http.Request) (*config.JWT, bool) {
	raw, err := lookupToken(r)
	if err != nil {
		unauthorized(w, http.StatusUnauthorized, err)
		return nil, false
	}
	token, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return t.key, nil },
		jwt.WithValidMethods([]string{t.method.Alg()}),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		unauthorized(w, http.StatusUnauthorized, err)
		return nil, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		unauthorized(w, http.StatusUnauthorized, errMissingClaims)
		return nil, false
	}
	// The parser checks exp if it is there; gin-jwt also required it.
	if _, ok := claims["exp"]; !ok {
		unauthorized(w, http.StatusBadRequest, errMissingExpField)
		return nil, false
	}
	var c config.JWT
	for name, v := range map[string]*string{
		"protocol": &c.Protocol,
		"host":     &c.Host,
		"username": &c.Username,
		"password": &c.Password,
	} {
		if *v, ok = claims[name].(string); !ok {
			unauthorized(w, http.StatusUnauthorized, errMissingClaims)
			return nil, false
		}
	}
	return &c, true
}

// lookupToken returns the token from the Authorization header, the token
// query parameter or the jwt cookie, the first that is not empty, as
// gin-jwt looked it up. If all are empty, the error is the last lookup's.
func lookupToken(r *http.Request) (string, error) {
	var err error
	for _, lookup := range []func(*http.Request) (string, error){
		tokenFromHeader, tokenFromQuery, tokenFromCookie,
	} {
		var token string
		if token, err = lookup(r); token != "" {
			return token, nil
		}
	}
	return "", err
}

func tokenFromHeader(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errEmptyAuthHeader
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || scheme != "Bearer" {
		return "", errInvalidAuthHeader
	}
	return token, nil
}

func tokenFromQuery(r *http.Request) (string, error) {
	token := r.URL.Query().Get("token")
	if token == "" {
		return "", errEmptyQueryToken
	}
	return token, nil
}

func tokenFromCookie(r *http.Request) (string, error) {
	var token string
	if c, err := r.Cookie("jwt"); err == nil {
		token, _ = url.QueryUnescape(c.Value) // as gin read cookies
	}
	if token == "" {
		return "", errEmptyCookieToken
	}
	return token, nil
}

// unauthorized replies with an authentication error as gin-jwt did: the
// realm in WWW-Authenticate, and the status code and message as JSON.
func unauthorized(w http.ResponseWriter, code int, err error) {
	w.Header().Set("WWW-Authenticate", "JWT realm="+realm)
	writeJSON(w, code, map[string]any{"code": code, "message": err.Error()})
}

// writeJSON replies with v as JSON, as gin's renderer did.
func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b) // nothing to do for a client that has gone
}
