// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import "fmt"

// A Registry records the message types a peer knows, by number and name.
// The zero value is an empty registry. Register all types before the
// first lookup; from then on a Registry is safe for concurrent use.
type Registry struct {
	names map[Type]string
}

// Register records t as the message type called name. It panics if t is
// zero, if name is empty, or if t is already registered, since a type
// number names one message for good.
func (r *Registry) Register(t Type, name string) {
	if t == 0 {
		panic("protocol: type 0 is reserved")
	}
	if name == "" {
		panic(fmt.Sprintf("protocol: type %#04x registered without a name", uint16(t)))
	}
	if old, ok := r.names[t]; ok {
		panic(fmt.Sprintf("protocol: type %#04x registered as both %q and %q", uint16(t), old, name))
	}
	if r.names == nil {
		r.names = make(map[Type]string)
	}
	r.names[t] = name
}

// Lookup returns the name t is registered under, and whether it is
// registered.
func (r *Registry) Lookup(t Type) (name string, ok bool) {
	name, ok = r.names[t]
	return name, ok
}

// Accept reports whether a receiver decodes a message with header h. It
// returns true if h.Type is registered. For an unregistered type, it
// returns false if h is ignorable, and the receiver skips the message;
// otherwise it returns an error wrapping ErrUnsupported, and the receiver
// closes the connection with the unsupported close code.
func (r *Registry) Accept(h Header) (bool, error) {
	if _, ok := r.names[h.Type]; ok {
		return true, nil
	}
	if h.Ignorable() {
		return false, nil
	}
	return false, fmt.Errorf("%w: type %#04x", ErrUnsupported, uint16(h.Type))
}
