// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L/usr/local/lib -lguac

#include <stdio.h>
#include <stdlib.h>
#include <syslog.h>

#include "../../guacamole/src/libguac/guacamole/client.h"

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
	"sync"
	"time"
	"unsafe"

	"changkun.de/x/occamy/internal/uuid"
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
	"release": clientLogError,
	"test":    clientLogInfo,
	"debug":   clientLogDebug,
}

// Client is a guacamole client container
type Client struct {
	guacClient *C.struct_guac_client
	ID         string
	running    bool
	data       interface{}
	lastSent   time.Time

	mu             sync.RWMutex
	users          *User // list of all connected users
	owner          *User
	connectedUsers int64

	handlerFree  func()
	handlerLog   func()
	handlerJoin  func()
	handlerLeave func()
	args         []string
}

// NewClient creates a new guacamole client
func NewClient() (*Client, error) {
	id := uuid.NewID("$")
	cid := C.CString(id)
	cli := C.guac_client_alloc(cid)
	if cli == nil {
		C.free(unsafe.Pointer(cid))
		return nil, errorStatus()
	}

	return &Client{
		guacClient: cli,

		ID:       id,
		running:  true,
		lastSent: time.Now(),
		args:     []string{},
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
	C.guac_client_stop(c.guacClient)
	C.guac_client_free(c.guacClient)
	c.guacClient = nil
}

// InitLogLevel initialize guacamole's libguac maximum log level
func (c *Client) InitLogLevel(level string) {
	maxLevel, ok := clientLogLevelTable[level]
	if !ok {
		maxLevel = clientLogInfo
	}
	C.init_client_log(c.guacClient, C.int(maxLevel))
}

// LoadProtocolPlugin initializes the given guac_client using the
// initialization routine provided by the plugin corresponding to the
// named protocol. This will automatically invoke guac_client_init
// within the plugin for the given protocol.
//
// Note that the connection will likely not be established until the
// first user (the "owner") is added to the client.
func (c *Client) LoadProtocolPlugin(proto string) error {
	cproto := C.CString(proto)
	defer C.free(unsafe.Pointer(cproto))

	if int(C.guac_client_load_plugin(c.guacClient, cproto)) != 0 {
		return errorStatus()
	}
	return nil
}
