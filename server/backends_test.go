// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/rdp"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/config"
	rdpproto "changkun.de/x/occamy/internal/rdp"
)

// fakeHostKey is a host key fingerprint in the form ssh-keygen -l prints.
const fakeHostKey = "SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"

// TestBackends checks that each protocol that occamyd serves has a
// backend of its own, named after it, and that no other has one.
func TestBackends(t *testing.T) {
	b := newBackends(nil)
	got := map[string]string{}
	for p, m := range b {
		got[p] = m.name
	}
	want := map[string]string{"ssh": "ssh", "vnc": "vnc", "rdp": "rdp", "kubernetes": "kubernetes"}
	if !maps.Equal(got, want) {
		t.Errorf("backends %v, want %v", got, want)
	}
}

func TestNewSSH(t *testing.T) {
	t.Setenv("OCCAMY_TEST_SSH_PASSWORD", "pw")
	tg := target{
		protocol: "ssh", addr: "127.0.0.1:22", hostKeys: []string{fakeHostKey},
		creds: config.Credentials{Username: "bob", PasswordEnv: "OCCAMY_TEST_SSH_PASSWORD"},
	}
	if b, err := newSSH(tg, screen{cols: 100, rows: 30}); err != nil {
		t.Fatal(err)
	} else if _, ok := b.(*ssh.Backend); !ok {
		t.Errorf("%T, want *ssh.Backend", b)
	}

	tg.creds = config.Credentials{Prompt: true}
	b, err := newSSH(tg, screen{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*askUser); !ok {
		t.Errorf("%T without a user name, want *askUser", b)
	}

	for name, bad := range map[string]target{
		"no host key policy":          {addr: "127.0.0.1:22", creds: config.Credentials{Username: "bob", Prompt: true}},
		"no host key policy, no user": {addr: "127.0.0.1:22", creds: config.Credentials{Prompt: true}},
		"unset password":              {addr: "127.0.0.1:22", insecureIgnoreHostKey: true, creds: config.Credentials{Username: "bob", PasswordEnv: "OCCAMY_TEST_UNSET"}},
		"missing key file":            {addr: "127.0.0.1:22", insecureIgnoreHostKey: true, creds: config.Credentials{Username: "bob", PrivateKeyFile: t.TempDir() + "/nope"}},
	} {
		if _, err := newSSH(bad, screen{}); err == nil {
			t.Errorf("%s: made a backend", name)
		}
	}
}

func TestNewVNC(t *testing.T) {
	t.Setenv("OCCAMY_TEST_VNC_PASSWORD", "pw")
	b, err := newVNC(target{addr: "127.0.0.1:5900", creds: config.Credentials{PasswordEnv: "OCCAMY_TEST_VNC_PASSWORD"}}, screen{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*vnc.Backend); !ok {
		t.Errorf("%T, want *vnc.Backend", b)
	}
	if _, err := newVNC(target{addr: "127.0.0.1:5900", creds: config.Credentials{PasswordEnv: "OCCAMY_TEST_UNSET"}}, screen{}); err == nil {
		t.Error("an unset password variable made a backend")
	}
	// The hello's size, both sides or neither.
	for _, s := range []screen{{width: 800, height: 600}, {width: 800}, {height: 600}, {cols: 80, rows: 24}} {
		if _, err := newVNC(target{addr: "127.0.0.1:5900", creds: config.Credentials{Prompt: true}}, s); err != nil {
			t.Errorf("screen %+v: %v", s, err)
		}
	}

	// ard, with the key that allows it named for the operator.
	cfg, err := vncConfig(target{connection: "mac", addr: "10.0.0.6:5900", ard: true, creds: config.Credentials{Username: "studio", Prompt: true}})
	if err != nil || !cfg.ARD || cfg.ARDSetting != `ard: true on connection "mac"` || cfg.Username != "studio" {
		t.Errorf("ard: %+v %v", cfg, err)
	}
	if cfg, _ := vncConfig(target{addr: "10.0.0.6:5900"}); cfg.ARD || !strings.Contains(cfg.ARDSetting, "ad-hoc targets have no ard") {
		t.Errorf("ad-hoc ard: %v %q", cfg.ARD, cfg.ARDSetting)
	}

	// The connection's username and certificate policy reach the
	// backend, with the CA file read.
	ca := writeCA(t)
	tls := config.ConnectionTLS{Require: true, ServerName: "nas", CAFile: ca, InsecureSkipVerify: true}
	cfg, err = vncConfig(target{addr: "10.0.0.5:5900", creds: config.Credentials{Username: "alice", Prompt: true}, tls: tls})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "alice" || cfg.Password != "" || !cfg.TLS.Require || cfg.TLS.ServerName != "nas" || cfg.TLS.RootCAs == nil || !cfg.TLS.InsecureSkipVerify {
		t.Errorf("vncConfig = %+v", cfg)
	}
	pins := []string{"SHA256:" + strings.Repeat("A", 43)}
	cfg, err = vncConfig(target{addr: "10.0.0.5:5900", creds: config.Credentials{Prompt: true}, tls: config.ConnectionTLS{Pins: pins}})
	if err != nil || !slices.Equal(cfg.TLS.Pins, pins) || cfg.TLS.RootCAs != nil || cfg.TLS.ServerName != "" {
		t.Errorf("pins: %+v %v", cfg.TLS, err)
	}
	// An ad-hoc target's certificate names the name it was opened by,
	// not the address it resolved to.
	cfg, err = vncConfig(target{addr: "10.0.0.5:5900", name: "nas.lab.internal", creds: config.Credentials{Prompt: true}})
	if err != nil || cfg.TLS.ServerName != "nas.lab.internal" {
		t.Errorf("ad-hoc: server name %q, %v", cfg.TLS.ServerName, err)
	}
	notPEM := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(notPEM, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tls := range map[string]config.ConnectionTLS{
		"missing CA file":    {CAFile: filepath.Join(t.TempDir(), "nope")},
		"CA file not PEM":    {CAFile: notPEM},
		"pins and a CA file": {CAFile: ca, Pins: pins},
	} {
		if _, err := newVNC(target{addr: "10.0.0.5:5900", creds: config.Credentials{Prompt: true}, tls: tls}, screen{}); err == nil {
			t.Errorf("%s: made a backend", name)
		}
	}
}

// TestLoadWarnings checks the warning of an rdp connection that may
// send its password inside TLS that checks no certificate.
func TestLoadWarnings(t *testing.T) {
	for _, tt := range []struct {
		name string
		f    config.File
		want []string
	}{
		{"unchecked TLS", config.File{Connections: []config.Connection{
			{ID: "tls", Protocol: "rdp", Security: rdp.SecurityTLS, TLS: config.ConnectionTLS{InsecureSkipVerify: true}},
			{ID: "any", Protocol: "rdp", Security: rdp.SecurityAny, TLS: config.ConnectionTLS{InsecureSkipVerify: true}},
			{ID: "nla", Protocol: "rdp", TLS: config.ConnectionTLS{InsecureSkipVerify: true}},
			{ID: "pinned", Protocol: "rdp", Security: rdp.SecurityTLS, TLS: config.ConnectionTLS{Pins: []string{"SHA256:x"}}},
			{ID: "vnc", Protocol: "vnc", TLS: config.ConnectionTLS{InsecureSkipVerify: true}},
		}}, []string{
			`connection "tls": security: tls may send the password inside TLS whose certificate tls.insecure_skip_verify accepts unchecked`,
			`connection "any": security: any may send the password`,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := loadWarnings(&tt.f)
			if len(got) != len(tt.want) {
				t.Fatalf("warnings %q, want %d", got, len(tt.want))
			}
			for i, w := range tt.want {
				if !strings.HasPrefix(got[i], w) {
					t.Errorf("warning %d: %q, want it to start %q", i, got[i], w)
				}
			}
		})
	}
}

// The native RDP sessions of a server share one budget, of
// limits.decode_cores.
func TestRDPBudget(t *testing.T) {
	f := testFile(t)
	f.Limits.DecodeCores = 3
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.shutdown)
	m, ok := s.backends["rdp"]
	if !ok {
		t.Fatal("no RDP backend")
	}
	budget := func() *rdpproto.Budget {
		b, err := m.make(target{addr: "127.0.0.1:3389", creds: config.Credentials{Prompt: true}}, screen{})
		if err != nil {
			t.Fatal(err)
		}
		return b.(*rdp.Backend).Budget()
	}
	first := budget()
	if first == nil || first.Cores() != 3 {
		t.Fatalf("a session's budget: %+v, want 3 cores", first)
	}
	if budget() != first {
		t.Error("two sessions have budgets of their own")
	}
}

func TestNewRDP(t *testing.T) {
	t.Setenv("OCCAMY_TEST_RDP_PASSWORD", "pw")
	b, err := newRDP(target{addr: "127.0.0.1:3389", creds: config.Credentials{PasswordEnv: "OCCAMY_TEST_RDP_PASSWORD"}}, screen{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*rdp.Backend); !ok {
		t.Errorf("%T, want *rdp.Backend", b)
	}
	if _, err := newRDP(target{addr: "127.0.0.1:3389", creds: config.Credentials{PasswordEnv: "OCCAMY_TEST_UNSET"}}, screen{}, nil); err == nil {
		t.Error("an unset password variable made a backend")
	}
	if _, err := newRDP(target{addr: "127.0.0.1:3389", security: "rdp", creds: config.Credentials{Prompt: true}}, screen{}, nil); err == nil {
		t.Error("an unknown security mode made a backend")
	}

	// The hello's size, both sides or neither, within an RDP desktop's.
	for _, tt := range []struct {
		s    screen
		w, h int
	}{
		{screen{}, 0, 0},
		{screen{width: 800}, 0, 0},
		{screen{height: 600}, 0, 0},
		{screen{cols: 80, rows: 24}, 0, 0},
		{screen{width: 1920, height: 1080}, 1920, 1080},
		{screen{width: 80, height: 60}, rdp.MinSide, rdp.MinSide},
		{screen{width: 99999, height: 1080}, rdp.MaxSide, 1080},
		{screen{width: 1920, height: 99999}, 1920, rdp.MaxSide},
		{screen{width: 8000, height: 8000}, 8000, rdp.MaxPixels / 8000},
	} {
		if w, h := rdpSize(tt.s); w != tt.w || h != tt.h {
			t.Errorf("rdpSize(%+v) = %d×%d, want %d×%d", tt.s, w, h, tt.w, tt.h)
		}
		if _, err := newRDP(target{addr: "127.0.0.1:3389", creds: config.Credentials{Prompt: true}}, tt.s, nil); err != nil {
			t.Errorf("screen %+v: %v", tt.s, err)
		}
	}

	// The connection's credentials, security mode and certificate policy
	// reach the backend, with the CA file read, and the key that allows
	// TLS is named for the operator.
	ca := writeCA(t)
	tls := config.ConnectionTLS{Require: true, ServerName: "desk", CAFile: ca, InsecureSkipVerify: true}
	cfg, err := rdpConfig(target{
		connection: "desk", addr: "10.0.0.5:3389", security: rdp.SecurityTLS, disableGFX: true, tls: tls,
		creds: config.Credentials{Username: "alice", PasswordEnv: "OCCAMY_TEST_RDP_PASSWORD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "10.0.0.5:3389" || cfg.Username != "alice" || cfg.Password != "pw" || cfg.Security != rdp.SecurityTLS || !cfg.DisableGFX ||
		cfg.TLS.ServerName != "desk" || cfg.TLS.RootCAs == nil || !cfg.TLS.InsecureSkipVerify || cfg.SecuritySetting != `security: tls on connection "desk"` {
		t.Errorf("rdpConfig = %+v", cfg)
	}
	pins := []string{"SHA256:" + strings.Repeat("A", 43)}
	cfg, err = rdpConfig(target{addr: "10.0.0.5:3389", creds: config.Credentials{Prompt: true}, tls: config.ConnectionTLS{Pins: pins}})
	if err != nil || !slices.Equal(cfg.TLS.Pins, pins) || cfg.TLS.RootCAs != nil || cfg.TLS.ServerName != "" || cfg.Security != "" || cfg.DisableGFX ||
		!strings.Contains(cfg.SecuritySetting, "ad-hoc rdp targets take NLA alone") {
		t.Errorf("pins: %+v %v", cfg, err)
	}
	// An ad-hoc target's certificate names the name it was opened by,
	// not the address it resolved to.
	cfg, err = rdpConfig(target{addr: "10.0.0.5:3389", name: "desk.lab.internal", creds: config.Credentials{Prompt: true}})
	if err != nil || cfg.TLS.ServerName != "desk.lab.internal" {
		t.Errorf("ad-hoc: server name %q, %v", cfg.TLS.ServerName, err)
	}
	// The username goes as it is: the backend splits DOMAIN\user
	// (internal/backend/rdp's TestRunSplitsDomain).
	cfg, err = rdpConfig(target{addr: "10.0.0.5:3389", creds: config.Credentials{Username: `LAB\alice`, Prompt: true}})
	if err != nil || cfg.Domain != "" || cfg.Username != `LAB\alice` {
		t.Errorf("username: domain %q, username %q, %v; want LAB\\alice as it is", cfg.Domain, cfg.Username, err)
	}
	for name, tls := range map[string]config.ConnectionTLS{
		"missing CA file":    {CAFile: filepath.Join(t.TempDir(), "nope")},
		"pins and a CA file": {CAFile: ca, Pins: pins},
	} {
		if _, err := newRDP(target{addr: "10.0.0.5:3389", creds: config.Credentials{Prompt: true}, tls: tls}, screen{}, nil); err == nil {
			t.Errorf("%s: made a backend", name)
		}
	}
}

// writeCA writes a self-signed CA certificate in PEM and returns its
// path.
func writeCA(t *testing.T) string {
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
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAskUser checks that a prompting ssh connection without a user name
// asks the owner for one, and keeps the size it is given before.
func TestNewKubernetes(t *testing.T) {
	t.Setenv("OCCAMY_TEST_K8S_TOKEN", "kube-token")
	no := false
	pod := config.Kubernetes{Namespace: "web", Pod: "web-0", Container: "app", Command: []string{"sh"}, TTY: &no}
	b, err := newKubernetes(target{addr: "k8s.internal:6443", kubernetes: pod, creds: config.Credentials{TokenEnv: "OCCAMY_TEST_K8S_TOKEN"}}, screen{cols: 120, rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*kubernetes.Backend); !ok || b.Class() != backend.Terminal {
		t.Errorf("%T of class %v, want a *kubernetes.Backend terminal", b, b.Class())
	}
	if _, err := newKubernetes(target{addr: "k8s.internal:6443", kubernetes: pod, creds: config.Credentials{TokenEnv: "OCCAMY_TEST_UNSET"}}, screen{}); err == nil {
		t.Error("an unset token variable made a backend")
	}
	// A terminal too large for the backend is refused, as the SSH
	// backend refuses it.
	if _, err := newKubernetes(target{addr: "k8s.internal:6443", kubernetes: pod, creds: config.Credentials{Prompt: true}}, screen{cols: kubernetes.MaxCols + 1, rows: 24}); err == nil {
		t.Error("a terminal wider than MaxCols made a backend")
	}

	// The connection's pod, credentials and certificate policy reach the
	// backend, with the token and the CA file read.
	ca := writeCA(t)
	tls := config.ConnectionTLS{ServerName: "kubernetes.default.svc", CAFile: ca}
	cfg, err := kubernetesConfig(target{addr: "k8s.internal:6443", kubernetes: pod, creds: config.Credentials{TokenEnv: "OCCAMY_TEST_K8S_TOKEN"}, tls: tls})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "k8s.internal:6443" || cfg.Namespace != "web" || cfg.Pod != "web-0" || cfg.Container != "app" ||
		!slices.Equal(cfg.Command, []string{"sh"}) || cfg.Attach || !cfg.NoTTY || cfg.Token != "kube-token" || cfg.PromptToken ||
		cfg.TLS.ServerName != "kubernetes.default.svc" || cfg.TLS.RootCAs == nil || cfg.TLS.InsecureSkipVerify {
		t.Errorf("kubernetesConfig = %+v", cfg)
	}
	yes := true
	pins := []string{"SHA256:" + strings.Repeat("A", 43)}
	cfg, err = kubernetesConfig(target{
		addr:       "k8s.internal:443",
		kubernetes: config.Kubernetes{Namespace: "default", Pod: "p", Attach: true, TTY: &yes},
		creds:      config.Credentials{ClientCertFile: "/c.pem", ClientKeyFile: "/k.pem"},
		tls:        config.ConnectionTLS{Pins: pins, InsecureSkipVerify: true},
	})
	if err != nil || !cfg.Attach || cfg.NoTTY || cfg.Token != "" || cfg.CertFile != "/c.pem" || cfg.KeyFile != "/k.pem" ||
		!slices.Equal(cfg.TLS.Pins, pins) || !cfg.TLS.InsecureSkipVerify || cfg.TLS.RootCAs != nil {
		t.Errorf("kubernetesConfig = %+v, %v", cfg, err)
	}
	if cfg, err := kubernetesConfig(target{addr: "k8s.internal:443", kubernetes: pod, creds: config.Credentials{Prompt: true}}); err != nil || !cfg.PromptToken || cfg.Token != "" {
		t.Errorf("prompt: %+v, %v", cfg, err)
	}
	notPEM := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newKubernetes(target{addr: "k8s.internal:443", kubernetes: pod, creds: config.Credentials{Prompt: true}, tls: config.ConnectionTLS{CAFile: notPEM}}, screen{}); err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Errorf("a CA file without a certificate: %v", err)
	}
}

func TestAskUser(t *testing.T) {
	b := &askUser{cfg: ssh.Config{Addr: "127.0.0.1:1", InsecureIgnoreHostKey: true, Cols: 80, Rows: 24}}
	if b.Class() != backend.Terminal {
		t.Error("not a terminal")
	}
	if err := b.Input(backend.Resize{Width: 100, Height: 30}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []backend.Resize{{Width: 0, Height: 30}, {Width: ssh.MaxCols + 1, Height: 30}, {Width: 100, Height: ssh.MaxRows + 1}} {
		if err := b.Input(r); err == nil {
			t.Errorf("size %dx%d accepted", r.Width, r.Height)
		}
	}
	if err := b.Input(backend.TermInput{Data: []byte("x")}); !errors.Is(err, ssh.ErrNotRunning) {
		t.Errorf("input before the user name: %v", err)
	}
	if b.cfg.Cols != 100 || b.cfg.Rows != 30 {
		t.Errorf("size %dx%d, want 100x30", b.cfg.Cols, b.cfg.Rows)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		if !strings.Contains(p.Instruction, "127.0.0.1:1") || len(p.Fields) != 1 || p.Fields[0].Secret {
			t.Errorf("prompt %+v", p)
		}
		return backend.PromptReply{Answers: []string{""}}, nil
	}}
	if err := b.Run(ctx, sink); err == nil || !strings.Contains(err.Error(), "no user name") {
		t.Errorf("an empty user name: %v", err)
	}

	b = &askUser{cfg: ssh.Config{Addr: "127.0.0.1:1", InsecureIgnoreHostKey: true}}
	sink = &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: []string{"bob"}}, nil
	}}
	err := b.Run(ctx, sink)
	if err == nil || strings.Contains(err.Error(), "user name") || strings.Contains(err.Error(), "prompt") {
		t.Errorf("Run with a user name, to a closed port: %v, want a dial error", err)
	}
	if b.b == nil {
		t.Error("no ssh backend made with the user name")
	}

	sink = &backendtest.Sink{}
	b = &askUser{cfg: ssh.Config{Addr: "127.0.0.1:1", InsecureIgnoreHostKey: true}}
	if err := b.Run(ctx, sink); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Errorf("a prompt nobody answers: %v", err)
	}
}
