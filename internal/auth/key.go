// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// LoadOrCreateKey reads occamyd's Ed25519 signing key from a PEM file in
// PKCS #8 form. If the file does not exist, it generates a key and writes
// it there, readable by its owner only, and reports created; the caller
// warns, because tokens signed before a lost key no longer verify.
func LoadOrCreateKey(path string) (key ed25519.PrivateKey, created bool, err error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the path comes from the configuration
	if errors.Is(err, fs.ErrNotExist) {
		key, err = createKey(path)
		return key, err == nil, err
	}
	if err != nil {
		return nil, false, err
	}
	key, err = parsePrivateKey(raw)
	if err != nil {
		return nil, false, fmt.Errorf("auth: %s: %w", path, err)
	}
	return key, false, nil
}

// createKey writes a new key to path. The file is created exclusively and
// renamed into place only when complete, so neither a concurrent start
// nor a crash leaves a partial key behind.
func createKey(path string) (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".signing-key-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name()) // fails harmlessly once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// Link fails if another process created the key first; then that key
	// is the one to use.
	if err := os.Link(f.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			key, _, err := LoadOrCreateKey(path)
			return key, err
		}
		return nil, err
	}
	return key, nil
}

func parsePrivateKey(raw []byte) (ed25519.PrivateKey, error) {
	b, _ := pem.Decode(raw)
	if b == nil || b.Type != "PRIVATE KEY" {
		return nil, errors.New("want a PEM block of type PRIVATE KEY")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("want an Ed25519 key, have %T", k)
	}
	return key, nil
}

// LoadPublicKey reads a trusted issuer's Ed25519 public key from a PEM
// file in PKIX form.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the path comes from the configuration
	if err != nil {
		return nil, err
	}
	key, err := ParsePublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("auth: %s: %w", path, err)
	}
	return key, nil
}

// ParsePublicKey parses an Ed25519 public key from a PEM block in PKIX
// form.
func ParsePublicKey(raw []byte) (ed25519.PublicKey, error) {
	b, _ := pem.Decode(raw)
	if b == nil || b.Type != "PUBLIC KEY" {
		return nil, errors.New("want a PEM block of type PUBLIC KEY")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("want an Ed25519 key, have %T", k)
	}
	return key, nil
}
