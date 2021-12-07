// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacd_test

import (
	"testing"

	"changkun.de/x/occamy/internal/guacd"
)

func TestNewClient(t *testing.T) {
	cli, err := guacd.NewClient()
	if err != nil {
		t.Errorf("%v", err)
		t.FailNow()
	}
	t.Run("init-log-level", func(t *testing.T) {
		cli.InitLogLevel("info")
		cli.InitLogLevel("unknown")
	})
	t.Run("load-protocol-plugin", func(t *testing.T) {
		err := cli.LoadProtocolPlugin("vnc")
		if err != nil {
			t.FailNow()
		}
		if cli.ID == "" {
			t.FailNow()
		}
	})
	cli.Close()
}
