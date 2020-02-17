// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package plugins

import (
	"errors"

	"github.com/changkun/occamy/plugins/vnc"
)

// SupportedProtocols ...
type SupportedProtocols string

// all supported protocols
const (
	ProtocolVNC SupportedProtocols = "vnc"
)

// Client is a interface that defines all needed function of a client
type Client interface {
	Join()
	Leave()
	Free()
}

// NewPlugin allocates a new client plugin of remote desktop server
func NewPlugin(proto SupportedProtocols) (Client, error) {
	switch proto {
	case ProtocolVNC:
		return vnc.NewClient(), nil
	}
	return nil, errors.New("unsupported protocol")
}
