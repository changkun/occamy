// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

var (
	Parse = parse
	Flags = flags
	Usage = usage
)

// Revalidate validates a File again, as ParseV1 does, and returns the
// problems found.
func Revalidate(f *File) Problems {
	c := &checker{lines: map[string]int{}}
	f.validate(c)
	return c.problems
}

// MaxSecretFile is the most a secret file may hold, for the test.
const MaxSecretFile = maxSecretFile
