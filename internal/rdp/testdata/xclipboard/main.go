// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command xclipboard reads or sets the CLIPBOARD selection on each X
// display from :10 to :19 that it can open. The RDP integration test runs
// it inside the xrdp container to see that clipboard text crosses
// xrdp's clipboard channel, whose X side xrdp-chansrv serves.
//
//	xclipboard get
//
// prints, for each display whose CLIPBOARD has an owner, a line of the
// display and its UTF-8 text, quoted as Go quotes strings.
//
//	xclipboard set TEXT
//
// owns CLIPBOARD on each display and serves TEXT to whoever asks, until
// another client takes the selection or a minute passes.
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func main() {
	switch {
	case len(os.Args) == 2 && os.Args[1] == "get":
		for _, d := range displays() {
			if text, err := get(d.x); err == nil {
				fmt.Printf("%s %s\n", d.name, strconv.Quote(text))
			} else {
				fmt.Fprintf(os.Stderr, "%s: %v\n", d.name, err)
			}
		}
	case len(os.Args) == 3 && os.Args[1] == "set":
		var wg sync.WaitGroup
		for _, d := range displays() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := set(d.x, os.Args[2]); err != nil {
					fmt.Fprintf(os.Stderr, "%s: %v\n", d.name, err)
				}
			}()
		}
		wg.Wait()
	default:
		fmt.Fprintln(os.Stderr, "usage: xclipboard get | xclipboard set TEXT")
		os.Exit(2)
	}
}

type display struct {
	name string
	x    *xgb.Conn
}

func displays() []display {
	var ds []display
	for d := 10; d < 20; d++ {
		name := fmt.Sprintf(":%d", d)
		if x, err := xgb.NewConnDisplay(name); err == nil {
			ds = append(ds, display{name, x})
		}
	}
	return ds
}

func atom(x *xgb.Conn, name string) (xproto.Atom, error) {
	r, err := xproto.InternAtom(x, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return r.Atom, nil
}

func window(x *xgb.Conn) (xproto.Window, error) {
	s := xproto.Setup(x).DefaultScreen(x)
	w, err := xproto.NewWindowId(x)
	if err != nil {
		return 0, err
	}
	err = xproto.CreateWindowChecked(x, s.RootDepth, w, s.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, s.RootVisual, 0, nil).Check()
	return w, err
}

// get converts CLIPBOARD to UTF8_STRING and returns it.
func get(x *xgb.Conn) (string, error) {
	clipboard, err := atom(x, "CLIPBOARD")
	if err != nil {
		return "", err
	}
	utf8String, err := atom(x, "UTF8_STRING")
	if err != nil {
		return "", err
	}
	owner, err := xproto.GetSelectionOwner(x, clipboard).Reply()
	if err != nil {
		return "", err
	}
	if owner.Owner == xproto.WindowNone {
		return "", fmt.Errorf("no owner")
	}
	w, err := window(x)
	if err != nil {
		return "", err
	}
	prop, err := atom(x, "XCLIPBOARD")
	if err != nil {
		return "", err
	}
	xproto.ConvertSelection(x, w, clipboard, utf8String, prop, xproto.TimeCurrentTime)
	events := make(chan xgb.Event)
	go func() {
		for {
			ev, err := x.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			if ev != nil {
				events <- ev
			}
		}
	}()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			return "", fmt.Errorf("no answer from the owner")
		case ev := <-events:
			n, ok := ev.(xproto.SelectionNotifyEvent)
			if !ok {
				continue
			}
			if n.Property == xproto.AtomNone {
				return "", fmt.Errorf("the owner refused UTF8_STRING")
			}
			r, err := xproto.GetProperty(x, true, w, prop, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
			if err != nil {
				return "", err
			}
			return string(r.Value), nil
		}
	}
}

// set owns CLIPBOARD and answers requests for its targets, UTF-8 text
// and Latin-1 text with text, until it loses the selection or a minute
// passes.
func set(x *xgb.Conn, text string) error {
	names := []string{"CLIPBOARD", "UTF8_STRING", "TARGETS", "STRING", "TEXT", "ATOM"}
	atoms := map[string]xproto.Atom{}
	for _, n := range names {
		a, err := atom(x, n)
		if err != nil {
			return err
		}
		atoms[n] = a
	}
	w, err := window(x)
	if err != nil {
		return err
	}
	if err := xproto.SetSelectionOwnerChecked(x, w, atoms["CLIPBOARD"], xproto.TimeCurrentTime).Check(); err != nil {
		return err
	}
	time.AfterFunc(time.Minute, func() { x.Close() })
	for {
		ev, err := x.WaitForEvent()
		if ev == nil && err == nil {
			return nil
		}
		switch ev := ev.(type) {
		case xproto.SelectionClearEvent:
			return nil
		case xproto.SelectionRequestEvent:
			reply := xproto.SelectionNotifyEvent{
				Time: ev.Time, Requestor: ev.Requestor, Selection: ev.Selection,
				Target: ev.Target, Property: ev.Property,
			}
			if reply.Property == xproto.AtomNone {
				reply.Property = ev.Target
			}
			switch ev.Target {
			case atoms["TARGETS"]:
				var b []byte
				for _, n := range []string{"TARGETS", "UTF8_STRING", "STRING", "TEXT"} {
					a := uint32(atoms[n])
					b = append(b, byte(a), byte(a>>8), byte(a>>16), byte(a>>24))
				}
				xproto.ChangeProperty(x, xproto.PropModeReplace, ev.Requestor, reply.Property, atoms["ATOM"], 32, uint32(len(b)/4), b)
			case atoms["UTF8_STRING"], atoms["TEXT"]:
				xproto.ChangeProperty(x, xproto.PropModeReplace, ev.Requestor, reply.Property, atoms["UTF8_STRING"], 8, uint32(len(text)), []byte(text))
			case atoms["STRING"]:
				var b []byte
				for _, r := range text {
					if r > 0xff {
						r = '?'
					}
					b = append(b, byte(r))
				}
				xproto.ChangeProperty(x, xproto.PropModeReplace, ev.Requestor, reply.Property, atoms["STRING"], 8, uint32(len(b)), b)
			default:
				reply.Property = xproto.AtomNone
			}
			xproto.SendEvent(x, false, ev.Requestor, 0, string(reply.Bytes()))
		}
	}
}
