// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package session

// Shares is the number of share tokens the registry holds.
func (r *Registry) Shares() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.shares)
}

// Indexed is the number of sessions in the registry's index of shares.
func (r *Registry) Indexed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}
