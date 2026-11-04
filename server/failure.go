// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/rdp"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/backend/vnc"
	rdpproto "changkun.de/x/occamy/internal/rdp"
)

// Why a session could not connect, as its viewers are told in the
// reason of their close (stream.CloseFor). Every viewer reads it, so it
// names no address, fingerprint or user: the log has those.
const (
	failAuth        backend.Failure = "The remote computer refused the user name or password."
	failSSHAuth     backend.Failure = "The remote computer refused the user name, password or key."
	failNoMethod    backend.Failure = "The remote computer offers no sign-in method that the server accepts."
	failCertificate backend.Failure = "The server doesn't trust the remote computer's certificate."
	failHostKey     backend.Failure = "The server doesn't trust the remote computer's host key."
	failRefused     backend.Failure = "The remote computer refused the connection."
	failTimeout     backend.Failure = "The remote computer didn't answer in time."
	failName        backend.Failure = "The server couldn't look up the remote computer's name."
	failNoRoute     backend.Failure = "The server has no route to the remote computer."
	failKubeAuth    backend.Failure = "The Kubernetes API server refused the credentials."
	failKubeDenied  backend.Failure = "The Kubernetes API server refused to open a shell in the pod."
)

// explained is a backend whose Run's error says why it could not
// connect, when the cause is one that explain knows.
type explained struct{ backend.Backend }

func (e explained) Run(ctx context.Context, sink backend.Sink) error {
	return explain(e.Backend.Run(ctx, sink))
}

// explainedError is a backend's error with what the session's viewers
// are told of it. Its text stays the backend's, for the log.
type explainedError struct {
	why backend.Failure
	err error
}

func (e *explainedError) Error() string   { return e.err.Error() }
func (e *explainedError) Unwrap() []error { return []error{e.why, e.err} }

// explain returns err with why the backend could not connect, if err
// says so: the remote refused the credentials or offers no sign-in that
// occamyd takes, its certificate or host key is not trusted, or dialing
// it failed. Any other error,
// including one of a connection that failed after it was made, is
// returned as it is, and its viewers are told only "failed".
func explain(err error) error {
	var f backend.Failure
	if err == nil || errors.As(err, &f) {
		return err
	}
	if why := cause(err); why != "" {
		return &explainedError{why: why, err: err}
	}
	return err
}

// cause is why err says a backend could not connect, or "".
func cause(err error) backend.Failure {
	var (
		vncCert  *vnc.CertificateError
		rdpCert  *rdpproto.CertificateError
		kubeCert *kubernetes.CertificateError
		hostKey  *ssh.HostKeyError
		rdpNeg   *rdpproto.NegotiationError
		rdpProto *rdpproto.ProtocolError
		status   *kubernetes.StatusError
		op       *net.OpError
		dns      *net.DNSError
	)
	switch {
	case errors.Is(err, vnc.ErrAuthFailed), errors.Is(err, rdp.ErrAuthFailed):
		return failAuth
	case errors.Is(err, ssh.ErrAuthFailed):
		return failSSHAuth
	case errors.Is(err, vnc.ErrNoSecurityType), errors.As(err, &rdpNeg), errors.As(err, &rdpProto):
		// Such as a Mac that offers only Apple Remote Desktop's sign-in,
		// or xrdp, which needs security: tls. The log says which.
		return failNoMethod
	case errors.As(err, &vncCert), errors.As(err, &rdpCert), errors.As(err, &kubeCert):
		return failCertificate
	case errors.As(err, &hostKey):
		return failHostKey
	case errors.As(err, &status) && status.HTTPStatus == http.StatusUnauthorized:
		return failKubeAuth
	case errors.As(err, &status) && status.HTTPStatus == http.StatusForbidden:
		return failKubeDenied
	case !errors.As(err, &op) || op.Op != "dial":
		// Only dialing tells that the remote was never reached.
		return ""
	case errors.As(err, &dns):
		return failName
	case errors.Is(err, syscall.ECONNREFUSED):
		return failRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return failNoRoute
	case op.Timeout():
		return failTimeout
	}
	return ""
}
