// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"slices"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestHostPort(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"example.com", "example.com:22"},
		{"example.com:2222", "example.com:2222"},
		{"example.com:", "example.com:22"},
		{"10.0.0.1", "10.0.0.1:22"},
		{"::1", "[::1]:22"},
		{"[::1]", "[::1]:22"},
		{"[::1]:2222", "[::1]:2222"},
	} {
		got, err := hostPort(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("hostPort(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestHostKeyAlgorithms(t *testing.T) {
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ed, err := gossh.NewPublicKey(edPub)
	if err != nil {
		t.Fatal(err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := gossh.NewPublicKey(&rsaPriv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	line := func(k gossh.PublicKey) string { return string(gossh.MarshalAuthorizedKey(k)) }

	for _, tt := range []struct {
		name     string
		entries  []string
		insecure bool
		want     []string
	}{
		{"keys only", []string{line(rs), line(ed)}, false,
			[]string{gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSA, gossh.KeyAlgoED25519}},
		{"key and fingerprint", []string{line(rs), gossh.FingerprintSHA256(ed)}, false,
			[]string{gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSA,
				gossh.KeyAlgoED25519, gossh.KeyAlgoECDSA256, gossh.KeyAlgoECDSA384, gossh.KeyAlgoECDSA521}},
		{"insecure", nil, true, preferredHostKeyAlgorithms},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, err := parseHostKeys(tt.entries, tt.insecure)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(h.algorithms, tt.want) {
				t.Errorf("algorithms %v, want %v", h.algorithms, tt.want)
			}
		})
	}
}

func TestFingerprintPadding(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	fp := gossh.FingerprintSHA256(k)
	h, err := parseHostKeys([]string{" " + fp + "= "}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.fingerprints, []string{fp}) {
		t.Errorf("fingerprints %v, want [%s]", h.fingerprints, fp)
	}
}

func TestCheckHostKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	for entry, want := range map[string]string{
		string(gossh.MarshalAuthorizedKey(k)): "",
		gossh.FingerprintSHA256(k):            "",
		"SHA256:c2hvcnQ":                      "host key is not a SHA-256 fingerprint",
		"ssh-ed25519 AAAA":                    "host key is not a public key",
		"":                                    "host key is not a public key",
	} {
		err := CheckHostKey(entry)
		if want == "" && err != nil || want != "" && (err == nil || !strings.HasPrefix(err.Error(), want)) {
			t.Errorf("CheckHostKey(%q) = %v, want %q", entry, err, want)
		}
	}
}
