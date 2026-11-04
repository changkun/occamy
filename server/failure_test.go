// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/rdp"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/backend/vnc/vnctest"
	rdpproto "changkun.de/x/occamy/internal/rdp"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// timeoutError is a dial's timeout, as net.Dialer returns it.
type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }

func dialError(err error) error {
	return fmt.Errorf("vnc: %w", &net.OpError{Op: "dial", Net: "tcp", Err: err})
}

// TestExplain checks that each cause a viewer can act on gets its
// sentence, and that the log keeps the backend's own text.
func TestExplain(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want backend.Failure // "" leaves the reason "failed"
	}{
		{"vnc password", fmt.Errorf("%w: %q", vnc.ErrAuthFailed, "bad password"), failAuth},
		{"rdp password", fmt.Errorf("%w: %w", rdp.ErrAuthFailed, errors.New("logon failure")), failAuth},
		{"vnc certificate", fmt.Errorf("vnc: %w", &vnc.CertificateError{Subject: "CN=mac", Err: errors.New("unknown authority")}), failCertificate},
		{"rdp certificate", fmt.Errorf("rdp: %w", &rdpproto.CertificateError{Subject: "CN=win", Err: errors.New("unknown authority")}), failCertificate},
		{"kubernetes certificate", fmt.Errorf("kubernetes: %w", &kubernetes.CertificateError{Subject: "CN=k8s", Err: errors.New("unknown authority")}), failCertificate},
		{"ssh host key", fmt.Errorf("ssh: connect to h:22: %w", &ssh.HostKeyError{Type: "ssh-ed25519", Fingerprint: "SHA256:x"}), failHostKey},
		{"kubernetes 401", &kubernetes.StatusError{HTTPStatus: 401}, failKubeAuth},
		{"kubernetes 403", &kubernetes.StatusError{HTTPStatus: 403}, failKubeDenied},
		{"kubernetes 500", &kubernetes.StatusError{HTTPStatus: 500}, ""},
		{"kubernetes command", &kubernetes.StatusError{Reason: "Forbidden"}, ""},
		{"refused", dialError(os.NewSyscallError("connect", syscall.ECONNREFUSED)), failRefused},
		{"no route", dialError(os.NewSyscallError("connect", syscall.EHOSTUNREACH)), failNoRoute},
		{"network unreachable", dialError(os.NewSyscallError("connect", syscall.ENETUNREACH)), failNoRoute},
		{"timeout", dialError(timeoutError{}), failTimeout},
		{"name", dialError(&net.DNSError{Err: "no such host", Name: "mac.lab", IsNotFound: true}), failName},
		{"reset after connecting", fmt.Errorf("vnc: %w", &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}), ""},
		{"timeout after connecting", fmt.Errorf("vnc: %w", &net.OpError{Op: "read", Net: "tcp", Err: timeoutError{}}), ""},
		{"another error", errors.New("vnc: the server sent an unknown encoding"), ""},
		{"a failure already", backend.Failure("the remote drew nothing"), "the remote drew nothing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := explain(tt.err)
			want := string(tt.want)
			if want == "" {
				want = session.Failed.String()
			}
			if got := stream.CloseFor(session.End{Reason: session.Failed, Err: err}); got != (protocol.Close{Code: protocol.CloseBackendFailed, Reason: want}) {
				t.Errorf("close %+v, want reason %q", got, want)
			}
			if err.Error() != tt.err.Error() {
				t.Errorf("the log reads %q, want the backend's %q", err, tt.err)
			}
			if !errors.Is(err, tt.err) {
				t.Errorf("%v no longer wraps the backend's error", err)
			}
		})
	}
	if explain(nil) != nil {
		t.Error("explain(nil) is not nil")
	}
	for _, f := range []backend.Failure{failAuth, failCertificate, failHostKey, failRefused, failTimeout, failName, failNoRoute, failKubeAuth, failKubeDenied} {
		if len(f) > 123 {
			t.Errorf("%q does not fit a close frame", f)
		}
	}
}

// TestExplainBackends runs the VNC backend against a remote that refuses
// the password and against a port that nothing listens on, and checks
// that the cause survives the backend's wrapping of its errors.
func TestExplainBackends(t *testing.T) {
	run := func(t *testing.T, tg target) error {
		t.Helper()
		b, err := newVNC(tg, screen{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
		defer cancel()
		return explained{b}.Run(ctx, &backendtest.Sink{})
	}
	check := func(t *testing.T, err error, want backend.Failure) {
		t.Helper()
		var f backend.Failure
		if !errors.As(err, &f) || f != want {
			t.Errorf("Run = %v, told %q, want %q", err, f, want)
		}
	}
	t.Run("wrong password", func(t *testing.T) {
		s := newVNCServer(t, vnctest.Options{Password: "right"})
		check(t, run(t, target{protocol: "vnc", addr: s.Addr(), creds: passwordFile(t, "wrong")}), failAuth)
	})
	t.Run("nothing listens", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		start := time.Now()
		check(t, run(t, target{protocol: "vnc", addr: addr}), failRefused)
		if d := time.Since(start); d > vncTimeout/2 {
			t.Errorf("the refused dial took %v", d)
		}
	})
}
