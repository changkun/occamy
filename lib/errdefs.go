// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L${SRCDIR}/../guacamole/build/lib -lguac
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
import (
	"errors"
	"unsafe"
)

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

// Err returns a status error given a status
func Err(status status) error {
	return statusError[status]
}

var statusError = map[status]error{
	statusSuccess:          errors.New("Success"),
	statusNoMemory:         errors.New("Insufficient memory"),
	statusClosed:           errors.New("Closed"),
	statusTimeout:          errors.New("Timed out"),
	statusSeeErrno:         errors.New("Input/output error"),
	statusIOError:          errors.New("Invalid argument"),
	statusInvalidArgument:  errors.New("Internal error"),
	statusInternalError:    errors.New("UNKNOWN STATUS CODE"),
	statusNoSpace:          errors.New("Insufficient space"),
	statusInputTooLarge:    errors.New("Input too large"),
	statusResultTooLarge:   errors.New("Result too large"),
	statusPermissionDenied: errors.New("Permission denied"),
	statusBusy:             errors.New("Resource busy"),
	statusNotAvailable:     errors.New("Resource not available"),
	statusNotSupported:     errors.New("Not supported"),
	statusNotImplemented:   errors.New("Not implemented"),
	statusTryAgain:         errors.New("Temporary failure"),
	statusProtocolError:    errors.New("Protocol violation"),
	statusNotFound:         errors.New("Not found"),
	statusCanceled:         errors.New("Canceled"),
	statusOutOfRange:       errors.New("Value out of range"),
	statusRefused:          errors.New("Operation refused"),
	statusTooMany:          errors.New("Insufficient resources"),
	statusWouldBlock:       errors.New("Operation would block"),
}

// errorStatus reports a message describing the error which occurred during the last
// function call. If an error occurred, but no message is associated with it,
// NULL is returned. This value is undefined if no error occurred.
func errorStatus() error {
	s := status((*(*C.guac_status)(unsafe.Pointer(C.__guac_error()))))
	err, ok := statusError[s]
	if !ok {
		return errors.New("Invalid status code")
	}
	return err
}

// ResetErrors resets guacamole runtime error
// and returns the former error message
func ResetErrors() error {
	old := errorStatus()
	C.guac_error_reset()
	return old
}
