// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

/*
#cgo LDFLAGS: -L/usr/local/lib -lguac
#include "../guacamole/src/libguac/guacamole/socket.h"
#include "../guacamole/src/libguac/guacamole/client.h"
*/
import "C"
import (
	"bufio"
	"image"
	"sync"
	"syscall"
	"time"

	"github.com/changkun/occamy/protocol"
)

// type ISocket interface {
// 	Read()
// 	Write()
// 	Flush()
// 	Lock()
// 	UnLock()
// 	Select()
// 	Free()
// }

// Socket is a wrapper of given open file descriptor
type Socket struct {
	guacSocket *C.struct_guac_socket
	once       sync.Once

	fd        int
	reader    *bufio.Reader
	writer    *bufio.Writer
	lastWrite time.Time
	readyBuf  []byte
	IsOpen    bool
	data      interface{}
}

// NewSocket allocates and initialize a new guac_socket object with given
// open file descriptor. The file descriptor will be automatically closed
// when the allocated guac_socket is freed.
//
// If an error occurs while allocating the guac_socket object, guac_error
// will be returned as error.
func NewSocket(fd int) (*Socket, error) {
	guacSocket := C.guac_socket_open(C.int(fd))
	if guacSocket == nil {
		return nil, errorStatus()
	}

	conn := protocol.NewIO(fd)
	return &Socket{
		fd:         fd,
		guacSocket: guacSocket,
		reader:     bufio.NewReader(conn),
		writer:     bufio.NewWriter(conn),
	}, nil
}

// Close closes the Socket and all associated resources.
func (s *Socket) Close() {
	s.once.Do(func() {
		syscall.Close(s.fd)
		C.guac_socket_free(s.guacSocket)
	})
}

// Read data from the socket, filling up to the specified number
// of bytes in the given buffer.
func (s *Socket) Read(buf []byte) (int, error) {
	return syscall.Read(s.fd, buf)
}

// Write all given data to the specified socket.
func (s *Socket) Write(buf []byte) error {
	for len(buf) > 0 {
		n, err := syscall.Write(s.fd, buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

func (s *Socket) SendAck()                                                    {}
func (s *Socket) SendBolb()                                                   {}
func (s *Socket) SendBody()                                                   {}
func (s *Socket) SendCfill()                                                  {}
func (s *Socket) SendClip()                                                   {}
func (s *Socket) SendClipboard()                                              {}
func (s *Socket) SendCopy()                                                   {}
func (s *Socket) SendCursor()                                                 {}
func (s *Socket) SendDisconnect()                                             {}
func (s *Socket) SendDispose()                                                {}
func (s *Socket) SendEnd(stream *Stream)                                      {}
func (s *Socket) SendError()                                                  {}
func (s *Socket) SendFile()                                                   {}
func (s *Socket) SendFilesystem()                                             {}
func (s *Socket) SendMouse()                                                  {}
func (s *Socket) SendMove()                                                   {}
func (s *Socket) SendName()                                                   {}
func (s *Socket) SendPipe()                                                   {}
func (s *Socket) SendRect()                                                   {}
func (s *Socket) SendImg(mode CompositeMode, l *Layer, name string, x, y int) {}
func (s *Socket) SendShade()                                                  {}
func (s *Socket) SendSize()                                                   {}
func (s *Socket) SendSync()                                                   {}
func (s *Socket) SendTransfer()                                               {}
func (s *Socket) WritePNG(stream *Stream, img *image.RGBA)                    {}
