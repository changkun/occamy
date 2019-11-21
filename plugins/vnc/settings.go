// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// Settings ...
type Settings struct {
	hostname          string
	port              int
	password          string
	encoding          string
	swapRedBlue       bool
	colorDepth        int
	readOnly          bool
	retries           int
	clipboardEncoding string
}

// Parse ...
func (s *Settings) Parse() {
}
