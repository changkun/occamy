// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"net/http"
	"testing"
	"time"
)

// TestE2ETokenSurvivesRestart checks what keeping the signing key on a
// volume is for: a token signed before occamy-v1 is replaced still works
// after it, so that nobody signs in again. It recreates the container
// rather than restarting it: a restart keeps the container's own layer,
// so it could not tell a volume from no mount at all. On a tmpfs, where
// the demo used to keep the key, or with no volume, the new container
// would make a new key and this token would stop verifying. It must be
// the token from before: a new sign-in would pass with any key.
func TestE2ETokenSurvivesRestart(t *testing.T) {
	addr := endpointV1(t)
	c := composeE2E(t)
	token, err := signIn(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := apiV1(addr, http.MethodGet, "/api/v1/connections", token, nil, nil); err != nil {
		t.Fatalf("before the recreate: %v", err)
	}
	c.run(t, "up", "-d", "--force-recreate", "--no-deps", "occamy-v1")

	// occamyd takes a moment to serve again. It rejects a token for good
	// with a 401, and that is the failure this test looks for, so wait
	// on /healthz rather than on the token.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := apiV1(addr, http.MethodGet, "/healthz", "", nil, nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("occamy-v1 is not back after the recreate: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := apiV1(addr, http.MethodGet, "/api/v1/connections", token, nil, nil); err != nil {
		t.Errorf("the token from before the recreate: %v", err)
	}
}
