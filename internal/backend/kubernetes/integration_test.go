// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build kubernetes_integration

package kubernetes_test

// The integration test runs the backend against a real API server: k3s
// in a container, with a busybox pod to exec in.
//
//	go test -tags kubernetes_integration -run Integration -timeout 15m ./internal/backend/kubernetes
//
// It starts a privileged container with podman, or the engine that
// OCCAMY_CONTAINER_ENGINE names, from OCCAMY_K3S_IMAGE or the default
// below, and pulls busybox inside it, so it needs the network. The
// container's name is the run's own, and the run removes that container
// alone when it ends.

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/kubernetes"
)

const defaultK3sImage = "docker.io/rancher/k3s:v1.34.1-k3s1"

// manifest is the namespace, the pod and the service accounts of the
// test: occamy may get and create pods/exec and pods/attach in the
// namespace, as docs/configuration.md tells operators to grant, and
// nobody may do neither.
const manifest = `
apiVersion: v1
kind: Namespace
metadata: {name: occamy-it}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: occamy, namespace: occamy-it}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: nobody, namespace: occamy-it}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: occamy-exec, namespace: occamy-it}
rules:
  - apiGroups: [""]
    resources: [pods/exec, pods/attach]
    verbs: [get, create]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: occamy-exec, namespace: occamy-it}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: occamy-exec}
subjects: [{kind: ServiceAccount, name: occamy, namespace: occamy-it}]
---
apiVersion: v1
kind: Pod
metadata: {name: shell, namespace: occamy-it}
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: box
      image: docker.io/library/busybox:1.37
      command: [sleep, "3600"]
`

type cluster struct {
	engine, name string
	addr         string
	roots        *x509.CertPool
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// run runs the engine with args, and stdin if not empty, and returns
// its standard output.
func (c *cluster) run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(c.engine, args...) // #nosec G204 -- the test's own arguments
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", c.engine, strings.Join(args, " "), err, errOut.String())
	}
	return out.String(), nil
}

func (c *cluster) kubectl(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := c.run(t, stdin, append([]string{"exec", "-i", c.name, "kubectl"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// startCluster starts k3s, applies the manifest and waits for the pod.
func startCluster(t *testing.T) *cluster {
	t.Helper()
	c := &cluster{
		engine: envOr("OCCAMY_CONTAINER_ENGINE", "podman"),
		name:   fmt.Sprintf("occamy-k8s-it-%d", time.Now().UnixNano()),
	}
	image := envOr("OCCAMY_K3S_IMAGE", defaultK3sImage)
	if _, err := c.run(t, "", "run", "-d", "--name", c.name, "--privileged",
		"-p", "127.0.0.1::6443", image, "server",
		"--disable=traefik", "--disable=metrics-server", "--disable=servicelb",
		"--tls-san=127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := c.run(t, "", "rm", "-f", "-t", "0", c.name); err != nil {
			t.Logf("removing the container: %v", err)
		}
	})
	port, err := c.run(t, "", "port", c.name, "6443/tcp")
	if err != nil {
		t.Fatal(err)
	}
	c.addr = strings.TrimSpace(strings.Split(port, "\n")[0])

	deadline := time.Now().Add(5 * time.Minute)
	for {
		out, err := c.run(t, "", "exec", c.name, "kubectl", "get", "--raw", "/readyz")
		if err == nil && strings.TrimSpace(out) == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("k3s did not get ready: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	// The service accounts' namespace may take a moment to exist.
	for {
		_, err := c.run(t, manifest, "exec", "-i", c.name, "kubectl", "apply", "-f", "-")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("applying the manifest: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	c.kubectl(t, "", "-n", "occamy-it", "wait", "--for=condition=Ready", "pod/shell", "--timeout=300s")

	ca := c.kubectl(t, "", "config", "view", "--raw", "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}")
	pem, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ca))
	if err != nil {
		t.Fatal(err)
	}
	c.roots = x509.NewCertPool()
	if !c.roots.AppendCertsFromPEM(pem) {
		t.Fatal("the cluster's CA is not PEM")
	}
	return c
}

func (c *cluster) token(t *testing.T, account string) kubernetes.Token {
	t.Helper()
	return kubernetes.Token(strings.TrimSpace(c.kubectl(t, "", "-n", "occamy-it", "create", "token", account, "--duration=1h")))
}

func (c *cluster) config(token kubernetes.Token) kubernetes.Config {
	return kubernetes.Config{
		Addr: c.addr, Namespace: "occamy-it", Pod: "shell", Container: "box",
		Token: token, TLS: kubernetes.TLS{RootCAs: c.roots},
	}
}

// awaitTerm waits for the sink's terminal output to hold want.
func awaitTerm(t *testing.T, sink *backendtest.Sink, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !bytes.Contains(sink.Term(), []byte(want)) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q in the output %q", want, sink.Term())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIntegration(t *testing.T) {
	c := startCluster(t)
	token := c.token(t, "occamy")

	t.Run("command", func(t *testing.T) {
		cfg := c.config(token)
		cfg.Command = []string{"sh", "-c", "echo occamy-$((6*7)); read line; echo got:$line; exit 3"}
		b := newBackend(t, cfg)
		sink := &backendtest.Sink{}
		r := start(t, b, sink)
		awaitTerm(t, sink, "occamy-42")
		input(t, b, backend.TermInput{Data: []byte("hi\r")})
		awaitTerm(t, sink, "got:hi")
		// An exit status other than 0 ends the session normally.
		if err := r.wait(t); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	})

	t.Run("default shell and resize", func(t *testing.T) {
		b := newBackend(t, c.config(token))
		sink := &backendtest.Sink{}
		r := start(t, b, sink)
		input(t, b, backend.TermInput{Data: []byte("stty size\r")})
		awaitTerm(t, sink, "24 80")
		input(t, b, backend.Resize{Width: 100, Height: 30})
		input(t, b, backend.TermInput{Data: []byte("stty size\r")})
		awaitTerm(t, sink, "30 100")
		input(t, b, backend.TermInput{Data: []byte("exit\r")})
		if err := r.wait(t); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	})

	t.Run("no tty", func(t *testing.T) {
		cfg := c.config(token)
		cfg.NoTTY = true
		cfg.Command = []string{"sh", "-c", "echo out; echo err >&2"}
		b := newBackend(t, cfg)
		sink := &backendtest.Sink{}
		r := start(t, b, sink)
		if err := r.wait(t); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
		if got := string(sink.Term()); !strings.Contains(got, "out\r\n") || !strings.Contains(got, "err\r\n") {
			t.Errorf("output %q, want out and err with CRLF", got)
		}
	})

	t.Run("missing container", func(t *testing.T) {
		cfg := c.config(token)
		cfg.Container = "nope"
		b := newBackend(t, cfg)
		r := start(t, b, &backendtest.Sink{})
		var se *kubernetes.StatusError
		if err := r.wait(t); !errors.As(err, &se) || strings.Contains(err.Error(), string(token)) {
			t.Errorf("Run = %v, want a StatusError without the token", err)
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		b := newBackend(t, c.config(c.token(t, "nobody")))
		r := start(t, b, &backendtest.Sink{})
		var se *kubernetes.StatusError
		if err := r.wait(t); !errors.As(err, &se) || se.HTTPStatus != 403 {
			t.Errorf("Run = %v, want the API server's 403", err)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		b := newBackend(t, c.config("not-a-token"))
		r := start(t, b, &backendtest.Sink{})
		var se *kubernetes.StatusError
		if err := r.wait(t); !errors.As(err, &se) || se.HTTPStatus != 401 {
			t.Errorf("Run = %v, want the API server's 401", err)
		}
	})

	t.Run("system roots", func(t *testing.T) {
		cfg := c.config(token)
		cfg.TLS = kubernetes.TLS{}
		b := newBackend(t, cfg)
		r := start(t, b, &backendtest.Sink{})
		var ce *kubernetes.CertificateError
		if err := r.wait(t); !errors.As(err, &ce) {
			t.Errorf("Run = %v, want the cluster's certificate refused", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		b := newBackend(t, c.config(token))
		sink := &backendtest.Sink{}
		r := start(t, b, sink)
		input(t, b, backend.TermInput{Data: []byte("echo ready\r")})
		awaitTerm(t, sink, "ready")
		r.cancel()
		if err := r.wait(t); !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	})
}
