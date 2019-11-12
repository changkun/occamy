package lib

import "github.com/google/uuid"

// NewID Generates a guaranteed-unique identifier which is a total of
// 37 characters long, having the given single-character prefix.
func NewID() string {
	return "$" + uuid.New().String()
}
