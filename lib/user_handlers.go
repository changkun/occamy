// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

import "github.com/changkun/occamy/protocol"

const (
	// UserMaxObjects is the index of a closed stream.
	UserMaxObjects = 64
	// UserUndefinedObjectIndex is the index of an object which has not
	// been defined.
	UserUndefinedObjectIndex = -1
	// UserObjectRootName is the stream name reserved for the root of a
	// Occamy protocol object.
	UserObjectRootName = "/"
)

// Occamy instruction handler map
var instructionHandlers = map[string]func(u *User, ins *protocol.Instruction) error{
	"sync":       handleSync,
	"mouse":      handleMouse,
	"key":        handleKey,
	"disconnect": handleDisconnect,
	"size":       handleSize,
	"file":       handleFile,
	"pipe":       handlePipe,
	"ack":        handleAck,
	"blob":       handleBlob,
	"end":        handleEnd,
	"get":        handleGet,
	"put":        handlePut,
}

func handleSync(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleMouse(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleKey(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleDisconnect(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleSize(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleFile(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handlePipe(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleAck(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleBlob(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleEnd(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handleGet(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}

func handlePut(u *User, ins *protocol.Instruction) error {
	// TODO:
	return nil
}
