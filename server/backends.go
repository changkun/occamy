// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"strings"
	"sync"

	"changkun.de/x/occamy/internal/backend"
	bridge "changkun.de/x/occamy/internal/backend/guacd"
	"changkun.de/x/occamy/internal/backend/rdp"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacd"
	rdpproto "changkun.de/x/occamy/internal/rdp"
)

// target is what a session connects to: a connection of the registry, or
// an ad-hoc target that its owner named.
type target struct {
	connection string // the registry's ID, empty for an ad-hoc target
	protocol   string
	// backend is what serves the target, config.BackendNative or
	// config.BackendGuacd; empty for the protocol's default.
	backend string
	addr    string // host:port to dial
	// name is the host name an ad-hoc target named, which addr holds the
	// address of; empty otherwise. A certificate names it.
	name  string
	creds config.Credentials
	// The host key policy of an ssh target.
	hostKeys              []string
	insecureIgnoreHostKey bool
	// tls is the certificate policy of a vnc or rdp target, and ard allows
	// a vnc target's Apple Remote Desktop authentication.
	tls config.ConnectionTLS
	ard bool
	// security is the security mode of an rdp target on the native
	// backend, empty for its default.
	security string
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
	// name is the backend as the session's state reports it: ssh, vnc or
	// guacd.
	name string
	make func(t target, s screen) (backend.Backend, error)
}

// backendKey names a backend of a protocol, config.BackendNative or
// config.BackendGuacd.
type backendKey struct{ protocol, backend string }

// backends is the backend factory: the maker of each protocol's
// sessions on each backend that serves it.
type backends map[backendKey]backendMaker

// register makes protocol's sessions on backend, the protocol's default
// if empty, with make, under the name the session's state reports.
func (b backends) register(protocol, backend, name string, make func(target, screen) (backend.Backend, error)) {
	b[backendKey{protocol, config.BackendOf(protocol, backend)}] = backendMaker{name: name, make: make}
}

// lookup returns the maker of protocol's sessions on backend, the
// protocol's default if empty.
func (b backends) lookup(protocol, backend string) (backendMaker, bool) {
	m, ok := b[backendKey{protocol, config.BackendOf(protocol, backend)}]
	return m, ok
}

// serves reports whether any backend serves protocol.
func (b backends) serves(protocol string) bool {
	for k := range b {
		if k.protocol == protocol {
			return true
		}
	}
	return false
}

// newBackends returns the factory: the native SSH backend for ssh, the
// native VNC and RDP backends for vnc and rdp, their default, and the
// guacd bridge at g for telnet, and for vnc and rdp with backend: guacd.
func newBackends(g config.Guacd) backends {
	b := backends{}
	b.register("ssh", config.BackendNative, "ssh", newSSH)
	b.register("vnc", config.BackendNative, "vnc", newVNC)
	b.register("rdp", config.BackendNative, "rdp", newRDP)
	for _, p := range []string{"vnc", "rdp", "telnet"} {
		b.register(p, config.BackendGuacd, "guacd", newBridge(g, p))
	}
	return b
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

// ardSetting names, for the error of a server that offers only Apple
// Remote Desktop authentication, the configuration key that allows it.
func ardSetting(t target) string {
	if t.connection == "" {
		return "ard: true on a connection for this target (ad-hoc targets have no ard)"
	}
	return fmt.Sprintf("ard: true on connection %q", t.connection)
}

// newRDP makes a native RDP backend, which connects with the desktop size
// of the owner's hello, within the sizes an RDP desktop may have.
func newRDP(t target, s screen) (backend.Backend, error) {
	cfg, err := rdpConfig(t)
	if err != nil {
		return nil, err
	}
	cfg.Width, cfg.Height = rdpSize(s)
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
// password and the CA file read. A username of the form
// DOMAIN\user logs on to the domain, as mstsc takes it.
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
		Addr:     t.addr,
		Username: t.creds.Username,
		Password: pw,
		Security: t.security,
		TLS: rdpproto.TLS{
			ServerName:         t.tls.ServerName,
			RootCAs:            roots,
			Pins:               t.tls.Pins,
			InsecureSkipVerify: t.tls.InsecureSkipVerify,
		},
		SecuritySetting: securitySetting(t),
	}
	if d, u, ok := strings.Cut(cfg.Username, `\`); ok {
		cfg.Domain, cfg.Username = d, u
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

// newBridge returns the maker of protocol's sessions through guacd at g.
// The guacd parameters are g's for the protocol, with the target, the
// user name and the stored password over them. RDP gets disable-gfx
// unless g sets it: through the graphics pipeline, xrdp's codec shifted
// colours by up to 22 levels before guacd saw them, and the bandwidth
// the pipeline saves lies between guacd and the remote, not the viewers
// (guacd-bridge, conformance corpus).
func newBridge(g config.Guacd, protocol string) func(target, screen) (backend.Backend, error) {
	return func(t target, s screen) (backend.Backend, error) {
		cfg, err := bridgeConfig(g, protocol, t, s)
		if err != nil {
			return nil, err
		}
		return bridge.New(g.Address, cfg, bridge.Options{}), nil
	}
}

// adhocParams are the guacd parameters that an ad-hoc target takes from
// guacd.params: the display's. The others, such as an SFTP host or a
// gateway, are meant for the configured connections, not for a target
// that a user chose.
var adhocParams = map[string]bool{
	"color-depth":           true,
	"color-scheme":          true,
	"cursor":                true,
	"disable-gfx":           true,
	"enable-font-smoothing": true,
	"font-name":             true,
	"font-size":             true,
	"resize-method":         true,
}

// bridgeConfig is the guacd handshake of a session of protocol on t.
func bridgeConfig(g config.Guacd, protocol string, t target, s screen) (guacd.Config, error) {
	host, port, err := net.SplitHostPort(t.addr)
	if err != nil {
		return guacd.Config{}, err
	}
	params := maps.Clone(g.Params[protocol])
	if params == nil {
		params = map[string]string{}
	}
	if t.connection == "" {
		maps.DeleteFunc(params, func(name, _ string) bool { return !adhocParams[name] })
	}
	params["hostname"], params["port"] = host, port
	if t.creds.Username != "" {
		params["username"] = t.creds.Username
	}
	pw, err := password(t.creds)
	if err != nil {
		return guacd.Config{}, err
	}
	if pw != "" {
		params["password"] = pw
	}
	if _, ok := params["disable-gfx"]; protocol == "rdp" && !ok {
		params["disable-gfx"] = "true"
	}
	return guacd.Config{
		Protocol: protocol,
		Params:   params,
		Width:    min(s.width, backend.MaxResizeSide),
		Height:   min(s.height, backend.MaxResizeSide),
	}, nil
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
