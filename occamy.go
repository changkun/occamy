// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"log"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/server"
)

// main serves the version 1 configuration file that -conf names through
// API v1.
func main() {
	f, err := config.ParseV1(config.Load())
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Run(f); err != nil {
		log.Fatal(err)
	}
}
