// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// rfc2409Group2 is the prime of RFC 2409's second Oakley group, of 1024
// bits, which macOS Screen Sharing sent with the generator 2 in 2017
// (Tenable's capture shows its first 32 bytes). It is a safe prime.
var rfc2409Group2 = bigHex("" +
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381" +
	"FFFFFFFFFFFFFFFF")

func bigHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("not hex: " + s)
	}
	return n
}

// ardKey appends the server's Apple Remote Desktop key: the generator,
// the key length n, and the prime and the public key in n bytes each.
func (s script) ardKey(g uint16, n int, p, y *big.Int) script {
	s = binary.BigEndian.AppendUint16(s, g)
	s = binary.BigEndian.AppendUint16(s, uint16(n))
	s = append(s, p.FillBytes(make([]byte, n))...)
	return append(s, y.FillBytes(make([]byte, n))...)
}

func TestARDKnownAnswer(t *testing.T) {
	// Each vector was computed apart from this package: the
	// Diffie-Hellman exchange and the key with Python's pow and hashlib,
	// the ciphertext with OpenSSL:
	//
	//	y, X, K = pow(g, xs, p), pow(g, xc, p), pow(pow(g, xs, p), xc, p)
	//	key = hashlib.md5(K.to_bytes(n, "big")).digest()
	//	openssl enc -aes-128-ecb -nopad -K <key>   # of the credentials
	seq := make([]byte, 128) // the bytes 0 to 127
	for i := range seq {
		seq[i] = byte(i)
	}
	high := make([]byte, 128) // the bytes 128 to 255
	for i := range high {
		high[i] = byte(128 + i)
	}
	for _, tt := range []struct {
		name           string
		g              int64
		p              *big.Int
		xs, xc         *big.Int // the server's private key and the client's
		user, password string
		pad            []byte // the random bytes the fields are filled with
		y, pub         string // the server's public key and the client's
		key            string
		plain          string // the credentials before encryption
		ct             string
	}{
		{
			// noVNC's test of its handler (tests/test.rfb.js), where every
			// 128 random bytes are 0 to 127: the prime, both private keys
			// and the padding. Its expected ciphertext is noVNC's, which
			// OpenSSL reproduces. The secret and the public keys have two
			// leading zero bytes, which the key and the public key keep.
			name: "noVNC", g: 0x7fff, p: new(big.Int).SetBytes(seq),
			xs: new(big.Int).SetBytes(seq), xc: new(big.Int).SetBytes(seq),
			user: "user", password: "password", pad: seq,
			y:   "0000ec059c14df5cd71f0be0adb98b90f5d95ec92932b9bb445cd298e4940033e79cf1e361ef9ac1a9fded6fde1043f46936bf538eeb0a21cff08253714447e8c407862c0cb666f8aeef52a063b7c80120cc30329654e5b5c827a204f0f04144f54230d515720c09d1b73dcd8aa428508578945dd674f878796571dce1056bed",
			pub: "0000ec059c14df5cd71f0be0adb98b90f5d95ec92932b9bb445cd298e4940033e79cf1e361ef9ac1a9fded6fde1043f46936bf538eeb0a21cff08253714447e8c407862c0cb666f8aeef52a063b7c80120cc30329654e5b5c827a204f0f04144f54230d515720c09d1b73dcd8aa428508578945dd674f878796571dce1056bed",
			key: "8592cc5e5b4b2da071a8087a15a1b8b5",
			plain: "757365720005060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f" +
				"70617373776f726400494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f",
			ct: "c727cc5fbe467f42056a99e47bec96ce3e6b0b0415f25cb80951237d38a701d7b691b74bf5c52f137a5e404c4da3de8fbaae5427f4b3e37253e72a6acd2b9f6e" +
				"d1f09df6edce8699c3705c3c1cea5b428326bbc36ea7d4f120fad4d5ca59b41547d9d1512a3d76f8417b624e8b6fca8932b925ad3a63bb352a7d0da5e8a3972a",
		},
		{
			// RFC 2409's group 2, as macOS sends it, with private keys of
			// 256 bits: the SHA-256 of "occamy ard server" and of "occamy
			// ard client".
			name: "RFC 2409 group 2", g: 2, p: rfc2409Group2,
			xs:   bigHex("9081272d20ac001ca6c4a6ad8b9d35c8e5db0a36026a20f5020d7c0d159e2c60"),
			xc:   bigHex("3e9f57c8c44df83e50c50ad5fad9b865e56e944a6d486a7906767c3a39597a8d"),
			user: "alice", password: "hunter2", pad: high,
			y:   "1ce1e13297b25262eb2d033e42a50f2c249e9100e51ed484a22f40a7de5a7b184ee033d1bec6c1b3a2e6aff8a1ad896b22823e85f0f5abef5cf7165f6c70f1064edf8f2d6bf4167e5b49f8d19154dde0fab59eac1440c6a65a81e96990927ff1bc4c5c7942ca01725e984a4c8874c3be3b56981f7ca14798a7e245356a48f629",
			pub: "dee387f7267243eea23894e0b658eb5a09228d0cd36459d182f483017d806006724567c5bdb9788250191dcd1defb828ad2bc93f23f1bb7167cd9b4db7a8038fb841038e0a3a683fc605f6c5df2b1029c3df904f29da795bf207b27034464e69a5d3181a8c228c57f77bb3c4d13a8822aa7702f4481c1165dc3f493f4fac80be",
			key: "36e3ea4094acfbc3f0c0c388620174e6",
			plain: "616c69636500868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9fa0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf" +
				"68756e7465723200c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedfe0e1e2e3e4e5e6e7e8e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeff",
			ct: "6480c23b2e45a617608d6759819eccd21f3cbedaec86b9d93892deb4221f8f91bf4b6112e6f7f3d63b5ec3b44c485a68cb78641844ca46ebbfff7976f07f3d08" +
				"2196baefa898a724d28afac58b0fbed49b714bbd93c04e595b166fcfd04e0a48069af3abe1759a37dcf332e7900a5ce31a58b4ac8eaa886fdf364751fb2a42dc",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := 128
			g := big.NewInt(tt.g)
			y := new(big.Int).Exp(g, tt.xs, tt.p)
			if got := hex.EncodeToString(y.FillBytes(make([]byte, n))); got != tt.y {
				t.Fatalf("the server's public key %s, want %s", got, tt.y)
			}
			if err := checkARDGroup(g, tt.p, y); err != nil {
				t.Fatal(err)
			}
			key := ardKey(n, tt.p, y, tt.xc)
			if got := hex.EncodeToString(key[:]); got != tt.key {
				t.Errorf("key %s, want %s", got, tt.key)
			}
			var plain [2 * ardField]byte
			copy(plain[:], tt.pad)
			ardCredentials(&plain, tt.user, tt.password)
			if got := hex.EncodeToString(plain[:]); got != tt.plain {
				t.Errorf("credentials %s, want %s", got, tt.plain)
			}
			ct := ardEncrypt(&key, &plain)
			if got := hex.EncodeToString(ct[:]); got != tt.ct {
				t.Errorf("ciphertext %s, want %s", got, tt.ct)
			}
			if plain != [2 * ardField]byte{} || key != [16]byte{} {
				t.Error("the credentials or the key were not cleared")
			}

			// The whole response, as ardAuth sends it.
			copy(plain[:], tt.pad)
			out := ardResponse(n, g, tt.p, y, tt.xc, tt.user, tt.password, &plain)
			if want := tt.ct + tt.pub; hex.EncodeToString(out) != want {
				t.Errorf("response\n%x, want\n%s", out, want)
			}
			if plain != [2 * ardField]byte{} {
				t.Error("ardResponse did not clear the credentials")
			}
		})
	}
}

func TestARDPrivateKey(t *testing.T) {
	rnd := mrand.NewChaCha8([32]byte{1})
	// A key of 256 bits for a prime of more.
	var lens []int
	for range 64 {
		x, err := ardPrivateKey(rnd, rfc2409Group2)
		if err != nil {
			t.Fatal(err)
		}
		lens = append(lens, x.BitLen())
	}
	// A key of 256 random bits has the top one with a chance of one in
	// two, so none of 64 has it with a chance of one in 2^64. The size is
	// written out, not ardExponent, so that a smaller one fails.
	if lo, hi := slices.Min(lens), slices.Max(lens); lo < 2 || hi < 256 || hi > 257 {
		t.Errorf("private keys of %d to %d bits for a 1024-bit prime, want up to 256", lo, hi)
	}
	// A key within [2, p-2] for a prime of 256 bits or fewer: each of
	// 2 to 5 for 7.
	seen := map[int64]int{}
	for range 400 {
		x, err := ardPrivateKey(rnd, big.NewInt(7))
		if err != nil {
			t.Fatal(err)
		}
		seen[x.Int64()]++
	}
	if len(seen) != 4 || seen[2] == 0 || seen[5] == 0 {
		t.Errorf("private keys for the prime 7: %v, want 2 to 5", seen)
	}
	small := new(big.Int).Lsh(one, ardExponent) // 257 bits
	small.Sub(small, one)                       // 256 bits
	for range 64 {
		x, _ := ardPrivateKey(rnd, small)
		if x.Cmp(two) < 0 || x.Cmp(new(big.Int).Sub(small, two)) > 0 {
			t.Fatalf("private key %x outside [2, p-2] for a prime of 256 bits", x)
		}
	}
	if _, err := ardPrivateKey(iotest.ErrReader(io.ErrUnexpectedEOF), rfc2409Group2); err == nil {
		t.Error("ardPrivateKey succeeded without random bytes")
	}
}

// ardScriptKey is a valid Apple Remote Desktop key in RFC 2409's group
// 2.
func ardScriptKey() script {
	y := new(big.Int).Exp(two, big.NewInt(0x1234567), rfc2409Group2)
	return script{}.ardKey(2, 128, rfc2409Group2, y)
}

func TestARDScripts(t *testing.T) {
	ard38 := func() script { return script{}.version("003.008").u8(1, secARD) }
	p := rfc2409Group2
	pm1 := new(big.Int).Sub(p, one)
	y := new(big.Int).Exp(two, big.NewInt(0x1234567), p)
	seven := big.NewInt(7)
	long := strings.Repeat("a", ardField)
	for _, tt := range []struct {
		name            string
		in              script
		ard, requireTLS bool
		user, password  string // what the owner gives; alice and hunter2 if empty
		// sent is what the client sends after its version: 0 for the
		// type, the response of a 128-byte key and ClientInit, -1 for all
		// but ClientInit, -2 for nothing, -3 for the type alone, and -5
		// as 0 with a 512-byte key.
		sent  int
		creds bool   // the credentials are asked for
		err   string // in the error; empty for success
		is    error
	}{
		{name: "3.8", in: ard38().u8(ardScriptKey()...).u32(0).init(10, 10, "Mac"), ard: true, creds: true},
		{
			name: "macOS's types", in: script{}.version("003.889").u8(4, 30, 33, 36, 35).u8(ardScriptKey()...).u32(0).init(10, 10, ""),
			ard: true, creds: true,
		},
		{name: "3.7", in: script{}.version("003.007").u8(1, secARD).u8(ardScriptKey()...).u32(0).init(10, 10, ""), ard: true, creds: true},
		{name: "3.3", in: script{}.version("003.003").u32(secARD).u8(ardScriptKey()...).u32(0).init(10, 10, ""), ard: true, creds: true},
		{
			name: "the longest credentials", in: ard38().u8(ardScriptKey()...).u32(0).init(10, 10, ""), ard: true, creds: true,
			user: long[1:], password: strings.Repeat("b", ardField-1),
		},
		{
			// p = 7 in 128 bytes: a private key of as many bits as p has.
			name: "a small prime", in: ard38().ardKey(3, 128, seven, two).u32(0).init(10, 10, ""), ard: true, creds: true,
		},
		{
			// As macOS 26.6 sends it, by one report: the generator 5 and
			// 512 bytes. The modulus here is odd, not prime.
			name: "macOS 26's group", in: script{}.version("003.889").u8(3, 30, 33, 36).
				ardKey(5, 512, new(big.Int).Sub(new(big.Int).Lsh(one, 4096), one), new(big.Int).Lsh(one, 4000)).u32(0).init(10, 10, ""),
			ard: true, creds: true, sent: -5,
		},
		{
			name: "3.8 failure", in: ard38().u8(ardScriptKey()...).u32(1).str("Authentication failed"), ard: true, creds: true,
			sent: -1, is: ErrAuthFailed, err: `"Authentication failed"`,
		},
		{
			// macOS sends the failure in little-endian order.
			name: "macOS's failure", in: ard38().u8(ardScriptKey()...).u8(1, 0, 0, 0).str("Authentication failed"), ard: true, creds: true,
			sent: -1, is: ErrAuthFailed, err: `"Authentication failed"`,
		},
		{
			name: "3.7 failure", in: script{}.version("003.007").u8(1, secARD).u8(ardScriptKey()...).u32(1), ard: true, creds: true,
			sent: -1, is: ErrAuthFailed,
		},

		// Not allowed.
		{name: "not allowed", in: ard38(), sent: -2, is: ErrNoSecurityType, err: "set Config.ARD"},
		{
			name: "not allowed, macOS's types", in: script{}.version("003.889").u8(4, 30, 33, 36, 35), sent: -2,
			is: ErrNoSecurityType, err: "the server offers [30 33 36 35]; Apple Remote Desktop authentication (type 30) is not allowed",
		},
		{name: "3.3 not allowed", in: script{}.version("003.003").u32(secARD), sent: -2, is: ErrNoSecurityType, err: "set Config.ARD"},
		{name: "required TLS", in: ard38(), ard: true, requireTLS: true, sent: -2, is: ErrNoSecurityType, err: "offers [30]"},
		{name: "3.3 required TLS", in: script{}.version("003.003").u32(secARD), ard: true, requireTLS: true, sent: -2, err: "without TLS"},

		// The key's bounds, checked before anything else.
		{name: "key length 0", in: ard38().u8(0, 2, 0, 0), ard: true, sent: -3, err: "key of 0 bytes, not 128 to 1024"},
		{name: "key length 127", in: ard38().u8(0, 2, 0, 127), ard: true, sent: -3, err: "key of 127 bytes"},
		{name: "key length 1025", in: ard38().u8(0, 2, 4, 1), ard: true, sent: -3, err: "key of 1025 bytes"},
		{name: "key length 65535", in: ard38().u8(0, 2, 0xff, 0xff), ard: true, sent: -3, err: "key of 65535 bytes"},
		{name: "largest key never sent", in: ard38().u8(0, 2, 4, 0), ard: true, sent: -3, is: io.ErrUnexpectedEOF},
		{name: "key cut short", in: ard38().u8(ardScriptKey()[:4+255]...), ard: true, sent: -3, is: io.ErrUnexpectedEOF},
		{name: "no key", in: ard38().u8(0, 2, 0), ard: true, sent: -3, is: io.ErrUnexpectedEOF},
		{name: "prime 0", in: ard38().ardKey(2, 128, new(big.Int), seven), ard: true, sent: -3, err: "prime is 0"},
		{name: "prime 1", in: ard38().ardKey(2, 128, one, seven), ard: true, sent: -3, err: "prime is 1"},
		{name: "prime 3", in: ard38().ardKey(2, 128, three, two), ard: true, sent: -3, err: "generator 2 is not within"},
		{name: "generator 0", in: ard38().ardKey(0, 128, p, y), ard: true, sent: -3, err: "generator 0 is not within"},
		{name: "generator 1", in: ard38().ardKey(1, 128, p, y), ard: true, sent: -3, err: "generator 1 is not within"},
		{name: "generator p-1", in: ard38().ardKey(6, 128, seven, two), ard: true, sent: -3, err: "generator 6 is not within"},
		{name: "generator p", in: ard38().ardKey(7, 128, seven, two), ard: true, sent: -3, err: "generator 7 is not within"},
		{name: "generator beyond p", in: ard38().ardKey(0xffff, 128, seven, two), ard: true, sent: -3, err: "generator 65535 is not within"},
		{name: "public key 0", in: ard38().ardKey(2, 128, p, new(big.Int)), ard: true, sent: -3, err: "public key is not within"},
		{name: "public key 1", in: ard38().ardKey(2, 128, p, one), ard: true, sent: -3, err: "public key is not within"},
		{name: "public key p-1", in: ard38().ardKey(2, 128, p, pm1), ard: true, sent: -3, err: "public key is not within"},
		{name: "public key p", in: ard38().ardKey(2, 128, p, p), ard: true, sent: -3, err: "public key is not within"},
		{
			name: "public key beyond p", in: ard38().ardKey(2, 128, seven, new(big.Int).Sub(new(big.Int).Lsh(one, 1024), one)),
			ard: true, sent: -3, err: "public key is not within",
		},

		// Credentials that their fields cannot hold whole.
		{name: "long username", in: ard38().u8(ardScriptKey()...), ard: true, creds: true, user: long, sent: -3, err: "username of at most 63 bytes"},
		{
			name: "long password", in: ard38().u8(ardScriptKey()...), ard: true, creds: true, password: long + "secret",
			sent: -3, err: "password of at most 63 bytes",
		},
		{
			name: "NUL in the username", in: ard38().u8(ardScriptKey()...), ard: true, creds: true, user: "alice\x00mallory",
			sent: -3, err: "username with a NUL",
		},
		{
			name: "NUL in the password", in: ard38().u8(ardScriptKey()...), ard: true, creds: true, password: "hunter2\x00",
			sent: -3, err: "password with a NUL",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			user, password := cmp.Or(tt.user, "alice"), cmp.Or(tt.password, "hunter2")
			asked := false
			a := auth{
				plain: func() (string, string, error) {
					asked = true
					return user, password, nil
				},
				hasUser: true, hasPassword: true,
				ard: tt.ard, requireTLS: tt.requireTLS, rand: mrand.NewChaCha8([32]byte{}),
			}
			var sent bytes.Buffer
			_, err := handshake(bytes.NewReader(tt.in), &sent, a, limits{side: 100, pixels: 5000})
			got := sent.Len() - 12
			want := 1 + 2*ardField + 128 + 1
			switch tt.sent {
			case -1: // no ClientInit
				want--
			case -2: // nothing
				want = 0
			case -3: // the type alone
				want = 1
			case -5: // a 512-byte key
				want = 1 + 2*ardField + 512 + 1
			}
			if strings.HasPrefix(string(tt.in), "RFB 003.003") && want > 0 {
				want-- // the server chose the type
			}
			if got != want {
				t.Errorf("the client sent %d bytes after its version, want %d", got, want)
			}
			if asked != tt.creds {
				t.Errorf("credentials asked for: %v, want %v", asked, tt.creds)
			}
			if tt.err == "" && tt.is == nil {
				if err != nil {
					t.Fatalf("handshake = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("handshake succeeded, want an error")
			}
			if tt.err != "" && !strings.Contains(err.Error(), tt.err) {
				t.Errorf("handshake = %v, want an error with %q", err, tt.err)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("handshake = %v, want %v", err, tt.is)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "aaaa") {
				t.Errorf("the error shows a credential: %v", err)
			}
		})
	}
}

func TestARD(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string // the server's, 3.8 if empty
		types   []byte
		subs    []uint32 // VeNCrypt's
		cfg     Config
		answer  []string // the owner's answers, if asked
		prompt  []backend.PromptField
		want    byte   // the type used; 0 for an error
		sent    string // what Apple Remote Desktop sent, if it ran
		err     error
		errText string // in the error
	}{
		{
			name: "configured", types: []byte{secARD}, cfg: Config{ARD: true, Username: "alice", Password: "hunter2"},
			want: secARD, sent: "alice:hunter2",
		},
		{
			name: "macOS's types", version: "003.889", types: []byte{30, 33, 36, 35},
			cfg: Config{ARD: true, Username: "alice", Password: "hunter2"}, want: secARD, sent: "alice:hunter2",
		},
		{
			name: "asks for both", types: []byte{secARD}, cfg: Config{ARD: true},
			answer: []string{"alice", "hunter2"}, prompt: []backend.PromptField{{Label: "Username"}, {Label: "Password", Secret: true}},
			want: secARD, sent: "alice:hunter2",
		},
		{
			name: "asks for the password", types: []byte{secARD}, cfg: Config{ARD: true, Username: "alice"},
			answer: []string{"hunter2"}, prompt: []backend.PromptField{{Label: "Password", Secret: true}},
			want: secARD, sent: "alice:hunter2",
		},
		{
			name: "asks for the username", types: []byte{secARD}, cfg: Config{ARD: true, Password: "hunter2"},
			answer: []string{"alice"}, prompt: []backend.PromptField{{Label: "Username"}},
			want: secARD, sent: "alice:hunter2",
		},
		{
			name: "a long answer", types: []byte{secARD}, cfg: Config{ARD: true, Username: "alice"},
			answer: []string{strings.Repeat("hunter2", 10)}, prompt: []backend.PromptField{{Label: "Password", Secret: true}},
			errText: "password of at most 63 bytes",
		},
		{
			name: "wrong password", types: []byte{secARD}, cfg: Config{ARD: true, Username: "alice", Password: "hunter3"},
			sent: "alice:hunter3", err: ErrAuthFailed, errText: `"Authentication failed"`,
		},
		{
			name: "wrong password, 3.7", version: "003.007", types: []byte{secARD},
			cfg: Config{ARD: true, Username: "alice", Password: "hunter3"}, sent: "alice:hunter3", err: ErrAuthFailed,
		},

		// Not allowed.
		{
			name: "not allowed", types: []byte{secARD}, cfg: Config{Username: "alice", Password: "hunter2"},
			err: ErrNoSecurityType, errText: "set Config.ARD",
		},
		{
			name: "not allowed, macOS's types", version: "003.889", types: []byte{30, 33, 36, 35},
			cfg: Config{Username: "alice", Password: "hunter2"}, err: ErrNoSecurityType, errText: "set Config.ARD",
		},
		{
			name: "not allowed, with VNC Authentication", types: []byte{secARD, secVNCAuth},
			cfg: Config{Username: "alice", Password: "hunter2"}, want: secVNCAuth,
		},

		// The choice among the types offered.
		{
			name: "VeNCrypt over ARD", types: []byte{secARD, secVeNCrypt}, subs: []uint32{subX509Plain},
			cfg:  Config{ARD: true, Username: "alice", Password: "hunter2", TLS: TLS{InsecureSkipVerify: true}},
			want: secVeNCrypt,
		},
		{
			name: "ARD over VNC Authentication with a username", types: []byte{secVNCAuth, secARD},
			cfg: Config{ARD: true, Username: "alice", Password: "hunter2"}, want: secARD, sent: "alice:hunter2",
		},
		{
			name: "VNC Authentication over ARD without a username", types: []byte{secARD, secVNCAuth},
			cfg: Config{ARD: true, Password: "hunter2"}, want: secVNCAuth,
		},
		{
			name: "ARD over None with a username", types: []byte{secNone, secARD},
			cfg: Config{ARD: true, Username: "alice", Password: "hunter2"}, want: secARD, sent: "alice:hunter2",
		},
		{name: "None over ARD without credentials", types: []byte{secARD, secNone}, cfg: Config{ARD: true}, want: secNone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			var log logBuffer
			opts := serverOptions{
				version: tt.version, types: tt.types, vencrypt: tt.subs, cert: newPKI(t).server,
				user: "alice", password: "hunter2",
			}
			cfg := tt.cfg
			cfg.Logger = log.logger()
			sink := &backendtest.Sink{}
			if tt.answer != nil {
				sink.Answer = answers(tt.answer...)
			}
			srv, _, r := runAgainst(t, opts, cfg, sink)
			if tt.want == 0 {
				err := r.wait(t)
				if err == nil || tt.err != nil && !errors.Is(err, tt.err) || !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("Run = %v, want %v with %q", err, tt.err, tt.errText)
				}
				if strings.Contains(err.Error(), "hunter") {
					t.Errorf("the error shows the password: %v", err)
				}
				if errors.Is(tt.err, ErrAuthFailed) && srv.Failures() != 1 {
					t.Errorf("%d failures, want 1", srv.Failures())
				}
			} else {
				c := srv.accept(t)
				if c.security != tt.want {
					t.Errorf("security type %d, want %d", c.security, tt.want)
				}
				if err := r.stop(t); !errors.Is(err, context.Canceled) {
					t.Errorf("Run = %v, want context.Canceled", err)
				}
			}
			if got := srv.ARDs(); tt.sent != "" && !slices.Equal(got, []string{tt.sent}) || tt.sent == "" && len(got) > 0 {
				t.Errorf("Apple Remote Desktop sent %q, want %q", got, tt.sent)
			}
			prompts := sink.Prompts()
			if len(prompts) != min(len(tt.prompt), 1) {
				t.Fatalf("%d prompts, want %d", len(prompts), min(len(tt.prompt), 1))
			}
			if len(prompts) == 1 && !slices.Equal(prompts[0].Fields, tt.prompt) {
				t.Errorf("prompt %+v, want fields %+v", prompts[0], tt.prompt)
			}
			logged := log.String()
			if warned := strings.Contains(logged, "Apple Remote Desktop"); warned != (tt.want == secARD) {
				t.Errorf("warned about Apple Remote Desktop: %v, want %v; log:\n%s", warned, tt.want == secARD, logged)
			}
			if strings.Contains(logged, "hunter") {
				t.Errorf("the log shows the password:\n%s", logged)
			}
		})
	}
}

func TestARDErrors(t *testing.T) {
	// The owner's error, and one of the random source, end the
	// handshake before anything is sent.
	errOwner := errors.New("the owner left")
	errRand := errors.New("no entropy")
	key := []byte(ardScriptKey())
	for _, tt := range []struct {
		name  string
		creds func() (string, string, error)
		rnd   io.Reader
		want  error
	}{
		{"prompt", func() (string, string, error) { return "", "", errOwner }, rand.Reader, errOwner},
		{"private key", func() (string, string, error) { return "alice", "hunter2", nil }, iotest.ErrReader(errRand), errRand},
		{
			"padding", func() (string, string, error) { return "alice", "hunter2", nil },
			io.MultiReader(io.LimitReader(rand.Reader, 64), iotest.ErrReader(errRand)), errRand,
		},
	} {
		var sent bytes.Buffer
		if err := ardAuth(bytes.NewReader(key), &sent, tt.rnd, tt.creds); !errors.Is(err, tt.want) {
			t.Errorf("%s: ardAuth = %v, want %v", tt.name, err, tt.want)
		}
		if sent.Len() > 0 {
			t.Errorf("%s: sent %d bytes", tt.name, sent.Len())
		}
	}
}

func TestARDNew(t *testing.T) {
	// ARD runs without TLS, so it cannot be allowed while TLS is
	// required.
	p := newPKI(t)
	for _, c := range []TLS{
		{Require: true},
		{RootCAs: p.roots},
		{Pins: []string{certFingerprint(p.leaf)}},
	} {
		if _, err := New(Config{Addr: "mac.example", ARD: true, TLS: c}); err == nil || !strings.Contains(err.Error(), "ARD") {
			t.Errorf("New(ARD, %+v) = %v, want an error about ARD", c, err)
		}
	}
	if _, err := New(Config{Addr: "mac.example", ARD: true, TLS: TLS{InsecureSkipVerify: true}}); err != nil {
		t.Errorf("New(ARD) = %v", err)
	}
}

func TestARDAllocations(t *testing.T) {
	// A key length beyond the bound allocates nothing for the key, and
	// the largest allows 2 KiB.
	a := auth{plain: func() (string, string, error) { return "alice", "hunter2", nil }, ard: true}
	for _, in := range []script{
		script{}.version("003.008").u8(1, secARD).u8(0, 2, 0xff, 0xff).u8(make([]byte, 64)...),
		script{}.version("003.008").u8(1, secARD).u8(0, 2, 4, 0), // a key that never comes
	} {
		got := allocated(func() { handshake(bytes.NewReader(in), io.Discard, a, defaultLimits) })
		if got > 16<<10 {
			t.Errorf("handshake of %q allocated %d bytes", in, got)
		}
	}
}

// BenchmarkARD measures the client's side of the exchange for keys of
// 128, 512 and 1024 bytes, the last the largest accepted: its private
// key, two exponentiations and the encryption. The modulus is odd but
// not prime, which costs the same.
func BenchmarkARD(b *testing.B) {
	for _, n := range []int{minARDKey, 512, maxARDKey} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			buf := make([]byte, n)
			rand.Read(buf)
			buf[0] |= 0x80
			buf[n-1] |= 1
			p := new(big.Int).SetBytes(buf)
			y, _ := rand.Int(rand.Reader, p)
			g := big.NewInt(2)
			for b.Loop() {
				x, err := ardPrivateKey(rand.Reader, p)
				if err != nil {
					b.Fatal(err)
				}
				var plain [2 * ardField]byte
				ardResponse(n, g, p, y, x, "alice", "hunter2", &plain)
			}
		})
	}
}
