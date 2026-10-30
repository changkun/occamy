// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/config"
)

// fakeHostKey is a host key fingerprint in the form ssh-keygen -l prints.
const fakeHostKey = "SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"

func TestBackendDefaults(t *testing.T) {
	b := newBackends(config.Guacd{Address: config.DefaultGuacdAddress})
	got := map[string]string{}
	for p, m := range b {
		got[p] = m.name
	}
	want := map[string]string{"ssh": "ssh", "vnc": "vnc", "rdp": "guacd", "telnet": "guacd"}
	if !maps.Equal(got, want) {
		t.Errorf("backends %v, want %v", got, want)
	}
}

func TestBridgeConfig(t *testing.T) {
	t.Setenv("OCCAMY_TEST_RDP_PASSWORD", "s3cret")
	g := config.Guacd{Params: map[string]map[string]string{
		"rdp":    {"security": "nla", "hostname": "configured"},
		"telnet": {"disable-gfx": "x"},
	}}
	tg := target{
		protocol: "rdp",
		addr:     "[fd00::5]:3390",
		creds:    config.Credentials{Username: "carol", PasswordEnv: "OCCAMY_TEST_RDP_PASSWORD"},
	}
	cfg, err := bridgeConfig(g, "rdp", tg, screen{width: 1920, height: 99999})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"security": "nla", "hostname": "fd00::5", "port": "3390",
		"username": "carol", "password": "s3cret", "disable-gfx": "true",
	}
	if cfg.Protocol != "rdp" || !maps.Equal(cfg.Params, want) || cfg.Width != 1920 || cfg.Height != backend.MaxResizeSide {
		t.Errorf("config %+v", cfg)
	}
	if g.Params["rdp"]["hostname"] != "configured" || len(g.Params["rdp"]) != 2 {
		t.Errorf("the configured parameters changed: %v", g.Params["rdp"])
	}

	g.Params["rdp"]["disable-gfx"] = "false"
	tg.creds = config.Credentials{Prompt: true}
	cfg, err = bridgeConfig(g, "rdp", tg, screen{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Params["disable-gfx"] != "false" || cfg.Params["password"] != "" || cfg.Params["username"] != "" || cfg.Width != 0 {
		t.Errorf("config %+v", cfg)
	}

	cfg, err = bridgeConfig(g, "telnet", target{addr: "10.0.0.1:23"}, screen{})
	if err != nil || cfg.Params["disable-gfx"] != "x" || len(cfg.Params) != 3 {
		t.Errorf("telnet: %+v %v", cfg, err)
	}

	tg.creds = config.Credentials{PasswordEnv: "OCCAMY_TEST_UNSET"}
	if _, err := bridgeConfig(g, "rdp", tg, screen{}); err == nil {
		t.Error("an unset password variable made a backend")
	}
	if _, err := newBridge(g, "rdp")(tg, screen{}); err == nil {
		t.Error("an unset password variable made a backend")
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
}

// TestAskUser checks that a prompting ssh connection without a user name
// asks the owner for one, and keeps the size it is given before.
func TestAskUser(t *testing.T) {
	b := &askUser{cfg: ssh.Config{Addr: "127.0.0.1:1", InsecureIgnoreHostKey: true, Cols: 80, Rows: 24}}
	if b.Class() != backend.Terminal {
		t.Error("not a terminal")
	}
	if err := b.Input(backend.Resize{Width: 100, Height: 30}); err != nil {
		t.Fatal(err)
	}
	if err := b.Input(backend.Resize{Width: 0, Height: 30}); err == nil {
		t.Error("an empty size accepted")
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
