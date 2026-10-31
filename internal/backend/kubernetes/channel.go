// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// The WebSocket subprotocols of pod exec and attach that the backend
// offers, in its order of preference (k8s.io/apimachinery's
// remotecommand). v4 carries the exit status as a metav1.Status on the
// error channel; v5 adds the close signal.
const (
	ProtocolV5 = "v5.channel.k8s.io"
	ProtocolV4 = "v4.channel.k8s.io"
)

// The channels: each message's first byte.
const (
	chanStdin  = 0
	chanStdout = 1
	chanStderr = 2
	chanError  = 3
	chanResize = 4
	// chanClose, in v5, is followed by the channel it closes.
	chanClose = 255
)

// Bounds on what the API server sends.
const (
	// maxMessage is the largest message the backend reads, its channel
	// byte included. The kubelet writes what the container runtime
	// reads at once, 32 KiB or less.
	maxMessage = 1 << 20
	// maxStatus is the most the error channel may carry, over all its
	// messages: a metav1.Status is a few hundred bytes.
	maxStatus = 16 << 10
	// maxQuoted is the most of a message from the server that an error
	// quotes.
	maxQuoted = 256
)

// errStatusTooLarge fails a session whose error channel carries more
// than maxStatus bytes.
var errStatusTooLarge = fmt.Errorf("kubernetes: the API server's status is larger than %d bytes", maxStatus)

// demux reads one message from r and passes it on: stdout and stderr to
// out, in pieces of at most len(buf) bytes, and the error channel to
// status, up to maxStatus bytes in all. An empty message, which the
// kubelet sends on stdout once the stream is ready, a v5 close signal,
// and a message on another channel are read to their end and dropped.
// It returns the error that ends the session, if any.
func demux(r io.Reader, buf []byte, out func([]byte), status *bytes.Buffer) error {
	var ch [1]byte
	if _, err := io.ReadFull(r, ch[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // an empty message
		}
		return err
	}
	switch ch[0] {
	case chanStdout, chanStderr:
		for {
			n, err := r.Read(buf)
			if n > 0 {
				out(buf[:n])
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	case chanError:
		// One byte past the cap tells a status that fits from one that
		// does not.
		if _, err := io.Copy(status, io.LimitReader(r, int64(maxStatus-status.Len())+1)); err != nil {
			return err
		}
		if status.Len() > maxStatus {
			return errStatusTooLarge
		}
		return nil
	}
	// A v5 close of a channel the server writes needs nothing: the
	// status, or the end of the stream, ends the session. Other channels
	// are not the server's to write.
	_, err := io.Copy(io.Discard, r)
	return err
}

// resizeMessage is the message that asks for a terminal of cols by rows:
// a TerminalSize in JSON on the resize channel.
func resizeMessage(cols, rows int) []byte {
	return fmt.Appendf([]byte{chanResize}, `{"Width":%d,"Height":%d}`, cols, rows)
}

// status is the part of a metav1.Status that the exit status needs.
type status struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Details *struct {
		Causes []struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"causes"`
	} `json:"details"`
}

// StatusError is a status from the API server that is not an exit
// status: the exec or attach failed, or the server refused the upgrade.
type StatusError struct {
	// HTTPStatus is the response's status code for a refused upgrade, and
	// 0 for a status on the error channel.
	HTTPStatus int
	// Reason is the status's reason, such as Forbidden or InternalError,
	// and Message what went wrong, both as the server wrote them; either
	// may be empty.
	Reason, Message string
}

func (e *StatusError) Error() string {
	var b bytes.Buffer
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, "kubernetes: the API server refused the stream with %d", e.HTTPStatus)
	} else {
		b.WriteString("kubernetes: the command failed")
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, ": %s", quote(e.Reason))
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", quote(e.Message))
	}
	return b.String()
}

// The reasons of an exit status (k8s.io/apimachinery's remotecommand).
const (
	reasonNonZeroExitCode = "NonZeroExitCode"
	causeExitCode         = "ExitCode"
)

// parseStatus reads the error channel's metav1.Status, as client-go's
// errorDecoderV4 does. It returns the command's exit code, 0 to 255, for
// Success and NonZeroExitCode, and otherwise an error that quotes at
// most maxQuoted bytes of what the server sent.
func parseStatus(b []byte) (int, error) {
	var s status
	if err := json.Unmarshal(b, &s); err != nil {
		return 0, fmt.Errorf("kubernetes: the API server's status is not JSON: %s", quote(string(b)))
	}
	switch s.Status {
	case "Success":
		return 0, nil
	case "Failure":
	default:
		return 0, fmt.Errorf("kubernetes: the API server's status is %s, neither Success nor Failure", quote(s.Status))
	}
	if s.Reason != reasonNonZeroExitCode {
		return 0, &StatusError{Reason: s.Reason, Message: s.Message}
	}
	if s.Details != nil {
		for _, c := range s.Details.Causes {
			if c.Reason != causeExitCode {
				continue
			}
			code, err := strconv.ParseUint(c.Message, 10, 8)
			if err != nil {
				return 0, fmt.Errorf("kubernetes: the API server's exit code %s is not a number from 0 to 255", quote(c.Message))
			}
			return int(code), nil
		}
	}
	return 0, errors.New("kubernetes: the API server's status has no exit code")
}

// quote quotes s for an error or a log line: at most maxQuoted bytes,
// cut at a character, with every control and invalid byte escaped.
func quote(s string) string {
	if len(s) <= maxQuoted {
		return strconv.QuoteToGraphic(s)
	}
	i := maxQuoted
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return strconv.QuoteToGraphic(s[:i]) + "..."
}

// onlcr turns each line feed in p into a carriage return and a line
// feed, as a terminal's ONLCR does, for output that no TTY translated.
func onlcr(dst, p []byte) []byte {
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			return append(dst, p...)
		}
		dst = append(dst, p[:i]...)
		dst = append(dst, '\r', '\n')
		p = p[i+1:]
	}
}
