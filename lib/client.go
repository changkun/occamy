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

// FIXME: Cgo somehow gives error message:
// c.guacClient.connection_id undefined (type *_Ctype_struct_guac_client has no field or method connection_id)
//
// Therefore, we need the following helper to obtain connection_id:
char *guac_client_get_identifier(guac_client *client) {
	return client->connection_id;
}
*/
import "C"
import (
	"errors"
	"sync"
	"unsafe"
)

// clientLogLevel All supported log levels used by the logging subsystem of each Guacamole
// client. With the exception of GUAC_LOG_TRACE, these log levels correspond to
// a subset of the log levels defined by RFC 5424.
type clientLogLevel int

const (
	// ClientLogError represents fatal errors.
	clientLogError clientLogLevel = C.GUAC_LOG_ERROR
	// ClientLogWarning represents non-fatal conditions that indicate problems.
	clientLogWarning clientLogLevel = C.GUAC_LOG_WARNING
	// ClientLogInfo represents informational messages of general interest to users or
	// administrators.
	clientLogInfo clientLogLevel = C.GUAC_LOG_INFO
	// ClientLogDebug represents informational messages which can be useful for debugging,
	// but are otherwise not useful to users or administrators. It is expected that
	// debug level messages, while verbose, will not negatively affect
	// performance.
	clientLogDebug clientLogLevel = C.GUAC_LOG_DEBUG
	// ClientLogTrace represents informational messages which can be useful for debugging,
	// like GUAC_LOG_DEBUG, but which are so low-level that they may affect
	// performance.
	clientLogTrace clientLogLevel = C.GUAC_LOG_TRACE
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
	guacClient   *C.struct_guac_client
	connectionID string
	once         sync.Once
}

// NewClient creates a new guacamole client
func NewClient() (*Client, error) {
	cli := C.guac_client_alloc()
	if cli == nil {
		return nil, errors.New(errorStatus())
	}
	return &Client{guacClient: cli}, nil
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

// Identifier returns the connection id of a guacamole client. the id will be allocated
// after calling c.LoadPlugin.
func (c *Client) Identifier() string {
	return C.GoString(C.guac_client_get_identifier(c.guacClient))
}
