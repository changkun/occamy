// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command querypointer prints where the pointer is on each X display from
// :10 to :19 that it can open, one "display x y" line each. The RDP
// integration test runs it inside the xrdp container to see that mouse
// input reaches the session.
package main

import (
	"fmt"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func main() {
	for d := 10; d < 20; d++ {
		name := fmt.Sprintf(":%d", d)
		x, err := xgb.NewConnDisplay(name)
		if err != nil {
			continue
		}
		root := xproto.Setup(x).DefaultScreen(x).Root
		if p, err := xproto.QueryPointer(x, root).Reply(); err == nil {
			fmt.Println(name, p.RootX, p.RootY)
		}
		x.Close()
	}
}
