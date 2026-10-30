// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

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
	want := &config.File{
		Version: 1,
		Listen:  "0.0.0.0:5636",
		TLS:     config.TLS{Cert: path("etc/occamy/tls.crt"), Key: path("etc/occamy/tls.key")},
		Debug:   config.Debug{Address: "localhost:6060"},
		Origins: []string{"https://occamy.example.com"},
		Auth: config.Auth{
			SigningKeyFile:      path("var/lib/occamy/signing.key"),
			AccessTokenLifetime: 15 * time.Minute,
			TrustedIssuers:      []config.TrustedIssuer{{Issuer: "gateway", PublicKeyFile: path("etc/occamy/gateway.pub")}},
			Users: []config.User{
				{Name: "alice", PasswordHash: "$2a$12$R6bvkO3lC3JV/OaHnySvquk.wobMPB3XleIdoBgEwlhjbcfaZi0km", Groups: []string{"ops"}},
				{Name: "bob", PasswordHash: "$2a$10$Ad6WcgC.YRhS7d8JbV/9..v/X5C7aAnBg5cVXdoz1F1d0uHmkmuH2", Groups: []string{"dev"}},
			},
			OIDC: &config.OIDC{
				Issuer:           "https://accounts.example.com",
				ClientID:         "occamy",
				ClientSecretFile: path("run/secrets/oidc-client-secret"),
				RedirectURL:      "https://occamy.example.com/api/v1/auth/oidc/callback",
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
				ID: "build-box", Name: "Build box", Protocol: "ssh", Target: "build.internal:22",
				Credentials: config.Credentials{Username: "deploy", PrivateKeyFile: path("etc/occamy/keys/deploy")},
				Allow:       config.Allow{Users: []string{"alice"}, Groups: []string{"ops"}},
			},
			{
				ID: "nas-desktop", Name: "NAS desktop", Protocol: "vnc", Target: "nas.internal:5900",
				Credentials: config.Credentials{PasswordEnv: "OCCAMY_NAS_VNC_PASSWORD"},
				Allow:       config.Allow{Groups: []string{"ops", "dev"}},
			},
			{
				ID: "windows", Name: "Windows workstation", Protocol: "rdp", Target: "10.0.4.20:3389",
				Credentials: config.Credentials{Username: "Administrator", PasswordFile: path("run/secrets/windows-password")},
				Allow:       config.Allow{Groups: []string{"ops"}},
			},
			{
				ID: "lab-shell", Name: "Lab shell", Protocol: "ssh", Target: "[fd00::22]:22",
				Credentials: config.Credentials{Prompt: true},
				Allow:       config.Allow{Groups: []string{"dev"}},
			},
		},
		AdhocAllowlist: []string{"10.0.0.0/8", "*.lab.internal"},
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
  - {id: box, protocol: ssh, target: box.internal, credentials: {username: deploy, private_key_file: DIR/key}, allow: {users: [alice]}}
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
	c := f.Connections[0]
	if c.Name != "box" || c.Target != "box.internal:22" {
		t.Errorf("connection name %q, target %q; want box, box.internal:22", c.Name, c.Target)
	}
	if f.LegacyV0 || f.AllowAdhocTargets || f.TLS != (config.TLS{}) || f.Debug.Address != "" {
		t.Errorf("optional features on by default: %+v", f)
	}

	f, err = parse(t, head+"  oidc: {issuer: https://id.example.com, client_id: occamy, redirect_url: https://occamy.example.com/cb}\n")
	if err != nil {
		t.Fatal(err)
	}
	o := f.Auth.OIDC
	if !reflect.DeepEqual(o.Scopes, config.DefaultOIDCScopes) || o.SubjectClaim != config.DefaultOIDCSubjectClaim || o.GroupsClaim != "" {
		t.Errorf("oidc defaults: scopes %v, subject claim %q, groups claim %q", o.Scopes, o.SubjectClaim, o.GroupsClaim)
	}
}

// TestParseV1Valid checks files that are valid in less common ways.
func TestParseV1Valid(t *testing.T) {
	tests := []struct {
		name, raw string
	}{
		{"targets", head + `connections:
  - {id: a, protocol: ssh, target: "[::1]:2222", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: vnc, target: "::1", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: rdp, target: 192.0.2.1, credentials: {password_env: OCCAMY_TEST_PASSWORD}, allow: {users: [alice]}}
  - {id: d.e_f-1, protocol: telnet, target: host_1.example.com., credentials: {password_file: DIR/password}, allow: {users: [alice]}}
`},
		{"names from oidc", head + `  oidc: {issuer: "http://localhost:8080/realms/x", client_id: occamy, redirect_url: "http://127.0.0.1:5636/cb", client_secret_env: OCCAMY_TEST_PASSWORD}
connections:
  - {id: a, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [carol@example.com], groups: [admins]}}
`},
		{"names from a trusted issuer", head + `  trusted_issuers: [{issuer: gateway, public_key_file: DIR/gateway.pub}]
connections:
  - {id: a, protocol: ssh, target: a, credentials: {prompt: true}, allow: {groups: [admins]}}
`},
		{"anchors", head + `connections:
  - &base {id: a, protocol: ssh, target: a, credentials: {prompt: true}, allow: {groups: [ops]}}
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
		{"debug on listen", head + "debug: {address: ':5636'}\n", []want{{"debug.address", "is the listen address"}}},
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
`, []want{
			{"auth.trusted_issuers[1].issuer", `"gateway" is already auth.trusted_issuers[0]`},
			{"auth.trusted_issuers[1].public_key_file", "does not exist"},
			{"auth.trusted_issuers[2].issuer", "must not be empty"},
			{"auth.trusted_issuers[3].public_key_file", "required"},
			{"auth.trusted_issuers[4].public_key_file", "want a PEM block of type PUBLIC KEY"},
		}},
		{"users", "version: 1\nauth:\n  signing_key_file: DIR/signing.key\n  users:\n" +
			`    - {name: alice, password_hash: "` + hash + `", groups: [ops, ""]}
    - {name: alice, password_hash: "` + hash + `"}
    - {name: "carol smith", password_hash: plaintext}
    - {name: dave, password_hash: "` + string(lowCost) + `"}
    - {name: erin, password_hash: "$2a$99$Ad6WcgC.YRhS7d8JbV/9..v/X5C7aAnBg5cVXdoz1F1d0uHmkmuH2"}
`, []want{
			{"auth.users[0].groups[1]", "must not be empty"},
			{"auth.users[1].name", `"alice" is already auth.users[0]`},
			{"auth.users[2].name", "has spaces or control characters"},
			{"auth.users[2].password_hash", "not a bcrypt hash"},
			{"auth.users[3].password_hash", "bcrypt cost 4 is below 10"},
			{"auth.users[4].password_hash", "not a bcrypt hash: crypto/bcrypt: cost 99"},
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

		// guacd.
		{"guacd address", head + "guacd: {address: ':4822'}\n", []want{{"guacd.address", "has no host"}}},
		{"guacd params", head + `guacd:
  params:
    kubernetes: {namespace: default}
    rdp: {password: hunter2, ignore-cert: "true"}
    vnc: {hostname: x, port: "5901"}
`, []want{
			{"guacd.params.kubernetes", `unknown protocol "kubernetes"`},
			{"guacd.params.rdp.password", "inline secrets are not allowed"},
			{"guacd.params.vnc.hostname", "set from each connection's target"},
			{"guacd.params.vnc.port", "set from each connection's target"},
		}},

		// Connections.
		{"ids", head + `connections:
  - {id: box, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: box, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: Build Box, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: ../x, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: .hidden, protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {protocol: ssh, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[1].id", `"box" is already the ID of connections[0]`},
			{"connections[2].id", "is not a connection ID"},
			{"connections[3].id", "is not a connection ID"},
			{"connections[4].id", "is not a connection ID"},
			{"connections[5].id", "required"},
		}},
		{"protocols", head + `connections:
  - {id: a, protocol: kubernetes, target: a, credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, target: b, credentials: {prompt: true}, allow: {users: [alice]}}
`, []want{
			{"connections[0].protocol", `unknown protocol "kubernetes": use ssh, vnc, rdp or telnet`},
			{"connections[1].protocol", "required"},
		}},
		{"targets", head + `connections:
  - {id: a, protocol: ssh, target: "ssh://a", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: b, protocol: ssh, target: "b:0", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: c, protocol: ssh, target: "c:ssh", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: d, protocol: ssh, target: "bad!host:22", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: e, protocol: ssh, target: "e:", credentials: {prompt: true}, allow: {users: [alice]}}
  - {id: f, protocol: ssh, credentials: {prompt: true}, allow: {users: [alice]}}
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
  - {id: f, protocol: ssh, target: f, credentials: {password_env: OCCAMY_TEST_PASSWORD}, allow: {users: [alice]}}
`, []want{
			{"connections[0].credentials.password_file", "missing does not exist"},
			{"connections[1].credentials.password_env", "OCCAMY_TEST_UNSET is not set"},
			{"connections[2].credentials.password_env", "OCCAMY_TEST_EMPTY is empty"},
			{"connections[3].credentials.password_env", `"1BAD" is not an environment variable name`},
			{"connections[4].credentials.private_key_file", "only ssh connections use a private key"},
			{"connections[5].credentials.username", "required for ssh unless prompt is true"},
		}},
		{"allow", head + `connections:
  - {id: a, protocol: ssh, target: a, credentials: {prompt: true}}
  - {id: b, protocol: ssh, target: b, credentials: {prompt: true}, allow: {users: [carol, alice], groups: [admins, dev, ""]}}
`, []want{
			{"connections[0].allow", "names no users or groups"},
			{"connections[1].allow.users[0]", `no static user is named "carol"`},
			{"connections[1].allow.groups[0]", `no static user is in group "admins"`},
			{"connections[1].allow.groups[2]", "must not be empty"},
		}},

		// Ad-hoc targets.
		{"adhoc without allowlist", head + "allow_adhoc_targets: true\n", []want{{"allow_adhoc_targets", "needs adhoc_allowlist"}}},
		{"adhoc allowlist", head + "adhoc_allowlist: [10.0.0.0/33, '*.', 'http://x', '*', '*.10.0.0.1']\n", []want{
			{"adhoc_allowlist[0]", "is not a CIDR range"},
			{"adhoc_allowlist[1]", "is not a CIDR range"},
			{"adhoc_allowlist[2]", "is not a CIDR range"},
			{"adhoc_allowlist[3]", "is not a CIDR range"},
			{"adhoc_allowlist[4]", "is not a CIDR range"},
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
}
