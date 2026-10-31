// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Defaults filled in by ParseV1.
const (
	DefaultListen              = ":5636"
	DefaultAccessTokenLifetime = 15 * time.Minute
	DefaultOIDCSubjectClaim    = "sub"
)

// Default limits, filled in by ParseV1 and by Limits.WithDefaults.
const (
	DefaultSessions          = 1000
	DefaultSessionsPerUser   = 10
	DefaultViewersPerSession = 50
	DefaultSharesPerSession  = 100
)

// DefaultViewerTimeout is streams.viewer_timeout's default, filled in
// by ParseV1 and by Streams.WithDefaults.
const DefaultViewerTimeout = 30 * time.Second

// DefaultOIDCScopes are the scopes requested when oidc.scopes is empty.
var DefaultOIDCScopes = []string{"openid", "profile", "email"}

// File is a version 1 configuration file. A File returned by ParseV1 has
// its defaults filled in and every connection target in host:port form.
// It holds no secrets: credentials say where a secret is, and the secret
// is read when it is used.
type File struct {
	Version           int          `yaml:"version"`
	Listen            string       `yaml:"listen"`
	TLS               TLS          `yaml:"tls"`
	Debug             Debug        `yaml:"debug"`
	Origins           []string     `yaml:"origins"`
	LegacyV0          bool         `yaml:"legacy_v0"`
	Auth              Auth         `yaml:"auth"`
	Guacd             Guacd        `yaml:"guacd"`
	Connections       []Connection `yaml:"connections"`
	AllowAdhocTargets bool         `yaml:"allow_adhoc_targets"`
	AdhocAllowlist    []string     `yaml:"adhoc_allowlist"`
	Limits            Limits       `yaml:"limits"`
	Streams           Streams      `yaml:"streams"`
}

// Streams bounds what occamyd keeps for one viewer.
type Streams struct {
	// ViewerTimeout is how long a viewer, hidden or not, may leave the
	// frames it was sent unacknowledged, or one write to it blocked,
	// before occamyd drops it with close too-slow. Each viewer holds the
	// screen it was last sent, which a viewer that stalls would keep for
	// as long as it stayed connected. Zero is the default.
	ViewerTimeout time.Duration `yaml:"viewer_timeout"`
}

// WithDefaults returns s with the defaults in place of zero.
func (s Streams) WithDefaults() Streams {
	if s.ViewerTimeout == 0 {
		s.ViewerTimeout = DefaultViewerTimeout
	}
	return s
}

// Limits bound what one user or one session holds, so that no user takes
// what the others need. Zero is the default.
type Limits struct {
	// Sessions is the most sessions occamyd runs at once, whoever owns
	// them: a trusted issuer can name new users without end, and each
	// has SessionsPerUser of its own.
	Sessions int `yaml:"sessions"`
	// SessionsPerUser is the most sessions one user owns at once.
	SessionsPerUser int `yaml:"sessions_per_user"`
	// ViewersPerSession is the most viewers a session takes besides its
	// owner, who can always join.
	ViewersPerSession int `yaml:"viewers_per_session"`
	// SharesPerSession is the most unexpired shares a session has.
	SharesPerSession int `yaml:"shares_per_session"`
}

// WithDefaults returns l with the defaults in place of zero.
func (l Limits) WithDefaults() Limits {
	if l.Sessions == 0 {
		l.Sessions = DefaultSessions
	}
	if l.SessionsPerUser == 0 {
		l.SessionsPerUser = DefaultSessionsPerUser
	}
	if l.ViewersPerSession == 0 {
		l.ViewersPerSession = DefaultViewersPerSession
	}
	if l.SharesPerSession == 0 {
		l.SharesPerSession = DefaultSharesPerSession
	}
	return l
}

// TLS names the certificate and key files. occamyd terminates TLS when
// both are set.
type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Debug configures the debug listener, which is off when Address is empty.
type Debug struct {
	Address string `yaml:"address"`
}

// Auth configures how callers sign in and how tokens are signed.
type Auth struct {
	SigningKeyFile      string          `yaml:"signing_key_file"`
	AccessTokenLifetime time.Duration   `yaml:"access_token_lifetime"`
	TrustedIssuers      []TrustedIssuer `yaml:"trusted_issuers"`
	Users               []User          `yaml:"users"`
	OIDC                *OIDC           `yaml:"oidc"`
}

// TrustedIssuer is a third party, such as a gateway, whose tokens occamyd
// accepts. Its users are its own: a subject it names is another user than
// a static user or another issuer's user of the same name.
type TrustedIssuer struct {
	Issuer        string `yaml:"issuer"`
	PublicKeyFile string `yaml:"public_key_file"`
	// LinkStaticUsers lets a subject of the issuer that names a static
	// user act as that user. Off, the issuer's tokens for such a subject
	// are refused, since a name belongs to one source unless the file
	// links them.
	LinkStaticUsers bool `yaml:"link_static_users"`
	// AllowedGroups are the groups the issuer's tokens may claim in
	// groups. The others are dropped: an issuer's groups are its own say
	// only where the file grants it, since a group in an allow list
	// opens a connection, stored credentials and all. None by default.
	AllowedGroups []string `yaml:"allowed_groups"`
}

// User is a static user who signs in with a password.
type User struct {
	Name         string   `yaml:"name"`
	PasswordHash string   `yaml:"password_hash"`
	Groups       []string `yaml:"groups"`
}

// OIDC configures sign-in through an OpenID Connect identity provider.
type OIDC struct {
	Issuer           string `yaml:"issuer"`
	ClientID         string `yaml:"client_id"`
	ClientSecretFile string `yaml:"client_secret_file"`
	ClientSecretEnv  string `yaml:"client_secret_env"`
	RedirectURL      string `yaml:"redirect_url"`
	// ReturnURLs are the only places a browser is sent back to after a
	// sign-in, with a one-time code or an error in the fragment. A
	// sign-in names one of them exactly. ParseV1 fills in the origin of
	// RedirectURL, with the path /, when the file lists none.
	ReturnURLs   []string `yaml:"return_urls"`
	Scopes       []string `yaml:"scopes"`
	SubjectClaim string   `yaml:"subject_claim"`
	GroupsClaim  string   `yaml:"groups_claim"`
	// RequireKnownUsers admits only the users that File.Admitted names:
	// in a connection's allow.users, in one of its allow.groups by the
	// groups claim, or, with LinkStaticUsers, a static user.
	RequireKnownUsers bool `yaml:"require_known_users"`
	// LinkStaticUsers lets an OIDC user whose subject names a static user
	// sign in under that name, with the groups of the groups claim. Off,
	// a subject that is a static user's name by SameName is refused,
	// since a name belongs to one source unless the file links them.
	LinkStaticUsers bool `yaml:"link_static_users"`
}

// Connection is an entry of the connection registry.
type Connection struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	Target   string `yaml:"target"`
	// HostKeys are the host keys an ssh target may present, each a public
	// key in authorized_keys format or a SHA-256 fingerprint as
	// ssh-keygen -l prints it. An ssh connection has HostKeys or
	// InsecureIgnoreHostKey, and other protocols neither.
	HostKeys []string `yaml:"host_keys"`
	// InsecureIgnoreHostKey accepts any host key from an ssh target, with
	// a warning, for demos.
	InsecureIgnoreHostKey bool `yaml:"insecure_ignore_host_key"`
	// TLS is the certificate policy of a vnc target's VeNCrypt. Other
	// protocols have none.
	TLS         ConnectionTLS `yaml:"tls"`
	Credentials Credentials   `yaml:"credentials"`
	Allow       Allow         `yaml:"allow"`
}

// ConnectionTLS is the policy for the certificate that a vnc target
// presents when VeNCrypt runs TLS, which the native VNC backend prefers
// whenever a server offers it. By default the certificate must chain to
// the system's roots and name the target's host.
type ConnectionTLS struct {
	// Require refuses a server that does not offer VeNCrypt with a
	// certificate, so that nobody on the way can strip it from the
	// security types the server offers. CAFile and Pins imply it.
	Require bool `yaml:"require"`
	// ServerName is the name the certificate must carry, if not the
	// target's host.
	ServerName string `yaml:"server_name"`
	// CAFile holds the PEM certificates of the CAs that replace the
	// system's roots, as for a private CA. It is read when a session
	// starts.
	CAFile string `yaml:"ca_file"`
	// Pins are the certificates accepted as they are, as an ssh
	// connection's host keys are: each a certificate in PEM or its
	// SHA-256 fingerprint, in hex as openssl x509 -fingerprint -sha256
	// prints it or in base64 after "SHA256:". They replace the roots, so
	// CAFile is not set with them.
	Pins []string `yaml:"pins"`
	// InsecureSkipVerify accepts any certificate that is not pinned, and
	// logs a warning with its fingerprint. For demos.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// set reports whether any of t's keys is set.
func (t ConnectionTLS) set() bool {
	return t.Require || t.ServerName != "" || t.CAFile != "" || len(t.Pins) > 0 || t.InsecureSkipVerify
}

// RootCAs reads the certificates in CAFile, and returns nil, for the
// system's roots, if it is not set.
func (t ConnectionTLS) RootCAs() (*x509.CertPool, error) {
	if t.CAFile == "" {
		return nil, nil
	}
	b, err := os.ReadFile(t.CAFile) // #nosec G304 -- the path comes from the operator's configuration
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s holds no PEM certificate", t.CAFile)
	}
	return pool, nil
}

// Credentials say how occamyd authenticates to a connection's target.
// Exactly one of PasswordFile, PasswordEnv, PrivateKeyFile and Prompt is
// set.
type Credentials struct {
	Username       string `yaml:"username"`
	PasswordFile   string `yaml:"password_file"`
	PasswordEnv    string `yaml:"password_env"`
	PrivateKeyFile string `yaml:"private_key_file"`
	// Prompt stores nothing: the session owner is asked for whatever the
	// target requires, and the answers are not kept.
	Prompt bool `yaml:"prompt"`
}

// Allow lists who may open a connection.
type Allow struct {
	Users  []string `yaml:"users"`
	Groups []string `yaml:"groups"`
}

// ErrNoSecret is returned when credentials store no secret of the kind
// asked for.
var ErrNoSecret = errors.New("config: no stored secret")

// Password reads the password from PasswordFile or PasswordEnv. Trailing
// line breaks in a file are dropped.
func (c Credentials) Password() (string, error) {
	return readSecret(c.PasswordFile, c.PasswordEnv)
}

// PrivateKey reads PrivateKeyFile.
func (c Credentials) PrivateKey() ([]byte, error) {
	if c.PrivateKeyFile == "" {
		return nil, ErrNoSecret
	}
	return os.ReadFile(c.PrivateKeyFile) // #nosec G304 -- the path comes from the operator's configuration
}

// ClientSecret reads the client secret from ClientSecretFile or
// ClientSecretEnv, and returns ErrNoSecret for a public client.
func (o OIDC) ClientSecret() (string, error) {
	return readSecret(o.ClientSecretFile, o.ClientSecretEnv)
}

func readSecret(file, env string) (string, error) {
	switch {
	case file != "":
		b, err := os.ReadFile(file) // #nosec G304 -- the path comes from the operator's configuration
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	case env != "":
		v, ok := os.LookupEnv(env)
		if !ok {
			return "", fmt.Errorf("config: environment variable %s is not set", env)
		}
		return v, nil
	}
	return "", ErrNoSecret
}

// Version returns the version of a configuration file: 1 for a file with
// version: 1, and 0 for a file without a version key, which is read as v0
// configuration. A version brought in by a merge key (<<) counts. Any
// other version is an error, and so is a version key in any YAML document
// but the first, which the v0 decoder would silently skip.
func Version(raw []byte) (int, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	version := 0
	for doc := 0; ; doc++ {
		var root yaml.Node
		err := dec.Decode(&root)
		if errors.Is(err, io.EOF) {
			return version, nil
		}
		if err != nil {
			return 0, fmt.Errorf("config: %w", err)
		}
		v := versionKey(&root)
		if v == nil {
			continue
		}
		if doc > 0 {
			return 0, fmt.Errorf("config: line %d: version is not in the file's first YAML document; a configuration file holds one document", v.Line)
		}
		var n int
		if v.Kind != yaml.ScalarNode || v.Tag != "!!int" || v.Decode(&n) != nil || n != 1 {
			return 0, fmt.Errorf("config: line %d: version %q is not supported: write version: 1, or leave the key out for a v0 file", v.Line, v.Value)
		}
		version = 1
	}
}

// versionKey returns the value of a document's top-level version key, or
// nil if it has none.
func versionKey(root *yaml.Node) *yaml.Node {
	m := root
	if m.Kind == yaml.DocumentNode && len(m.Content) == 1 {
		m = m.Content[0]
	}
	for m.Kind == yaml.AliasNode && m.Alias != nil {
		m = m.Alias
	}
	if m.Kind != yaml.MappingNode {
		return nil
	}
	return lookupKey(m, "version", 0)
}

// lookupKey returns the value of key in mapping m, following aliases and
// merge keys as the decoder does: a key of m wins over a merged one, and
// an earlier merged mapping over a later one.
func lookupKey(m *yaml.Node, key string, depth int) *yaml.Node {
	for m.Kind == yaml.AliasNode && m.Alias != nil {
		m = m.Alias
	}
	if depth > 16 {
		return nil
	}
	if m.Kind == yaml.SequenceNode { // the value of a merge key
		for _, item := range m.Content {
			if v := lookupKey(item, key, depth+1); v != nil {
				return v
			}
		}
		return nil
	}
	if m.Kind != yaml.MappingNode {
		return nil
	}
	var merges []*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Tag == "!!merge" {
			merges = append(merges, v)
			continue
		}
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return v
		}
	}
	for _, v := range merges {
		if found := lookupKey(v, key, depth+1); found != nil {
			return found
		}
	}
	return nil
}

// ParseV1 decodes a version 1 configuration file, rejecting unknown keys,
// fills in defaults and validates it. Validation checks the file system
// and the environment for the files and variables it refers to, without
// reading any secret. The error, if any, is Problems, listing every
// problem found.
func ParseV1(raw []byte) (*File, error) {
	f := &File{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	err := dec.Decode(f)
	var te *yaml.TypeError
	if err != nil && !errors.As(err, &te) {
		return nil, fmt.Errorf("config: %w", err)
	}

	// The decoder has checked the syntax and refused excessive aliasing,
	// so the walk over the same document stays small.
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	k := &keys{lines: map[string]int{}, byLine: map[int]string{}}
	k.walk(&root, reflect.TypeFor[File](), "")
	c := &checker{lines: k.lines, problems: k.problems}
	if te != nil {
		if k.typeErrors(c, te) {
			// A mapping with a duplicate key is not decoded at all, so
			// validating the rest would report what is missing from it.
			return nil, c.problems
		}
	} else {
		var next yaml.Node
		if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
			c.add("", "a configuration file holds one YAML document")
		}
	}

	f.validate(c)
	if len(c.problems) > 0 {
		return nil, c.problems
	}
	return f, nil
}

// keys walks the YAML node tree beside the type it decodes into. It
// records the line of every key path, so that problems found later can
// name their line, and it explains the keys that KnownFields rejects.
type keys struct {
	lines    map[string]int // key path to line
	byLine   map[int]string // line to the last key path on it
	unknown  map[int]bool   // lines with an unknown key
	problems Problems
}

func (k *keys) record(path string, line int) {
	if _, ok := k.lines[path]; !ok {
		k.lines[path] = line
	}
	k.byLine[line] = path
}

func (k *keys) walk(n *yaml.Node, t reflect.Type, path string) {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			k.walk(c, t, path)
		}
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return
		}
		fields := yamlFields(t)
		var merges []*yaml.Node
		// Merged keys are walked last, so that the lines of the keys
		// they override are the ones recorded.
		defer func() {
			for _, m := range merges {
				k.merge(m, t, path)
			}
		}()
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Tag == "!!merge" {
				merges = append(merges, value)
				continue
			}
			p := joinPath(path, key.Value)
			k.record(p, key.Line)
			ft, ok := fields[key.Value]
			if !ok {
				k.reject(key, t, p, fields)
				continue
			}
			k.walk(value, ft, p)
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := joinPath(path, n.Content[i].Value)
			k.record(p, n.Content[i].Line)
			k.walk(n.Content[i+1], t.Elem(), p)
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return
		}
		for i, item := range n.Content {
			p := fmt.Sprintf("%s[%d]", path, i)
			k.record(p, item.Line)
			k.walk(item, t.Elem(), p)
		}
	}
}

// merge walks the mappings that a merge key (<<: *anchor) brings in.
func (k *keys) merge(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			k.walk(c, t, path)
		}
		return
	}
	k.walk(n, t, path)
}

// reject reports a key that t does not have.
func (k *keys) reject(key *yaml.Node, t reflect.Type, path string, fields map[string]reflect.Type) {
	if k.unknown == nil {
		k.unknown = map[int]bool{}
	}
	k.unknown[key.Line] = true
	msg := hints[t][key.Value]
	if msg == "" {
		msg = "unknown key"
		if s := suggest(key.Value, fields); s != "" {
			msg += ", did you mean " + s + "?"
		}
	}
	k.problems = append(k.problems, Problem{Line: key.Line, Path: path, Message: msg})
}

var (
	unknownField = regexp.MustCompile(`^line (\d+): field .* not found in type `)
	duplicateKey = regexp.MustCompile(`^line \d+: mapping key .* already defined at line \d+$`)
	lineMessage  = regexp.MustCompile(`^line (\d+): (.*)$`)
)

// typeErrors adds the decoder's errors that the walk has not already
// explained, such as a string where a list belongs, and reports whether
// one of them is a duplicate key.
func (k *keys) typeErrors(c *checker, te *yaml.TypeError) (duplicate bool) {
	for _, e := range te.Errors {
		duplicate = duplicate || duplicateKey.MatchString(e)
		if m := unknownField.FindStringSubmatch(e); m != nil {
			if line, _ := strconv.Atoi(m[1]); k.unknown[line] {
				continue
			}
		}
		m := lineMessage.FindStringSubmatch(e)
		if m == nil {
			c.problems = append(c.problems, Problem{Message: e})
			continue
		}
		line, _ := strconv.Atoi(m[1])
		msg := m[2]
		if strings.HasSuffix(msg, "into time.Duration") {
			msg = "not a duration: write a number with a unit, such as 15m or 1h30m"
		}
		c.problems = append(c.problems, Problem{Line: line, Path: k.byLine[line], Message: msg})
	}
	return duplicate
}

// hints explain keys that a version 1 file rejects but that a reader may
// well write: inline secrets, and keys of v0 files.
var hints = map[reflect.Type]map[string]string{
	reflect.TypeFor[File](): {
		"address": "v1 calls the listen address listen",
		"mode":    "v1 has no mode; debug.address turns on the debug listener",
	},
	reflect.TypeFor[Auth](): { // #nosec G101 -- messages about keys, not credentials
		"jwt_secret": "v1 signs tokens with the Ed25519 key in signing_key_file",
		"jwt_alg":    "v1 signs tokens with the Ed25519 key in signing_key_file",
	},
	reflect.TypeFor[User](): { // #nosec G101 -- messages about keys, not credentials
		"password": "static users have a bcrypt password_hash, never a password",
	},
	reflect.TypeFor[Credentials](): {
		"password":    "inline secrets are not allowed, so that the file can be committed: use password_file, password_env or prompt: true",
		"private_key": "inline keys are not allowed, so that the file can be committed: use private_key_file",
	},
	reflect.TypeFor[OIDC](): {
		"client_secret": "inline secrets are not allowed, so that the file can be committed: use client_secret_file or client_secret_env",
	},
}

// yamlFields returns the keys of a struct type and their types.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields
}

// suggest returns the known key that key is most likely a misspelling of,
// or "".
func suggest(key string, fields map[string]reflect.Type) string {
	best, bestDist := "", 3
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if strings.HasPrefix(name, key+"_") {
			return name // signing_key for signing_key_file
		}
		if d := distance(key, name); d < bestDist {
			best, bestDist = name, d
		}
	}
	return best
}

// distance is the Levenshtein distance between a and b.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
