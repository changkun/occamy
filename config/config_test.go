// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"os"
	"testing"

	"github.com/changkun/occamy/config"
)

func TestJWT_GenerateID(t *testing.T) {
	os.Args[1] = "-conf=../conf.yaml"
	config.Init()

	j := config.JWT{
		Protocol: "vnc",
		Host:     "0.0.0.0:5636",
		Username: "occamy",
		Password: "occamy",
	}
	want := "d742d2c10082f08506028cfb09cd1674"
	if want != j.GenerateID() {
		t.Error("jwt hash error, got: ", j.GenerateID())
		t.FailNow()
	}
}
