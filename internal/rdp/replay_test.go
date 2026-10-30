// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// The replay tests play back what xrdp sent in the integration test
// (testdata/xrdp-*.trace.gz, rewritten with -rdp.record) from a fake
// server, so that the connection sequence and the decoders run against
// real PDUs without a container.

// loadTrace reads a recording: frames, each after its length in four
// bytes, big-endian.
func loadTrace(t testing.TB, name string) [][]byte {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	for len(b) >= 4 {
		n := int(binary.BigEndian.Uint32(b))
		if 4+n > len(b) {
			t.Fatalf("%s: truncated", name)
		}
		frames = append(frames, b[4:4+n])
		b = b[4+n:]
	}
	return frames
}

var testCert = sync.OnceValues(func() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rdp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
})

// fakeServer plays recorded server frames to a client.
type fakeServer struct {
	// frames are the server's frames, the X.224 connection confirm first.
	frames [][]byte
	// protocol, if set, replaces the security protocol the connection
	// confirm selects. ProtocolHybrid and ProtocolHybridEx run CredSSP
	// against the credentials below.
	protocol uint32
	domain   string
	user     string
	password string

	// creds are the credentials the client delegated through CredSSP.
	creds tsPasswordCreds
}

// serve runs the server's side of conn. After the last frame it waits for
// the client's Font List PDU and closes the connection.
func (s *fakeServer) serve(conn net.Conn) error {
	defer conn.Close()
	t := newTransport(conn, nil)
	if _, _, err := t.readFrame(nil); err != nil {
		return fmt.Errorf("connection request: %w", err)
	}
	cc := bytes.Clone(s.frames[0])
	if s.protocol != 0 {
		binary.LittleEndian.PutUint32(cc[len(cc)-4:], s.protocol)
	}
	if _, err := conn.Write(cc); err != nil {
		return err
	}
	cert, err := testCert()
	if err != nil {
		return err
	}
	tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err := tc.Handshake(); err != nil {
		return fmt.Errorf("TLS: %w", err)
	}
	r := bufio.NewReader(tc)
	if s.protocol&(ProtocolHybrid|ProtocolHybridEx) != 0 {
		creds, err := acceptCredSSP(r, tc, cert.Leaf, s.domain, s.user, s.password)
		if err != nil {
			return fmt.Errorf("CredSSP: %w", err)
		}
		s.creds = creds
		if s.protocol&ProtocolHybridEx != 0 {
			if _, err := tc.Write([]byte{0, 0, 0, 0}); err != nil {
				return err
			}
		}
	}
	fontList := make(chan error, 1)
	go func() {
		ct := &transport{conn: tc, r: r}
		for {
			f, _, err := ct.readFrame(nil)
			if err != nil {
				fontList <- err
				return
			}
			if isFontList(f) {
				fontList <- nil
				return
			}
		}
	}()
	for _, f := range s.frames[1:] {
		if _, err := tc.Write(f); err != nil {
			return err
		}
	}
	return <-fontList
}

// isFontList reports whether a client frame is the Font List PDU.
func isFontList(f frame) bool {
	if f.fastPath {
		return false
	}
	r := reader{b: f.data}
	if r.u8()&mcsDomainPDUChoiceMask != mcsSendDataRequest {
		return false
	}
	r.skip(5)
	if n := r.u8(); n&0x80 != 0 {
		r.u8()
	}
	r.skip(2) // totalLength
	typ := r.u16() & 0xf
	r.skip(2 + 4 + 1 + 1 + 2) // pduSource, shareId, pad1, streamId, uncompressedLength
	return r.err == nil && typ == pduData && r.u8() == pdu2FontList
}

// replay connects a client to s over a pipe and serves the connection
// until the server hangs up. It returns the client and its screen.
func replay(t *testing.T, s *fakeServer, cfg Config) (*Client, *screen) {
	t.Helper()
	cconn, sconn := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- s.serve(sconn) }()
	cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	if cfg.Username == "" {
		cfg.Username, cfg.Password = "occamy", "occamy"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Connect(ctx, cconn, cfg)
	if err != nil {
		t.Fatalf("Connect: %v (server: %v)", err, <-served)
	}
	scr := &screen{}
	err = c.Run(ctx, scr)
	if err := <-served; err != nil {
		t.Fatalf("server: %v", err)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Run returned %v, want io.EOF once the recording ends", err)
	}
	return c, scr
}

// Every recording ends on the scene's wallpaper with the swatch grey, as
// the integration test saw it, and the client decoded it with the codec
// of its colour depth.
func TestReplay(t *testing.T) {
	for _, tc := range []struct {
		bpp   int
		tol   int
		codec string
	}{
		{32, 0, "planar"},
		{24, 0, "rle24"},
		{16, 8, "rle16"},
		{15, 8, "rle15"},
	} {
		t.Run(fmt.Sprintf("%dbpp", tc.bpp), func(t *testing.T) {
			frames := loadTrace(t, fmt.Sprintf("testdata/xrdp-%dbpp.trace.gz", tc.bpp))
			c, scr := replay(t, &fakeServer{frames: frames}, Config{ColorDepth: tc.bpp})
			if bad, first := matchesWallpaper(scr.snapshot(), tc.tol); bad != 0 {
				t.Errorf("%d pixels differ from the wallpaper, the first at %v", bad, first)
			}
			// The swatch keeps the colour the last key painted in the
			// recorded session, but it is one colour.
			sw := scr.snapshot().SubImage(image.Rect(16, 16, 48, 48)).(*image.RGBA)
			for y := 16; y < 48; y++ {
				for x := 16; x < 48; x++ {
					if sw.RGBAAt(x, y) != sw.RGBAAt(16, 16) {
						t.Fatalf("swatch pixel (%d, %d) is %v, (16, 16) %v", x, y, sw.RGBAAt(x, y), sw.RGBAAt(16, 16))
					}
				}
			}
			if !c.Finalized() {
				t.Error("not finalized")
			}
			if c.Protocol() != ProtocolSSL {
				t.Errorf("protocol %#x", c.Protocol())
			}
			st := c.Stats()
			if st.Bitmaps[tc.codec] == 0 || len(st.Bitmaps) != 1 {
				t.Errorf("bitmaps %v, want %s only", st.Bitmaps, tc.codec)
			}
			if len(st.Ignored) != 0 {
				t.Errorf("ignored %v", st.Ignored)
			}
			if scr.cursor == nil {
				t.Error("no pointer shape")
			}
		})
	}
}

// With NLA the client authenticates through CredSSP before the MCS
// connection, and delegates its credentials.
func TestReplayNLA(t *testing.T) {
	for _, p := range []uint32{ProtocolHybrid, ProtocolHybridEx} {
		t.Run(fmt.Sprintf("protocol%d", p), func(t *testing.T) {
			s := &fakeServer{
				frames:   loadTrace(t, "testdata/xrdp-32bpp.trace.gz"),
				protocol: p,
				domain:   "DOMAIN", user: "occamy", password: "s3cret",
			}
			c, scr := replay(t, s, Config{Domain: "DOMAIN", Username: "occamy", Password: "s3cret"})
			if c.Protocol() != p {
				t.Errorf("protocol %#x, want %#x", c.Protocol(), p)
			}
			if got := s.creds; string(got.UserName) != string(utf16le("occamy")) ||
				string(got.DomainName) != string(utf16le("DOMAIN")) ||
				string(got.Password) != string(utf16le("s3cret")) {
				t.Errorf("delegated credentials %q", got)
			}
			if bad, _ := matchesWallpaper(scr.snapshot(), 0); bad != 0 {
				t.Errorf("%d pixels differ from the wallpaper", bad)
			}
		})
	}
}

// A wrong password fails CredSSP with the server's NTSTATUS.
func TestReplayNLAWrongPassword(t *testing.T) {
	s := &fakeServer{
		frames:   loadTrace(t, "testdata/xrdp-32bpp.trace.gz"),
		protocol: ProtocolHybrid,
		user:     "occamy", password: "s3cret",
	}
	cconn, sconn := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- s.serve(sconn) }()
	_, err := Connect(context.Background(), cconn, Config{
		Username: "occamy", Password: "wrong",
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
	})
	var ce *CredSSPError
	if !errors.As(err, &ce) || ce.Code != statusLogonFailure {
		t.Errorf("Connect returned %v, want STATUS_LOGON_FAILURE", err)
	}
	<-served
}

// xrdp answers a request for CredSSP alone with standard RDP security,
// which the client refuses.
func TestRefusesStandardSecurity(t *testing.T) {
	s := &fakeServer{frames: loadTrace(t, "testdata/xrdp-32bpp.trace.gz"), protocol: 0}
	cc := s.frames[0]
	s.frames = [][]byte{append(bytes.Clone(cc[:len(cc)-4]), 0, 0, 0, 0)}
	cconn, sconn := net.Pipe()
	go s.serve(sconn)
	_, err := Connect(context.Background(), cconn, Config{
		Username: "occamy", Password: "occamy", Protocols: ProtocolHybrid,
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
	})
	if err == nil {
		t.Fatal("Connect accepted standard RDP security")
	}
}
