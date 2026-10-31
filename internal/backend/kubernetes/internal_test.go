// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
)

func TestHostPort(t *testing.T) {
	for _, tt := range []struct{ in, want, host string }{
		{"k8s.example", "k8s.example:443", "k8s.example"},
		{"k8s.example:6443", "k8s.example:6443", "k8s.example"},
		{"10.0.0.1", "10.0.0.1:443", "10.0.0.1"},
		{"::1", "[::1]:443", "::1"},
		{"[::1]", "[::1]:443", "::1"},
		{"[fd00::1]:6443", "[fd00::1]:6443", "fd00::1"},
	} {
		got, host, err := hostPort(tt.in)
		if err != nil || got != tt.want || host != tt.host {
			t.Errorf("hostPort(%q) = %q, %q, %v; want %q, %q", tt.in, got, host, err, tt.want, tt.host)
		}
	}
}

func TestEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name      string
		ns, pod   string
		container string
		command   []string
		attach    bool
		tty       bool
		want      string
	}{
		{"exec", "apps", "web-0", "app", []string{"sh", "-c", "echo a&b=c d"}, false, true,
			"https://k8s:443/api/v1/namespaces/apps/pods/web-0/exec?command=sh&command=-c&command=echo+a%26b%3Dc+d&container=app&stderr=false&stdin=true&stdout=true&tty=true"},
		{"attach", "default", "db.example", "", nil, true, false,
			"https://k8s:443/api/v1/namespaces/default/pods/db.example/attach?stderr=true&stdin=true&stdout=true&tty=false"},
		// A name a check let through is still one path segment.
		{"escaped", "a/b", "c?d", "", nil, false, true,
			"https://k8s:443/api/v1/namespaces/a%2Fb/pods/c%3Fd/exec?stderr=false&stdin=true&stdout=true&tty=true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := endpoint("k8s:443", tt.ns, tt.pod, tt.container, tt.command, tt.attach, tt.tty).String(); got != tt.want {
				t.Errorf("endpoint = %s\nwant       %s", got, tt.want)
			}
		})
	}
}

func TestParseStatus(t *testing.T) {
	for _, tt := range []struct {
		in   string
		code int
		err  string
	}{
		{`{"metadata":{},"status":"Success"}`, 0, ""},
		{`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"42"}]}}`, 42, ""},
		{`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"Other","message":"x"},{"reason":"ExitCode","message":"255"}]}}`, 255, ""},
		{`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"-1"}]}}`, 0, "not a number from 0 to 255"},
		{`{"status":"Failure","reason":"NonZeroExitCode","details":{}}`, 0, "no exit code"},
		{`{"status":"Failure","reason":"InternalError","message":"boom"}`, 0, `the command failed: "InternalError": "boom"`},
		{`{"status":"Failure"}`, 0, "the command failed"},
		{`{"status":"Weird"}`, 0, `"Weird", neither`},
		{`{}`, 0, `"", neither`},
		{``, 0, "not JSON"},
		{`[1]`, 0, "not JSON"},
	} {
		code, err := parseStatus([]byte(tt.in))
		switch {
		case tt.err == "" && (err != nil || code != tt.code):
			t.Errorf("parseStatus(%s) = %d, %v; want %d", tt.in, code, err, tt.code)
		case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
			t.Errorf("parseStatus(%s) = %d, %v; want an error with %q", tt.in, code, err, tt.err)
		}
	}
}

func TestQuote(t *testing.T) {
	long := strings.Repeat("é", maxQuoted) // two bytes each
	for _, tt := range []struct{ in, want string }{
		{"plain", `"plain"`},
		{"\x1b[2J\r\nX-Evil: 1", `"\x1b[2J\r\nX-Evil: 1"`},
		{"\xff", `"\xff"`},
		{long, `"` + long[:maxQuoted] + `"...`},
		{"a" + long, `"a` + long[:maxQuoted-2] + `"...`},
	} {
		if got := quote(tt.in); got != tt.want {
			t.Errorf("quote(%.20q) = %.40q, want %.40q", tt.in, got, tt.want)
		}
	}
}

func TestCut(t *testing.T) {
	long := strings.Repeat("é", maxQuoted) // two bytes each
	for _, tt := range []struct{ in, want string }{
		{"plain", "plain"},
		{`Get "https://k8s:443/": EOF`, `Get "https://k8s:443/": EOF`},
		{"\x1b[2J\r\nX-Evil: 1\t", "�[2J��X-Evil: 1�"},
		{"a\xffb", "a�b"},
		{long, long[:maxQuoted] + "..."},
		{"a" + long, "a" + long[:maxQuoted-2] + "..."},
	} {
		if got := cut(tt.in); got != tt.want {
			t.Errorf("cut(%.20q) = %.40q, want %.40q", tt.in, got, tt.want)
		}
	}
	err := serverText{io.ErrUnexpectedEOF}
	if !errors.Is(err, io.ErrUnexpectedEOF) || err.Error() != io.ErrUnexpectedEOF.Error() {
		t.Errorf("serverText = %v, does not wrap its error", err)
	}
}

func TestONLCR(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"a", "a"},
		{"\n", "\r\n"},
		{"a\nb\n\nc", "a\r\nb\r\n\r\nc"},
		{"a\r\nb", "a\r\r\nb"},
	} {
		if got := string(onlcr(nil, []byte(tt.in))); got != tt.want {
			t.Errorf("onlcr(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestResizeMessage(t *testing.T) {
	m := resizeMessage(MaxCols, MaxRows)
	var sz struct{ Width, Height int }
	if m[0] != chanResize || json.Unmarshal(m[1:], &sz) != nil || sz.Width != MaxCols || sz.Height != MaxRows {
		t.Errorf("resizeMessage = %q", m)
	}
}

func TestCheckPin(t *testing.T) {
	for _, ok := range []string{strings.Repeat("AB", 32), strings.Repeat("ab:", 31) + "ab", "SHA256:" + strings.Repeat("A", 43)} {
		if err := CheckPin(ok); err != nil {
			t.Errorf("CheckPin(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "AB", "SHA256:AAAA", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----"} {
		if err := CheckPin(bad); err == nil {
			t.Errorf("CheckPin(%q) = nil", bad)
		}
	}
}

func TestTokenMethods(t *testing.T) {
	tok := Token("secret")
	if tok.String() != redacted || tok.GoString() != redacted {
		t.Errorf("String %q, GoString %q", tok.String(), tok.GoString())
	}
	var b Backend
	if b.Class() != backend.Terminal {
		t.Errorf("Class = %v", b.Class())
	}
	b.Demand(backend.Demand{})
}

func TestCheckToken(t *testing.T) {
	for _, ok := range []Token{"", "abc", "eyJhbGciOiJSUzI1NiJ9.e30.c2ln", "abcdef.0123456789abcdef", "a~b+c/d="} {
		if err := checkToken(ok); err != nil {
			t.Errorf("checkToken(%s) = %v", string(ok), err)
		}
	}
	for _, bad := range []Token{"a b", "a\tb", "a\r\nX: y", "a\x00", "é", "a\x7f"} {
		if err := checkToken(bad); !errors.Is(err, errBadToken) {
			t.Errorf("checkToken(%q) = %v", string(bad), err)
		}
	}
}

// demuxAll runs demux on msg, read in pieces as small as iotest's, and
// returns what reached the terminal and the status.
func demuxAll(msg []byte, half bool, status *bytes.Buffer) ([]byte, error) {
	var out []byte
	var r io.Reader = bytes.NewReader(msg)
	if half {
		r = iotest.HalfReader(r)
	}
	buf := make([]byte, 7)
	err := demux(r, buf, func(p []byte) {
		if len(p) > len(buf) {
			panic("a piece larger than the buffer")
		}
		out = append(out, p...)
	}, status)
	return out, err
}

func TestDemux(t *testing.T) {
	for _, tt := range []struct {
		name   string
		msg    []byte
		out    string
		status string
	}{
		{"empty", nil, "", ""},
		{"stdout's ready", []byte{chanStdout}, "", ""},
		{"stdout", append([]byte{chanStdout}, "hello, world"...), "hello, world", ""},
		{"stderr", append([]byte{chanStderr}, "oops\n"...), "oops\n", ""},
		{"status", append([]byte{chanError}, `{"status":"Success"}`...), "", `{"status":"Success"}`},
		{"stdin echoed", []byte{chanStdin, 'x'}, "", ""},
		{"resize", []byte{chanResize, '{'}, "", ""},
		{"close", []byte{chanClose, chanStdout}, "", ""},
		{"unknown", []byte{42, 1, 2, 3}, "", ""},
	} {
		for _, half := range []bool{false, true} {
			var st bytes.Buffer
			out, err := demuxAll(tt.msg, half, &st)
			if err != nil || string(out) != tt.out || st.String() != tt.status {
				t.Errorf("%s: demux = %q, status %q, %v", tt.name, out, st.String(), err)
			}
		}
	}
	// The status is capped over every message.
	var st bytes.Buffer
	if _, err := demuxAll(append([]byte{chanError}, bytes.Repeat([]byte("a"), maxStatus-1)...), false, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := demuxAll([]byte{chanError, 'b'}, false, &st); err != nil || st.Len() != maxStatus {
		t.Fatalf("status of %d bytes: %v", st.Len(), err)
	}
	if _, err := demuxAll([]byte{chanError, 'c'}, false, &st); !errors.Is(err, errStatusTooLarge) || st.Len() > maxStatus+1 {
		t.Errorf("status of %d bytes: %v, want errStatusTooLarge", st.Len(), err)
	}
	// One message as large as the read limit is not kept whole either.
	st.Reset()
	if _, err := demuxAll(append([]byte{chanError}, bytes.Repeat([]byte("d"), maxMessage-1)...), false, &st); !errors.Is(err, errStatusTooLarge) || st.Len() > maxStatus+1 {
		t.Errorf("status of %d bytes from one message: %v, want errStatusTooLarge with at most %d kept", st.Len(), err, maxStatus+1)
	}
}

// FuzzDemux feeds any message to demux: it never panics, passes stdout
// and stderr on whole and in pieces of at most the buffer's size, keeps
// at most one byte past the status's bound, and fails only for a status
// over it.
func FuzzDemux(f *testing.F) {
	f.Add([]byte{}, 0)
	f.Add([]byte{chanStdout}, 0)
	f.Add(append([]byte{chanStdout}, "\x1b[31mred\x1b[m\r\n"...), 0)
	f.Add(append([]byte{chanStderr}, "error\n"...), 0)
	f.Add(append([]byte{chanError}, `{"metadata":{},"status":"Success"}`...), 0)
	f.Add(append([]byte{chanError}, `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"3"}]}}`...), 16)
	f.Add([]byte{chanClose, chanStdin}, 0)
	f.Add([]byte{chanResize, '{', '}'}, 0)
	f.Add([]byte{0xfe, 1, 2}, maxStatus-1)
	f.Fuzz(func(t *testing.T, msg []byte, prior int) {
		prior = min(max(prior, 0), maxStatus)
		var st bytes.Buffer
		st.Write(bytes.Repeat([]byte(" "), prior))
		for _, half := range []bool{false, true} {
			st.Truncate(prior)
			out, err := demuxAll(msg, half, &st)
			if st.Len() > maxStatus+1 {
				t.Fatalf("status of %d bytes", st.Len())
			}
			if len(msg) == 0 {
				if err != nil || out != nil || st.Len() != prior {
					t.Fatalf("an empty message: %q, %d, %v", out, st.Len(), err)
				}
				continue
			}
			switch msg[0] {
			case chanStdout, chanStderr:
				if err != nil || !bytes.Equal(out, msg[1:]) {
					t.Fatalf("output %q of %q, %v", out, msg, err)
				}
			case chanError:
				tooBig := prior+len(msg)-1 > maxStatus
				if tooBig != errors.Is(err, errStatusTooLarge) || err != nil && !tooBig {
					t.Fatalf("status of %d+%d bytes: %v", prior, len(msg)-1, err)
				}
				if !tooBig && !bytes.Equal(st.Bytes()[prior:], msg[1:]) {
					t.Fatal("the status changed on the way")
				}
				if out != nil {
					t.Fatal("output from the error channel")
				}
			default:
				if err != nil || out != nil || st.Len() != prior {
					t.Fatalf("channel %d: %q, %d, %v", msg[0], out, st.Len(), err)
				}
			}
		}
	})
}

// FuzzParseStatus feeds any status to parseStatus: it never panics,
// returns an exit code from 0 to 255 or an error, and an error is short,
// valid UTF-8, and free of control characters, whatever the server
// wrote.
func FuzzParseStatus(f *testing.F) {
	f.Add([]byte(`{"metadata":{},"status":"Success"}`))
	f.Add([]byte(`{"metadata":{},"status":"Failure","message":"command terminated with non-zero exit code: error executing command [sh -c exit 3], exit code 3","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"3"}]}}`))
	f.Add([]byte(`{"status":"Failure","reason":"InternalError","message":"container not found (\"app\")"}`))
	f.Add([]byte(`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"256"}]}}`))
	f.Add([]byte(`{"status":"Failure","reason":"NonZeroExitCode","details":null}`))
	f.Add([]byte(`{"status":"Failure","message":"\u001b[2J\r\n` + strings.Repeat("x", 300) + `"}`))
	f.Add([]byte("not json \xff"))
	f.Fuzz(func(t *testing.T, b []byte) {
		code, err := parseStatus(b)
		if err == nil {
			if code < 0 || code > 255 {
				t.Fatalf("exit code %d", code)
			}
			return
		}
		if code != 0 {
			t.Fatalf("exit code %d with %v", code, err)
		}
		msg := err.Error()
		if !utf8.ValidString(msg) {
			t.Fatalf("an error that is not UTF-8: %q", msg)
		}
		if strings.ContainsFunc(msg, func(r rune) bool { return r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 }) {
			t.Fatalf("a control character in %q", msg)
		}
		// Two quoted fields of at most maxQuoted bytes, each escaped to
		// at most four times its length, and the words around them.
		if len(msg) > 2*(4*maxQuoted+5)+100 {
			t.Fatalf("an error of %d bytes", len(msg))
		}
	})
}
