// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/changkun/occamy/config"
	"github.com/changkun/occamy/lib"
	"github.com/changkun/occamy/protocol"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// Session is an occamy proxy session that shares connection
// within an user group
type Session struct {
	connectedUsers uint64
	id             string
	once           sync.Once
	client         *lib.Client // shared client in a session
}

// NewSession creates a new occamy proxy session
func NewSession(proto string) (*Session, error) {
	runtime.LockOSThread() // without unlock to exit the Go thread

	cli, err := lib.NewClient()
	if err != nil {
		return nil, fmt.Errorf("occamy-lib: new client error: %v", err)
	}

	sess := &Session{client: cli}
	if err = sess.initialize(proto); err != nil {
		sess.Close()
		return nil, fmt.Errorf("occamy-lib: session initialization failed with error: %v", err)
	}
	return sess, nil
}

// ID reports the session id
func (s *Session) ID() string {
	return s.id
}

// Join adds the given socket as a new user to the given process, automatically
// reading/writing from the socket via read/write threads. The given socket,
// parser, and any associated resources will be freed unless the user is not
// added successfully.
func (s *Session) Join(ws *websocket.Conn, conf *config.JWT, owner bool) error {

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("occamy-proxy: new socket pair error: %v", err)
	}
	lib.ResetErrors()

	// 1. user thread
	go func(fd int, owner bool) {
		defer s.Close()

		sock, err := lib.NewSocket(fd)
		if err != nil {
			logrus.Errorf("occamy-lib: create guac socket error: %v", err)
			return
		}
		defer sock.Close()
		user, err := lib.NewUser(sock, s.client, owner)
		if err != nil {
			logrus.Errorf("occamy-lib: create guac user error: %v", err)
			return
		}
		defer user.Close()

		atomic.AddUint64(&s.connectedUsers, 1)
		defer atomic.AddUint64(&s.connectedUsers, ^uint64(0))

		err = user.HandleConnection() // block until disconnect/completion
		if err != nil {
			logrus.Errorf("occamy-lib: handle user connection error: %v", err)
		}

	}(fds[0], owner)

	// 2. handshake
	conn := protocol.NewInstructionIO(fds[1])
	err = s.handshake(conn, ws, conf)
	if err != nil {
		conn.Close()
		return err
	}

	// 3. proxy io
	return s.serveIO(conn, ws)
}

func (s *Session) initialize(proto string) error {
	s.client.InitLogLevel(config.Runtime.MaxLogLevel)
	err := s.client.LoadProtocolPlugin(proto)
	if err != nil {
		return err
	}
	s.id = s.client.Identifier()
	return nil
}

// Close ...
func (s *Session) Close() {
	if atomic.LoadUint64(&s.connectedUsers) > 0 {
		return
	}
	s.client.Close()
}

func (s *Session) handshake(conn *protocol.InstructionIO, ws *websocket.Conn, conf *config.JWT) error {
	ins, err := conn.Read()
	if err != nil {
		return err
	}

	// FIXME: try to remove handshake via reimplement libguac
	conn.Write(protocol.NewInstruction([]string{"size", "1204", "768", "96"}))
	conn.Write(protocol.NewInstruction([]string{"audio", ""}))
	conn.Write(protocol.NewInstruction([]string{"video", ""}))
	conn.Write(protocol.NewInstruction([]string{"image", ""}))

	// prepare coresponding arg values
	host, port, err := net.SplitHostPort(conf.Host)
	if err != nil {
		return err
	}
	args := ins.Args()
	connectIns := []string{"connect"}
	connectIns = append(connectIns, make([]string, len(args))...)
	for i := 0; i < len(args); i++ {
		var value string
		switch args[i] {
		case "hostname":
			value = host
		case "port":
			value = port
		case "username":
			value = conf.Username
		case "password":
			value = conf.Password
		}
		connectIns[i+1] = value
	}
	conn.Write(protocol.NewInstruction(connectIns))

	ins, err = conn.Read()
	if !ins.Expect("ready") {
		return errors.New("not ready")
	}
	return nil
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
			logrus.Info("from libguac: ", string(raw))
			err = ws.WriteMessage(websocket.TextMessage, raw)
			if err != nil {
				break
			}
		}
		exit <- err
		wg.Done()
	}(conn, ws)
	go func(conn *protocol.InstructionIO, ws *websocket.Conn) {
		var err error
		for {
			_, buf, err := ws.ReadMessage()
			if err != nil {
				break
			}
			logrus.Info("from client: ", string(buf))
			_, err = conn.WriteRaw(buf)
			if err != nil {
				break
			}
		}
		exit <- err
		wg.Done()
	}(conn, ws)
	err = <-exit
	conn.Close()
	return
}
