// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package backendtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// UnlocksDeferred fails t for every mu.Lock() in the Go files of the
// package in dir, but its tests, whose next line is not
// "defer mu.Unlock()", for a mutex such as "r.mu": a panic between the
// two would leave the mutex locked, and every goroutine that waits for
// it, such as the session's owner goroutine in Input, waiting forever.
func UnlocksDeferred(t testing.TB, dir, mu string) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	locks := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name) // #nosec G304 -- a package's own files
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if !strings.Contains(l, mu+".Lock()") {
				continue
			}
			locks++
			if strings.TrimSpace(l) != mu+".Lock()" || i+1 == len(lines) || strings.TrimSpace(lines[i+1]) != "defer "+mu+".Unlock()" {
				t.Errorf("%s:%d: %s is not unlocked by defer", name, i+1, mu)
			}
		}
	}
	if locks == 0 {
		t.Errorf("no %s.Lock() in %s", mu, dir)
	}
}
