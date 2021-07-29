// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

const (
	// UserMaxObjects is the index of a closed stream.
	UserMaxObjects = 64
	// UserUndefinedObjectIndex is the index of an object which has not
	// been defined.
	UserUndefinedObjectIndex = -1
	// UserObjectRootName is the stream name reserved for the root of a
	// Occamy protocol object.
	UserObjectRootName = "/"
	// UserStreamIndexMimetype is the mimetype of a stream containing
	// a map of available stream names to their corresponding mimetypes.
	// The root of a Guacamole protocol object is guaranteed to have
	// this type.
	UserStreamIndexMimetype = "application/vnd.glyptodon.guacamole.stream-index+json"
)
