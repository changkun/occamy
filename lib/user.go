// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L${SRCDIR}/../guacamole/build/lib -lguac
#include <stdlib.h>
#include "../guacamole/src/libguac/guacamole/parser.h"
#include "../guacamole/src/libguac/guacamole/user.h"
#include "../guacamole/src/libguac/guacamole/client.h"
#include "../guacamole/src/libguac/guacamole/protocol.h"
#include "../guacamole/src/libguac/guacamole/socket.h"

void set_user_info(guac_user* user) {
	user->info.optimal_width = 1024;
	user->info.optimal_height = 768;
	user->info.optimal_resolution = 96;
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
	"image"
	"net"
	"sync"
	"time"
	"unsafe"

	"github.com/changkun/occamy/config"
	"github.com/changkun/occamy/protocol"
	"github.com/sirupsen/logrus"
)

// UserMaxStreams is the character prefix which identifies a user ID.
const UserMaxStreams = 64

// UserClosedStreamIndex is the maximum number of inbound or
// outbound streams supported by any one lib.User
const UserClosedStreamIndex = -1

// User is the representation of a physical connection within a larger logical connection
// which may be shared. Logical connections are represented by guac_client.
type User struct {
	guacUser   *C.struct_guac_user
	guacClient *C.struct_guac_client
	once       sync.Once

	ID                string
	owner             bool
	active            bool
	lastReceived      time.Time
	lastFrameDuration time.Duration
	processingLag     time.Duration
	info              connectInformation
	client            *Client
	sock              *Socket
	poolStream        *Pool
	poolObject        *Pool
	inputStreams      [UserMaxStreams]Stream
	outputStreams     [UserMaxStreams]Stream
	prev, next        *User // points to next connected user
	data              interface{}
}

type connectInformation struct {
	Host     string
	Port     string
	Username string
	Password string

	optimalWidth      int
	optimalHeight     int
	optimalResolution int
}

// NewUser creates a user and associate the user with any specific client
func NewUser(s *Socket, c *Client, owner bool, jwt *config.JWT) (*User, error) {
	id := NewID(prefixUser)
	uid := C.CString(id)

	user := C.guac_user_alloc(uid)
	if user == nil {
		C.free(unsafe.Pointer(uid))
		return nil, errorStatus()
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

	// initialize streams
	istreams := [UserMaxStreams]Stream{}
	for i := 0; i < UserMaxStreams; i++ {
		istreams[i] = Stream{Index: UserClosedStreamIndex}
	}
	ostreams := [UserMaxStreams]Stream{}
	for i := 0; i < UserMaxStreams; i++ {
		ostreams[i] = Stream{Index: UserClosedStreamIndex}
	}

	return &User{
		guacUser:   user,
		guacClient: c.guacClient,

		ID:                id,
		owner:             owner,
		active:            true,
		lastReceived:      time.Now(),
		lastFrameDuration: 0,
		processingLag:     0,
		poolStream:        NewPool(0),
		inputStreams:      istreams,
		outputStreams:     ostreams,
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

func (u *User) Prepare() error {
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

// HandleConnection handles all I/O for the portion of a user's Occamy
// connection without the handshake process. This function blocks until
// the connection/user is aborted or the user disconnects.
func (u *User) HandleConnection(done chan struct{}) {
	// this should be called only if handshake is success.
	C.guac_client_add_user(u.guacUser)
	C.guac_user_input_thread(u.guacUser, C.int(int(usecTimeout))) // block here
	C.guac_client_remove_user(u.guacClient, u.guacUser)
	logrus.Infof("User %s disconnected (%d users remain)", u.ID, int(u.guacClient.connected_users))
	C.guac_protocol_send_disconnect(u.guacUser.socket)
	C.guac_socket_flush(u.guacUser.socket)
	close(done)
}

// HandleConnectionGo handles all I/O for the portion of a user's Occamy
// connection without the handshake process. This function blocks until
// the connection/user is aborted or the user disconnects.
func (u *User) HandleConnectionGo(done chan error) {
	// FIXME: Go version, this does not work at the moment
	p := protocol.NewParser()

	// Occamy user input loop
	for u.client.isRunning() && u.isActive() {
		raw, err := u.sock.reader.ReadBytes(byte(';'))
		if err != nil {
			if errors.Is(err, Err(statusTimeout)) {
				u.Debug("User is not responding.")
				u.Abort(err)
			} else {
				if errors.Is(err, Err(statusClosed)) {
					u.Debug("Occamy connection failure")
				}
				u.Stop()
			}
			done <- err
			return
		}
		ins := protocol.Instruction{}
		err = p.Parse(raw, &ins)
		if err != nil {
			u.Debug("Parse instruction error: %v", err)
			done <- err
			return
		}

		err = u.HandleInstruction(&ins)
		if err != nil {
			u.Debug("User connection aborted.", err)
			u.Debug("Failing instruction handler in user was %s", ins.Opcode())
			u.Stop()
			done <- err
			return
		}
	}

	close(done)
}

// Stop signals the given user that it must disconnect, or advises
// cooperating services that the given user is no longer connected.
func (u *User) Stop() {
	u.active = false
}

// HandleInstruction calls the appropriate handler defined by the given
// user for the given instruction. A comparison is made between the
// instruction opcode and the initial handler lookup table defined in
// user_handlers.go. The intial handlers will in turn call the user's
// handler (if defined).
func (u *User) HandleInstruction(ins *protocol.Instruction) error {
	handler, ok := instructionHandlers[ins.Opcode()]
	if !ok {
		return errors.New("unknown opcode")
	}

	return handler(u, ins)
}

// StreamPNG streams the image data of the given surface over an image
// stream ("img" instruction) as PNG-encoded data. The image stream will
// be automatically allocated and freed.
func (u *User) StreamPNG(mode CompositeMode, layer *Layer, x, y int, img *image.RGBA) {
	s := NewStreamFromUser(u)
	u.sock.SendImg(mode, layer, "image/png", x, y)
	u.sock.WritePNG(s, img)
	u.sock.SendEnd(s)
	s.FreeToUser(u)
}

// Debug logs debug information
func (u *User) Debug(format string, args ...interface{}) {
	logrus.Debugf(fmt.Sprintf("[u:%s] %s", u.ID, format), args)
}

// Abort sends error message to client and stops the connection
func (u *User) Abort(err error) {
	u.client.SendError("Aborted. Error: ", err)
	u.Stop()
}
