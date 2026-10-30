// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package session_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/session"
)

func TestRegistry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := session.NewRegistry(ctx)
		id, s, owner, err := r.Open(session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if owner.Role() != session.RoleOwner || len(id) < 20 {
			t.Fatalf("owner role %v, id %q", owner.Role(), id)
		}
		if got, err := r.Get(id); err != nil || got != s {
			t.Fatalf("Get(%q) = %v, %v", id, got, err)
		}
		if _, err := r.Get("nope"); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Get of an unknown id returned %v", err)
		}

		if _, err := r.Share(id, session.RoleOwner, time.Hour); !errors.Is(err, session.ErrShareRole) {
			t.Errorf("owner share returned %v", err)
		}
		if _, err := r.Share("nope", session.RoleViewer, time.Hour); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("share of an unknown session returned %v", err)
		}
		viewTok, err := r.Share(id, session.RoleViewer, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		roTok, _ := r.Share(id, session.RoleReadOnly, time.Minute)
		if viewTok == roTok {
			t.Fatal("two shares got the same token")
		}

		v, err := r.JoinShared(viewTok)
		if err != nil || v.Role() != session.RoleViewer {
			t.Fatalf("JoinShared = %v, %v", v, err)
		}
		if _, err := r.JoinShared("forged"); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("forged token returned %v", err)
		}

		// The read-only token expires after a minute.
		time.Sleep(time.Minute)
		if _, err := r.JoinShared(roTok); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("expired token returned %v", err)
		}

		// The session ends: it leaves the registry and its tokens die.
		s.Close()
		synctest.Wait()
		if r.Len() != 0 {
			t.Errorf("%d sessions after the only one ended", r.Len())
		}
		if _, err := r.JoinShared(viewTok); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("token of an ended session returned %v", err)
		}
	})
}

func TestRegistryShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := session.NewRegistry(ctx)
		for range 3 {
			if _, _, _, err := r.Open(session.Config{Backend: backendtest.NewScreen(64, 48, 0)}); err != nil {
				t.Fatal(err)
			}
		}
		if r.Len() != 3 {
			t.Fatalf("%d sessions, want 3", r.Len())
		}
		cancel()
		synctest.Wait()
		if r.Len() != 0 {
			t.Errorf("%d sessions after shutdown", r.Len())
		}
	})
}
