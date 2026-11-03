// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package tests runs end-to-end and pressure tests against a running
// occamyd, normally the demo stack's occamy-v1 service from
// docker/docker-compose.yaml, which serves API v1 and protocol v1 from
// docker/occamy-v1.yaml:
//
//	make build
//	make run OCCAMY_DEBUG=1
//	make e2e
//
// The tests are skipped unless OCCAMY_E2E_V1_ENDPOINT is set. The
// pressure test also needs OCCAMY_E2E_V1_DEBUG, occamy-v1's debug
// listener, which make run OCCAMY_DEBUG=1 turns on. A test that reaches
// into the stack's rdp container, to kill its channel server and read
// its log, also needs OCCAMY_E2E_COMPOSE, the stack's compose command,
// such as "docker compose -f docker/docker-compose.yaml" with an
// absolute path. make e2e sets all three. The test of the web client
// needs Chrome, which it looks for on the host unless OCCAMY_E2E_CHROME
// names one; without one it is skipped, and fails in CI.
package tests

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// TestMain checks that occamyd is still up after the tests. Tearing down
// a session used to crash occamyd after the client had already left,
// which no session in the tests would notice.
func TestMain(m *testing.M) {
	code := m.Run()
	if addr := os.Getenv("OCCAMY_E2E_V1_ENDPOINT"); addr != "" && code == 0 {
		time.Sleep(2 * time.Second)
		if _, err := signIn(addr); err != nil {
			fmt.Fprintf(os.Stderr, "occamy-v1 did not survive the tests: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}
