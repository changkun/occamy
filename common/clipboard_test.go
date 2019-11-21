// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package common_test

import (
	"testing"

	"github.com/changkun/occamy/common"
)

func TestNewClipboard(t *testing.T) {
	if common.NewClipboard(0) == nil {
		t.Fatalf("new clipboard failed")
	}
}
