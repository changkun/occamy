// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L/usr/local/lib -lguac
#include <stdlib.h>
#include "../guacamole/src/libguac/guacamole/parser.h"
#include "../guacamole/src/libguac/guacamole/user.h"
#include "../guacamole/src/libguac/guacamole/client.h"
#include "../guacamole/src/libguac/guacamole/protocol.h"
#include "../guacamole/src/libguac/guacamole/socket.h"

const char *mimetypes[] = {"", NULL};
void set_user_info(guac_user* user) {
	user->info.optimal_width = 1024;
	user->info.optimal_height = 768;
	user->info.optimal_resolution = 96;
	user->info.audio_mimetypes = (const char**) mimetypes;
	user->info.video_mimetypes = (const char**) mimetypes;
	user->info.image_mimetypes = (const char**) mimetypes;
}
int get_args_length(const char** args) {
	int i = 0;
	int argc = 0;
	for (i=0; args[i] != NULL; i++) {
		argc++;
	}
	return argc;
}
static char** makeCharArray(int size) {
	return calloc(sizeof(char*), size);
}
static void setArrayString(char **a, char *s, int n) {
	a[n] = s;
}
static void freeCharArray(char **a, int size) {
	int i;
	for (i = 0; i < size; i++)
		free(a[i]);
	free(a);
}
static int join_handler_bridge(guac_user* user, int argc, char** argv) {
	int retval = 0;
	if (user->client->join_handler)
		retval = user->client->join_handler(user, argc, argv);
	return retval;
}
*/
import "C"
import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
	"unsafe"

	"github.com/changkun/occamy/config"
	"github.com/sirupsen/logrus"
)

// User is the representation of a physical connection within a larger logical connection
// which may be shared. Logical connections are represented by guac_client.
type User struct {
	ID         string
	guacUser   *C.struct_guac_user
	guacClient *C.struct_guac_client
	info       connectInformation
	once       sync.Once

	client *Client
	next   *User // points to next connected user
}

type connectInformation struct {
	Host     string
	Port     string
	Username string
	Password string
}

// NewUser creates a user and associate the user with any specific client
func NewUser(s *Socket, c *Client, owner bool, jwt *config.JWT) (*User, error) {
	id := NewID(prefixUser)
	uid := C.CString(id)

	user := C.guac_user_alloc(uid)
	if user == nil {
		C.free(unsafe.Pointer(uid))
		return nil, errors.New(errorStatus())
	}
	user.socket = s.guacSocket
	user.client = c.guacClient
	if owner {
		user.owner = C.int(1)
	} else {
		user.owner = C.int(0)
	}

	host, port, err := net.SplitHostPort(jwt.Host)
	if err != nil {
		C.free(unsafe.Pointer(uid))
		return nil, err
	}

	return &User{
		ID:         id,
		guacUser:   user,
		guacClient: c.guacClient,
		info: connectInformation{
			Host:     host,
			Port:     port,
			Username: jwt.Username,
			Password: jwt.Password,
		},
		client: c,
	}, nil
}

// Close frees the user and detach the association to the attached client
func (u *User) Close() {
	u.once.Do(func() {
		C.guac_user_free(u.guacUser)
	})
}

// isActive checks if a user is still active
func (u *User) isActive() bool {
	if u.guacUser.active != 0 {
		return true
	}
	return false
}

const usecTimeout time.Duration = 15 * time.Millisecond

func (u *User) MockHandshake() error {
	// general args
	C.set_user_info(u.guacUser)

	// client args
	length := int(C.get_args_length(u.guacClient.args))
	tmpslice := (*[1 << 30]*C.char)(unsafe.Pointer(u.guacClient.args))[:length:length]
	args := make([]string, length)
	for i, s := range tmpslice {
		args[i] = C.GoString(s)
	}
	for i := range args {
		switch args[i] {
		case "hostname":
			args[i] = u.info.Host
		case "port":
			args[i] = u.info.Port
		case "username":
			args[i] = u.info.Username
		case "password":
			args[i] = u.info.Password
		default:
			args[i] = ""
		}
	}

	// create args for C
	cargs := C.makeCharArray(C.int(len(args)))
	defer C.freeCharArray(cargs, C.int(len(args)))
	for i, arg := range args {
		cstr := C.CString(arg)
		C.setArrayString(cargs, cstr, C.int(i))
	}

	var ret C.int = 0
	// initiate join handler
	if u.guacClient.join_handler != nil {
		ret = C.join_handler_bridge(u.guacUser, C.int(len(args)), cargs)
	}

	if int(ret) != 0 {
		logrus.Errorf("User %s could NOT join connection %s",
			C.GoString(u.guacUser.user_id), C.GoString(u.guacClient.connection_id))
		return errors.New("occamy-lib: user cannot join")
	}

	return nil
}

// HandleConnection handles all I/O for the portion of a user's Guacamole connection
// without the handshake process. This function blocks until the connection/user is
// aborted or the user disconnects.
func (u *User) HandleConnection(done chan struct{}) {
	// this should be called only if handshake is success.
	C.guac_client_add_user(u.guacUser)
	C.guac_user_input_thread(u.guacUser, C.int(int(usecTimeout))) // block here

	// FIXME: THIS IS A TIGHT CGO CALL LOOP
	// p := C.guac_parser_alloc()
	// defer C.guac_parser_free(p)
	// for u.client.isRunning() && u.isActive() {
	// 	if int(C.guac_parser_read(p, u.guacUser.socket, C.int(int(usecTimeout)))) != 0 {
	// 		logrus.Info("Guacamole connection failure.")
	// 		C.guac_user_stop(u.guacUser)
	// 		break
	// 	}

	// 	if C.guac_user_handle_instruction(u.guacUser, p.opcode, p.argc, p.argv) < 0 {
	// 		logrus.Error("occamy-lib: user connection aborted.")
	// 		logrus.Error("occamy-lib: Failing instruction handler in user was XXX")
	// 		C.guac_user_stop(u.guacUser)
	// 		break
	// 	}
	// }

	C.guac_client_remove_user(u.guacClient, u.guacUser)
	logrus.Infof("User %s disconnected (%d users remain)", u.ID, int(u.guacClient.connected_users))
	C.guac_protocol_send_disconnect(u.guacUser.socket)
	C.guac_socket_flush(u.guacUser.socket)
	close(done)
}

// Debug logs debug information
func (u *User) Debug(format string, args ...interface{}) {
	logrus.Debugf(fmt.Sprintf("[u:%s] %s", u.ID, format), args)
}
