// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The following code is modified from
// https://github.com/deluan/bring
// Authored by Deluan Quintao released under MIT license.

package guac

import (
	"log"
	"strconv"

	"changkun.de/x/occamy/internal/protocol"
)

// Handler func for  Guacamole instructions
type handlerFunc = func(client *Client, args []string) error

// Handlers for all instruction opcodes receivable by this Guacamole client.
var handlers = map[string]handlerFunc{
	"blob": func(c *Client, args []string) error {
		idx := parseInt(args[0])
		return c.streams.append(idx, args[1])
	},

	"copy": func(c *Client, args []string) error {
		srcL := parseInt(args[0])
		srcX := parseInt(args[1])
		srcY := parseInt(args[2])
		srcWidth := parseInt(args[3])
		srcHeight := parseInt(args[4])
		mask := parseInt(args[5])
		dstL := parseInt(args[6])
		dstX := parseInt(args[7])
		dstY := parseInt(args[8])
		c.display.copy(srcL, srcX, srcY, srcWidth, srcHeight,
			dstL, dstX, dstY, byte(mask))
		return nil
	},

	"cfill": func(c *Client, args []string) error {
		mask := parseInt(args[0])
		layerIdx := parseInt(args[1])
		r := parseInt(args[2])
		g := parseInt(args[3])
		b := parseInt(args[4])
		a := parseInt(args[5])
		c.display.fill(layerIdx, byte(r), byte(g), byte(b), byte(a), byte(mask))
		return nil
	},

	"cursor": func(c *Client, args []string) error {
		cursorHotspotX := parseInt(args[0])
		cursorHotspotY := parseInt(args[1])
		srcL := parseInt(args[2])
		srcX := parseInt(args[3])
		srcY := parseInt(args[4])
		srcWidth := parseInt(args[5])
		srcHeight := parseInt(args[6])
		c.display.setCursor(cursorHotspotX, cursorHotspotY,
			srcL, srcX, srcY, srcWidth, srcHeight)
		return nil
	},

	"disconnect": func(c *Client, args []string) error {
		c.session.Terminate()
		return nil
	},

	"dispose": func(c *Client, args []string) error {
		layerIdx := parseInt(args[0])
		c.display.dispose(layerIdx)
		return nil
	},

	"end": func(c *Client, args []string) error {
		idx := parseInt(args[0])
		c.streams.end(idx)
		c.streams.delete(idx)
		return nil
	},

	"error": func(c *Client, args []string) error {
		log.Printf("Received error from server: (%s) - %s", args[1], args[0])
		return nil
	},

	"img": func(c *Client, args []string) error {
		s := c.streams.get(parseInt(args[0]))
		op := byte(parseInt(args[1]))
		layerIdx := parseInt(args[2])
		//mimetype := args[3] // Not used
		x := parseInt(args[4])
		y := parseInt(args[5])
		s.onEnd = func(s *stream) {
			c.display.draw(layerIdx, x, y, op, s)
		}
		return nil
	},

	"log": func(c *Client, args []string) error {
		log.Printf("Log from server:  %s", args[0])
		return nil
	},

	"rect": func(c *Client, args []string) error {
		layerIdx := parseInt(args[0])
		x := parseInt(args[1])
		y := parseInt(args[2])
		w := parseInt(args[3])
		h := parseInt(args[4])
		c.display.rect(layerIdx, x, y, w, h)
		return nil
	},

	"size": func(c *Client, args []string) error {
		layerIdx := parseInt(args[0])
		w := parseInt(args[1])
		h := parseInt(args[2])
		c.display.resize(layerIdx, w, h)
		return nil
	},

	"sync": func(c *Client, args []string) error {
		c.display.flush()
		if err := c.session.Send(protocol.NewInstruction("sync", args...)); err != nil {
			log.Printf("Failed to send 'sync' back to server: %s", err)
			return err
		}
		if c.onSync != nil {
			img, ts := c.display.getCanvas()
			c.onSync(img, ts)
		}
		return nil
	},
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
