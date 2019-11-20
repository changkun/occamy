// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L/usr/local/lib -lguac

#include <stdio.h>
#include <stdlib.h>
#include <syslog.h>

#include "../guacamole/src/libguac/guacamole/client.h"

int max_log_level;

void occamy_client_log(guac_client* client, guac_client_log_level level, const char* format, va_list args) {
    int priority;
    char message[2048];
    if (level > max_log_level) return;
    vsnprintf(message, sizeof(message), format, args);
    syslog(priority, "%s", message);
    fprintf(stderr, "occamy-lib[%li]: %s\n", (unsigned long int)pthread_self(), message);
}
void init_client_log(guac_client* client, int level) {
	client->log_handler = occamy_client_log;
	max_log_level = level;
}
*/
import "C"
import (
	"errors"
	"sync"
	"time"
	"unsafe"
)

// ClientMouse ...
type ClientMouse int

// ClientMouse constants
const (
	ClientMouseLeft       ClientMouse = 0x01
	ClientMouseMiddle     ClientMouse = 0x02
	ClientMouseRight      ClientMouse = 0x04
	ClientMouseScrollUp   ClientMouse = 0x08
	ClientMouseScrollDown ClientMouse = 0x10
)

// ClientState gives current states of the Occamy client. Currently,
// the only two states are ClientStateRunning and ClientStateStopping.
type ClientState int

const (
	// ClientStateRunning is the state of the client from when it has
	// been allocated by the main daemon until it is killed or disconnected.
	ClientStateRunning ClientState = iota
	// ClientStateStopping is the state of the client when a stop has
	// been requested, signalling the I/O threads to shutdown.
	ClientStateStopping
)

// clientLogLevel All supported log levels used by the logging subsystem of each Occamy
// client. With the exception of GUAC_LOG_TRACE, these log levels correspond to
// a subset of the log levels defined by RFC 5424.
type clientLogLevel int

const (
	// ClientLogError represents fatal errors.
	clientLogError clientLogLevel = 3
	// ClientLogWarning represents non-fatal conditions that indicate problems.
	clientLogWarning clientLogLevel = 4
	// ClientLogInfo represents informational messages of general interest to users or
	// administrators.
	clientLogInfo clientLogLevel = 6
	// ClientLogDebug represents informational messages which can be useful for debugging,
	// but are otherwise not useful to users or administrators. It is expected that
	// debug level messages, while verbose, will not negatively affect
	// performance.
	clientLogDebug clientLogLevel = 7
	// ClientLogTrace represents informational messages which can be useful for debugging,
	// like GUAC_LOG_DEBUG, but which are so low-level that they may affect
	// performance.
	clientLogTrace clientLogLevel = 8
)

// clientLogLevelTable provides a mapping from configuration string to guacamole
// libguac log level
var clientLogLevelTable = map[string]clientLogLevel{
	"info":    clientLogInfo,
	"error":   clientLogError,
	"warning": clientLogWarning,
	"debug":   clientLogDebug,
	"trace":   clientLogTrace,
}

// Client is a guacamole client container
type Client struct {
	guacClient *C.struct_guac_client
	once       sync.Once

	ID            string
	socket        *Socket
	state         ClientState
	data          interface{}
	lastSent      time.Time
	poolBuffer    *Pool
	poolLayer     *Pool
	poolStream    *Pool
	outputStreams [64]Stream

	mu             sync.RWMutex
	users          *User // list of all connected users
	owner          *User
	connectedUsers int64

	handlerFree  func()
	handlerLog   func()
	handlerJoin  func()
	handlerLeave func()
	args         []string
	pluginHandle interface{}
}

// NewClient creates a new guacamole client
func NewClient() (*Client, error) {
	id := NewID(prefixClient)
	cid := C.CString(id)
	cli := C.guac_client_alloc(cid)
	if cli == nil {
		C.free(unsafe.Pointer(cid))
		return nil, errors.New(errorStatus())
	}

	// initialize streams
	streams := [64]Stream{}
	for i := 0; i < 64; i++ {
		streams[i] = Stream{Index: ClientClosedStreamIndex}
	}

	return &Client{
		guacClient: cli,

		ID:            id,
		socket:        nil, // TODO: Set up socket to broadcast to all users
		state:         ClientStateRunning,
		lastSent:      time.Now(),
		poolBuffer:    NewPool(BufferPoolInitialSize),
		poolLayer:     NewPool(BufferPoolInitialSize),
		poolStream:    NewPool(0),
		outputStreams: streams,
		args:          []string{},
	}, nil
}

// isRunning checks if a client is still running
func (c *Client) isRunning() bool {
	if c.guacClient.state == C.GUAC_CLIENT_RUNNING {
		return true
	}
	return false
}

// Close closes the corresponding guacamole client
func (c *Client) Close() {
	c.once.Do(func() {
		C.guac_client_stop(c.guacClient)
		C.guac_client_free(c.guacClient)
		c.guacClient = nil
	})
}

// InitLogLevel initialize guacamole's libguac maximum log level
func (c *Client) InitLogLevel(level string) {
	maxLevel, ok := clientLogLevelTable[level]
	if !ok {
		maxLevel = clientLogInfo
	}
	C.init_client_log(c.guacClient, C.int(maxLevel))
}

// LoadProtocolPlugin initializes the given guac_client using the initialization routine
// provided by the plugin corresponding to the named protocol. This will automatically
// invoke guac_client_init within the plugin for the given protocol.
//
// Note that the connection will likely not be established until the first
// user (the "owner") is added to the client.
func (c *Client) LoadProtocolPlugin(proto string) error {
	cproto := C.CString(proto)
	defer C.free(unsafe.Pointer(cproto))

	if int(C.guac_client_load_plugin(c.guacClient, cproto)) != 0 {
		return errors.New(errorStatus())
	}
	return nil
}

// ForeachUser calls the given function on all currently-connected users
// of the given client. The function will be given a reference to a
// lib.User and the specified arbitrary data. The value returned by the
// callback will be ignored.
func (c *Client) ForeachUser(
	callback func(u *User, data interface{}) interface{},
	data interface{},
) {
	c.mu.RLock()
	u := c.users
	for u != nil {
		callback(u, data)
		u = u.next
	}
	c.mu.RUnlock()
}

// StreamPNG streams the image data of the given surface over an image
// stream ("img" instruction) as PNG-encoded data. The image stream will
// be automatically allocated and freed.
func (c *Client) StreamPNG(s *Socket, mode CompositeMode, layer *Layer, x, y int, surface interface{}) {
	// stream := NewStreamFromClient(c)
	// protocol.SendImg(s, stream, mode, layer, "image/png", x, y)
}
