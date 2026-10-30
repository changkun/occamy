// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"errors"
	"fmt"

	"changkun.de/x/occamy/protocol"
)

// This example sends a message of a made-up type, a note with a number
// and a text, and receives it.
func Example() {
	const typeNote protocol.Type = 0x7001

	// Sending: the header, then the fields in order.
	msg := protocol.AppendHeader(nil, protocol.Header{Type: typeNote})
	msg = protocol.AppendUint32(msg, 42)
	msg = protocol.AppendString(msg, "hello")

	// Receiving: split the message, check the type, read the fields.
	var known protocol.Registry
	known.Register(typeNote, "note")

	h, body, err := protocol.Split(msg, protocol.Limits{})
	if err != nil {
		fmt.Println(err)
		return
	}
	ok, err := known.Accept(h)
	if err != nil {
		fmt.Println("close the connection:", err)
		return
	}
	if !ok {
		return // an unknown, ignorable message
	}
	d := protocol.NewDecoder(body)
	n, text := d.ReadUint32(), d.ReadString()
	if err := d.Finish(); err != nil {
		fmt.Println(err)
		return
	}
	name, _ := known.Lookup(h.Type)
	fmt.Println(name, n, text)

	// A receiver that does not know the type closes the connection,
	// unless the sender marked the message ignorable.
	var empty protocol.Registry
	_, err = empty.Accept(h)
	fmt.Println(errors.Is(err, protocol.ErrUnsupported))
	ok, err = empty.Accept(protocol.Header{Type: typeNote, Flags: protocol.FlagIgnorable})
	fmt.Println(ok, err)

	// Output:
	// note 42 hello
	// true
	// false <nil>
}

// This example opens a session. The client's hello travels in the
// upgrade request's query and its token in Sec-WebSocket-Protocol; the
// server answers with a welcome whose capabilities both peers use from
// then on.
func Example_handshake() {
	// The client builds the upgrade request.
	hello := protocol.Hello{
		Width: 1280, Height: 720, Visible: true, Name: "example/1.0",
		Capabilities: protocol.Capabilities{
			Codecs:     []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw},
			CacheSlots: 256,
		},
	}
	query, err := protocol.EncodeHelloQuery(hello)
	if err != nil {
		fmt.Println(err)
		return
	}
	offered := []string{protocol.Subprotocol + ", " + protocol.TokenSubprotocol("a token")}

	// The server reads it, and answers with protocol.Subprotocol alone.
	token, err := protocol.TokenFromSubprotocols(offered)
	if err != nil {
		fmt.Println(err)
		return
	}
	got, err := protocol.DecodeHelloQuery(query)
	if err != nil {
		fmt.Println(err)
		return
	}
	server := protocol.Capabilities{
		Codecs:     []protocol.Codec{protocol.CodecRaw, protocol.CodecDeflate, protocol.CodecPNG},
		CacheSlots: 1024,
	}
	welcome := protocol.Welcome{
		ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical, SessionID: "s-1",
		Capabilities: protocol.Agree(got.Capabilities, server),
	}
	msg := welcome.Append(nil)

	// The client reads the welcome, its first message.
	m, err := protocol.Decode(msg, protocol.ServerMessages(), protocol.Limits{})
	if err != nil {
		fmt.Println(err)
		return
	}
	w := m.(*protocol.Welcome)
	limits := w.Capabilities.Limits()
	fmt.Println(token, got.Width, got.Height)
	codecs := w.Capabilities.Codecs
	fmt.Println(w.Role, w.Class, len(codecs), codecs[0], codecs[1], w.Capabilities.FrameWindow, limits.CacheSlots)

	// Output:
	// a token 1280 720
	// owner graphical 2 png raw 2 256
}

// This example receives a frame as a client: two tiles, the second drawn
// from the client's cache, and the frame-end, which the client
// acknowledges.
func ExampleDecode() {
	frame := [][]byte{
		protocol.Tile{FrameID: 1, W: 64, H: 64, Codec: protocol.CodecPNG, Slot: 1, Payload: []byte("PNG")}.Append(nil),
		protocol.Tile{FrameID: 1, X: 64, W: 64, H: 64, Codec: protocol.CodecCached, Slot: 1}.Append(nil),
		protocol.FrameEnd{FrameID: 1, ServerTime: 16667, LastSeq: 3}.Append(nil),
	}
	r := protocol.ServerMessages()
	l := protocol.Limits{CacheSlots: 16}
	for _, msg := range frame {
		m, err := protocol.Decode(msg, r, l)
		if err != nil {
			fmt.Println("close with", protocol.CloseCodeFor(err))
			return
		}
		switch m := m.(type) {
		case *protocol.Tile:
			fmt.Println("tile at", m.X, m.Y, m.Codec, "slot", m.Slot)
		case *protocol.FrameEnd:
			fmt.Println("frame", m.FrameID, "reflects input", m.LastSeq)
			ack := protocol.FrameAck{FrameID: m.FrameID, DecodeTime: 900}.Append(nil)
			_ = ack // sent to the server
		}
	}

	// A message cut short closes the connection.
	_, err := protocol.Decode(frame[2][:8], r, l)
	fmt.Println("close with", protocol.CloseCodeFor(err))

	// Output:
	// tile at 0 0 png slot 1
	// tile at 64 0 cached slot 1
	// frame 1 reflects input 3
	// close with protocol-error
}
