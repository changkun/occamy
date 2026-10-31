// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/config"
)

func TestVersion(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: "address: 0.0.0.0:5636\nmode: debug\n", want: 0},
		{raw: "- a\n- b\n", want: 0},
		{raw: "version: 1\n", want: 1},
		{raw: "# comment\n---\nlisten: :5636\nversion: 1\n", want: 1},
		{raw: "version: 2\n", wantErr: true},
		{raw: "version: 0\n", wantErr: true},
		{raw: "version: \"1\"\n", wantErr: true},
		{raw: "version: [1]\n", wantErr: true},
		{raw: "version:\n", wantErr: true},
		{raw: "address: [\n", wantErr: true},
		// A merged version counts, as the decoder reads it.
		{raw: "<<: {version: 1}\nlisten: :5636\n", want: 1},
		{raw: "base: &b {version: 1}\n<<: *b\n", want: 1},
		{raw: "<<: [{mode: debug}, {version: 1}]\n", want: 1},
		{raw: "<<: {version: 2}\n", wantErr: true},
		{raw: "version: 1\n<<: {version: 2}\n", want: 1},
		{raw: "a: {version: 1}\n", want: 0},
		{raw: "- {version: 1}\n", want: 0},
		// A version in a later document is an error, not a v0 file.
		{raw: "---\n---\nversion: 1\n", wantErr: true},
		{raw: "address: :5636\n---\nversion: 1\n", wantErr: true},
		{raw: "address: :5636\n---\nmode: debug\n", want: 0},
	}
	for _, tt := range tests {
		got, err := config.Version([]byte(tt.raw))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Version(%q) = %d, %v; want %d, error %v", tt.raw, got, err, tt.want, tt.wantErr)
		}
	}
}

// TestVersionV0File checks that the v0 file in the repository is still
// read as v0.
func TestVersionV0File(t *testing.T) {
	raw, err := os.ReadFile("../../conf.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := config.Version(raw); err != nil || v != 0 {
		t.Fatalf("Version(conf.yaml) = %d, %v; want 0", v, err)
	}
	c, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Address != "0.0.0.0:5636" {
		t.Errorf("conf.yaml address = %q", c.Address)
	}
}

// fixture creates the files and environment variables that test files
// refer to, and returns the directory to put in place of DIR.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"key", "password", "tls.crt", "tls.key", "secret"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePublicKey(t, filepath.Join(dir, "gateway.pub"))
	writeCertificate(t, filepath.Join(dir, "ca.pem"))
	t.Setenv("OCCAMY_TEST_PASSWORD", "s3cret")
	t.Setenv("OCCAMY_TEST_EMPTY", "")
	return dir
}

// writePublicKey writes a trusted issuer's Ed25519 public key.
func writePublicKey(t *testing.T, path string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeCertificate writes a self-signed CA certificate in PEM.
func writeCertificate(t *testing.T, path string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "occamy test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func parse(t *testing.T, raw string) (*config.File, error) {
	t.Helper()
	dir := fixture(t)
	return config.ParseV1([]byte(strings.ReplaceAll(raw, "DIR", dir)))
}

// TestParseV1Example checks that the documented example file loads, by
// putting the files it names into a temporary directory.
func TestParseV1Example(t *testing.T) {
	raw, err := os.ReadFile("testdata/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, prefix := range []string{"/etc/occamy/", "/var/lib/occamy/", "/run/secrets/"} {
		raw = []byte(strings.ReplaceAll(string(raw), prefix, filepath.Join(dir, prefix)+"/"))
	}
	for _, name := range []string{
		"etc/occamy/tls.crt",
		"etc/occamy/tls.key",
		"etc/occamy/keys/deploy",
		"run/secrets/oidc-client-secret",
		"run/secrets/windows-password",
		"run/secrets/k8s-token",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePublicKey(t, filepath.Join(dir, "etc/occamy/gateway.pub"))
	writeCertificate(t, filepath.Join(dir, "etc/occamy/k8s-ca.crt"))
	// The signing key is created on first start, so only its directory
	// exists.
	if err := os.MkdirAll(filepath.Join(dir, "var/lib/occamy"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCCAMY_NAS_VNC_PASSWORD", "s3cret")

	got, err := config.ParseV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := func(name string) string { return filepath.Join(dir, name) }
	yes := true
	want := &config.File{
		Version: 1,
		Listen:  "0.0.0.0:5636",
		TLS:     config.TLS{Cert: path("etc/occamy/tls.crt"), Key: path("etc/occamy/tls.key")},
		Debug:   config.Debug{Address: "localhost:6060"},
		Origins: []string{"https://occamy.example.com"},
		Auth: config.Auth{
			SigningKeyFile:      path("var/lib/occamy/signing.key"),
			AccessTokenLifetime: 15 * time.Minute,
			TrustedIssuers:      []config.TrustedIssuer{{Issuer: "gateway", PublicKeyFile: path("etc/occamy/gateway.pub"), LinkStaticUsers: true, AllowedGroups: []string{"ops"}}},
			Users: []config.User{
				{Name: "alice", PasswordHash: "$2a$12$R6bvkO3lC3JV/OaHnySvquk.wobMPB3XleIdoBgEwlhjbcfaZi0km", Groups: []string{"ops"}},
				{Name: "bob", PasswordHash: "$2a$10$Ad6WcgC.YRhS7d8JbV/9..v/X5C7aAnBg5cVXdoz1F1d0uHmkmuH2", Groups: []string{"dev"}},
			},
			OIDC: &config.OIDC{
				Issuer:           "https://accounts.example.com",
				ClientID:         "occamy",
				ClientSecretFile: path("run/secrets/oidc-client-secret"),
				RedirectURL:      "https://occamy.example.com/api/v1/auth/oidc/callback",
				ReturnURLs:       []string{"https://occamy.example.com/"},
				Scopes:           []string{"openid", "profile", "email", "groups"},
				SubjectClaim:     "email",
				GroupsClaim:      "groups",
			},
		},
		Guacd: config.Guacd{
			Address: "localhost:4822",
			Params:  map[string]map[string]string{"rdp": {"ignore-cert": "true"}},
		},
		Connections: []config.Connection{
			{
				ID: "build-box", Name: "Build box", Protocol: "ssh", Target: "build.internal:22", Backend: "native",
				HostKeys:    []string{"SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"},
				Credentials: config.Credentials{Username: "deploy", PrivateKeyFile: path("etc/occamy/keys/deploy")},
				Allow:       config.Allow{Users: []string{"alice"}, Groups: []string{"ops"}},
			},
			{
				ID: "nas-desktop", Name: "NAS desktop", Protocol: "vnc", Target: "nas.internal:5900", Backend: "native",
				TLS:         config.ConnectionTLS{Pins: []string{"B7:2C:51:9E:0D:44:A3:6F:18:E2:95:7B:C0:3A:61:DD:4F:89:12:B6:E7:05:9C:73:2A:F1:58:0E:C4:93:6B:1D"}},
				Credentials: config.Credentials{PasswordEnv: "OCCAMY_NAS_VNC_PASSWORD"},
				Allow:       config.Allow{Groups: []string{"ops", "dev"}},
			},
			{
				ID: "studio-mac", Name: "Studio Mac", Protocol: "vnc", Target: "studio-mac.internal:5900", Backend: "native",
				ARD:         true,
				Credentials: config.Credentials{Username: "studio", Prompt: true},
				Allow:       config.Allow{Users: []string{"alice"}},
			},
			{
				ID: "windows", Name: "Windows workstation", Protocol: "rdp", Target: "10.0.4.20:3389", Backend: "guacd",
				Credentials: config.Credentials{Username: "Administrator", PasswordFile: path("run/secrets/windows-password")},
				Allow:       config.Allow{Groups: []string{"ops"}},
			},
			{
				ID: "lab-shell", Name: "Lab shell", Protocol: "ssh", Target: "[fd00::22]:22", Backend: "native",
				InsecureIgnoreHostKey: true,
				Credentials:           config.Credentials{Prompt: true},
				Allow:                 config.Allow{Groups: []string{"dev"}},
			},
			{
				ID: "web-shell", Name: "Web pod shell", Protocol: "kubernetes", Target: "k8s.internal:6443", Backend: "native",
				Kubernetes: config.Kubernetes{
					Namespace: "web", Pod: "web-0", Container: "app",
					Command: kubernetes.DefaultCommand, TTY: &yes,
				},
				TLS:         config.ConnectionTLS{CAFile: path("etc/occamy/k8s-ca.crt")},
				Credentials: config.Credentials{TokenFile: path("run/secrets/k8s-token")},
				Allow:       config.Allow{Groups: []string{"ops"}},
			},
		},
		AdhocAllowlist: []string{"10.0.0.0/8", "*.lab.internal:22"},
		AdhocBackends:  map[string]string{"ssh": "native", "vnc": "native", "rdp": "guacd", "telnet": "guacd"},
		Limits:         config.Limits{Sessions: 500, SessionsPerUser: 20, ViewersPerSession: 50, SharesPerSession: 100},
		Streams:        config.Streams{ViewerTimeout: 45 * time.Second},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseV1(testdata/v1.yaml) =\n%+v\nwant\n%+v", got, want)
	}
}

const hash = "$2a$10$Ad6WcgC.YRhS7d8JbV/9..v/X5C7aAnBg5cVXdoz1F1d0uHmkmuH2"

// head starts a valid file: a signing key and the static users alice in
// group ops and bob in group dev.
const head = `version: 1
auth:
  signing_key_file: DIR/signing.key
  users:
    - {name: alice, password_hash: "` + hash + `", groups: [ops]}
    - {name: bob, password_hash: "` + hash + `", groups: [dev]}
`

// conn is a valid connection list.
const conn = `connections:
  - {id: box, protocol: ssh, insecure_ignore_host_key: true, target: box.internal, credentials: {username: deploy, private_key_file: DIR/key}, allow: {users: [alice]}}
`

func TestParseV1Defaults(t *testing.T) {
	f, err := parse(t, head+conn)
	if err != nil {
		t.Fatal(err)
	}
	if f.Listen != config.DefaultListen {
		t.Errorf("listen = %q, want %q", f.Listen, config.DefaultListen)
	}
	if f.Guacd.Address != config.DefaultGuacdAddress {
		t.Errorf("guacd.address = %q, want %q", f.Guacd.Address, config.DefaultGuacdAddress)
	}
	if f.Auth.AccessTokenLifetime != config.DefaultAccessTokenLifetime {
		t.Errorf("access_token_lifetime = %v, want %v", f.Auth.AccessTokenLifetime, config.DefaultAccessTokenLifetime)
	}
	if want := (config.Limits{Sessions: config.DefaultSessions, SessionsPerUser: config.DefaultSessionsPerUser, ViewersPerSession: config.DefaultViewersPerSession, SharesPerSession: config.DefaultSharesPerSession}); f.Limits != want {
		t.Errorf("limits = %+v, want %+v", f.Limits, want)
	}
	if f.Streams.ViewerTimeout != config.DefaultViewerTimeout {
		t.Errorf("streams.viewer_timeout = %v, want %v", f.Streams.ViewerTimeout, config.DefaultViewerTimeout)
	}
	c := f.Connections[0]
	if c.Name != "box" || c.Target != "box.internal:22" {
		t.Errorf("connection name %q, target %q; want box, box.internal:22", c.Name, c.Target)
	}
	if f.LegacyV0 || f.AllowAdhocTargets || f.TLS != (config.TLS{}) || f.Debug.Address != "" {
		t.Errorf("optional features on by default: %+v", f)
	}

	f, err = parse(t, head+`connections:
  - {id: pod, protocol: kubernetes, target: k8s.internal, kubernetes: {pod: web-0}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: attach, protocol: kubernetes, target: k8s.internal, kubernetes: {pod: web-0, attach: true}, credentials: {prompt: true}, allow: {users: [alice]}}
`)
	if err != nil {
		t.Fatal(err)
	}
	k := f.Connections[0]
	if k.Target != "k8s.internal:443" || k.Backend != config.BackendNative {
		t.Errorf("kubernetes target %q, backend %q; want k8s.internal:443, native", k.Target, k.Backend)
	}
	if k.Kubernetes.Namespace != "default" || !reflect.DeepEqual(k.Kubernetes.Command, kubernetes.DefaultCommand) || k.Kubernetes.TTY == nil || !*k.Kubernetes.TTY {
		t.Errorf("kubernetes defaults %+v: want namespace default, the default command, a tty", k.Kubernetes)
	}
	if &k.Kubernetes.Command[0] == &kubernetes.DefaultCommand[0] {
		t.Error("the connection shares the default command's array")
	}
	if a := f.Connections[1].Kubernetes; a.Command != nil {
		t.Errorf("attach filled in the command %q", a.Command)
	}
	if _, ok := f.AdhocBackends["kubernetes"]; ok {
		t.Errorf("adhoc_backends has kubernetes: %v", f.AdhocBackends)
	}

	f, err = parse(t, head+"  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://occamy.example.com:8443/occamy/cb}\n")
	if err != nil {
		t.Fatal(err)
	}
	o := f.Auth.OIDC
	if !reflect.DeepEqual(o.Scopes, config.DefaultOIDCScopes) || o.SubjectClaim != config.DefaultOIDCSubjectClaim || o.GroupsClaim != "" || o.RequireKnownUsers {
		t.Errorf("oidc defaults: scopes %v, subject claim %q, groups claim %q, require known users %v", o.Scopes, o.SubjectClaim, o.GroupsClaim, o.RequireKnownUsers)
	}
	if want := []string{"https://occamy.example.com:8443/"}; !reflect.DeepEqual(o.ReturnURLs, want) {
		t.Errorf("oidc return URLs %q, want %q: the origin of the redirect URL", o.ReturnURLs, want)
	}
}

// TestKnownNames checks the groups that a file names, which OIDC sign-in
// maps to, and the users and groups that require_known_users admits: the
// static users, and names in allow lists that are theirs, only when
// link_static_users links them.
func TestKnownNames(t *testing.T) {
	for _, tt := range []struct {
		link  string
		users map[string]bool
	}{
		{"false", map[string]bool{"carol": true}},
		{"true", map[string]bool{"alice": true, "bob": true, "carol": true, "Bob": true}},
	} {
		f, err := parse(t, head+`  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://o.example.com/cb, link_static_users: `+tt.link+`}
`+conn+`  - {id: b, protocol: ssh, insecure_ignore_host_key: true, target: b, credentials: {prompt: true}, allow: {users: [carol, Bob], groups: [admins]}}
`)
		if err != nil {
			t.Fatal(err)
		}
		users, groups := f.Admitted()
		if !reflect.DeepEqual(users, tt.users) || !reflect.DeepEqual(groups, map[string]bool{"admins": true}) {
			t.Errorf("link_static_users %s: Admitted = %v, %v, want %v, [admins]", tt.link, users, groups, tt.users)
		}
		if got, want := f.KnownGroups(), map[string]bool{"ops": true, "dev": true, "admins": true}; !reflect.DeepEqual(got, want) {
			t.Errorf("KnownGroups = %v, want %v", got, want)
		}
	}
}

// TestSameName checks when two user names pass for one: under Unicode
// case folding, once in NFC.
func TestSameName(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"alice", "alice", true},
		{"alice", "Alice", true},
		{"alice", "ALICE", true},
		{"zo\u00eb", "zoe\u0308", true}, // precomposed and decomposed
		{"Zo\u00cb", "zoe\u0308", true}, // and in another case
		{"\u212aim", "kim", true},       // Kelvin sign, which folds to k
		{"alice", "alice ", false},
		{"alice", "al\u0456ce", false},    // Cyrillic i: a look-alike, not the same
		{"alice", "\uff41lice", false},    // fullwidth a
		{"stra\u00dfe", "strasse", false}, // simple folding only
	} {
		if got := config.SameName(tt.a, tt.b); got != tt.want {
			t.Errorf("SameName(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestOverlap checks when the debug listener would share the public
// listener's port.
func TestOverlap(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{":5636", ":5636", true},
		{":5636", "127.0.0.1:5636", true},
		{":5636", "[::1]:5636", true},
		{"0.0.0.0:5636", "127.0.0.1:5636", true},
		{"0.0.0.0:5636", "[::1]:5636", true}, // Go listens on both families
		{"[::]:5636", "10.0.0.1:5636", true},
		{"127.0.0.1:5636", "localhost:5636", true},
		{"[::1]:5636", "LocalHost.:5636", true},
		{"127.0.0.1:5636", "[::ffff:127.0.0.1]:5636", true},
		{"[fe80::1%en0]:5636", "[fe80::1]:5636", true},
		{"occamy.internal:5636", "Occamy.Internal:5636", true},
		{"10.0.0.1:5636", "10.0.0.1:05636", true},
		{":5636", ":6060", false},
		{":5636", "localhost:6060", false},
		{"10.0.0.1:5636", "127.0.0.1:5636", false},
		{"127.0.0.1:5636", "127.0.0.2:5636", false},
		{"127.0.0.1:5636", "[::1]:5636", false},
		{"[::1]:5636", "occamy.internal:5636", false},
		{":0", ":0", false},
		{"nope", ":5636", false},
	} {
		if got := config.Overlap(tt.a, tt.b); got != tt.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
		if got := config.Overlap(tt.b, tt.a); got != tt.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", tt.b, tt.a, got, tt.want)
		}
	}
}

// TestParseV1Backends checks that every connection and every protocol of
// adhoc_backends ends up with a backend: its own, or the protocol's
// default, which is native for ssh and guacd for the others.
func TestParseV1Backends(t *testing.T) {
	f, err := parse(t, head+`connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: ssh, backend: native, insecure_ignore_host_key: true, target: b, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: vnc, target: c, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: vnc, backend: native, target: d, tls: {require: true}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: vnc, backend: guacd, target: e, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: vnc, backend: native, ard: true, target: f, tls: {insecure_skip_verify: true}, credentials: {username: alice, prompt: true}, allow: {users: [alice]}}
  - {id: g, protocol: rdp, target: g, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: h, protocol: telnet, backend: guacd, target: h, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: i, protocol: kubernetes, target: i, kubernetes: {pod: p}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: j, protocol: kubernetes, backend: native, target: j, kubernetes: {pod: p}, credentials: {prompt: true}, allow: {users: [alice]}}
adhoc_backends: {vnc: native}
`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range f.Connections {
		got = append(got, c.ID+":"+c.Backend)
	}
	want := []string{"a:native", "b:native", "c:guacd", "d:native", "e:guacd", "f:native", "g:guacd", "h:guacd", "i:native", "j:native"}
	if !slices.Equal(got, want) {
		t.Errorf("backends %v, want %v", got, want)
	}
	if want := map[string]string{"ssh": "native", "vnc": "native", "rdp": "guacd", "telnet": "guacd"}; !maps.Equal(f.AdhocBackends, want) {
		t.Errorf("adhoc_backends %v, want %v", f.AdhocBackends, want)
	}
	for _, tt := range []struct{ protocol, backend, want string }{
		{"ssh", "", "native"}, {"vnc", "", "guacd"}, {"rdp", "", "guacd"}, {"telnet", "", "guacd"}, {"kubernetes", "", "native"},
		{"vnc", "native", "native"}, {"rdp", "guacd", "guacd"},
	} {
		if got := config.BackendOf(tt.protocol, tt.backend); got != tt.want {
			t.Errorf("BackendOf(%q, %q) = %q, want %q", tt.protocol, tt.backend, got, tt.want)
		}
	}
}

// TestParseV1Valid checks files that are valid in less common ways.
func TestParseV1Valid(t *testing.T) {
	tests := []struct {
		name, raw string
	}{
		{"targets", head + `connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: "[::1]:2222", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: vnc, target: "::1", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: rdp, target: 192.0.2.1, credentials: {password_env: OCCAMY_TEST_PASSWORD}, allow: {users: [alice]}}
  - {id: d.e_f-1, protocol: telnet, target: host_1.example.com., credentials: {password_file: DIR/password}, allow: {users: [alice]}}
`},
		{"kubernetes", head + `connections:
  - {id: a, protocol: kubernetes, target: "[fd00::6443]:6443", kubernetes: {pod: p}, credentials: {token_env: OCCAMY_TEST_PASSWORD}, allow: {users: [alice]}}
  - {id: b, protocol: kubernetes, target: k8s.example.com, kubernetes: {namespace: kube-system, pod: coredns-7db6d8ff4d-x2v9k.a, container: coredns, command: [/bin/ls, -l]}, tls: {pins: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]}, credentials: {client_cert_file: DIR/tls.crt, client_key_file: DIR/tls.key}, allow: {users: [alice]}}
  - {id: c, protocol: kubernetes, target: c, kubernetes: {pod: p, attach: true, tty: false}, tls: {insecure_skip_verify: true, server_name: kubernetes.default.svc}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: kubernetes, backend: native, target: d, kubernetes: {pod: p}, tls: {ca_file: DIR/ca.pem}, credentials: {token_file: DIR/secret}, allow: {users: [alice]}}
`},
		{"merged version", "<<: {version: 1}\n" + strings.TrimPrefix(head, "version: 1\n")},
		{"debug on its own port", head + "listen: ':5636'\ndebug: {address: 'localhost:6060'}\n"},
		{"debug on the port of another address", head + "listen: '10.0.0.1:5636'\ndebug: {address: '127.0.0.1:5636'}\n"},
		{"names from oidc", head + `  oidc: {issuer: "http://localhost:8080/realms/x", client_id: occamy, redirect_url: "http://127.0.0.1:5636/cb", client_secret_env: OCCAMY_TEST_PASSWORD}
connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [carol@example.com], groups: [admins]}}
`},
		{"names from a trusted issuer", head + `  trusted_issuers: [{issuer: gateway, public_key_file: DIR/gateway.pub}]
connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {groups: [admins]}}
`},
		{"anchors", head + `connections:
  - &base {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {groups: [ops]}}
  - <<: *base
    id: b
`},
		{"adhoc", head + `allow_adhoc_targets: true
adhoc_allowlist: [10.0.0.0/8, "fd00::/8", 192.0.2.7, host.example.com, "*.lab.internal"]
`},
		{"tls and debug", head + `tls: {cert: DIR/tls.crt, key: DIR/tls.key}
debug: {address: "localhost:6060"}
origins: ["HTTPS://Occamy.Example.com:8443/", "http://localhost:5173"]
listen: "[::]:443"
legacy_v0: true
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parse(t, tt.raw); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestParseV1Origins(t *testing.T) {
	f, err := parse(t, head+`origins: ["HTTPS://Occamy.Example.com:8443/"]`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://occamy.example.com:8443"}; !reflect.DeepEqual(f.Origins, want) {
		t.Errorf("origins = %q, want %q", f.Origins, want)
	}
}

type want struct{ path, msg string }

func TestParseV1Problems(t *testing.T) {
	lowCost, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  string
		want []want
	}{
		// Keys.
		{"typo", head + "conections: []\n", []want{{"conections", "unknown key, did you mean connections?"}}},
		{"nested typo", head + `connections:
  - {id: a, protocol: vnc, target: a, credentials: {pasword_file: DIR/password}, allow: {users: [alice]}}
`, []want{
			{"connections[0].credentials.pasword_file", "unknown key, did you mean password_file?"},
			{"connections[0].credentials", "no credential source"},
		}},
		{"v0 key", head + "address: 0.0.0.0:5636\n", []want{{"address", "v1 calls the listen address listen"}}},
		{"signing key without _file", "version: 1\nauth: {signing_key: DIR/signing.key}\n", []want{
			{"auth.signing_key", "did you mean signing_key_file?"},
			{"auth.signing_key_file", "required"},
		}},
		{"inline password", head + `connections:
  - {id: a, protocol: vnc, target: a, credentials: {password: hunter2}, allow: {users: [alice]}}
`, []want{
			{"connections[0].credentials.password", "inline secrets are not allowed, so that the file can be committed: use password_file, password_env or prompt: true"},
			{"connections[0].credentials", "no credential source"},
		}},
		{"inline client secret", head + "  oidc: {issuer: https://id.example.com, client_id: occamy, client_secret: hunter2, redirect_url: https://o.example.com/cb}\n", []want{
			{"auth.oidc.client_secret", "use client_secret_file or client_secret_env"},
		}},
		{"plain password", "version: 1\nauth:\n  signing_key_file: DIR/signing.key\n  users: [{name: alice, password: hunter2}]\n", []want{
			{"auth.users[0].password", "bcrypt password_hash"},
			{"auth.users[0].password_hash", "required"},
		}},
		{"wrong type", head + "origins: https://o.example.com\n", []want{{"origins", "cannot unmarshal"}}},
		{"two documents", head + "---\nlisten: :1\n", []want{{"", "one YAML document"}}},
		{"duplicate key", head + "listen: :1\nlisten: :2\n", []want{{"listen", "already defined"}}},
		{"no version", "auth: {signing_key_file: DIR/signing.key}\n", []want{{"version", "must be 1"}}},

		// Listeners and TLS.
		{"listen", head + "listen: nope\n", []want{{"listen", `"nope" is not host:port`}}},
		{"listen port", head + "listen: localhost:99999\n", []want{{"listen", "invalid port"}}},
		{"listen host", head + "listen: 'bad host:1'\n", []want{{"listen", "invalid host"}}},
		{"tls cert alone", head + "tls: {cert: DIR/tls.crt}\n", []want{{"tls.key", "required when tls.cert is set"}}},
		{"tls key alone", head + "tls: {key: DIR/tls.key}\n", []want{{"tls.cert", "required when tls.key is set"}}},
		{"tls missing file", head + "tls: {cert: DIR/missing.crt, key: DIR/tls.key}\n", []want{{"tls.cert", "missing.crt does not exist"}}},
		{"tls directory", head + "tls: {cert: DIR, key: DIR/tls.key}\n", []want{{"tls.cert", "is a directory"}}},
		{"debug on listen", head + "debug: {address: ':5636'}\n", []want{{"debug.address", `":5636" shares a port with listen ":5636"`}}},
		{"debug beside a wildcard", head + "listen: ':5636'\ndebug: {address: 127.0.0.1:5636}\n", []want{{"debug.address", "shares a port with listen"}}},
		{"debug on localhost", head + "listen: 127.0.0.1:5636\ndebug: {address: localhost:5636}\n", []want{{"debug.address", "shares a port with listen"}}},
		{"debug address", head + "debug: {address: localhost}\n", []want{{"debug.address", "is not host:port"}}},
		{"origin scheme", head + "origins: [occamy.example.com, 'ftp://o.example.com']\n", []want{
			{"origins[0]", "needs the scheme http or https"},
			{"origins[1]", "needs the scheme http or https"},
		}},
		{"origin path", head + "origins: ['https://o.example.com/app', 'https://o.example.com?x', 'https://u@o.example.com']\n", []want{
			{"origins[0]", "is not an origin"},
			{"origins[1]", "is not an origin"},
			{"origins[2]", "is not an origin"},
		}},
		{"origin host", head + "origins: ['https://']\n", []want{{"origins[0]", "has no host"}}},

		// Auth.
		{"no signing key", "version: 1\n", []want{{"auth.signing_key_file", "required"}}},
		{"signing key directory missing", "version: 1\nauth: {signing_key_file: DIR/none/signing.key}\n", []want{{"auth.signing_key_file", "nor its directory"}}},
		{"signing key is a directory", "version: 1\nauth: {signing_key_file: DIR}\n", []want{{"auth.signing_key_file", "is a directory"}}},
		{"lifetime short", "version: 1\nauth: {signing_key_file: DIR/signing.key, access_token_lifetime: 30s}\n", []want{{"auth.access_token_lifetime", "30s is not between 1m and 12h"}}},
		{"lifetime long", "version: 1\nauth: {signing_key_file: DIR/signing.key, access_token_lifetime: 12h1m}\n", []want{{"auth.access_token_lifetime", "12h1m is not between"}}},
		{"lifetime negative", "version: 1\nauth: {signing_key_file: DIR/signing.key, access_token_lifetime: -5m}\n", []want{{"auth.access_token_lifetime", "not between"}}},
		{"lifetime without unit", "version: 1\nauth: {signing_key_file: DIR/signing.key, access_token_lifetime: 900}\n", []want{{"auth.access_token_lifetime", "not a duration"}}},
		{"trusted issuers", head + `  trusted_issuers:
    - {issuer: gateway, public_key_file: DIR/gateway.pub}
    - {issuer: gateway, public_key_file: DIR/missing.pub}
    - {public_key_file: DIR/gateway.pub}
    - {issuer: other}
    - {issuer: wrong, public_key_file: DIR/key}
    - {issuer: occamy, public_key_file: DIR/gateway.pub}
    - {issuer: grouped, public_key_file: DIR/gateway.pub, allowed_groups: [ops, ""]}
`, []want{
			{"auth.trusted_issuers[1].issuer", `"gateway" is already auth.trusted_issuers[0]`},
			{"auth.trusted_issuers[1].public_key_file", "does not exist"},
			{"auth.trusted_issuers[2].issuer", "must not be empty"},
			{"auth.trusted_issuers[3].public_key_file", "required"},
			{"auth.trusted_issuers[4].public_key_file", "want a PEM block of type PUBLIC KEY"},
			{"auth.trusted_issuers[5].issuer", `"occamy" is occamyd's own issuer`},
			{"auth.trusted_issuers[6].allowed_groups[1]", "must not be empty"},
		}},
		{"users", "version: 1\nauth:\n  signing_key_file: DIR/signing.key\n  users:\n" +
			`    - {name: alice, password_hash: "` + hash + `", groups: [ops, ""]}
    - {name: alice, password_hash: "` + hash + `"}
    - {name: "carol smith", password_hash: plaintext}
    - {name: dave, password_hash: "` + string(lowCost) + `"}
    - {name: erin, password_hash: "$2a$99$Ad6WcgC.YRhS7d8JbV/9..v/X5C7aAnBg5cVXdoz1F1d0uHmkmuH2"}
    - {name: "fr\u200bank", password_hash: "` + hash + `"}
    - {name: "` + strings.Repeat("g", config.MaxNameBytes+1) + `", password_hash: "` + hash + `"}
`, []want{
			{"auth.users[0].groups[1]", "must not be empty"},
			{"auth.users[1].name", `"alice" is already auth.users[0]`},
			{"auth.users[2].name", "has spaces or control characters"},
			{"auth.users[2].password_hash", "not a bcrypt hash"},
			{"auth.users[3].password_hash", "bcrypt cost 4 is below 10"},
			{"auth.users[4].password_hash", "not a bcrypt hash: crypto/bcrypt: cost 99"},
			{"auth.users[5].name", "is not a usable name"},
			{"auth.users[6].name", "is not a usable name"},
		}},
		{"oidc required", head + "  oidc: {}\n", []want{
			{"auth.oidc.issuer", "required"},
			{"auth.oidc.client_id", "required"},
			{"auth.oidc.redirect_url", "required"},
		}},
		{"oidc urls", head + "  oidc: {issuer: 'http://id.example.com', client_id: occamy, redirect_url: '/callback'}\n", []want{
			{"auth.oidc.issuer", "needs https; http is accepted only on localhost"},
			{"auth.oidc.redirect_url", "is not an absolute http or https URL"},
		}},
		{"oidc secrets", head + "  oidc: {issuer: 'https://id.example.com', client_id: occamy, redirect_url: 'https://o.example.com/cb', client_secret_file: DIR/missing, client_secret_env: OCCAMY_TEST_UNSET, scopes: [profile]}\n", []want{
			{"auth.oidc", "client_secret_file and client_secret_env are both set"},
			{"auth.oidc.client_secret_file", "does not exist"},
			{"auth.oidc.client_secret_env", "OCCAMY_TEST_UNSET is not set"},
			{"auth.oidc.scopes", "must include openid"},
		}},
		{"oidc return urls", head + `  oidc:
    issuer: https://id.example.com
    client_id: occamy
    redirect_url: https://o.example.com/cb
    return_urls: ['/app', 'http://o.example.com/', 'https://o.example.com/#/signed-in', 'https://o.example.com/#', 'https://me@o.example.com/']
`, []want{
			{"auth.oidc.return_urls[0]", "is not an absolute http or https URL"},
			{"auth.oidc.return_urls[1]", "needs https; http is accepted only on localhost"},
			{"auth.oidc.return_urls[2]", "has a fragment"},
			{"auth.oidc.return_urls[3]", "has a fragment"},
			{"auth.oidc.return_urls[4]", "has a user name in it"},
		}},
		{"oidc known users: groups without groups_claim", `version: 1
auth:
  signing_key_file: DIR/signing.key
  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://o.example.com/cb, require_known_users: true}
connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {groups: [admins]}}
`, []want{
			{"auth.oidc.require_known_users", "the file admits no one"},
		}},
		{"oidc known users: static users not linked", head + `  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://o.example.com/cb, require_known_users: true}
`, []want{
			{"auth.oidc.require_known_users", "the file admits no one"},
		}},
		// OIDC refuses a static user's name without link_static_users, so
		// an allow list that names only static users admits no one.
		{"oidc known users: only static users allowed", head + `  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://o.example.com/cb, require_known_users: true}
` + conn, []want{
			{"auth.oidc.require_known_users", "the file admits no one"},
		}},
		{"oidc known users: only static users' names in another case", head + `  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://o.example.com/cb, require_known_users: true}
connections:
  - {id: box, protocol: ssh, insecure_ignore_host_key: true, target: box.internal, credentials: {prompt: true}, allow: {users: [Alice, BOB]}}
`, []want{
			{"auth.oidc.require_known_users", "the file admits no one"},
		}},

		// guacd.
		{"guacd address", head + "guacd: {address: ':4822'}\n", []want{{"guacd.address", "has no host"}}},
		{"guacd params", head + `guacd:
  params:
    kubernetes: {namespace: default}
    spice: {port: "5930"}
    rdp: {password: hunter2, ignore-cert: "true", sftp-password: hunter2, gateway-password: hunter2, sftp-passphrase: hunter2, sftp-private-key: hunter2, client-secret: hunter2, Password: hunter2, SFTP-PASSWORD: hunter2}
    vnc: {hostname: x, port: "5901", Hostname: x}
    telnet: {password-regex: '[Pp]assword:', username-regex: 'login:'}
`, []want{
			{"guacd.params.kubernetes", "guacd does not serve kubernetes"},
			{"guacd.params.rdp.Password", "inline secrets are not allowed"},
			{"guacd.params.rdp.SFTP-PASSWORD", "inline secrets are not allowed"},
			{"guacd.params.rdp.client-secret", "inline secrets are not allowed"},
			{"guacd.params.rdp.gateway-password", "inline secrets are not allowed"},
			{"guacd.params.rdp.password", "inline secrets are not allowed"},
			{"guacd.params.rdp.sftp-passphrase", "inline secrets are not allowed"},
			{"guacd.params.rdp.sftp-password", "inline secrets are not allowed"},
			{"guacd.params.rdp.sftp-private-key", "inline secrets are not allowed"},
			{"guacd.params.spice", `unknown protocol "spice"`},
			{"guacd.params.spice.port", "set from each connection's target"},
			{"guacd.params.vnc.Hostname", "set from each connection's target"},
			{"guacd.params.vnc.hostname", "set from each connection's target"},
			{"guacd.params.vnc.port", "set from each connection's target"},
		}},

		// Connections.
		{"ids", head + `connections:
  - {id: box, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: box, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: Build Box, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: ../x, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: .hidden, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[1].id", `"box" is already the ID of connections[0]`},
			{"connections[2].id", "is not a connection ID"},
			{"connections[3].id", "is not a connection ID"},
			{"connections[4].id", "is not a connection ID"},
			{"connections[5].id", "required"},
		}},
		{"protocols", head + `connections:
  - {id: a, protocol: spice, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, target: b, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].protocol", `unknown protocol "spice": use ssh, vnc, rdp, telnet or kubernetes`},
			{"connections[1].protocol", "required"},
		}},
		{"targets", head + `connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: "ssh://a", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: ssh, insecure_ignore_host_key: true, target: "b:0", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: ssh, insecure_ignore_host_key: true, target: "c:ssh", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: ssh, insecure_ignore_host_key: true, target: "bad!host:22", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: ssh, insecure_ignore_host_key: true, target: "e:", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: ssh, insecure_ignore_host_key: true, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].target", "is not host:port"},
			{"connections[1].target", "invalid port"},
			{"connections[2].target", "invalid port"},
			{"connections[3].target", "invalid host"},
			{"connections[4].target", "invalid port"},
			{"connections[5].target", "required"},
		}},
		{"credential sources", head + `connections:
  - {id: a, protocol: vnc, target: a, allow: {users: [alice]}}
  - {id: b, protocol: vnc, target: b, credentials: {password_file: DIR/password, prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].credentials", "no credential source: set one of password_file, password_env, private_key_file or prompt: true"},
			{"connections[1].credentials", "more than one credential source (password_file, prompt): set exactly one"},
		}},
		{"credential references", head + `connections:
  - {id: a, protocol: vnc, target: a, credentials: {password_file: DIR/missing}, allow: {users: [alice]}}
  - {id: b, protocol: vnc, target: b, credentials: {password_env: OCCAMY_TEST_UNSET}, allow: {users: [alice]}}
  - {id: c, protocol: vnc, target: c, credentials: {password_env: OCCAMY_TEST_EMPTY}, allow: {users: [alice]}}
  - {id: d, protocol: vnc, target: d, credentials: {password_env: 1BAD}, allow: {users: [alice]}}
  - {id: e, protocol: vnc, target: e, credentials: {private_key_file: DIR/key}, allow: {users: [alice]}}
  - {id: f, protocol: ssh, insecure_ignore_host_key: true, target: f, credentials: {password_env: OCCAMY_TEST_PASSWORD}, allow: {users: [alice]}}
`, []want{
			{"connections[0].credentials.password_file", "missing does not exist"},
			{"connections[1].credentials.password_env", "OCCAMY_TEST_UNSET is not set"},
			{"connections[2].credentials.password_env", "OCCAMY_TEST_EMPTY is empty"},
			{"connections[3].credentials.password_env", `"1BAD" is not an environment variable name`},
			{"connections[4].credentials.private_key_file", "only ssh connections use a private key"},
			{"connections[5].credentials.username", "required for ssh unless prompt is true"},
		}},
		{"host keys", head + `connections:
  - {id: a, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: ssh, target: b, host_keys: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"], insecure_ignore_host_key: true, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: ssh, target: c, host_keys: ["SHA256:short", "ssh-ed25519 AAAA"], credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: vnc, target: d, host_keys: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"], insecure_ignore_host_key: true, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].host_keys", "required for ssh"},
			{"connections[1].insecure_ignore_host_key", "set host_keys or insecure_ignore_host_key, not both"},
			{"connections[2].host_keys[0]", "host key is not a SHA-256 fingerprint"},
			{"connections[2].host_keys[1]", "host key is not a public key"},
			{"connections[3].host_keys", "only ssh connections have host keys"},
			{"connections[3].insecure_ignore_host_key", "only ssh connections have host keys"},
		}},
		{"vnc tls", head + `connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, tls: {require: true}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: vnc, backend: native, target: b, tls: {ca_file: DIR/ca.pem, pins: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: vnc, backend: native, target: c, tls: {ca_file: DIR/password}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: vnc, backend: native, target: d, tls: {ca_file: DIR/missing.pem}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: vnc, backend: native, target: e, tls: {pins: ["SHA256:short", "zz"]}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: vnc, backend: native, target: f, tls: {require: true, server_name: nas, ca_file: DIR/ca.pem}, credentials: {username: alice, prompt: true}, allow: {users: [alice]}}
  - {id: g, protocol: vnc, backend: native, target: g, tls: {pins: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"], insecure_skip_verify: true}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: h, protocol: vnc, backend: native, target: h, tls: {ca_file: DIR/ca.pem, insecure_skip_verify: true}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: i, protocol: vnc, backend: native, target: i, tls: {insecure_skip_verify: true}, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].tls", "only vnc and kubernetes connections have tls"},
			{"connections[1].tls.pins", "set ca_file or pins, not both"},
			{"connections[2].tls.ca_file", "holds no PEM certificate"},
			{"connections[3].tls.ca_file", "missing.pem does not exist"},
			{"connections[4].tls.pins[0]", "not a certificate or a SHA-256 fingerprint"},
			{"connections[4].tls.pins[1]", "not a certificate or a SHA-256 fingerprint"},
			{"connections[6].tls.insecure_skip_verify", "set ca_file or pins, or insecure_skip_verify, not both"},
			{"connections[7].tls.insecure_skip_verify", "set ca_file or pins, or insecure_skip_verify, not both"},
		}},
		{"kubernetes", head + `connections:
  - {id: a, protocol: kubernetes, backend: guacd, target: a, kubernetes: {pod: p}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: kubernetes, target: b, kubernetes: {namespace: web}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: kubernetes, target: c, kubernetes: {namespace: Web, pod: "../../nodes/x/proxy", container: "a/b"}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: kubernetes, target: d, kubernetes: {pod: p, attach: true, command: [sh]}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: kubernetes, target: e, kubernetes: {pod: p, command: ["", "-c", "id"]}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: ssh, insecure_ignore_host_key: true, target: f, kubernetes: {pod: p}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: g, protocol: kubernetes, target: "https://k8s.example.com:6443", kubernetes: {pod: p}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: h, protocol: kubernetes, target: h, kubernetes: {pod: "web-0.", namespace: "-web", container: "x123456789x123456789x123456789x123456789x123456789x123456789xxxx"}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: i, protocol: kubernetes, target: i, host_keys: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"], ard: true, kubernetes: {pod: p}, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].backend", "kubernetes connections always use occamyd's native Kubernetes backend"},
			{"connections[1].kubernetes.pod", "required"},
			{"connections[2].kubernetes.namespace", `"Web" is not a namespace`},
			{"connections[2].kubernetes.pod", `"../../nodes/x/proxy" is not a pod name`},
			{"connections[2].kubernetes.container", `"a/b" is not a container name`},
			{"connections[3].kubernetes.command", "set attach or command, not both"},
			{"connections[4].kubernetes.command[0]", "must not be empty"},
			{"connections[5].kubernetes", "only kubernetes connections have kubernetes"},
			{"connections[6].target", "write the API server as host:port"},
			{"connections[7].kubernetes.namespace", `"-web" is not a namespace`},
			{"connections[7].kubernetes.pod", `"web-0." is not a pod name`},
			{"connections[7].kubernetes.container", "is longer than 63 characters"},
			{"connections[8].host_keys", "only ssh connections have host keys"},
			{"connections[8].ard", "only vnc connections have ard"},
		}},
		{"kubernetes credentials", head + `connections:
  - {id: a, protocol: kubernetes, target: a, kubernetes: {pod: p}, allow: {users: [alice]}}
  - {id: b, protocol: kubernetes, target: b, kubernetes: {pod: p}, credentials: {username: u, password_file: DIR/password}, allow: {users: [alice]}}
  - {id: c, protocol: kubernetes, target: c, kubernetes: {pod: p}, credentials: {client_cert_file: DIR/tls.crt}, allow: {users: [alice]}}
  - {id: d, protocol: kubernetes, target: d, kubernetes: {pod: p}, credentials: {client_key_file: DIR/tls.key}, allow: {users: [alice]}}
  - {id: e, protocol: kubernetes, target: e, kubernetes: {pod: p}, credentials: {token_file: DIR/secret, prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: kubernetes, target: f, kubernetes: {pod: p}, credentials: {token_env: OCCAMY_TEST_MISSING}, allow: {users: [alice]}}
  - {id: g, protocol: kubernetes, target: g, kubernetes: {pod: p}, credentials: {token_file: DIR/missing}, allow: {users: [alice]}}
  - {id: h, protocol: kubernetes, target: h, kubernetes: {pod: p}, credentials: {private_key_file: DIR/key}, allow: {users: [alice]}}
  - {id: i, protocol: vnc, target: i, credentials: {token_env: OCCAMY_TEST_PASSWORD}, allow: {users: [alice]}}
  - {id: j, protocol: ssh, insecure_ignore_host_key: true, target: j, credentials: {username: u, client_cert_file: DIR/tls.crt, client_key_file: DIR/tls.key}, allow: {users: [alice]}}
  - {id: k, protocol: kubernetes, target: k, kubernetes: {pod: p}, credentials: {token: hunter2}, allow: {users: [alice]}}
`, []want{
			{"connections[10].credentials.token", "inline secrets are not allowed, so that the file can be committed: use token_file, token_env or prompt: true"},
			{"connections[0].credentials", "no credential source: set one of token_file, token_env, client_cert_file with client_key_file, or prompt: true"},
			{"connections[1].credentials.username", "kubernetes connections authenticate with a bearer token"},
			{"connections[1].credentials.password_file", "kubernetes connections authenticate with a bearer token"},
			{"connections[2].credentials.client_key_file", "required with client_cert_file"},
			{"connections[3].credentials.client_cert_file", "required with client_key_file"},
			{"connections[4].credentials", "more than one credential source (token_file, prompt)"},
			{"connections[5].credentials.token_env", "OCCAMY_TEST_MISSING"},
			{"connections[6].credentials.token_file", "does not exist"},
			{"connections[7].credentials.private_key_file", "only ssh connections use a private key"},
			{"connections[8].credentials.token_env", "only kubernetes connections use a bearer token or a client certificate"},
			{"connections[9].credentials.client_cert_file", "only kubernetes connections use a bearer token or a client certificate"},
			{"connections[9].credentials.client_key_file", "only kubernetes connections use a bearer token or a client certificate"},
			{"connections[10].credentials", "no credential source"},
		}},
		{"kubernetes tls", head + `connections:
  - {id: a, protocol: kubernetes, target: a, kubernetes: {pod: p}, tls: {ca_file: DIR/ca.pem, pins: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: kubernetes, target: b, kubernetes: {pod: p}, tls: {pins: ["SHA256:short"]}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: kubernetes, target: c, kubernetes: {pod: p}, tls: {pins: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"], insecure_skip_verify: true}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: kubernetes, target: d, kubernetes: {pod: p}, tls: {ca_file: DIR/password}, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].tls.pins", "set ca_file or pins, not both"},
			{"connections[1].tls.pins[0]", "not a certificate or a SHA-256 fingerprint"},
			{"connections[2].tls.insecure_skip_verify", "set ca_file or pins, or insecure_skip_verify, not both"},
			{"connections[3].tls.ca_file", "holds no PEM certificate"},
		}},
		{"backend", head + `connections:
  - {id: a, protocol: ssh, backend: guacd, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: rdp, backend: native, target: b, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: telnet, backend: native, target: c, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: vnc, backend: nativ, target: d, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: vnc, target: e, tls: {require: true}, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: vnc, backend: guacd, target: f, tls: {insecure_skip_verify: true}, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].backend", "ssh connections always use occamyd's native SSH backend"},
			{"connections[1].backend", "the native RDP backend is not available yet"},
			{"connections[2].backend", "telnet has no native backend"},
			{"connections[3].backend", `unknown backend "nativ": use guacd or native`},
			{"connections[4].tls", "set backend: native"},
			{"connections[5].tls", "set backend: native"},
		}},
		{"ard", head + `connections:
  - {id: a, protocol: ssh, ard: true, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: vnc, ard: true, target: b, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: vnc, backend: native, ard: true, tls: {require: true}, target: c, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: vnc, backend: native, ard: true, tls: {pins: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]}, target: d, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: vnc, backend: native, ard: true, tls: {ca_file: DIR/ca.pem}, target: e, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: vnc, backend: native, ard: true, tls: {insecure_skip_verify: true, server_name: mac}, target: f, credentials: {username: alice, prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].ard", "only vnc connections have ard"},
			{"connections[1].ard", "set backend: native"},
			{"connections[2].ard", "cannot be allowed while tls requires TLS"},
			{"connections[3].ard", "cannot be allowed while tls requires TLS"},
			{"connections[4].ard", "cannot be allowed while tls requires TLS"},
		}},
		{"adhoc backends", head + `adhoc_backends: {ssh: guacd, vnc: native, rdp: native, telnet: guacd, kubernetes: guacd, x: y}
`, []want{
			{"adhoc_backends.kubernetes", "kubernetes has no ad-hoc targets"},
			{"adhoc_backends.rdp", "the native RDP backend is not available yet"},
			{"adhoc_backends.ssh", "ssh connections always use occamyd's native SSH backend"},
			{"adhoc_backends.x", `unknown protocol "x"`},
		}},
		{"allow", head + `connections:
  - {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}}
  - {id: b, protocol: ssh, insecure_ignore_host_key: true, target: b, credentials: {prompt: true}, allow: {users: [carol, alice], groups: [admins, dev, ""]}}
`, []want{
			{"connections[0].allow", "names no users or groups"},
			{"connections[1].allow.users[0]", `no static user is named "carol"`},
			{"connections[1].allow.groups[0]", `no static user is in group "admins"`},
			{"connections[1].allow.groups[2]", "must not be empty"},
		}},

		// Limits.
		{"negative limits", head + "limits: {sessions: -4, sessions_per_user: -1, viewers_per_session: -2, shares_per_session: -3}\n", []want{
			{"limits.sessions", "must be at least 1"},
			{"limits.sessions_per_user", "must be at least 1"},
			{"limits.viewers_per_session", "must be at least 1"},
			{"limits.shares_per_session", "must be at least 1"},
		}},
		{"short viewer timeout", head + "streams: {viewer_timeout: 500ms}\n", []want{{"streams.viewer_timeout", "500ms is less than 1s"}}},
		{"negative viewer timeout", head + "streams: {viewer_timeout: -1s}\n", []want{{"streams.viewer_timeout", "-1s is less than 1s"}}},

		// Ad-hoc targets.
		{"adhoc without allowlist", head + "allow_adhoc_targets: true\n", []want{{"allow_adhoc_targets", "needs adhoc_allowlist"}}},
		{"adhoc allowlist", head + "adhoc_allowlist: [10.0.0.0/33, '*.', 'http://x', '*', '*.10.0.0.1', '10.0.0.0/8:0', 'a.example:22-21', 'a.example:', '[10.0.0.1]:22', '[fd00::1]22', 'fe80::1%en0', 'a.example:x']\n", []want{
			{"adhoc_allowlist[0]", "is not a CIDR range"},
			{"adhoc_allowlist[1]", "is not a CIDR range"},
			{"adhoc_allowlist[2]", "is not a port"},
			{"adhoc_allowlist[3]", "is not a CIDR range"},
			{"adhoc_allowlist[4]", "is not a CIDR range"},
			{"adhoc_allowlist[5]", "is not a port"},
			{"adhoc_allowlist[6]", "is not a port"},
			{"adhoc_allowlist[7]", "followed by no port"},
			{"adhoc_allowlist[8]", "only an IPv6 range or address goes in brackets"},
			{"adhoc_allowlist[9]", "brackets are followed by :port"},
			{"adhoc_allowlist[10]", "is not a CIDR range"},
			{"adhoc_allowlist[11]", "is not a port"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parse(t, tt.raw)
			var problems config.Problems
			if !errors.As(err, &problems) {
				t.Fatalf("ParseV1() = %+v, %v; want problems", f, err)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error repeats a secret: %v", err)
			}
			if len(problems) != len(tt.want) {
				t.Fatalf("got %d problems, want %d:\n%v", len(problems), len(tt.want), err)
			}
			for i, w := range tt.want {
				p := problems[i]
				if p.Path != w.path || !strings.Contains(p.Message, w.msg) {
					t.Errorf("problem %d = %s: %s; want %s: ...%s...", i, p.Path, p.Message, w.path, w.msg)
				}
			}
		})
	}
}

// TestParseV1Lines checks that problems name their lines, including a
// problem about a key that is not in the file.
func TestParseV1Lines(t *testing.T) {
	_, err := parse(t, head+`conections: []
connections:
  - id: a
    protocol: vnc
    target: a
    allow:
      users: [carol]
`)
	var problems config.Problems
	if !errors.As(err, &problems) {
		t.Fatalf("ParseV1() error = %v, want problems", err)
	}
	want := []struct {
		line int
		path string
	}{
		{7, "conections"},
		{9, "connections[0].credentials"}, // not in the file: the line of connections[0]
		{13, "connections[0].allow.users[0]"},
	}
	if len(problems) != len(want) {
		t.Fatalf("got %d problems, want %d:\n%v", len(problems), len(want), err)
	}
	for i, w := range want {
		if problems[i].Line != w.line || problems[i].Path != w.path {
			t.Errorf("problem %d at line %d, %s; want line %d, %s", i, problems[i].Line, problems[i].Path, w.line, w.path)
		}
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "config: 3 problems:\n\tline 7: conections: unknown key, did you mean connections?\n") {
		t.Errorf("error = %q", msg)
	}
}

// TestParseV1MergeLines checks that a key which overrides a merged key
// is reported on its own line.
func TestParseV1MergeLines(t *testing.T) {
	_, err := parse(t, head+`connections:
  - &base {id: a, protocol: ssh, insecure_ignore_host_key: true, target: a, credentials: {prompt: true}, allow: {groups: [ops]}}
  - <<: *base
    id: B
`)
	var problems config.Problems
	if !errors.As(err, &problems) || len(problems) != 1 {
		t.Fatalf("ParseV1() error = %v, want one problem", err)
	}
	if p := problems[0]; p.Line != 10 || p.Path != "connections[1].id" {
		t.Errorf("problem at line %d, %s; want line 10, connections[1].id", p.Line, p.Path)
	}
}

func TestParseV1Unreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	dir := fixture(t)
	if err := os.Chmod(filepath.Join(dir, "password"), 0); err != nil {
		t.Fatal(err)
	}
	raw := head + `connections:
  - {id: a, protocol: vnc, target: a, credentials: {password_file: DIR/password}, allow: {users: [alice]}}
`
	_, err := config.ParseV1([]byte(strings.ReplaceAll(raw, "DIR", dir)))
	var problems config.Problems
	if !errors.As(err, &problems) || len(problems) != 1 || !strings.Contains(problems[0].Message, "is not readable") {
		t.Fatalf("ParseV1() error = %v, want password_file not readable", err)
	}
}

func TestNormalizeTarget(t *testing.T) {
	for _, tt := range []struct{ protocol, target, want, err string }{
		{"ssh", "box.internal", "box.internal:22", ""},
		{"vnc", "10.0.0.5", "10.0.0.5:5900", ""},
		{"rdp", "[fd00::1]", "[fd00::1]:3389", ""},
		{"telnet", "host:2323", "host:2323", ""},
		{"ssh", "user@box", "", "is not host:port"},
		{"ssh", "box:0", "", "invalid port"},
		{"kubernetes", "k8s.internal", "k8s.internal:443", ""},
		{"kubernetes", "https://k8s.internal", "", "is not host:port"},
		{"spice", "box", "", `unknown protocol "spice"`},
	} {
		got, err := config.NormalizeTarget(tt.protocol, tt.target)
		if got != tt.want || (err == nil) != (tt.err == "") || err != nil && !strings.Contains(err.Error(), tt.err) {
			t.Errorf("NormalizeTarget(%q, %q) = %q, %v; want %q, %q", tt.protocol, tt.target, got, err, tt.want, tt.err)
		}
	}
}

func TestSecrets(t *testing.T) {
	dir := fixture(t)
	file := filepath.Join(dir, "password")
	if err := os.WriteFile(file, []byte("pass word\r\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := (config.Credentials{PasswordFile: file}).Password(); err != nil || got != "pass word" {
		t.Errorf("Password() from a file = %q, %v; want %q", got, err, "pass word")
	}
	if got, err := (config.Credentials{PasswordEnv: "OCCAMY_TEST_PASSWORD"}).Password(); err != nil || got != "s3cret" {
		t.Errorf("Password() from the environment = %q, %v; want s3cret", got, err)
	}
	if _, err := (config.Credentials{PasswordEnv: "OCCAMY_TEST_UNSET"}).Password(); err == nil {
		t.Error("Password() from an unset variable did not fail")
	}
	if _, err := (config.Credentials{Prompt: true}).Password(); !errors.Is(err, config.ErrNoSecret) {
		t.Errorf("Password() with prompt = %v, want ErrNoSecret", err)
	}
	key := filepath.Join(dir, "key")
	if got, err := (config.Credentials{PrivateKeyFile: key}).PrivateKey(); err != nil || string(got) != "s3cret\n" {
		t.Errorf("PrivateKey() = %q, %v", got, err)
	}
	if _, err := (config.Credentials{PasswordFile: file}).PrivateKey(); !errors.Is(err, config.ErrNoSecret) {
		t.Errorf("PrivateKey() without a key = %v, want ErrNoSecret", err)
	}
	if got, err := (config.OIDC{ClientSecretFile: key}).ClientSecret(); err != nil || got != "s3cret" {
		t.Errorf("ClientSecret() = %q, %v; want s3cret", got, err)
	}
	if _, err := (config.OIDC{}).ClientSecret(); !errors.Is(err, config.ErrNoSecret) {
		t.Errorf("ClientSecret() for a public client = %v, want ErrNoSecret", err)
	}
	if got, err := (config.Credentials{TokenFile: key}).Token(); err != nil || got != "s3cret" {
		t.Errorf("Token() from a file = %q, %v; want s3cret", got, err)
	}
	if got, err := (config.Credentials{TokenEnv: "OCCAMY_TEST_PASSWORD"}).Token(); err != nil || got != "s3cret" {
		t.Errorf("Token() from the environment = %q, %v; want s3cret", got, err)
	}
	if _, err := (config.Credentials{ClientCertFile: key, ClientKeyFile: key}).Token(); !errors.Is(err, config.ErrNoSecret) {
		t.Errorf("Token() with a client certificate = %v, want ErrNoSecret", err)
	}
}

func TestAdhocProtocol(t *testing.T) {
	for _, tt := range []struct {
		protocol string
		want     bool
	}{
		{"ssh", true}, {"vnc", true}, {"rdp", true}, {"telnet", true},
		{"kubernetes", false}, {"spice", false}, {"", false},
	} {
		if got := config.AdhocProtocol(tt.protocol); got != tt.want {
			t.Errorf("AdhocProtocol(%q) = %v, want %v", tt.protocol, got, tt.want)
		}
	}
}
