// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"image"
	"log"
	"os"

	"changkun.de/x/occamy/internal/guac"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

func main() {
	if len(os.Args) < 3 {
		log.Fatal("Usage: occamy-gui <vnc|rdp> host:port")
		return
	}

	a, err := NewApp(os.Args[1], os.Args[2])
	if err != nil {
		log.Fatalf("cannot create Occamy client: %v", err)
	}
	go a.Run()
	app.Main()
}

type App struct {
	client *guac.Client
	win    *app.Window
}

func NewApp(protocol, addr string) (a *App, err error) {
	log.SetPrefix("occamy: ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	a = &App{}
	a.win = app.NewWindow(app.Title("Occamy GUI Client"))
	a.client, err = guac.NewClient("0.0.0.0:5636", map[string]string{
		"host":     addr,
		"protocol": protocol,
		"username": "",
		"password": "vncpassword",
	}, a.win)
	if err != nil {
		return nil, err
	}
	w, h := 1280*2, 1024*2
	a.win.Option(
		app.Size(unit.Px(float32(w)), unit.Px(float32(h))),
		app.MaxSize(unit.Px(float32(w)), unit.Px(float32(h))),
		app.MinSize(unit.Px(float32(w)), unit.Px(float32(h))),
	)
	return a, nil
}

func (a *App) Run() {
	for e := range a.win.Events() {
		switch e := e.(type) {
		case system.DestroyEvent:
			log.Println(e.Err)
			os.Exit(0)
		case system.FrameEvent:
			ops := &op.Ops{}
			a.updateScreen(ops, e.Queue)
			e.Frame(ops)
		case pointer.Event:
			if err := a.client.SendMouse(
				image.Point{X: int(e.Position.X), Y: int(e.Position.Y)},
				guac.MouseToGioButton[e.Buttons]); err != nil {
				log.Println(err)
			}
			a.win.Invalidate()
		case key.Event:
			log.Printf("%+v, %+v", e.Name, e.Modifiers)

			// TODO: keyboard seems problematic, yet.
			// See https://todo.sr.ht/~eliasnaur/gio/319
			// var keycode guac.KeyCode
			// switch {
			// case e.Modifiers.Contain(key.ModCtrl):
			// 	keycode = guac.KeyCode(guac.KeyLeftControl)
			// case e.Modifiers.Contain(key.ModCommand):
			// 	keycode = guac.KeyCode(guac.KeyLeftControl)
			// case e.Modifiers.Contain(key.ModShift):
			// 	keycode = guac.KeyCode(guac.KeyLeftShift)
			// case e.Modifiers.Contain(key.ModAlt):
			// 	keycode = guac.KeyCode(guac.KeyLeftAlt)
			// case e.Modifiers.Contain(key.ModSuper):
			// 	keycode = guac.KeyCode(guac.KeySuper)
			// }
			// err := a.client.SendKey(keycode, e.State == key.Press)
			// if err != nil {
			// 	log.Println(err)
			// }
		}
	}
}

func (a *App) updateScreen(ops *op.Ops, q event.Queue) {
	img, _ := a.client.Screen()
	paint.NewImageOp(img).Add(ops)
	paint.PaintOp{}.Add(ops)
}
