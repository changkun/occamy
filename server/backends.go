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
	"sync"

	"changkun.de/x/occamy/internal/backend"
	bridge "changkun.de/x/occamy/internal/backend/guacd"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacd"
)

// target is what a session connects to: a connection of the registry, or
// an ad-hoc target that its owner named.
type target struct {
	connection string // the registry's ID, empty for an ad-hoc target
	protocol   string
	addr       string // host:port to dial
	creds      config.Credentials
	// The host key policy of an ssh target.
	hostKeys              []string
	insecureIgnoreHostKey bool
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

// backends is the backend factory: the maker of each protocol's
// sessions.
type backends map[string]backendMaker

// register makes protocol's sessions with make, under the backend name.
func (b backends) register(protocol, name string, make func(target, screen) (backend.Backend, error)) {
	b[protocol] = backendMaker{name: name, make: make}
}

// newBackends returns the factory with each protocol's default backend:
// the native backends for ssh and vnc, and the guacd bridge at g for rdp
// and telnet. No configuration key picks another backend yet.
func newBackends(g config.Guacd) backends {
	b := backends{}
	b.register("ssh", "ssh", newSSH)
	b.register("vnc", "vnc", newVNC)
	for _, p := range []string{"rdp", "telnet"} {
		b.register(p, "guacd", newBridge(g, p))
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

// newVNC makes a native VNC backend. VNC Authentication has no user
// name, so a configured one is not used.
func newVNC(t target, _ screen) (backend.Backend, error) {
	pw, err := password(t.creds)
	if err != nil {
		return nil, err
	}
	return vnc.New(vnc.Config{Addr: t.addr, Password: pw})
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
