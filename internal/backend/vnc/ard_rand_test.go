// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"math/big"
	"testing"
)

// TestARDFreshRandomness runs the handshake as Run does, with no random
// source of the test's own, so that it draws from crypto/rand: each
// connection must send a private key's worth of fresh randomness. The
// other tests feed a fixed source, so that a default source that always
// returns the same bytes would pass them.
func TestARDFreshRandomness(t *testing.T) {
	p := rfc2409Group2
	y := new(big.Int).Exp(two, big.NewInt(0x1234567), p)
	in := script{}.version("003.008").u8(1, secARD).ardKey(2, 128, p, y).u32(0).init(10, 10, "")
	seen := map[string]bool{}
	for range 3 {
		a := auth{plain: func() (string, string, error) { return "alice", "hunter2", nil }, ard: true}
		var out bytes.Buffer
		if _, err := handshake(bytes.NewReader(in), &out, a, defaultLimits); err != nil {
			t.Fatal(err)
		}
		const head = len("RFB 003.008\n") + 1 // the version, then the type chosen
		resp := out.Bytes()[head : head+2*ardField+128]
		pub := new(big.Int).SetBytes(resp[2*ardField:])
		// A private key of 256 random bits makes a public key that no
		// small power of the generator equals.
		for e := int64(2); e < 1<<12; e++ {
			if new(big.Int).Exp(two, big.NewInt(e), p).Cmp(pub) == 0 {
				t.Fatalf("the client's public key is 2^%d", e)
			}
		}
		if seen[string(resp)] {
			t.Fatal("two connections sent the same response")
		}
		seen[string(resp)] = true
	}
}
