// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/backend/ssh"
)

// Bounds of auth.access_token_lifetime. The upper bound is the longest
// lifetime that occamyd's token verifier accepts.
const (
	MinAccessTokenLifetime = time.Minute
	MaxAccessTokenLifetime = auth.DefaultMaxLifetime
)

// MinBcryptCost is the lowest bcrypt cost a static user's hash may have.
const MinBcryptCost = bcrypt.DefaultCost

// defaultPorts maps each protocol a connection may use to the port that a
// target without one gets.
var defaultPorts = map[string]string{"ssh": "22", "vnc": "5900", "rdp": "3389", "telnet": "23"}

const protocolList = "ssh, vnc, rdp or telnet"

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	envPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	bcryptPattern = regexp.MustCompile(`^\$2[aby]?\$\d\d\$[./A-Za-z0-9]{53}$`)
)

// Problem is one problem in a configuration file.
type Problem struct {
	Line    int    // line in the file, or 0 when it has none
	Path    string // key path, such as connections[2].target
	Message string
}

func (p Problem) String() string {
	var b strings.Builder
	if p.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", p.Line)
	}
	if p.Path != "" {
		b.WriteString(p.Path + ": ")
	}
	b.WriteString(p.Message)
	return b.String()
}

// Problems is every problem found in a configuration file, in the order
// of the file's sections.
type Problems []Problem

func (ps Problems) Error() string {
	if len(ps) == 1 {
		return "config: " + ps[0].String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "config: %d problems:", len(ps))
	for _, p := range ps {
		b.WriteString("\n\t" + p.String())
	}
	return b.String()
}

// checker collects problems.
type checker struct {
	lines    map[string]int
	problems Problems
}

// add records a problem at path, on the line of path or, for a key that
// is not in the file, of its nearest ancestor.
func (c *checker) add(path, format string, args ...any) {
	line := 0
	for p := path; p != ""; {
		if l, ok := c.lines[p]; ok {
			line = l
			break
		}
		i := strings.LastIndexAny(p, ".[")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	c.problems = append(c.problems, Problem{Line: line, Path: path, Message: fmt.Sprintf(format, args...)})
}

func (f *File) validate(c *checker) {
	if f.Version != 1 {
		c.add("version", "must be 1")
	}
	if f.Listen == "" {
		f.Listen = DefaultListen
	}
	c.address("listen", f.Listen, false)
	switch {
	case f.TLS.Cert != "" && f.TLS.Key == "":
		c.add("tls.key", "required when tls.cert is set")
	case f.TLS.Key != "" && f.TLS.Cert == "":
		c.add("tls.cert", "required when tls.key is set")
	}
	c.file("tls.cert", f.TLS.Cert)
	c.file("tls.key", f.TLS.Key)
	if f.Debug.Address != "" {
		c.address("debug.address", f.Debug.Address, false)
		if Overlap(f.Listen, f.Debug.Address) {
			c.add("debug.address", "%q shares a port with listen %q: give the debug listener a port of its own, since whatever reaches the one could reach the other", f.Debug.Address, f.Listen)
		}
	}
	for i, o := range f.Origins {
		f.Origins[i] = c.origin(fmt.Sprintf("origins[%d]", i), o)
	}
	f.Auth.validate(c)
	f.validateGuacd(c)
	f.validateConnections(c)
	if f.AllowAdhocTargets && len(f.AdhocAllowlist) == 0 {
		c.add("allow_adhoc_targets", "needs adhoc_allowlist: list the networks and hosts that ad-hoc targets may reach")
	}
	for i, e := range f.AdhocAllowlist {
		if _, err := ParseAllowEntry(e); err != nil {
			c.add(fmt.Sprintf("adhoc_allowlist[%d]", i), "%v", err)
		}
	}
	for _, l := range []struct {
		path string
		n    int
	}{
		{"limits.sessions_per_user", f.Limits.SessionsPerUser},
		{"limits.viewers_per_session", f.Limits.ViewersPerSession},
		{"limits.shares_per_session", f.Limits.SharesPerSession},
	} {
		if l.n < 0 {
			c.add(l.path, "must be at least 1, or 0 for the default")
		}
	}
	f.Limits = f.Limits.WithDefaults()
}

func (a *Auth) validate(c *checker) {
	if a.SigningKeyFile == "" {
		c.add("auth.signing_key_file", "required: occamyd signs its tokens with the Ed25519 key in this file, and creates the file if it does not exist")
	} else {
		c.signingKey("auth.signing_key_file", a.SigningKeyFile)
	}
	if a.AccessTokenLifetime == 0 {
		a.AccessTokenLifetime = DefaultAccessTokenLifetime
	}
	if a.AccessTokenLifetime < MinAccessTokenLifetime || a.AccessTokenLifetime > MaxAccessTokenLifetime {
		c.add("auth.access_token_lifetime", "%s is not between %s and %s",
			shortDuration(a.AccessTokenLifetime), shortDuration(MinAccessTokenLifetime), shortDuration(MaxAccessTokenLifetime))
	}

	issuers := map[string]int{}
	for i, ti := range a.TrustedIssuers {
		p := fmt.Sprintf("auth.trusted_issuers[%d]", i)
		if c.name(p+".issuer", ti.Issuer) {
			if ti.Issuer == auth.Issuer {
				// The issuer picks the verification key, so a third party
				// by this name would stand in for occamyd itself.
				c.add(p+".issuer", "%q is occamyd's own issuer: name the third party something else", ti.Issuer)
			} else if j, ok := issuers[ti.Issuer]; ok {
				c.add(p+".issuer", "%q is already auth.trusted_issuers[%d]", ti.Issuer, j)
			} else {
				issuers[ti.Issuer] = i
			}
		}
		if ti.PublicKeyFile == "" {
			c.add(p+".public_key_file", "required")
		} else if c.file(p+".public_key_file", ti.PublicKeyFile) {
			// A public key is no secret, so it is parsed now rather than
			// when the first token arrives.
			if _, err := auth.LoadPublicKey(ti.PublicKeyFile); err != nil {
				c.add(p+".public_key_file", "%v", err)
			}
		}
	}

	users := map[string]int{}
	for i, u := range a.Users {
		p := fmt.Sprintf("auth.users[%d]", i)
		if c.name(p+".name", u.Name) {
			if j, ok := users[u.Name]; ok {
				c.add(p+".name", "%q is already auth.users[%d]", u.Name, j)
			} else {
				users[u.Name] = i
			}
		}
		c.passwordHash(p+".password_hash", u.PasswordHash)
		for j, g := range u.Groups {
			c.name(fmt.Sprintf("%s.groups[%d]", p, j), g)
		}
	}

	if o := a.OIDC; o != nil {
		c.providerURL("auth.oidc.issuer", o.Issuer)
		if o.ClientID == "" {
			c.add("auth.oidc.client_id", "required")
		}
		if o.ClientSecretFile != "" && o.ClientSecretEnv != "" {
			c.add("auth.oidc", "client_secret_file and client_secret_env are both set: set one, or neither for a public client")
		}
		c.file("auth.oidc.client_secret_file", o.ClientSecretFile)
		c.env("auth.oidc.client_secret_env", o.ClientSecretEnv)
		c.providerURL("auth.oidc.redirect_url", o.RedirectURL)
		if len(o.Scopes) == 0 {
			o.Scopes = slices.Clone(DefaultOIDCScopes)
		} else if !slices.Contains(o.Scopes, "openid") {
			c.add("auth.oidc.scopes", "must include openid")
		}
		if o.SubjectClaim == "" {
			o.SubjectClaim = DefaultOIDCSubjectClaim
		}
	}
}

// shortDuration formats d as it would be written in a file: 12h, not
// 12h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (f *File) validateGuacd(c *checker) {
	if f.Guacd.Address == "" {
		f.Guacd.Address = DefaultGuacdAddress
	}
	c.address("guacd.address", f.Guacd.Address, true)
	for _, protocol := range slices.Sorted(maps.Keys(f.Guacd.Params)) {
		p := "guacd.params." + protocol
		if _, ok := defaultPorts[protocol]; !ok {
			c.add(p, "unknown protocol %q: use %s", protocol, protocolList)
		}
		for _, name := range slices.Sorted(maps.Keys(f.Guacd.Params[protocol])) {
			switch {
			case secretParam(name):
				c.add(p+"."+name, "inline secrets are not allowed, so that the file can be committed: set credentials on each connection, with password_file, password_env, private_key_file or prompt: true")
			case name == "hostname" || name == "port":
				c.add(p+"."+name, "set from each connection's target")
			}
		}
	}
}

// secretParam reports whether a guacd parameter holds a secret, such as
// password, sftp-password, gateway-password, passphrase or
// sftp-private-key.
func secretParam(name string) bool {
	for _, s := range []string{"password", "passphrase", "private-key", "secret"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

func (f *File) validateConnections(c *checker) {
	users, groups := map[string]bool{}, map[string]bool{}
	for _, u := range f.Auth.Users {
		users[u.Name] = true
		for _, g := range u.Groups {
			groups[g] = true
		}
	}
	// Names in allow lists can be checked only when static users are the
	// only identities: OIDC claims and tokens from trusted issuers carry
	// users and groups that the file does not list.
	closed := f.Auth.OIDC == nil && len(f.Auth.TrustedIssuers) == 0

	ids := map[string]int{}
	for i := range f.Connections {
		cn := &f.Connections[i]
		p := fmt.Sprintf("connections[%d]", i)
		switch {
		case cn.ID == "":
			c.add(p+".id", "required")
		case !idPattern.MatchString(cn.ID):
			c.add(p+".id", "%q is not a connection ID: use 1 to 64 lowercase letters, digits, '.', '_' and '-', starting with a letter or digit", cn.ID)
		default:
			if j, ok := ids[cn.ID]; ok {
				c.add(p+".id", "%q is already the ID of connections[%d]", cn.ID, j)
			} else {
				ids[cn.ID] = i
			}
		}
		if cn.Name == "" {
			cn.Name = cn.ID
		}
		port, known := defaultPorts[cn.Protocol]
		switch {
		case cn.Protocol == "":
			c.add(p+".protocol", "required: %s", protocolList)
		case !known:
			c.add(p+".protocol", "unknown protocol %q: use %s", cn.Protocol, protocolList)
		}
		if cn.Target == "" {
			c.add(p+".target", "required")
		} else if t, err := normalizeTarget(cn.Target, port); err != nil {
			c.add(p+".target", "%v", err)
		} else {
			cn.Target = t
		}
		c.credentials(p+".credentials", cn.Protocol, cn.Credentials)
		if known {
			c.hostKeys(p, cn)
		}
		c.allow(p+".allow", cn.Allow, closed, users, groups)
	}
}

// hostKeys checks the host key policy of a connection: an ssh connection
// pins host keys or ignores them explicitly, since the native SSH backend
// accepts no other key, and other protocols have no host keys.
func (c *checker) hostKeys(path string, cn *Connection) {
	if cn.Protocol != "ssh" {
		if len(cn.HostKeys) > 0 {
			c.add(path+".host_keys", "only ssh connections have host keys")
		}
		if cn.InsecureIgnoreHostKey {
			c.add(path+".insecure_ignore_host_key", "only ssh connections have host keys")
		}
		return
	}
	switch {
	case len(cn.HostKeys) == 0 && !cn.InsecureIgnoreHostKey:
		c.add(path+".host_keys", "required for ssh: list the server's host keys or their SHA-256 fingerprints (ssh-keyscan, ssh-keygen -l), or set insecure_ignore_host_key: true for a demo")
	case len(cn.HostKeys) > 0 && cn.InsecureIgnoreHostKey:
		c.add(path+".insecure_ignore_host_key", "set host_keys or insecure_ignore_host_key, not both")
	}
	for i, k := range cn.HostKeys {
		if err := ssh.CheckHostKey(k); err != nil {
			c.add(fmt.Sprintf("%s.host_keys[%d]", path, i), "%v", err)
		}
	}
}

func (c *checker) credentials(path, protocol string, cr Credentials) {
	var set []string
	for _, s := range []struct {
		name string
		on   bool
	}{
		{"password_file", cr.PasswordFile != ""},
		{"password_env", cr.PasswordEnv != ""},
		{"private_key_file", cr.PrivateKeyFile != ""},
		{"prompt", cr.Prompt},
	} {
		if s.on {
			set = append(set, s.name)
		}
	}
	switch len(set) {
	case 0:
		c.add(path, "no credential source: set one of password_file, password_env, private_key_file or prompt: true")
	case 1:
	default:
		c.add(path, "more than one credential source (%s): set exactly one", strings.Join(set, ", "))
	}
	c.file(path+".password_file", cr.PasswordFile)
	c.env(path+".password_env", cr.PasswordEnv)
	c.file(path+".private_key_file", cr.PrivateKeyFile)
	if cr.PrivateKeyFile != "" && protocol != "ssh" {
		c.add(path+".private_key_file", "only ssh connections use a private key")
	}
	if protocol == "ssh" && cr.Username == "" && !cr.Prompt {
		c.add(path+".username", "required for ssh unless prompt is true")
	}
}

func (c *checker) allow(path string, a Allow, closed bool, users, groups map[string]bool) {
	if len(a.Users) == 0 && len(a.Groups) == 0 {
		c.add(path, "names no users or groups, so nobody could open the connection")
	}
	for i, u := range a.Users {
		p := fmt.Sprintf("%s.users[%d]", path, i)
		if c.name(p, u) && closed && !users[u] {
			c.add(p, "no static user is named %q", u)
		}
	}
	for i, g := range a.Groups {
		p := fmt.Sprintf("%s.groups[%d]", path, i)
		if c.name(p, g) && closed && !groups[g] {
			c.add(p, "no static user is in group %q", g)
		}
	}
}

// name checks a user, group or issuer name, and reports whether it is
// valid.
func (c *checker) name(path, s string) bool {
	switch {
	case s == "":
		c.add(path, "must not be empty")
		return false
	case !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}):
		c.add(path, "%q has spaces or control characters", s)
		return false
	}
	return true
}

func (c *checker) passwordHash(path, h string) {
	if h == "" {
		c.add(path, "required")
		return
	}
	if !bcryptPattern.MatchString(h) {
		c.add(path, "not a bcrypt hash: expected $2a$, $2b$ or $2y$, a two-digit cost, $ and 53 characters")
		return
	}
	cost, err := bcrypt.Cost([]byte(h))
	switch {
	case err != nil:
		c.add(path, "not a bcrypt hash: %v", err)
	case cost < MinBcryptCost:
		c.add(path, "bcrypt cost %d is below %d", cost, MinBcryptCost)
	}
}

// address checks a host:port address to listen on or dial.
func (c *checker) address(path, addr string, needHost bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		c.add(path, "%q is not host:port", addr)
		return
	}
	switch {
	case host == "" && needHost:
		c.add(path, "%q has no host", addr)
	case host != "" && !validHost(host):
		c.add(path, "%q has an invalid host", addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		c.add(path, "%q has an invalid port", addr)
	}
}

// Overlap reports whether listeners on the host:port addresses a and b
// could take connections to one address and port. That is so when the
// ports are equal and the hosts share an address: an empty or
// unspecified host (0.0.0.0, ::) listens on every address of both
// families, as Go listens on it, and localhost is 127.0.0.1 and ::1. Other
// host names are compared by name. Port 0, which the system picks, never
// overlaps. Where the system lets both bind, as macOS does for a specific
// address beside a wildcard, the more specific listener takes that
// address's connections, so a proxy meant for one reaches the other.
func Overlap(a, b string) bool {
	ha, pa, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	hb, pb, err := net.SplitHostPort(b)
	if err != nil {
		return false
	}
	na, errA := strconv.ParseUint(pa, 10, 16)
	nb, errB := strconv.ParseUint(pb, 10, 16)
	if errA != nil || errB != nil || na != nb || na == 0 {
		return false
	}
	sa, sb := hostAddrs(ha), hostAddrs(hb)
	if sa == nil || sb == nil {
		return true // a wildcard
	}
	for k := range sa {
		if sb[k] {
			return true
		}
	}
	return false
}

// hostAddrs returns the addresses a listener on host takes, as keys, or
// nil for every address.
func hostAddrs(host string) map[string]bool {
	if host == "" {
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.IsUnspecified() {
			return nil
		}
		return map[string]bool{a.Unmap().WithZone("").String(): true}
	}
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "localhost" {
		return map[string]bool{"127.0.0.1": true, "::1": true}
	}
	return map[string]bool{"name:" + name: true}
}

// origin checks an origin and returns it in the form browsers send:
// scheme and host in lower case, and no trailing slash.
func (c *checker) origin(path, o string) string {
	u, err := url.Parse(o)
	switch {
	case err != nil:
		c.add(path, "%q is not a URL", o)
	case u.Scheme != "http" && u.Scheme != "https":
		c.add(path, "%q needs the scheme http or https", o)
	case u.Host == "":
		c.add(path, "%q has no host", o)
	case u.User != nil || u.Opaque != "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		c.add(path, "%q is not an origin: write scheme://host or scheme://host:port", o)
	default:
		return u.Scheme + "://" + strings.ToLower(u.Host)
	}
	return o
}

// providerURL checks a URL of the identity provider or of occamyd's
// callback, which must be https except on the loopback interface.
func (c *checker) providerURL(path, s string) {
	if s == "" {
		c.add(path, "required")
		return
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http":
		c.add(path, "%q is not an absolute http or https URL", s)
	case u.Scheme == "http" && !isLoopback(u.Hostname()):
		c.add(path, "%q needs https; http is accepted only on localhost", s)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// file checks that name, if set, is a regular file that occamyd can read,
// and reports whether it is. It opens the file and closes it again, so no
// secret is read.
func (c *checker) file(path, name string) bool {
	if name == "" {
		return false
	}
	info, err := os.Stat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.add(path, "%s does not exist", name)
		return false
	case err != nil:
		c.add(path, "%v", err)
		return false
	case info.IsDir():
		c.add(path, "%s is a directory", name)
		return false
	}
	fh, err := os.Open(name) // #nosec G304 -- the path comes from the operator's configuration
	switch {
	case errors.Is(err, fs.ErrPermission):
		c.add(path, "%s is not readable", name)
		return false
	case err != nil:
		c.add(path, "%v", err)
		return false
	}
	fh.Close()
	return true
}

// signingKey checks the signing key file, which may be missing because
// occamyd creates it on first start, as long as its directory exists.
func (c *checker) signingKey(path, name string) {
	if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
		dir := filepath.Dir(name)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			c.add(path, "neither %s nor its directory %s exists", name, dir)
		}
		return
	}
	c.file(path, name)
}

// env checks that the environment variable name, if set, has a value.
func (c *checker) env(path, name string) {
	if name == "" {
		return
	}
	if !envPattern.MatchString(name) {
		c.add(path, "%q is not an environment variable name", name)
		return
	}
	v, ok := os.LookupEnv(name)
	switch {
	case !ok:
		c.add(path, "%s is not set", name)
	case v == "":
		c.add(path, "%s is empty", name)
	}
}

// NormalizeTarget checks a target of protocol, host:port or a host
// alone, and returns it as host:port with the protocol's default port
// filled in, as ParseV1 stores the targets of connections.
func NormalizeTarget(protocol, target string) (string, error) {
	port, ok := defaultPorts[protocol]
	if !ok {
		return "", fmt.Errorf("unknown protocol %q: use %s", protocol, protocolList)
	}
	return normalizeTarget(target, port)
}

// normalizeTarget checks a connection target, host:port or a host alone,
// and returns it as host:port with defaultPort filled in. With no default
// port, for an unknown protocol, a target without a port is returned as
// it is.
func normalizeTarget(target, defaultPort string) (string, error) {
	if strings.ContainsAny(target, "/?#@ ") {
		return "", fmt.Errorf("%q is not host:port", target)
	}
	host, port, hasPort := target, "", false
	switch {
	case strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]"):
		host = target[1 : len(target)-1] // [::1]
	case strings.HasPrefix(target, "["):
		h, p, err := net.SplitHostPort(target)
		if err != nil {
			return "", fmt.Errorf("%q is not host:port", target)
		}
		host, port, hasPort = h, p, true
	case strings.Count(target, ":") == 1:
		host, port, hasPort = strings.Cut(target, ":")
	}
	if !validHost(host) {
		return "", fmt.Errorf("%q has an invalid host", target)
	}
	if !hasPort {
		if defaultPort == "" {
			return target, nil
		}
		port = defaultPort
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", fmt.Errorf("%q has an invalid port", target)
	}
	return net.JoinHostPort(host, port), nil
}

// validHost reports whether h is an IP address or a DNS name.
func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		if strings.ContainsFunc(label, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
		}) {
			return false
		}
	}
	return true
}
