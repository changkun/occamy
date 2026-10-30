// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signing.key")
	key, created, err := LoadOrCreateKey(path)
	if err != nil || !created {
		t.Fatalf("first load: created %v, err %v", created, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("mode %v, want 0600", mode)
		}
	}
	again, created, err := LoadOrCreateKey(path)
	if err != nil || created {
		t.Fatalf("second load: created %v, err %v", created, err)
	}
	if !key.Equal(again) {
		t.Error("second load returned a different key")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want the key alone", len(entries))
	}
}

// Processes starting together agree on one key.
func TestLoadOrCreateKeyConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signing.key")
	keys := make([]ed25519.PrivateKey, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Go(func() {
			k, _, err := LoadOrCreateKey(path)
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		})
	}
	wg.Wait()
	for _, k := range keys[1:] {
		if !k.Equal(keys[0]) {
			t.Fatal("concurrent loads returned different keys")
		}
	}
}

func TestLoadKeyRejects(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	ecPub, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pemOf := func(typ string, der []byte) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	}
	dir := t.TempDir()
	for name, raw := range map[string][]byte{
		"empty":      nil,
		"not pem":    []byte("occamy"),
		"ecdsa":      pemOf("PRIVATE KEY", ecDER),
		"public key": pemOf("PUBLIC KEY", pubDER),
		"bad der":    pemOf("PRIVATE KEY", []byte{1, 2, 3}),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadOrCreateKey(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	for name, raw := range map[string][]byte{
		"ecdsa":       pemOf("PUBLIC KEY", ecPub),
		"private key": pemOf("PRIVATE KEY", ecDER),
	} {
		if _, err := ParsePublicKey(raw); err == nil {
			t.Errorf("public %s: parsed", name)
		}
	}
	if _, err := LoadPublicKey(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing public key: loaded")
	}
}

// A key written by LoadOrCreateKey signs tokens that its public half, as
// a trusted issuer would publish it, verifies.
func TestPublicKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key, _, err := LoadOrCreateKey(filepath.Join(dir, "signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(dir, "signing.pub")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := NewSigner(key, "gateway", "occamy").Access("alice", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier("occamy", map[string]ed25519.PublicKey{"gateway": pub})
	if _, err := v.VerifyAccess(tok); err != nil {
		t.Error(err)
	}
}
