// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/rdp"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/config"
	rdpproto "changkun.de/x/occamy/internal/rdp"
)

// target is what a session connects to: a connection of the registry, or
// an ad-hoc target that its owner named.
type target struct {
	connection string // the registry's ID, empty for an ad-hoc target
	protocol   string
	addr       string // host:port to dial
	// name is the host name an ad-hoc target named, which addr holds the
	// address of; empty otherwise. A certificate names it.
	name  string
	creds config.Credentials
	// The host key policy of an ssh target.
	hostKeys              []string
	insecureIgnoreHostKey bool
	// tls is the certificate policy of a vnc or rdp target or of a
	// kubernetes target's API server, and ard allows a vnc target's Apple
	// Remote Desktop authentication.
	tls config.ConnectionTLS
	ard bool
	// security is the security mode of an rdp target, empty for its
	// default, and disableGFX keeps it from asking for the graphics
	// pipeline.
	security   string
	disableGFX bool
	// kubernetes is the pod of a kubernetes target, whose addr is the
	// API server's.
	kubernetes config.Kubernetes
}

// screen is the size that the owner's hello asks for: pixels for a
// graphical session, character cells for a terminal. Zero fields leave
// the backend's defaults.
type screen struct {
	width, height int
	cols, rows    int
}

// backendMaker makes the backend of one session.
type backendMaker struct {
	// name is the backend as the session's state reports it: ssh, vnc,
	// rdp or kubernetes.
	name string
	make func(t target, s screen) (backend.Backend, error)
}

// backends is the backend factory: the maker of each protocol's
// sessions.
type backends map[string]backendMaker

// newBackends returns the factory: occamyd's own SSH, VNC, RDP and
// Kubernetes backends, each named after its protocol. The RDP sessions
// share budget, the decoding of the process.
func newBackends(budget *rdpproto.Budget) backends {
	return backends{
		"ssh":        {name: "ssh", make: newSSH},
		"vnc":        {name: "vnc", make: newVNC},
		"rdp":        {name: "rdp", make: func(t target, s screen) (backend.Backend, error) { return newRDP(t, s, budget) }},
		"kubernetes": {name: "kubernetes", make: newKubernetes},
	}
}

// password reads the stored password of c, or returns "" if c stores
// none, as for prompt: true or a private key.
func password(c config.Credentials) (string, error) {
	if c.PasswordFile == "" && c.PasswordEnv == "" {
		return "", nil
	}
	pw, err := c.Password()
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return pw, nil
}

// newSSH makes a native SSH backend. Without a user name, as a prompting
// connection may have, the owner is asked for one first.
func newSSH(t target, s screen) (backend.Backend, error) {
	cfg := ssh.Config{
		Addr:                  t.addr,
		User:                  t.creds.Username,
		HostKeys:              t.hostKeys,
		InsecureIgnoreHostKey: t.insecureIgnoreHostKey,
		Cols:                  s.cols,
		Rows:                  s.rows,
	}
	if t.creds.PrivateKeyFile != "" {
		cfg.KeyFiles = []string{t.creds.PrivateKeyFile}
	}
	pw, err := password(t.creds)
	if err != nil {
		return nil, err
	}
	cfg.Password = pw
	if cfg.User != "" {
		return ssh.New(cfg)
	}
	// Check the rest of the configuration now, as New would.
	probe := cfg
	probe.User = "user"
	if _, err := ssh.New(probe); err != nil {
		return nil, err
	}
	return &askUser{cfg: cfg}, nil
}

// newVNC makes a native VNC backend, which asks the server for the size
// of the owner's hello once the server shows that it can resize.
func newVNC(t target, s screen) (backend.Backend, error) {
	cfg, err := vncConfig(t)
	if err != nil {
		return nil, err
	}
	if s.width > 0 && s.height > 0 {
		cfg.Width, cfg.Height = s.width, s.height
	}
	return vnc.New(cfg)
}

// vncConfig is the native VNC backend's configuration for t, with the
// password and the CA file read.
func vncConfig(t target) (vnc.Config, error) {
	pw, err := password(t.creds)
	if err != nil {
		return vnc.Config{}, err
	}
	roots, err := t.tls.RootCAs()
	if err != nil {
		return vnc.Config{}, fmt.Errorf("reading the CA file: %w", err)
	}
	cfg := vnc.Config{
		Addr:       t.addr,
		Username:   t.creds.Username,
		Password:   pw,
		ARD:        t.ard,
		ARDSetting: ardSetting(t),
		TLS: vnc.TLS{
			Require:            t.tls.Require,
			ServerName:         t.tls.ServerName,
			RootCAs:            roots,
			Pins:               t.tls.Pins,
			InsecureSkipVerify: t.tls.InsecureSkipVerify,
		},
	}
	// An ad-hoc target's address is the one its name resolved to, and
	// the certificate names the name.
	if cfg.TLS.ServerName == "" {
		cfg.TLS.ServerName = t.name
	}
	return cfg, nil
}

// newKubernetes makes a native Kubernetes backend, which runs the
// connection's command in its pod, or attaches to the container, through
// the API server at t.addr, in a terminal of the hello's size.
func newKubernetes(t target, s screen) (backend.Backend, error) {
	cfg, err := kubernetesConfig(t)
	if err != nil {
		return nil, err
	}
	cfg.Cols, cfg.Rows = s.cols, s.rows
	return kubernetes.New(cfg)
}

// kubernetesConfig is the native Kubernetes backend's configuration for
// t, with the token and the CA file read.
func kubernetesConfig(t target) (kubernetes.Config, error) {
	roots, err := t.tls.RootCAs()
	if err != nil {
		return kubernetes.Config{}, fmt.Errorf("reading the CA file: %w", err)
	}
	k := t.kubernetes
	cfg := kubernetes.Config{
		Addr:        t.addr,
		Namespace:   k.Namespace,
		Pod:         k.Pod,
		Container:   k.Container,
		Command:     k.Command,
		Attach:      k.Attach,
		NoTTY:       k.TTY != nil && !*k.TTY,
		PromptToken: t.creds.Prompt,
		CertFile:    t.creds.ClientCertFile,
		KeyFile:     t.creds.ClientKeyFile,
		TLS: kubernetes.TLS{
			ServerName:         t.tls.ServerName,
			RootCAs:            roots,
			Pins:               t.tls.Pins,
			InsecureSkipVerify: t.tls.InsecureSkipVerify,
		},
	}
	if t.creds.TokenFile != "" || t.creds.TokenEnv != "" {
		token, err := t.creds.Token()
		if err != nil {
			return kubernetes.Config{}, fmt.Errorf("reading the token: %w", err)
		}
		cfg.Token = kubernetes.Token(token)
	}
	return cfg, nil
}

// ardSetting names, for the error of a server that offers only Apple
// Remote Desktop authentication, the configuration key that allows it.
func ardSetting(t target) string {
	if t.connection == "" {
		return "ard: true on a connection for this target (ad-hoc targets have no ard)"
	}
	return fmt.Sprintf("ard: true on connection %q", t.connection)
}

// newRDP makes a native RDP backend, which connects with the desktop size
// of the owner's hello, within the sizes an RDP desktop may have, and
// decodes within budget.
func newRDP(t target, s screen, budget *rdpproto.Budget) (backend.Backend, error) {
	cfg, err := rdpConfig(t)
	if err != nil {
		return nil, err
	}
	cfg.Width, cfg.Height = rdpSize(s)
	cfg.Budget = budget
	return rdp.New(cfg)
}

// rdpSize is the desktop size to connect with for the hello's size:
// zero, for the backend's default, without both sides; otherwise each
// side within rdp.MinSide and rdp.MaxSide, and the height cut until the
// area is within rdp.MaxPixels. Viewers scale what does not fit.
func rdpSize(s screen) (width, height int) {
	if s.width <= 0 || s.height <= 0 {
		return 0, 0
	}
	width = min(max(s.width, rdp.MinSide), rdp.MaxSide)
	height = min(max(s.height, rdp.MinSide), rdp.MaxSide, rdp.MaxPixels/width)
	return width, height
}

// rdpConfig is the native RDP backend's configuration for t, with the
// password and the CA file read. The backend splits a username of the
// form DOMAIN\user.
func rdpConfig(t target) (rdp.Config, error) {
	pw, err := password(t.creds)
	if err != nil {
		return rdp.Config{}, err
	}
	roots, err := t.tls.RootCAs()
	if err != nil {
		return rdp.Config{}, fmt.Errorf("reading the CA file: %w", err)
	}
	cfg := rdp.Config{
		Addr:       t.addr,
		Username:   t.creds.Username,
		Password:   pw,
		Security:   t.security,
		DisableGFX: t.disableGFX,
		TLS: rdpproto.TLS{
			ServerName:         t.tls.ServerName,
			RootCAs:            roots,
			Pins:               t.tls.Pins,
			InsecureSkipVerify: t.tls.InsecureSkipVerify,
		},
		SecuritySetting: securitySetting(t),
	}
	// An ad-hoc target's address is the one its name resolved to, and
	// the certificate names the name.
	if cfg.TLS.ServerName == "" {
		cfg.TLS.ServerName = t.name
	}
	return cfg, nil
}

// securitySetting names, for the error of a server that selects
// standard RDP security when offered NLA alone, the configuration key
// that lets it take TLS.
func securitySetting(t target) string {
	if t.connection == "" {
		return "security: tls on a connection for this target (ad-hoc rdp targets take NLA alone)"
	}
	return fmt.Sprintf("security: tls on connection %q", t.connection)
}

// loadWarnings returns what occamyd warns of when it loads f: each rdp
// connection that sends the password inside TLS without checking the
// certificate, with security tls or any, which may skip NLA, and
// tls.insecure_skip_verify.
func loadWarnings(f *config.File) []string {
	var warnings []string
	for _, c := range f.Connections {
		if (c.Security == rdp.SecurityTLS || c.Security == rdp.SecurityAny) && c.TLS.InsecureSkipVerify {
			warnings = append(warnings, fmt.Sprintf("connection %q: security: %s may send the password inside TLS whose certificate tls.insecure_skip_verify accepts unchecked, so anyone in between can read it; for demos only",
				c.ID, c.Security))
		}
	}
	return warnings
}

// askUser is an SSH backend whose user name the owner gives: Run asks
// for it, then runs the SSH backend. A resize before then sizes the
// terminal it starts with; other input before then is refused, as the
// SSH backend refuses input before it runs.
type askUser struct {
	mu  sync.Mutex
	cfg ssh.Config
	b   *ssh.Backend
}

func (a *askUser) Class() backend.Class    { return backend.Terminal }
func (a *askUser) Demand(d backend.Demand) {}

func (a *askUser) Input(ev backend.InputEvent) error {
	a.mu.Lock()
	b := a.b
	if b == nil {
		defer a.mu.Unlock()
		if r, ok := ev.(backend.Resize); ok {
			if r.Width < 1 || r.Height < 1 || r.Width > ssh.MaxCols || r.Height > ssh.MaxRows {
				return fmt.Errorf("ssh: invalid terminal size %dx%d", r.Width, r.Height)
			}
			a.cfg.Cols, a.cfg.Rows = r.Width, r.Height
			return nil
		}
		return ssh.ErrNotRunning
	}
	a.mu.Unlock()
	return b.Input(ev)
}

func (a *askUser) Run(ctx context.Context, sink backend.Sink) error {
	a.mu.Lock()
	addr := a.cfg.Addr
	a.mu.Unlock()
	reply, err := sink.Prompt(ctx, backend.Prompt{
		Instruction: "User name for " + addr,
		Fields:      []backend.PromptField{{Label: "User name"}},
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("ssh: prompt: %w", err)
	}
	if len(reply.Answers) != 1 || reply.Answers[0] == "" {
		return errors.New("ssh: no user name given")
	}
	a.mu.Lock()
	cfg := a.cfg
	cfg.User = reply.Answers[0]
	b, err := ssh.New(cfg)
	a.b = b
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return b.Run(ctx, sink)
}
