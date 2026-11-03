// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

// TestV0Refused checks that a v0 file, without a version, is refused
// with ErrV0, which says to write a version 1 file, rather than with the
// problems of every v0 key.
func TestV0Refused(t *testing.T) {
	for _, raw := range []string{
		"address: 0.0.0.0:5636\nmode: release\nauth:\n  jwt_secret: occamy\nguacd:\n  address: localhost:4822\n",
		"",
		"listen: 127.0.0.1:5637\n",
	} {
		if _, err := config.ParseV1([]byte(raw)); !errors.Is(err, config.ErrV0) {
			t.Errorf("ParseV1(%q) = %v, want ErrV0", raw, err)
		}
	}
	if !strings.Contains(config.ErrV0.Error(), "version 1") {
		t.Errorf("ErrV0 does not say what to write: %v", config.ErrV0)
	}
}

// TestShippedConf checks the conf.yaml that the image and the archives
// ship: a version 1 file whose only problem is the user it leaves to the
// operator, since occamyd has no default user.
func TestShippedConf(t *testing.T) {
	raw, err := os.ReadFile("../../conf.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := config.Version(raw); err != nil || v != 1 {
		t.Fatalf("Version(conf.yaml) = %d, %v; want 1", v, err)
	}
	_, err = config.ParseV1(raw)
	var problems config.Problems
	if !errors.As(err, &problems) {
		t.Fatalf("ParseV1(conf.yaml) = %v, want the problem of a file without users", err)
	}
	for _, p := range problems {
		switch {
		case p.Path == "auth" && strings.Contains(p.Message, "nobody could sign in"):
		case p.Path == "auth.signing_key_file":
			// /var/lib/occamy exists in the image, not on every host.
		default:
			t.Errorf("conf.yaml: %v", p)
		}
	}
}
