// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import "errors"

// errReset stands for a reset connection, which Plan 9 reports by a
// message, not an errno: HungUp sees only the end of the stream there.
var errReset = errors.New("rdp: connection reset")
