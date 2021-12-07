// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The following code is modified from
// https://github.com/deluan/bring
// Authored by Deluan Quintao released under MIT license.

package guac

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"changkun.de/x/occamy/internal/protocol"
	"github.com/gorilla/websocket"
)

type SessionState int

const (
	SessionClosed SessionState = iota
	SessionHandshake
	SessionActive
)

var ErrNotConnected = errors.New("not connected")

const pingFrequency = 5 * time.Second

// Session is used to create and keep a connection with a guacd server,
// and it is responsible for the initial handshake and to send and receive instructions.
// Instructions received are put in the In channel. Instructions are sent using the Send() function
type session struct {
	State SessionState
	Id    string

	tunnel *websocket.Conn
	ins    chan *protocol.Instruction
	done   chan bool
	config map[string]string
}

// newSession creates a new connection with the guacd server, using the configuration provided
func newSession(addr string, config map[string]string) (*session, error) {
	b, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Post("http://"+addr+"/api/v1/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	b, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	token := struct {
		Token string `json:"token"`
	}{}
	err = json.Unmarshal(b, &token)
	if err != nil {
		return nil, err
	}

	c, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/api/v1/connect?token="+token.Token, nil)
	if err != nil {
		return nil, err
	}

	s := &session{
		State:  SessionClosed,
		done:   make(chan bool),
		tunnel: c,
		ins:    make(chan *protocol.Instruction, 100),
		config: config,
	}
	go s.sender()
	log.Printf("Initiating %s session with %s", strings.ToUpper(config["protocol"]), addr)
	s.State = SessionActive
	log.Printf("Handshake successful. Got connection ID %s", s.Id)
	return s, nil
}

// Terminate the current session, disconnecting from the server
func (s *session) Terminate() {
	if s.State == SessionClosed {
		return
	}
	close(s.done)
}

// Send instructions to the server. Multiple instructions are sent in one single transaction
func (s *session) Send(ins ...*protocol.Instruction) (err error) {
	// Serialize the sending instructions.
	for _, i := range ins {
		s.ins <- i
	}
	return
}

func (s *session) sender() {
	for {
		select {
		case ins := <-s.ins:
			log.Printf("C> %s", ins)
			err := s.tunnel.WriteMessage(websocket.TextMessage, []byte(ins.String()))
			if err != nil {
				return
			}
		case <-s.done:
			_ = s.tunnel.WriteMessage(websocket.TextMessage, []byte(protocol.NewInstruction("disconnect").String()))
			s.State = SessionClosed
			s.tunnel.Close()
			return
		}
	}
}
