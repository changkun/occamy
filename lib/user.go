// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L/usr/local/lib -lguac
#include "../guacamole/src/libguac/guacamole/user.h"
*/
import "C"
import (
	"errors"
	"sync"
	"time"
)

// User is the representation of a physical connection within a larger logical connection
// which may be shared. Logical connections are represented by guac_client.
type User struct {
	guacUser *C.struct_guac_user
	once     sync.Once
}

// NewUser creates a user and associate the user with any specific client
func NewUser(s *Socket, c *Client, owner bool) (*User, error) {
	user := C.guac_user_alloc()
	if user == nil {
		return nil, errors.New(errorStatus())
	}
	user.socket = s.guacSocket
	user.client = c.guacClient
	if owner {
		user.owner = C.int(1)
	} else {
		user.owner = C.int(0)
	}
	return &User{guacUser: user}, nil
}

// Close frees the user and detach the association to the attached client
func (u *User) Close() {
	u.once.Do(func() {
		C.guac_user_free(u.guacUser)
	})
}

const usecTimeout time.Duration = 15 * time.Millisecond

// HandleConnection handles all I/O for the portion of a user's Guacamole connection
// following the initial "select" instruction, including the rest of the handshake.
// The handshake-related properties of the given guac_user are automatically
// populated, and HandleConnection() is invoked for all instructions received after
// the handshake has completed. This function blocks until the connection/user is aborted
// or the user disconnects.
func (u *User) HandleConnection() error {
	if int(C.guac_user_handle_connection(u.guacUser, C.int(int(usecTimeout)))) != 0 {
		return errors.New(errorStatus())
	}
	return nil
}
