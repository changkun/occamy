// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

const (
	// FrameDuration is the maximum duration of a frame in milliseconds.
	FrameDuration = 40
	// FrameTimeout is the amount of time to allow per message read
	// within a frame, in milliseconds. If the server is silent for at
	// least this amount of time, the frame will be considered finished.
	FrameTimeout = 0
	// FrameStartTimeout is the amount of time to wait for a new message
	// from the VNC server when beginning a new frame. This value must
	// be kept reasonably small such that a slow VNC server will not
	// prevent external events from being handled (such as the stop
	// signal from guac_client_stop()), but large enough that the
	// message handling loop does not eat up CPU spinning.
	FrameStartTimeout = 1000000
	// ConnectInterval is the number of milliseconds to wait between
	// connection attempts.
	ConnectInterval = 1000
	// ClientKey which can be used with the rfbClientGetClientData
	// function to return the associated guac_client.
	ClientKey = "GUAC_VNC"
)

// client plugin arguments
var clientArgs = []string{
	"hostname",
	"port",
	"read-only",
	"encodings",
	"password",
	"swap-red-blue",
	"color-depth",
	"cursor",
	"autoretry",
	"dest-host",
	"dest-port",
	"reverse-connect",
	"listen-timeout",
}

// Settings ...
type Settings struct {
	hostname    string
	port        int
	password    string
	encoding    string
	swapRedBlue bool
	colorDepth  int
	readOnly    bool
	retries     int
}

// Parse ...
func (s *Settings) Parse() {
}

// Client ...
type Client struct {
}

// NewClient ...
func NewClient() *Client {
	return &Client{}
}

// Join ...
func (c *Client) Join() {

}

// Leave ...
func (c *Client) Leave() {

}

// Free ...
func (c *Client) Free() {

}
