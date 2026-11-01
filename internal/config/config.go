// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"go.yaml.in/yaml/v3"
)

// JWT is a configuration structure between browser client and server. The
// v0 login requires a protocol, a host and a password.
type JWT struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// GenerateID returns the session key of the token: a SHA-256 hash of its
// claims. Each claim is length-prefixed, so that different claims never
// run together into the same input (username "ab" and password "c"
// against username "a" and password "bc").
func (j *JWT) GenerateID() string {
	h := sha256.New()
	for _, claim := range []string{j.Protocol, j.Host, j.Username, j.Password} {
		fmt.Fprintf(h, "%d.%s,", len(claim), claim)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// GuacdParams returns the guacd connection parameters for the token:
// hostname and port from its host, its username and password, and the
// defaults for its protocol for any other parameter.
func (j *JWT) GuacdParams(defaults map[string]map[string]string) (map[string]string, error) {
	host, port, err := net.SplitHostPort(j.Host)
	if err != nil {
		return nil, err
	}
	params := map[string]string{
		"hostname": host,
		"port":     port,
		"username": j.Username,
		"password": j.Password,
	}
	for name, value := range defaults[j.Protocol] {
		if _, ok := params[name]; !ok {
			params[name] = value
		}
	}
	return params, nil
}

// DefaultGuacdAddress is the guacd address used when none is configured.
const DefaultGuacdAddress = "localhost:4822"

// Guacd configures the connection to guacd.
type Guacd struct {
	Address string `yaml:"address"`
	// Params holds connection parameters per protocol. Token values
	// override them.
	Params map[string]map[string]string `yaml:"params"`
}

// Modes, as gin named them when v0 ran on it. Debug mode serves pprof.
const (
	ModeDebug   = "debug"
	ModeRelease = "release"
	ModeTest    = "test"
)

// config is a
type config struct {
	Address string `yaml:"address"`
	Mode    string `yaml:"mode"` // debug, release or test; debug if empty
	Auth    struct {
		JWTSecret    string `yaml:"jwt_secret"`
		JWTAlgorithm string `yaml:"jwt_alg"`
	} `yaml:"auth"`
	Client bool  `yaml:"client"`
	Guacd  Guacd `yaml:"guacd"`
}

// Runtime configurations
var Runtime = &config{}

// Load sets up the log, parses the command line and returns the contents
// of the configuration file that -conf names. It exits if the file
// cannot be read. Version tells a v0 file, for Init, from a v1 file, for
// ParseV1.
func Load() []byte {
	log.SetPrefix("occamy: ")
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmsgprefix)

	loc := flags(flag.CommandLine)
	flag.Usage = func() { usage(flag.CommandLine.Output(), flag.CommandLine) }
	flag.Parse()
	raw, err := os.ReadFile(*loc)
	if err != nil {
		log.Fatalf("cannot open given config file, error: %v", err)
	}
	return raw
}

// flags defines occamyd's flags on fs and returns -conf's value.
func flags(fs *flag.FlagSet) *string {
	return fs.String("conf", "./conf.yaml", "the configuration `file`: a v1 file, or a v0 file")
}

// usage writes occamyd's help to w: what it does, how to run it, and
// the flags of fs.
func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `occamyd is Occamy's remote desktop server. It serves the connections
that its configuration file lists: through API v1, protocol v1 and the
web client for a v1 file (version: 1), and through the v0 routes for a
v0 file.

Usage:

	occamyd [-conf file]

Flags:

`)
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// Init parses a v0 configuration file into Runtime. It refuses a v1
// file, which the lenient v0 decoder would otherwise read with defaults.
func Init(raw []byte) error {
	c, err := parse(raw)
	if err != nil {
		return err
	}
	Runtime = c
	return nil
}

// Debug reports whether occamyd runs in debug mode. An empty mode is
// debug, as it was in gin.
func (c *config) Debug() bool {
	return c.Mode == "" || c.Mode == ModeDebug
}

// parse parses a config file and fills in defaults.
func parse(raw []byte) (*config, error) {
	switch v, err := Version(raw); {
	case err != nil:
		return nil, err
	case v != 0:
		return nil, errors.New("config: a version 1 file is not v0 configuration; ParseV1 reads it")
	}
	c := &config{}
	if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, err
	}
	switch c.Mode {
	case "", ModeDebug, ModeRelease, ModeTest:
	default:
		// gin.SetMode panicked on any other mode.
		return nil, fmt.Errorf("unknown mode %q, want %s, %s or %s", c.Mode, ModeDebug, ModeRelease, ModeTest)
	}
	if c.Guacd.Address == "" {
		c.Guacd.Address = DefaultGuacdAddress
	}
	return c, nil
}
