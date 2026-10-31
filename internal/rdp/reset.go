// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build !plan9

package rdp

import "syscall"

// errReset is the error of a connection the peer reset.
var errReset error = syscall.ECONNRESET
