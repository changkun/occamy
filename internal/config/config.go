// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"crypto/sha256"
	"flag"
	"fmt"
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

// Init initialize the runtime configurations
func Init() {
	log.SetPrefix("occamy: ")
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmsgprefix)

	loc := flag.String("conf", "./conf.yaml", "path to the runtime config file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `a modern guacamole protocol based remote desktop proxy written in Go.
Usage:`)
		flag.PrintDefaults()
	}
	flag.Parse()
	raw, err := os.ReadFile(*loc)
	if err != nil {
		log.Fatalf("cannot open given config file, error: %v", err)
	}
	Runtime, err = parse(raw)
	if err != nil {
		log.Fatalf("failed of parsing config file, error: %v", err)
	}
}

// Debug reports whether occamyd runs in debug mode. An empty mode is
// debug, as it was in gin.
func (c *config) Debug() bool {
	return c.Mode == "" || c.Mode == ModeDebug
}

// parse parses a config file and fills in defaults.
func parse(raw []byte) (*config, error) {
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
