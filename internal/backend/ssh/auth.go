// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ssh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	gossh "golang.org/x/crypto/ssh"

	"changkun.de/x/occamy/internal/backend"
)

// HostKeyError is the error, wrapped in Run's, for a host key that the
// configuration does not accept.
type HostKeyError struct {
	Type        string // the key's algorithm, such as ssh-ed25519
	Fingerprint string // as ssh.FingerprintSHA256 prints it
}

func (e *HostKeyError) Error() string {
	return fmt.Sprintf("host key %s %s is not pinned", e.Type, e.Fingerprint)
}

// preferredHostKeyAlgorithms are the host key algorithms offered when a
// fingerprint is pinned, in the order OpenSSH's client prefers them, so
// that the server presents the key whose fingerprint ssh(1) shows.
// x/crypto's own default puts ECDSA ahead of Ed25519.
var preferredHostKeyAlgorithms = []string{
	gossh.KeyAlgoED25519,
	gossh.KeyAlgoECDSA256,
	gossh.KeyAlgoECDSA384,
	gossh.KeyAlgoECDSA521,
	gossh.KeyAlgoRSASHA512,
	gossh.KeyAlgoRSASHA256,
}

// hostKeys is the host key policy of a connection.
type hostKeys struct {
	keys         []gossh.PublicKey
	fingerprints []string
	insecure     bool
	// algorithms are the host key algorithms offered to the server.
	algorithms []string
}

// parseHostKeys parses the configured host keys.
func parseHostKeys(entries []string, insecure bool) (hostKeys, error) {
	h := hostKeys{insecure: insecure}
	for i, e := range entries {
		key, fp, err := parseHostKey(e)
		if err != nil {
			return hostKeys{}, fmt.Errorf("ssh: host key %d %w", i, err)
		}
		if key != nil {
			h.keys = append(h.keys, key)
		} else {
			h.fingerprints = append(h.fingerprints, fp)
		}
	}
	if len(h.keys) == 0 && len(h.fingerprints) == 0 && !insecure {
		return hostKeys{}, errors.New("ssh: no host key policy: pin host keys or ignore them explicitly")
	}
	h.algorithms = h.hostKeyAlgorithms()
	return h, nil
}

// CheckHostKey reports whether entry is a host key as Config.HostKeys
// takes it: a public key in authorized_keys format, or a SHA-256
// fingerprint. The configuration checks its entries with it.
func CheckHostKey(entry string) error {
	if _, _, err := parseHostKey(entry); err != nil {
		return fmt.Errorf("host key %w", err)
	}
	return nil
}

// parseHostKey parses one entry of Config.HostKeys: a public key, or
// else a fingerprint in the form ssh.FingerprintSHA256 returns. The error
// completes "host key ...".
func parseHostKey(e string) (gossh.PublicKey, string, error) {
	e = strings.TrimSpace(e)
	if fp, ok := strings.CutPrefix(e, "SHA256:"); ok {
		fp = strings.TrimRight(fp, "=")
		sum, err := base64.RawStdEncoding.DecodeString(fp)
		if err != nil || len(sum) != sha256.Size {
			return nil, "", errors.New("is not a SHA-256 fingerprint")
		}
		return nil, "SHA256:" + fp, nil
	}
	key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(e))
	if err != nil {
		return nil, "", fmt.Errorf("is not a public key: %w", err)
	}
	return key, "", nil
}

// hostKeyAlgorithms lists the algorithms of the pinned keys first, so
// that a server with several host keys presents a pinned one. If a
// fingerprint is pinned, or nothing is, the preferred algorithms follow.
// Certificate algorithms are left out: a certificate never matches a
// pinned key.
func (h *hostKeys) hostKeyAlgorithms() []string {
	var algos []string
	add := func(a ...string) {
		for _, a := range a {
			if !slices.Contains(algos, a) {
				algos = append(algos, a)
			}
		}
	}
	for _, k := range h.keys {
		if k.Type() == gossh.KeyAlgoRSA {
			add(gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSA)
		} else {
			add(k.Type())
		}
	}
	if len(h.fingerprints) > 0 || len(h.keys) == 0 {
		add(preferredHostKeyAlgorithms...)
	}
	return algos
}

// callback returns the host key check for one Run, which warns at most
// once through warn about an unverified key.
func (h *hostKeys) callback(addr string, log *slog.Logger, warn *sync.Once) gossh.HostKeyCallback {
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		fp := gossh.FingerprintSHA256(key)
		raw := key.Marshal()
		for _, k := range h.keys {
			if bytes.Equal(k.Marshal(), raw) {
				return nil
			}
		}
		if slices.Contains(h.fingerprints, fp) {
			return nil
		}
		if h.insecure {
			warn.Do(func() {
				log.Warn("ssh: accepting an unverified host key", "addr", addr, "type", key.Type(), "fingerprint", fp)
			})
			return nil
		}
		return &HostKeyError{Type: key.Type(), Fingerprint: fp}
	}
}

// clientConfig returns the client configuration for one Run. Prompts go
// to sink and end with ctx.
//
// Authentication tries the key files, then the configured password, then
// keyboard-interactive through the owner. Without a configured password,
// the owner is asked for one last. x/crypto tries only the methods that
// the server offers, and each method once.
//
// The configured password is sent at most once per connection, by
// whichever method asks for it first: the password method, or a
// keyboard-interactive challenge that asks a single hidden question, as
// PAM does when the server disables the password method. A password
// asked for again was wrong, and a hidden question after the password
// method has already sent it is another factor, such as a one-time code;
// both go to the owner.
func (b *Backend) clientConfig(ctx context.Context, sink backend.Sink, r *run) *gossh.ClientConfig {
	var sent atomic.Bool // the configured password has been sent
	var auth []gossh.AuthMethod
	if len(b.signers) > 0 {
		auth = append(auth, gossh.PublicKeys(b.signers...))
	}
	if b.password != "" {
		auth = append(auth, gossh.PasswordCallback(func() (string, error) {
			sent.Store(true)
			return b.password, nil
		}))
	}
	auth = append(auth, gossh.KeyboardInteractive(b.challenge(ctx, sink, &sent)))
	if b.password == "" {
		auth = append(auth, gossh.PasswordCallback(b.askPassword(ctx, sink)))
	}
	return &gossh.ClientConfig{
		User:              b.user,
		Auth:              auth,
		HostKeyCallback:   b.pins.callback(b.addr, b.log, &r.warn),
		HostKeyAlgorithms: b.pins.algorithms,
	}
}

// challenge answers a keyboard-interactive challenge that asks a single
// hidden question with the configured password, unless sent says it has
// been sent already, and passes every other challenge to the owner.
func (b *Backend) challenge(ctx context.Context, sink backend.Sink, sent *atomic.Bool) gossh.KeyboardInteractiveChallenge {
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		if len(questions) == 0 {
			// Servers send rounds without questions, as OpenSSH does
			// once PAM has finished; there is nothing to ask.
			return nil, nil
		}
		if b.password != "" && len(questions) == 1 && len(echos) == 1 && !echos[0] && sent.CompareAndSwap(false, true) {
			return []string{b.password}, nil
		}
		p := backend.Prompt{
			Name:        name,
			Instruction: instruction,
			Fields:      make([]backend.PromptField, len(questions)),
		}
		for i, q := range questions {
			p.Fields[i] = backend.PromptField{Label: q, Secret: i >= len(echos) || !echos[i]}
		}
		return ask(ctx, sink, p)
	}
}

// askPassword asks the owner for a password.
func (b *Backend) askPassword(ctx context.Context, sink backend.Sink) func() (string, error) {
	return func() (string, error) {
		answers, err := ask(ctx, sink, backend.Prompt{
			Instruction: "Password for " + b.user + "@" + b.addr,
			Fields:      []backend.PromptField{{Label: "Password", Secret: true}},
		})
		if err != nil {
			return "", err
		}
		return answers[0], nil
	}
}

// ask asks the owner p and returns one answer per field. The answers are
// passed to x/crypto and kept nowhere else.
func ask(ctx context.Context, sink backend.Sink, p backend.Prompt) ([]string, error) {
	reply, err := sink.Prompt(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("ssh: prompt: %w", err)
	}
	if len(reply.Answers) != len(p.Fields) {
		return nil, fmt.Errorf("ssh: prompt: %d answers for %d questions", len(reply.Answers), len(p.Fields))
	}
	return reply.Answers, nil
}
