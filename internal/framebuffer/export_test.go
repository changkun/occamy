// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package framebuffer

// Held is the number of Locks not yet unlocked or released, read under
// the lock that records them.
func Held(f *Framebuffer) int {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	return len(f.held)
}
