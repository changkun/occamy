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
