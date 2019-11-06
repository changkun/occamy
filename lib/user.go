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
#include <stdio.h>
#include <syslog.h>

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



void _occamy_log(const char* format, va_list args) {
    int priority;
    char message[2048];
    vsnprintf(message, sizeof(message), format, args);
    syslog(priority, "%s", message);
    fprintf(stderr, "occamy-lib[%li]: %s\n", (unsigned long int)pthread_self(), message);
}
void occamy_log(const char* format, ...) {
    va_list args;
    va_start(args, format);
	_occamy_log(format, args);
	va_end(args);
}
static void printArray(char **a, int size) {
	int i;
	for (i = 0; i < size; i++) {
		occamy_log("arg: %s", a[i]);
	}
}
*/
import "C"
import (
	"errors"
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
	guacUser   *C.struct_guac_user
	guacClient *C.struct_guac_client
	info       connectInformation
	once       sync.Once
}

type connectInformation struct {
	Host     string
	Port     string
	Username string
	Password string
}

// NewUser creates a user and associate the user with any specific client
func NewUser(s *Socket, c *Client, owner bool, jwt *config.JWT) (*User, error) {
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

	host, port, err := net.SplitHostPort(jwt.Host)
	if err != nil {
		return nil, err
	}

	return &User{
		guacUser:   user,
		guacClient: c.guacClient,
		info: connectInformation{
			Host:     host,
			Port:     port,
			Username: jwt.Username,
			Password: jwt.Password,
		},
	}, nil
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

func (u *User) HandleConnectionWithHandshake() error {
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
	logrus.Info("args: ", args)

	cargs := C.makeCharArray(C.int(len(args)))
	defer C.freeCharArray(cargs, C.int(len(args)))

	for i, arg := range args {
		cstr := C.CString(arg)
		C.setArrayString(cargs, cstr, C.int(i))
	}
	C.printArray(cargs, C.int(len(args)))

	if int(C.guac_client_add_user(u.guacClient, u.guacUser, C.int(len(args)), cargs)) != 0 {
		logrus.Errorf("User %s could NOT join connection %s",
			C.GoString(u.guacUser.user_id), C.GoString(u.guacClient.connection_id))
		return errors.New(errorStatus())
	}

	parser := C.guac_parser_alloc()
	C.guac_user_start(parser, u.guacUser, C.int(int(usecTimeout))) // block here
	C.guac_client_remove_user(u.guacClient, u.guacUser)
	C.guac_parser_free(parser)
	logrus.Infof("User %s disconnected (%d users remain)",
		C.GoString(u.guacUser.user_id), int(u.guacClient.connected_users))
	return nil
}
