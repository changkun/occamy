// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"crypto/des" // #nosec G502 -- VNC Authentication is DES by definition (RFC 6143, section 7.2.2)
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math/bits"
	"slices"
	"unicode/utf8"
)

// Security types (RFC 6143, section 7.2).
const (
	secInvalid = 0
	secNone    = 1
	secVNCAuth = 2
)

const (
	// maxName bounds the desktop name in ServerInit.
	maxName = 4096
	// maxReason bounds the part of a failure's reason string that is
	// read; the server closes the connection after it anyway.
	maxReason = 4096
)

// limits bound the remote screen.
type limits struct {
	side   int // the largest width or height
	pixels int // the largest area
}

var defaultLimits = limits{side: MaxSide, pixels: MaxPixels}

// check returns an error if a screen of w×h is empty or exceeds l.
func (l limits) check(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("vnc: the server reported an empty screen of %d×%d", w, h)
	}
	if w > l.side || h > l.side || w*h > l.pixels {
		return fmt.Errorf("vnc: the server's screen of %d×%d exceeds the limits of %d pixels a side and %d in all", w, h, l.side, l.pixels)
	}
	return nil
}

// serverInit is what the handshake learns about the server.
type serverInit struct {
	version  int  // the minor version spoken: 3, 7 or 8
	security byte // the security type used
	size     image.Point
	name     string
}

// auth is what the security handshake needs from the backend.
type auth struct {
	// password returns the password for VNC Authentication, and plain
	// the username and password for VeNCrypt's Plain, asking the owner
	// for what the configuration lacks.
	password func() (string, error)
	plain    func() (user, password string, err error)
	// hasUser and hasPassword say what the configuration holds. They
	// choose between the types a server offers, so that nobody is asked
	// for credentials that a server does not need.
	hasUser, hasPassword bool
	// startTLS runs a TLS handshake on the connection, after which the
	// handshake's reads and writes go through TLS. Nil leaves VeNCrypt
	// out.
	startTLS func() error
	// requireTLS refuses every security type but VeNCrypt's X.509
	// subtypes.
	requireTLS bool
}

// handshake runs the handshake from ProtocolVersion to ServerInit (RFC
// 6143, sections 7.1 to 7.3), asking for the screen to be shared with
// other clients. r and w are the connection, whose two directions
// a.startTLS replaces when VeNCrypt runs TLS.
func handshake(r io.Reader, w io.Writer, a auth, lim limits) (serverInit, error) {
	var init serverInit
	var b [24]byte
	if err := read(r, b[:12]); err != nil {
		return init, fmt.Errorf("vnc: read the protocol version: %w", err)
	}
	v, err := parseVersion(b[:12])
	if err != nil {
		return init, err
	}
	init.version = v
	if _, err := w.Write(fmt.Appendf(nil, "RFB 003.%03d\n", v)); err != nil {
		return init, fmt.Errorf("vnc: %w", err)
	}
	if init.security, err = security(r, w, v, a); err != nil {
		return init, err
	}
	if _, err := w.Write([]byte{1}); err != nil { // ClientInit, shared
		return init, fmt.Errorf("vnc: %w", err)
	}
	if err := read(r, b[:24]); err != nil {
		return init, fmt.Errorf("vnc: read ServerInit: %w", err)
	}
	width, height := int(binary.BigEndian.Uint16(b[0:])), int(binary.BigEndian.Uint16(b[2:]))
	if err := lim.check(width, height); err != nil {
		return init, err
	}
	init.size = image.Pt(width, height)
	// The server's own pixel format, b[4:20], does not matter: the
	// backend sets its own before asking for pixels.
	n := binary.BigEndian.Uint32(b[20:])
	if n > maxName {
		return init, fmt.Errorf("vnc: a desktop name of %d bytes exceeds %d", n, maxName)
	}
	name := make([]byte, n)
	if err := read(r, name); err != nil {
		return init, fmt.Errorf("vnc: read the desktop name: %w", err)
	}
	init.name = text(name)
	return init, nil
}

// parseVersion parses a ProtocolVersion message and returns the minor
// version of RFB 3 to reply with. 3.7 is 3.7; 3.8 and anything later,
// such as Apple's 3.889 or 4.x, is answered with 3.8, as TigerVNC does;
// any other 3.x is 3.3, as RFC 6143 says.
func parseVersion(b []byte) (int, error) {
	if len(b) != 12 || string(b[:4]) != "RFB " || b[7] != '.' || b[11] != '\n' {
		return 0, fmt.Errorf("vnc: not an RFB server: it sent %q", b)
	}
	major, ok1 := decimal(b[4:7])
	minor, ok2 := decimal(b[8:11])
	switch {
	case !ok1 || !ok2:
		return 0, fmt.Errorf("vnc: not an RFB server: it sent %q", b)
	case major < 3:
		return 0, fmt.Errorf("vnc: unsupported protocol version %d.%d", major, minor)
	case major > 3, minor >= 8:
		return 8, nil
	case minor == 7:
		return 7, nil
	}
	return 3, nil
}

// decimal parses three ASCII digits.
func decimal(b []byte) (int, bool) {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// security runs the security handshake for the minor version v and
// returns the type used.
func security(r io.Reader, w io.Writer, v int, a auth) (byte, error) {
	var t byte
	if v == 3 {
		// The server decides (RFC 6143, appendix A.1).
		var b [4]byte
		if err := read(r, b[:]); err != nil {
			return 0, fmt.Errorf("vnc: read the security type: %w", err)
		}
		switch st := binary.BigEndian.Uint32(b[:]); st {
		case secInvalid:
			return 0, refused(r)
		case secNone, secVNCAuth:
			t = byte(st)
		default:
			return 0, fmt.Errorf("%w: the server chose type %d", ErrNoSecurityType, st)
		}
		if a.requireTLS {
			return 0, fmt.Errorf("%w: the server chose type %d, without TLS", ErrNoSecurityType, t)
		}
	} else {
		var b [256]byte
		if err := read(r, b[:1]); err != nil {
			return 0, fmt.Errorf("vnc: read the security types: %w", err)
		}
		if b[0] == 0 {
			return 0, refused(r)
		}
		offered := b[1 : 1+int(b[0])]
		if err := read(r, offered); err != nil {
			return 0, fmt.Errorf("vnc: read the security types: %w", err)
		}
		if t = choose(offered, a); t == secInvalid {
			return 0, fmt.Errorf("%w: the server offers %v", ErrNoSecurityType, offered)
		}
		if _, err := w.Write([]byte{t}); err != nil {
			return 0, fmt.Errorf("vnc: %w", err)
		}
	}
	// inner is the type that authenticates: VeNCrypt's subtype within it.
	inner := uint32(t)
	var err error
	switch t {
	case secVNCAuth:
		err = vncAuth(r, w, a.password)
	case secVeNCrypt:
		inner, err = veNCrypt(r, w, a)
	}
	if err != nil {
		return 0, err
	}
	// SecurityResult follows every type but None before 3.8 (RFC 6143,
	// appendix A), and so VeNCrypt too, unless its subtype is None, as
	// TigerVNC has it.
	if inner == secNone && v < 8 {
		return t, nil
	}
	var b [4]byte
	if err := read(r, b[:]); err != nil {
		return 0, fmt.Errorf("vnc: read the security result: %w", err)
	}
	if binary.BigEndian.Uint32(b[:]) != 0 {
		// Only 3.8 gives a reason.
		if v >= 8 {
			return 0, fmt.Errorf("%w: %q", ErrAuthFailed, reason(r))
		}
		return 0, ErrAuthFailed
	}
	return t, nil
}

// vncAuth runs VNC Authentication: the server's challenge comes back
// encrypted with the password.
func vncAuth(r io.Reader, w io.Writer, password func() (string, error)) error {
	var challenge [16]byte
	if err := read(r, challenge[:]); err != nil {
		return fmt.Errorf("vnc: read the challenge: %w", err)
	}
	pw, err := password()
	if err != nil {
		return err
	}
	response := vncAuthResponse(pw, challenge)
	if _, err := w.Write(response[:]); err != nil {
		return fmt.Errorf("vnc: %w", err)
	}
	return nil
}

// choose picks a security type from those offered: VeNCrypt if a can
// start TLS, since its X.509 subtypes encrypt; otherwise, unless
// a.requireTLS, VNC Authentication over None if a password is configured
// or None is not offered, None if it is offered; or secInvalid.
func choose(offered []byte, a auth) byte {
	if a.startTLS != nil && slices.Contains(offered, secVeNCrypt) {
		return secVeNCrypt
	}
	if a.requireTLS {
		return secInvalid
	}
	none, auth := slices.Contains(offered, secNone), slices.Contains(offered, secVNCAuth)
	switch {
	case auth && (a.hasPassword || !none):
		return secVNCAuth
	case none:
		return secNone
	}
	return secInvalid
}

// refused reads the reason the server gives for refusing the connection.
func refused(r io.Reader) error {
	return fmt.Errorf("%w: %q", ErrRefused, reason(r))
}

// reason reads a reason string, or as much of its first maxReason bytes
// as arrive.
func reason(r io.Reader) string {
	var b [4]byte
	if err := read(r, b[:]); err != nil {
		return ""
	}
	buf := make([]byte, min(binary.BigEndian.Uint32(b[:]), maxReason))
	n, _ := io.ReadFull(r, buf)
	return text(buf[:n])
}

// vncAuthResponse encrypts the challenge with DES, keyed with the
// password's first 8 bytes padded with zeros, each byte's bits reversed,
// as every VNC implementation does (RFC 6143, section 7.2.2, leaves the
// reversal out).
func vncAuthResponse(password string, challenge [16]byte) [16]byte {
	var key [8]byte
	copy(key[:], password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key[:]) // #nosec G405 -- see the import
	clear(key[:])
	if err != nil {
		panic(err) // the key has the right size
	}
	var out [16]byte
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:])
	return out
}

// read fills b from r. A connection that ends early is
// io.ErrUnexpectedEOF, even before the first byte.
func read(r io.Reader, b []byte) error {
	_, err := io.ReadFull(r, b)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// text returns a string the server sent, which RFC 6143 leaves without
// an encoding: as UTF-8 if it is valid UTF-8, as Latin-1 otherwise.
func text(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return string(fromLatin1(b))
}
