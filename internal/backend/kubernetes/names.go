// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// Kubernetes's limits on names (k8s.io/apimachinery's validation).
const (
	// maxLabel is the length of a DNS-1123 label: a namespace or a
	// container name.
	maxLabel = 63
	// maxSubdomain is the length of a DNS-1123 subdomain: a pod name.
	maxSubdomain = 253
)

// CheckNamespace reports whether s can name a namespace: a DNS-1123
// label, 1 to 63 lower-case letters, digits and '-', starting and ending
// with a letter or digit.
func CheckNamespace(s string) error { return checkLabel("namespace", s) }

// CheckContainer reports whether s can name a container: a DNS-1123
// label, as a namespace.
func CheckContainer(s string) error { return checkLabel("container name", s) }

// CheckPod reports whether s can name a pod: a DNS-1123 subdomain, 1 to
// 253 characters of DNS-1123 labels separated by '.'.
func CheckPod(s string) error {
	if s == "" {
		return errors.New("kubernetes: no pod name")
	}
	if len(s) > maxSubdomain {
		return fmt.Errorf("kubernetes: pod name %q is longer than %d characters", s, maxSubdomain)
	}
	for label := range strings.SplitSeq(s, ".") {
		if !isLabel(label) {
			return fmt.Errorf("kubernetes: %q is not a pod name: use lower-case letters, digits, '-' and '.', with a letter or digit at each end of every part between dots", s)
		}
	}
	return nil
}

func checkLabel(what, s string) error {
	switch {
	case s == "":
		return fmt.Errorf("kubernetes: no %s", what)
	case len(s) > maxLabel:
		return fmt.Errorf("kubernetes: %s %q is longer than %d characters", what, s, maxLabel)
	case !isLabel(s):
		return fmt.Errorf("kubernetes: %q is not a %s: use lower-case letters, digits and '-', starting and ending with a letter or digit", s, what)
	}
	return nil
}

// isLabel reports whether s is a DNS-1123 label of at most 63
// characters.
func isLabel(s string) bool {
	if s == "" || len(s) > maxLabel || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// endpoint is the URL of the exec, or attach, subresource of a pod on
// the API server at addr, a host:port. The names have been checked, so
// none holds a '/', a '?' or a '%': nothing in them reaches another path,
// and url.Values escapes the query. The URL holds no secret.
func endpoint(addr, namespace, pod, container string, command []string, attach, tty bool) *url.URL {
	sub := "exec"
	if attach {
		sub = "attach"
	}
	q := url.Values{}
	for _, arg := range command {
		q.Add("command", arg)
	}
	if container != "" {
		q.Set("container", container)
	}
	q.Set("stdin", "true")
	q.Set("stdout", "true")
	// With a TTY the terminal merges stderr into stdout, and the API
	// server refuses both.
	q.Set("stderr", strconv.FormatBool(!tty))
	q.Set("tty", strconv.FormatBool(tty))
	// Escaping each name changes nothing in a checked one; it is there
	// in case a check ever lets a '/' through.
	return &url.URL{
		Scheme:   "https",
		Host:     addr,
		Path:     "/api/v1/namespaces/" + namespace + "/pods/" + pod + "/" + sub,
		RawPath:  "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod) + "/" + sub,
		RawQuery: q.Encode(),
	}
}

// Token is a bearer token. It prints as [redacted] with every verb of
// fmt and in slog, so that a Config or a Token that reaches a log line
// or an error leaks nothing.
type Token string

const redacted = "[redacted]"

// String implements fmt.Stringer.
func (Token) String() string { return redacted }

// GoString implements fmt.GoStringer.
func (Token) GoString() string { return redacted }

// Format implements fmt.Formatter, for every verb.
func (Token) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue implements slog.LogValuer.
func (Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, for encoding/json and
// the like.
func (Token) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// errBadToken is the error for a token that cannot go in a header. It
// does not quote the token.
var errBadToken = errors.New("kubernetes: the bearer token has characters other than visible ASCII, such as spaces or line breaks")

// checkToken reports whether t can go in an Authorization header:
// visible ASCII only, so that it cannot end the header or add another.
func checkToken(t Token) error {
	for i := range len(t) {
		if t[i] < 0x21 || t[i] > 0x7e {
			return errBadToken
		}
	}
	return nil
}
