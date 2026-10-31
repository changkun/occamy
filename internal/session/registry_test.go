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

		// A share opens only its own session.
		other, _, _, err := r.Open(session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.JoinShared(other, viewTok); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("a share used on another session returned %v", err)
		}
		v, err := r.JoinShared(id, viewTok)
		if err != nil || v.Role() != session.RoleViewer {
			t.Fatalf("JoinShared = %v, %v", v, err)
		}
		if _, err := r.JoinShared(id, "forged"); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("forged token returned %v", err)
		}

		// The read-only token expires after a minute.
		time.Sleep(time.Minute)
		if _, err := r.JoinShared(id, roTok); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("expired token returned %v", err)
		}

		// The session ends: it leaves the registry and its tokens die.
		s.Close()
		synctest.Wait()
		if r.Len() != 1 {
			t.Errorf("%d sessions after one of two ended", r.Len())
		}
		if _, err := r.JoinShared(id, viewTok); !errors.Is(err, session.ErrNotFound) {
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

// Expired shares leave when the session mints the next one, rather than
// piling up until it ends.
func TestRegistrySharesExpire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := session.NewRegistry(ctx)
		id, _, _, err := r.Open(session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := r.Share(id, session.RoleViewer, time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Minute)
		if _, err := r.Share(id, session.RoleViewer, time.Minute); err != nil {
			t.Fatal(err)
		}
		if n := r.Shares(); n != 1 {
			t.Errorf("%d shares held, want the live one", n)
		}
	})
}

// A session holds at most MaxShares unexpired shares, its expired ones
// make room, and its shares leave with it.
func TestRegistryShareLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := session.NewRegistry(ctx)
		r.MaxShares = 2
		id, s, _, err := r.Open(session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		if err != nil {
			t.Fatal(err)
		}
		other, so, _, err := r.Open(session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		if err != nil {
			t.Fatal(err)
		}
		short, err := r.Share(id, session.RoleViewer, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Share(id, session.RoleViewer, time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Share(id, session.RoleViewer, time.Hour); !errors.Is(err, session.ErrShareLimit) {
			t.Errorf("a third share returned %v, want ErrShareLimit", err)
		}
		if _, err := r.Share(other, session.RoleViewer, time.Hour); err != nil {
			t.Errorf("another session's share: %v", err)
		}
		time.Sleep(time.Minute)
		if _, err := r.JoinShared(id, short); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("an expired share returned %v", err)
		}
		if _, err := r.Share(id, session.RoleReadOnly, time.Hour); err != nil {
			t.Errorf("a share after one expired: %v", err)
		}
		if n := r.Shares(); n != 3 {
			t.Errorf("%d shares held, want 3", n)
		}
		s.Close()
		synctest.Wait()
		if n := r.Shares(); n != 1 {
			t.Errorf("%d shares held after the session ended, want the other's", n)
		}
		if n := r.Indexed(); n != 1 {
			t.Errorf("%d sessions indexed after one ended, want the other", n)
		}
		so.Close()
		synctest.Wait()
		if n, m := r.Shares(), r.Indexed(); n != 0 || m != 0 {
			t.Errorf("%d shares and %d sessions indexed after both ended", n, m)
		}
	})
}
