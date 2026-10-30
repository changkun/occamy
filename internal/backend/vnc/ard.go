// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// Apple Remote Desktop authentication, the security type 30 with which
// macOS Screen Sharing logs in its users' accounts, as libvncclient
// (HandleARDAuth), noVNC (_negotiateARDAuth) and TigerVNC's viewer
// (CSecurityDH) implement it. The server sends a Diffie-Hellman group
// and its public key; the client answers with the username and the
// password, encrypted with AES-128 in ECB mode under the MD5 of the
// shared secret, and with its own public key. SecurityResult follows.
//
// Nothing authenticates the server's key, so anyone between the backend
// and the server can run the exchange with each of them and read the
// password, which on macOS is the account's own; the session that
// follows is not encrypted either. The backend therefore chooses type 30
// only when Config.ARD allows it, and never when TLS is required.

import (
	"crypto/aes"
	"crypto/md5" // #nosec G501 -- Apple Remote Desktop keys AES with MD5 by definition
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"strings"
)

// secARD is the Apple Remote Desktop security type.
const secARD = 30

const (
	// minARDKey and maxARDKey bound the length of the server's prime and
	// public key, in bytes, as TigerVNC's viewer bounds them: from a
	// 1024-bit group, which macOS sent in 2017, past the 4096-bit one
	// reported from macOS 26, to an 8192-bit one.
	minARDKey = 128
	maxARDKey = 1024
	// ardField is the size of each of the two fields of the credentials,
	// the username's and the password's. Each ends in a NUL, so it holds
	// a credential of 63 bytes at most.
	ardField = 64
	// ardExponent is the size of the private key, in bits, for a prime of
	// more bits. The shared secret becomes an AES-128 key, so a longer
	// key would buy nothing, and it bounds the exponentiations that an
	// 8192-bit group costs.
	ardExponent = 256
)

var (
	one   = big.NewInt(1)
	two   = big.NewInt(2)
	three = big.NewInt(3)
)

// ardAuth runs Apple Remote Desktop authentication after the client has
// chosen it. It checks what the server sent before it asks for the
// credentials, as TigerVNC does, so that a server that sends a bad key
// costs the owner no prompt. rnd makes the private key and the bytes
// that fill the credentials' fields.
func ardAuth(r io.Reader, w io.Writer, rnd io.Reader, creds func() (string, string, error)) error {
	var b [4]byte
	if err := read(r, b[:]); err != nil {
		return fmt.Errorf("vnc: read the Apple Remote Desktop key: %w", err)
	}
	g := new(big.Int).SetBytes(b[:2])
	n := int(binary.BigEndian.Uint16(b[2:]))
	if n < minARDKey || n > maxARDKey {
		return fmt.Errorf("vnc: an Apple Remote Desktop key of %d bytes, not %d to %d", n, minARDKey, maxARDKey)
	}
	key := make([]byte, 2*n)
	if err := read(r, key); err != nil {
		return fmt.Errorf("vnc: read the Apple Remote Desktop key: %w", err)
	}
	p := new(big.Int).SetBytes(key[:n])
	y := new(big.Int).SetBytes(key[n:])
	if err := checkARDGroup(g, p, y); err != nil {
		return err
	}
	user, password, err := creds()
	if err != nil {
		return err
	}
	if err := checkARDCredential("username", user); err != nil {
		return err
	}
	if err := checkARDCredential("password", password); err != nil {
		return err
	}
	x, err := ardPrivateKey(rnd, p)
	if err != nil {
		return err
	}
	defer clear(x.Bits())
	var plain [2 * ardField]byte
	if _, err := io.ReadFull(rnd, plain[:]); err != nil {
		return fmt.Errorf("vnc: %w", err)
	}
	out := ardResponse(n, g, p, y, x, user, password, &plain)
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("vnc: %w", err)
	}
	return nil
}

// checkARDGroup checks the server's generator g, prime p and public key
// y: p must be more than 1, and g and y within [2, p-2], as OpenSSL
// checks a public key, so that neither is 0, 1 or p-1, which would make
// the shared secret one that anyone can tell. p is not checked to be
// prime, which would cost as much as the exchange: a server that sends
// a bad group already holds the password it is sent.
func checkARDGroup(g, p, y *big.Int) error {
	if p.Cmp(one) <= 0 {
		return fmt.Errorf("vnc: the server's Apple Remote Desktop prime is %v", p)
	}
	top := new(big.Int).Sub(p, two)
	if g.Cmp(two) < 0 || g.Cmp(top) > 0 {
		return fmt.Errorf("vnc: the server's Apple Remote Desktop generator %v is not within [2, p-2]", g)
	}
	if y.Cmp(two) < 0 || y.Cmp(top) > 0 {
		return fmt.Errorf("vnc: the server's Apple Remote Desktop public key is not within [2, p-2]")
	}
	return nil
}

// checkARDCredential refuses a username or password that its field
// cannot hold whole: longer than 63 bytes, or with a NUL before its end,
// where the server would cut it. Neither is cut short instead, as
// libvncclient and noVNC do, since a login with less than the owner gave
// fails, or succeeds as someone else. The error does not show it.
func checkARDCredential(what, s string) error {
	if len(s) >= ardField {
		return fmt.Errorf("vnc: Apple Remote Desktop takes a %s of at most %d bytes", what, ardField-1)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("vnc: Apple Remote Desktop cannot send a %s with a NUL byte", what)
	}
	return nil
}

// ardPrivateKey returns a private key for the prime p, uniform in [2,
// 2^256+1], or in [2, p-2] for a prime of 256 bits or fewer. p must be at
// least 4, which checkARDGroup ensures.
func ardPrivateKey(rnd io.Reader, p *big.Int) (*big.Int, error) {
	limit := new(big.Int).Lsh(one, ardExponent)
	if pm3 := new(big.Int).Sub(p, three); pm3.Cmp(limit) < 0 {
		limit = pm3
	}
	x, err := rand.Int(rnd, limit)
	if err != nil {
		return nil, fmt.Errorf("vnc: the Apple Remote Desktop private key: %w", err)
	}
	return x.Add(x, two), nil
}

// ardResponse returns what the client sends for the private key x: the
// credentials, encrypted under the key that ardKey derives, then the
// client's public key, g^x mod p in n bytes. plain holds the random
// bytes that fill the credentials' fields; it is cleared.
func ardResponse(n int, g, p, y, x *big.Int, user, password string, plain *[2 * ardField]byte) []byte {
	key := ardKey(n, p, y, x)
	ardCredentials(plain, user, password)
	out := make([]byte, 2*ardField+n)
	ct := ardEncrypt(&key, plain)
	copy(out, ct[:])
	new(big.Int).Exp(g, x, p).FillBytes(out[2*ardField:])
	return out
}

// ardKey returns the AES key: the MD5 of the shared secret, y^x mod p,
// in n bytes, with the leading zeros that libvncclient, noVNC and
// TigerVNC's viewer keep.
func ardKey(n int, p, y, x *big.Int) [md5.Size]byte {
	k := new(big.Int).Exp(y, x, p)
	secret := make([]byte, n)
	k.FillBytes(secret)
	clear(k.Bits())
	key := md5.Sum(secret) // #nosec G401 -- see the import
	clear(secret)
	return key
}

// ardCredentials writes the username and the password, each with its
// NUL, into the first and the second field of plain, leaving the bytes
// after them as they are. Both must fit, which checkARDCredential
// checks.
func ardCredentials(plain *[2 * ardField]byte, user, password string) {
	plain[copy(plain[:ardField], user)] = 0
	plain[ardField+copy(plain[ardField:], password)] = 0
}

// ardEncrypt encrypts plain with AES-128 in ECB mode, each block on its
// own, as Apple Remote Desktop does, and clears the key and plain. The
// cipher's own copy of the key, in crypto/aes, cannot be cleared.
func ardEncrypt(key *[md5.Size]byte, plain *[2 * ardField]byte) [2 * ardField]byte {
	block, err := aes.NewCipher(key[:])
	clear(key[:])
	if err != nil {
		panic(err) // the key has the right size
	}
	var out [2 * ardField]byte
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	clear(plain[:])
	return out
}
