// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/lib"
	"changkun.de/x/occamy/internal/protocol"
	"github.com/gorilla/websocket"
)

// Session is an occamy proxy session that shares connection
// within an user group
type Session struct {
	ID             string
	connectedUsers uint64
	once           sync.Once
	client         *lib.Client // shared client in a session
}

// NewSession creates a new occamy proxy session
func NewSession(proto string) (*Session, error) {
	runtime.LockOSThread() // without unlock to exit the Go thread

	cli, err := lib.NewClient()
	if err != nil {
		return nil, fmt.Errorf("occamy-lib: new client error: %w", err)
	}

	s := &Session{client: cli}
	s.client.InitLogLevel(config.Runtime.Mode)
	err = s.client.LoadProtocolPlugin(proto)
	if err != nil {
		s.close()
		return nil, fmt.Errorf("occamy-lib: load protocol plugin failed: %w", err)
	}
	s.ID = s.client.ID
	return s, nil
}

// Join adds the given socket as a new user to the given process, automatically
// reading/writing from the socket via read/write threads. The given socket,
// parser, and any associated resources will be freed unless the user is not
// added successfully.
func (s *Session) Join(ws *websocket.Conn, jwt *config.JWT, owner bool, unlock func()) error {
	defer s.close()
	lib.ResetErrors()

	// 1. prepare socket pair
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		unlock()
		return fmt.Errorf("new socket pair error: %w", err)
	}

	// 2. create guac socket using fds[0]
	sock, err := lib.NewSocket(fds[0])
	if err != nil {
		return fmt.Errorf("occamy-lib: create guac socket error: %w", err)
	}
	defer sock.Close()

	// 3. create guac user using created guac socket
	u, err := lib.NewUser(sock, s.client, owner, jwt)
	if err != nil {
		return fmt.Errorf("occamy-lib: create guac user error: %w", err)
	}
	defer u.Close()

	// 4. count new user
	atomic.AddUint64(&s.connectedUsers, 1)
	defer atomic.AddUint64(&s.connectedUsers, ^uint64(0))

	// 5. preparing connection
	err = u.Prepare()
	if err != nil {
		unlock()
		return fmt.Errorf("occamy-lib: handle user connection error: %w", err)
	}
	unlock()

	// 6. handle connection
	done := make(chan struct{}, 1)
	go u.HandleConnection(done) // block until disconnect/completion

	// 7. proxy io
	conn := protocol.NewInstructionIO(fds[1])
	defer conn.Close()

	err = s.serveIO(conn, ws)
	<-done
	return err
}

// Close closes a session.
func (s *Session) close() {
	if atomic.LoadUint64(&s.connectedUsers) > 0 {
		return
	}
	s.client.Close()
}

func (s *Session) serveIO(conn *protocol.InstructionIO, ws *websocket.Conn) (err error) {
	wg := sync.WaitGroup{}
	exit := make(chan error, 2)
	wg.Add(2)
	go func(conn *protocol.InstructionIO, ws *websocket.Conn) {
		var err error
		for {
			raw, err := conn.ReadRaw()
			if err != nil {
				break
			}
			err = ws.WriteMessage(websocket.TextMessage, raw)
			if err != nil {
				break
			}
		}
		exit <- err
		log.Println("reading from desktop terminated.")
		wg.Done()
	}(conn, ws)
	go func(conn *protocol.InstructionIO, ws *websocket.Conn) {
		var err error
		for {
			_, buf, err := ws.ReadMessage()
			if err != nil {
				break
			}
			_, err = conn.WriteRaw(buf)
			if err != nil {
				break
			}
		}
		exit <- err
		log.Println("reading from client terminated.")
		wg.Done()
	}(conn, ws)
	err = <-exit
	conn.Close()
	wg.Wait()
	log.Println("IO goroutines are terminated.")
	return
}
