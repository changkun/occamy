// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import "syscall"

// IO is a fd wrap that implements io.Reader and io.Writer
type IO struct {
	fd int
}

// NewIO creates an IO
func NewIO(fd int) *IO {
	return &IO{fd}
}

// Read implements io.Reader
func (i IO) Read(buf []byte) (n int, err error) {
	n, err = syscall.Read(i.fd, buf)
	if err != nil {
		n = 0
	}
	return
}

// Write implements io.Writer
func (i IO) Write(buf []byte) (n int, err error) {
	n, err = syscall.Write(i.fd, buf)
	if err != nil {
		n = 0
	}
	return
}

// Close closes the IO
func (i IO) Close() error {
	return syscall.Close(i.fd)
}
