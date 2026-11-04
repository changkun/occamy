// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
)

// Load sets up the log, parses the command line and returns the contents
// of the configuration file that -conf names, for ParseV1. It exits if
// the file cannot be read.
func Load() []byte {
	log.SetPrefix("occamy: ")
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmsgprefix)

	loc := flags(flag.CommandLine)
	flag.Usage = func() { usage(flag.CommandLine.Output(), flag.CommandLine) }
	flag.Parse()
	raw, err := os.ReadFile(*loc)
	if err != nil {
		log.Fatalf("cannot read the configuration file: %v; name another with -conf", err)
	}
	return raw
}

// flags defines occamyd's flags on fs and returns -conf's value.
func flags(fs *flag.FlagSet) *string {
	return fs.String("conf", "./conf.yaml", "read the configuration from `file`, which must set version: 1")
}

// usage writes occamyd's help to w: what it does, how to run it, and
// the flags of fs.
func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `occamyd is Occamy's remote desktop server. It serves the remote desktops
and terminals that its configuration file lists to the web client,
Occamy for macOS and other clients of API v1 and protocol v1.

Usage:

	occamyd [-conf file]

Flags:

`)
	fs.SetOutput(w)
	fs.PrintDefaults()
}
