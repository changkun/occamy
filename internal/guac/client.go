// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The following code is modified from
// https://github.com/deluan/bring
// Authored by Deluan Quintao released under MIT license.

package guac

import (
	"errors"
	"image"
	"log"
	"strconv"
	"time"

	"changkun.de/x/occamy/internal/protocol"
	"gioui.org/app"
)

var ErrInvalidKeyCode = errors.New("invalid key code")

type OnSyncFunc = func(image image.Image, lastUpdate int64)

// Guacamole protocol client. Automatically handles incoming and outgoing Guacamole instructions,
// updating its display using one or more graphic primitives.
type Client struct {
	session *session
	display *display
	streams streams
	onSync  OnSyncFunc
}

// NewClient creates a Client and connects it to the guacd server with the provided configuration. Logger is optional
func NewClient(addr string, config map[string]string, win *app.Window) (*Client, error) {
	s, err := newSession(addr, config)
	if err != nil {
		return nil, err
	}

	c := &Client{
		session: s,
		display: newDisplay(),
		streams: newStreams(),
	}
	go func() {
		ping := time.NewTicker(pingFrequency)
		defer ping.Stop()
		for {
			select {
			case <-ping.C:
				err := s.Send(protocol.NewInstruction("nop"))
				if err != nil {
					log.Printf("Failed ping the server: %s", err)
				}
			case <-s.done:
				return
			}
		}
	}()

	inschan := make(chan *protocol.Instruction, 100)
	go func() {
		for {
			_, raw, err := s.tunnel.ReadMessage()
			if err != nil {
				log.Printf("Disconnecting from server. Reason: %v", err)
				s.Terminate()
				break
			}
			ins, err := protocol.ParseInstruction(raw)
			if err != nil {
				log.Printf("Failed to parse instruction: %v", err)
				s.Terminate()
				break
			}
			if ins.Opcode != "blob" {
				log.Printf("S> %s", ins)
			}
			if ins.Opcode == "nop" {
				continue
			}

			inschan <- ins
		}
	}()
	go func() {
		log.Println("client instruction handler started!")
		for ins := range inschan {
			h, ok := handlers[ins.Opcode]
			if !ok {
				log.Printf("Instruction not implemented: %s", ins.Opcode)
				continue
			}
			err = h(c, ins.Args)
			if err != nil {
				s.Terminate()
			}
		}
	}()
	return c, nil
}

func (c *Client) OnSync(f OnSyncFunc) {
	c.onSync = f
}

// Returns a snapshot of the current screen, together with the last updated timestamp
func (c *Client) Screen() (image image.Image, lastUpdate int64) {
	return c.display.getCanvas()
}

// Returns the current session state
func (c *Client) State() SessionState {
	return c.session.State
}

// Send mouse events to the server. An event is composed by position of the
// cursor, and a list of any currently pressed MouseButtons
func (c *Client) SendMouse(p image.Point, pressedButtons ...MouseButton) error {
	if c.session.State != SessionActive {
		return ErrNotConnected
	}

	buttonMask := 0
	for _, b := range pressedButtons {
		buttonMask |= int(b)
	}
	c.display.moveCursor(p.X, p.Y)
	err := c.session.Send(protocol.NewInstruction("mouse", strconv.Itoa(p.X), strconv.Itoa(p.Y), strconv.Itoa(buttonMask)))
	if err != nil {
		return err
	}
	return nil
}

// Send the sequence of characters as they were typed. Only works with simple chars
// (no combination with control keys)
func (c *Client) SendText(sequence string) error {
	if c.session.State != SessionActive {
		return ErrNotConnected
	}

	for _, ch := range sequence {
		keycode := strconv.Itoa(int(ch))
		err := c.session.Send(protocol.NewInstruction("key", keycode, "1"))
		if err != nil {
			return nil
		}
		err = c.session.Send(protocol.NewInstruction("key", keycode, "0"))
		if err != nil {
			return nil
		}
	}
	return nil
}

// Send key presses and releases.
func (c *Client) SendKey(key KeyCode, pressed bool) error {
	if c.session.State != SessionActive {
		return ErrNotConnected
	}

	p := "0"
	if pressed {
		p = "1"
	}
	keySym, ok := keySyms[key]
	if !ok {
		return ErrInvalidKeyCode
	}
	for _, k := range keySym {
		keycode := strconv.Itoa(k)
		err := c.session.Send(protocol.NewInstruction("key", keycode, p))
		if err != nil {
			return nil
		}
	}
	return nil
}
