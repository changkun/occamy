// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"crypto/md5"
	"flag"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// JWT is a configuration structure between browser client and server
type JWT struct {
	Protocol string `form:"protocol" json:"protocol" binding:"required"`
	Host     string `form:"host"     json:"host"     binding:"required"`
	Username string `form:"username" json:"username"`
	Password string `form:"password" json:"password" binding:"required"`
}

// GenerateID generates a unique id based on JWT information
func (j *JWT) GenerateID() string {
	h := md5.New()
	h.Write([]byte(fmt.Sprintf("%s%s%s%s", j.Protocol, j.Host, j.Username, j.Password)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// config is a
type config struct {
	Address     string `yaml:"address"`
	Mode        string `yaml:"mode"`
	MaxLogLevel string `yaml:"max_log_level"`
	Auth        struct {
		JWTSecret    string `yaml:"jwt_secret"`
		JWTAlgorithm string `yaml:"jwt_alg"`
	} `yaml:"auth"`
}

// Runtime configurations
var Runtime = &config{}

// Only set when built
var (
	Version   = "x.y.z"
	BuildTime = "2019-02-01"
	GitCommit = "abcdefg"
)

const (
	usage = `
a modern guacamole protocol based remote desktop proxy written in Go.
Version: %s
Build: %s
Git commit: %s
Usage:
`
)

// Init initialize the runtime configurations
func Init() {
	loc := flag.String("conf", "./conf.yaml", "path to the runtime config file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, fmt.Sprintf(usage, Version, BuildTime, GitCommit))
		flag.PrintDefaults()
	}
	flag.Parse()
	raw, err := ioutil.ReadFile(*loc)
	if err != nil {
		logrus.Fatalf("occamy-proxy: cannot open given config file, error: %v", err)
	}
	err = yaml.Unmarshal(raw, Runtime)
	if err != nil {
		logrus.Fatalf("occamy-proxy: failed of parsing config file, error: %v", err)
	}
	lvl, err := logrus.ParseLevel(Runtime.MaxLogLevel)
	if err != nil {
		logrus.Fatalf("occamy-proxy: unknown log level in config file")
	}
	logrus.SetLevel(lvl)
	gin.SetMode(Runtime.Mode)
}
