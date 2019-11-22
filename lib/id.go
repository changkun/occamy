// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

import "github.com/google/uuid"

const (
	prefixUser   = "@"
	prefixClient = "$"
)

// NewID Generates a guaranteed-unique identifier which is a total of
// 37 characters long, having the given single-character prefix.
func NewID(prefix string) string {
	return prefix + uuid.New().String()
}
