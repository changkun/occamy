// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L/usr/local/lib -lguac
#include <stdlib.h>
#include <string.h>
#include "../guacamole/src/libguac/guacamole/error.h"
#include "../guacamole/src/libguac/guacamole/client.h"

void guac_error_reset() {
	guac_error = GUAC_STATUS_SUCCESS;
	guac_error_message = NULL;
}
*/
import "C"
import "unsafe"

// status defines all consts in guac_status
type status int

const (
	// No errors occurred and the operation was successful.
	statusSuccess status = iota

	// Insufficient memory to complete the operation.
	statusNoMemory

	// The resource associated with the operation can no longer be used as it
	// has reached the end of its normal lifecycle.
	statusClosed

	// Time ran out before the operation could complete.
	statusTimeout

	// An error occurred, and further information about the error is already
	// stored in errno.
	statusSeeErrno

	// An I/O error prevented the operation from succeeding.
	statusIOError

	// The operation could not be performed because an invalid argument was
	// given.
	statusInvalidArgument

	// The operation failed due to a bug in the software or a serious system
	// problem.
	statusInternalError

	// Insufficient space remaining to complete the operation.
	statusNoSpace

	// The operation failed because the input provided is too large.
	statusInputTooLarge

	// The operation failed because the result could not be stored in the
	// space provided.
	statusResultTooLarge

	// Permission was denied to perform the operation.
	statusPermissionDenied

	// The operation could not be performed because associated resources are
	// busy.
	statusBusy

	// The operation could not be performed because, while the associated
	// resources do exist, they are not currently available for use.
	statusNotAvailable

	// The requested operation is not supported.
	statusNotSupported

	// Support for the requested operation is not yet implemented.
	statusNotImplemented

	// The operation is temporarily unable to be performed, but may succeed if
	// reattempted.
	statusTryAgain

	// A violation of the Guacamole protocol occurred.
	statusProtocolError

	// The operation could not be performed because the requested resources do
	// not exist.
	statusNotFound

	// The operation was canceled prior to completion.
	statusCanceled

	// The operation could not be performed because input values are outside
	// the allowed range.
	statusOutOfRange

	// The operation could not be performed because access to an underlying
	// resource is explicitly not allowed, though not necessarily due to
	// permissions.
	statusRefused

	// The operation failed because too many resources are already in use.
	statusTooMany

	// The operation was not performed because it would otherwise block.
	statusWouldBlock
)

var statusString = map[status]string{
	statusSuccess:          "Success",
	statusNoMemory:         "Insufficient memory",
	statusClosed:           "Closed",
	statusTimeout:          "Timed out",
	statusSeeErrno:         "Input/output error",
	statusIOError:          "Invalid argument",
	statusInvalidArgument:  "Internal error",
	statusInternalError:    "UNKNOWN STATUS CODE",
	statusNoSpace:          "Insufficient space",
	statusInputTooLarge:    "Input too large",
	statusResultTooLarge:   "Result too large",
	statusPermissionDenied: "Permission denied",
	statusBusy:             "Resource busy",
	statusNotAvailable:     "Resource not available",
	statusNotSupported:     "Not supported",
	statusNotImplemented:   "Not implemented",
	statusTryAgain:         "Temporary failure",
	statusProtocolError:    "Protocol violation",
	statusNotFound:         "Not found",
	statusCanceled:         "Canceled",
	statusOutOfRange:       "Value out of range",
	statusRefused:          "Operation refused",
	statusTooMany:          "Insufficient resources",
	statusWouldBlock:       "Operation would block",
}

// errorStatus reports a message describing the error which occurred during the last
// function call. If an error occurred, but no message is associated with it,
// NULL is returned. This value is undefined if no error occurred.
func errorStatus() string {
	s := status((*(*C.guac_status)(unsafe.Pointer(C.__guac_error()))))
	val, ok := statusString[s]
	if !ok {
		return "Invalid status code"
	}
	return val
}

// ResetErrors resets guacamole runtime error
// and returns the former error message
func ResetErrors() string {
	old := errorStatus()
	C.guac_error_reset()
	return old
}
