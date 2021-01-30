// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"runtime"

	"changkun.de/x/occamy/server"
)

func init() {
	runtime.LockOSThread()
}

func main() {
	server.Run()
}
