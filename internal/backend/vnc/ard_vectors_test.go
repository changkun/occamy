// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// ardVectors are Apple Remote Desktop exchanges computed apart from this
// package, by a review of it, with Python's pow and hashlib and OpenSSL,
// from the wire format alone:
//
//	y, X, K = pow(g, xs, p), pow(g, x, p), pow(y, x, p)
//	key = hashlib.md5(K.to_bytes(n, "big")).digest()
//	plain = user + b"\0" + pad[len(user)+1:64] + password + b"\0" + pad[...]
//	client = openssl enc -aes-128-ecb -nopad -K key (plain) + X.to_bytes(n, "big")
//
// rnd is the stream that the client draws from: the bytes from which
// crypto/rand.Int makes the private key, x = int(bytes) + 2 (32 bytes
// for a prime of more than 256 bits, one byte masked to 2 bits for p =
// 7), then the 128 bytes that pad the credentials' fields.
var ardVectors = []struct {
	name           string
	server         string // the generator, n, p and y as the server sends them
	rnd            string
	user, password string
	key            string // the MD5 of the secret, for the record
	client         string // the ciphertext and the client's public key
}{
	{
		// A private key searched for so that both the secret and the
		// client's public key start with a zero byte.
		name:   "RFC 2409 group 2, the secret and X led by a zero byte",
		server: "00020080ffffffffffffffffc90fdaa22168c234c4c6628b80dc1cd129024e088a67cc74020bbea63b139b22514a08798e3404ddef9519b3cd3a431b302b0a6df25f14374fe1356d6d51c245e485b576625e7ec6f44c42e9a637ed6b0bff5cb6f406b7edee386bfb5a899fa5ae9f24117c4b1fe649286651ece65381ffffffffffffffff15744a5e7412ff250fcaf365691478cc4722331d7e1ed226c367a531292b78a8a23f10db8377cb5fb6695188452212e23017188f91c935e6f3cc6086cdeeb683783502869646ca14d0f8d44de7990f32b230b174978dd111de44adf0b7d447ffdcd7ee311e26929afb881f61a62b4739262db6ec4e624cfd743143468bdae86f",
		rnd:    "81a429cb17250b7762032b7f63a5de97ba57c46d526fab81ca03883eadda14b7a5a2abb0b9868f949d9ae3e8f1fec7ccd5d2db2029363f040d0a1318616e777c45424b5059a6afb4bdba8388919ee7ecf5f2fbc0c9d6df242d2a3338010e171c65626b7079464f545d5aa3a8b1be878c95929be0e9f6ffc4cdcad3d8212e373c05020b1019666f747d7a4348515ea7acb5b2bb8089969fe4edeaf3f8c1ced7dc",
		user:   "alice", password: "correct horse",
		key:    "bdfc06e1a35ff3c8a9ee872c34ca9e0b",
		client: "17afa27cb45aa2c909072e5cece763236630c6a97174b9e7177d8a3e5169b4b5f198d14d0b64e4abb1ec2a66afd7bdaffc17c2aed826f1e8f4222d302a32f2ab0590b17a04544f3c0d86c8a643a33b0fae3de614a0509cdc11eff48d195d926e69230d5b44d0ddd995e7281d6aa1121f6878938ac36deb267e76c4052658ef9b00531ca74ca4a0025f936d6e6f0fba173ace501321dac9dcbdacacaa6547cd30bb6a37ff4fce8cf645e810ce20e7b9aece609931b0a921f83a4094c4cd27660c59684a97decacf2a232054b24dbc5b8caeba3d8693b356a8908677d0e2a4bdc60caaefbc5c0a5b0f652d01e5ee78d8b26e02acddcea8e1bb60f0dee765a55261",
	},
	{
		// The same prime in a field of 129 bytes, with the generator 5.
		name:   "an odd key length of 129 bytes",
		server: "0005008100ffffffffffffffffc90fdaa22168c234c4c6628b80dc1cd129024e088a67cc74020bbea63b139b22514a08798e3404ddef9519b3cd3a431b302b0a6df25f14374fe1356d6d51c245e485b576625e7ec6f44c42e9a637ed6b0bff5cb6f406b7edee386bfb5a899fa5ae9f24117c4b1fe649286651ece65381ffffffffffffffff00a2e8a194d5fe3be2d09259469253f68734a28f74f425a8a2f5dadeb5835591b72b2ce736ddae07b604bd1f697ddce2d7eea007f5dec62c2672e159ca7716a8cccecd7b81cf9b626f06eba23b881921be0d871c970f831d1d2c7711d3c9c56577399d94fc6b1cd2df7bec33b977803104198b1321b810f07ae05283697b23e1fe",
		rnd:    "9bdbb1d9aa4021a99b4059ee35fbc67c295d73f0096d143d91cac5292527a08ea5a2abb0b9868f949d9ae3e8f1fec7ccd5d2db2029363f040d0a1318616e777c45424b5059a6afb4bdba8388919ee7ecf5f2fbc0c9d6df242d2a3338010e171c65626b7079464f545d5aa3a8b1be878c95929be0e9f6ffc4cdcad3d8212e373c05020b1019666f747d7a4348515ea7acb5b2bb8089969fe4edeaf3f8c1ced7dc",
		user:   "bob", password: "päss",
		key:    "90e845b6c7e741cf4b67266798264343",
		client: "89e55b452be4748f4a97dfb4fafb60879414d7be2c575a1d9470d7194f4dcdcfa25dba9f739d52b5c61bf9f7d61dc263e6501b7b8bcef0303d9b94c32be16d403b0d70f1c1856cdf54761089c758d8a8058bf30600f2b44107b52b0a32a6a3298ff81307239a447752ac453f08f3dc23eac0da328b4f1a6ba843f95b7877ff5400e9f8cdec65bd18ed7663a4893e314ca77a4be4603ae1962622eda972a80038938238a5e00c1388d3a3acaf47f935cf3cc79f6ce4b66abecca9a5ba47bba14291517297eafc71d667e2e1070c4cef8fdf561f06b06b9eca71c549ee33996d3d782aa8c58ebb3517f2a737f84fcb4457d4fa0d012bd584cb66614289301fbf6d2b",
	},
	{
		// p = 7 and g = 3 in 128 bytes; the byte 0xfe makes x = 4. The
		// secret and X have 127 leading zero bytes.
		name:   "p = 7 in 128 bytes",
		server: "0003008000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000070000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000005",
		rnd:    "fea5a2abb0b9868f949d9ae3e8f1fec7ccd5d2db2029363f040d0a1318616e777c45424b5059a6afb4bdba8388919ee7ecf5f2fbc0c9d6df242d2a3338010e171c65626b7079464f545d5aa3a8b1be878c95929be0e9f6ffc4cdcad3d8212e373c05020b1019666f747d7a4348515ea7acb5b2bb8089969fe4edeaf3f8c1ced7dc",
		user:   "u", password: "",
		key:    "a7baf790759b618648008baa69b28278",
		client: "947dc3cc9891b94f1c35df4bac46e6b3d17b8664de3f2a565efd9be18fefcd4f78345951d5ede4bc7a4a8a5c3678e94336bee66b706af82e33819bafb7d34de90ec78c660d68c15ab54341adbe0d1556d52bace70fdea3a400d33d478eb9caeab77441382719649d545046d6b314191bead935516ee85250f7e1aa5a08d3aac30000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000004",
	},
	{
		// The longest credentials that fit, with no padding after the NUL.
		name:   "63-byte credentials",
		server: "00020080ffffffffffffffffc90fdaa22168c234c4c6628b80dc1cd129024e088a67cc74020bbea63b139b22514a08798e3404ddef9519b3cd3a431b302b0a6df25f14374fe1356d6d51c245e485b576625e7ec6f44c42e9a637ed6b0bff5cb6f406b7edee386bfb5a899fa5ae9f24117c4b1fe649286651ece65381ffffffffffffffff15744a5e7412ff250fcaf365691478cc4722331d7e1ed226c367a531292b78a8a23f10db8377cb5fb6695188452212e23017188f91c935e6f3cc6086cdeeb683783502869646ca14d0f8d44de7990f32b230b174978dd111de44adf0b7d447ffdcd7ee311e26929afb881f61a62b4739262db6ec4e624cfd743143468bdae86f",
		rnd:    "72e382be84f68e7c86cec6a55e1ada4dd6a59bc4174ab15ca25752f4cec7e880a5a2abb0b9868f949d9ae3e8f1fec7ccd5d2db2029363f040d0a1318616e777c45424b5059a6afb4bdba8388919ee7ecf5f2fbc0c9d6df242d2a3338010e171c65626b7079464f545d5aa3a8b1be878c95929be0e9f6ffc4cdcad3d8212e373c05020b1019666f747d7a4348515ea7acb5b2bb8089969fe4edeaf3f8c1ced7dc",
		user:   "UUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUUU", password: "PPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPP",
		key:    "21b8eec9412d725c48b2d3a488540a5a",
		client: "58ba189feeffd2148debf337eab623d158ba189feeffd2148debf337eab623d158ba189feeffd2148debf337eab623d1b2f5f9162e59d56671a5997ede872d494c24db59a1ca4df0ae937ad48f8ec69b4c24db59a1ca4df0ae937ad48f8ec69b4c24db59a1ca4df0ae937ad48f8ec69b734e0a437eb79ef2b60446ef184bce3cfc2796737860ae211abfc711bbfcf0ca57a045253196c35166a97568a0625b99e28cd9bfd2e611ee5846ad0912cc484abf986373804468018045f912ef8a456bcc934f6bc0197dad0ce5e73c590d2acb5597756248cc9ef46cbd958a0d26c811f6f1f768c216844f9ce4aff9d6a5ac28cdae7c6c7d0deeb9b20b55f36d7b37bf",
	},
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestARDVectors runs ardVectors through the handshake in each version,
// with the server's bytes written out by hand and none of ard.go's
// helpers, and compares every byte that the client writes.
func TestARDVectors(t *testing.T) {
	for _, v := range ardVectors {
		for _, version := range []string{"003.008", "003.007", "003.889", "003.003"} {
			t.Run(v.name+"/"+version, func(t *testing.T) {
				var in []byte
				in = append(in, "RFB "+version+"\n"...)
				if version == "003.003" {
					in = binary.BigEndian.AppendUint32(in, secARD)
				} else {
					in = append(in, 1, secARD)
				}
				in = append(in, unhex(t, v.server)...)
				in = binary.BigEndian.AppendUint32(in, 0) // SecurityResult: OK
				in = binary.BigEndian.AppendUint16(in, 10)
				in = binary.BigEndian.AppendUint16(in, 10)
				in = append(in, make([]byte, 16)...)
				in = binary.BigEndian.AppendUint32(in, 3)
				in = append(in, "Mac"...)

				a := auth{
					plain: func() (string, string, error) { return v.user, v.password, nil },
					ard:   true,
					rand:  bytes.NewReader(unhex(t, v.rnd)),
				}
				var out bytes.Buffer
				init, err := handshake(bytes.NewReader(in), &out, a, defaultLimits)
				if err != nil {
					t.Fatalf("handshake = %v", err)
				}
				if init.security != secARD || init.name != "Mac" {
					t.Fatalf("init = %+v", init)
				}
				reply := "003.008"
				if version == "003.007" || version == "003.003" {
					reply = version
				}
				var want []byte
				want = append(want, "RFB "+reply+"\n"...)
				if version != "003.003" {
					want = append(want, secARD)
				}
				want = append(want, unhex(t, v.client)...)
				want = append(want, 1) // ClientInit, shared
				if !bytes.Equal(out.Bytes(), want) {
					t.Errorf("the client wrote\n%x\nwant\n%x", out.Bytes(), want)
				}
			})
		}
	}
}
