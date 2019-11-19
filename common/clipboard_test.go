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
