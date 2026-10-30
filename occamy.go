// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"log"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/server"
)

// main serves the configuration file that -conf names: a v1 file
// (version: 1) through API v1, and a v0 file, without a version, as
// before.
func main() {
	raw := config.Load()
	version, err := config.Version(raw)
	if err != nil {
		log.Fatal(err)
	}
	if version == 0 {
		if err := config.Init(raw); err != nil {
			log.Fatalf("failed of parsing config file, error: %v", err)
		}
		server.RunV0()
		return
	}
	f, err := config.ParseV1(raw)
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Run(f); err != nil {
		log.Fatal(err)
	}
}
