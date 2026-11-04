// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"log/slog"
	"os"
	"slices"
	"sync"
)

// reloaded is what load reads from files, read again when one of them
// changes, so that a renewed certificate or a rotated key takes effect
// without a restart, which would end every session. Each get compares
// the files' sizes and modification times with those at the last load.
// A load that fails keeps the value before it, with a warning, until
// the files change again.
type reloaded[T any] struct {
	what  string // what the files hold, for the log
	paths []string
	load  func() (T, error)

	mu    sync.Mutex
	stamp []fileStamp
	v     T
}

// fileStamp is how a file looked: its modification time and size, or
// the zero stamp if it could not be read.
type fileStamp struct {
	mod, size int64
}

// newReloaded loads the value from paths, and fails if it cannot.
func newReloaded[T any](what string, load func() (T, error), paths ...string) (*reloaded[T], error) {
	r := &reloaded[T]{what: what, paths: paths, load: load, stamp: stamps(paths)}
	v, err := load()
	if err != nil {
		return nil, err
	}
	r.v = v
	return r, nil
}

// get returns the value, loaded again first if a file changed.
func (r *reloaded[T]) get() T {
	now := stamps(r.paths)
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Equal(now, r.stamp) {
		return r.v
	}
	r.stamp = now
	v, err := r.load()
	if err != nil {
		slog.Warn(r.what+" changed but doesn't load; keeping the one before", "err", err)
		return r.v
	}
	r.v = v
	slog.Info(r.what + " reloaded")
	return v
}

func stamps(paths []string) []fileStamp {
	s := make([]fileStamp, len(paths))
	for i, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			s[i] = fileStamp{mod: fi.ModTime().UnixNano(), size: fi.Size()}
		}
	}
	return s
}
