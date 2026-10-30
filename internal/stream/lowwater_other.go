// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build !linux && !darwin

package stream

import "net"

// SetLowWater does nothing where TCP_NOTSENT_LOWAT is not available.
func SetLowWater(c net.Conn, lowWater int) error { return nil }
