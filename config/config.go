// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"crypto/md5"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/gin-gonic/gin"
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
	Address string `yaml:"address"`
	Mode    string `yaml:"mode"`
	Auth    struct {
		JWTSecret    string `yaml:"jwt_secret"`
		JWTAlgorithm string `yaml:"jwt_alg"`
	} `yaml:"auth"`
	Client bool `yaml:"client"`
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
		log.Fatalf("cannot open given config file, error: %v", err)
	}
	err = yaml.Unmarshal(raw, Runtime)
	if err != nil {
		log.Fatalf("failed of parsing config file, error: %v", err)
	}
	gin.SetMode(Runtime.Mode)
}
