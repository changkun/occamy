// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The following code is modified from
// https://github.com/deluan/bring
// Authored by Deluan Quintao released under MIT license.

package guac

import (
	"fmt"
	"image"
	"image/draw"
	"log"
	"time"

	"changkun.de/x/occamy/internal/uuid"
)

var compositeOperations = map[byte]draw.Op{
	0xC: draw.Src,
	0xE: draw.Over,
}

type display struct {
	cursor         *layer
	cursorHotspotX int
	cursorHotspotY int
	cursorX        int
	cursorY        int
	tasks          []task
	layers         layers
	defaultLayer   *layer
	canvas         *image.RGBA
	lastUpdate     int64
}

func newDisplay() *display {
	d := &display{
		cursor: newBuffer(),
		layers: newLayers(),
		canvas: image.NewRGBA(image.Rectangle{}),
	}
	d.defaultLayer = d.layers.getDefault()
	return d
}

type taskFunc func() error

type task struct {
	taskFunc taskFunc
	name     string
	uuid     string
}

func (t *task) String() string {
	return fmt.Sprintf("%s [%s]", t.name, t.uuid)
}

func (d *display) scheduleTask(name string, t taskFunc) {
	task := task{
		taskFunc: t,
		name:     name,
		uuid:     uuid.NewID(""),
	}
	log.Printf("Adding new task: %s. Total: %d", task.String(), len(d.tasks)+1)
	d.tasks = append(d.tasks, task)
}

func (d *display) processSingleTask(t task) {
	log.Printf("Executing task %s", t.String())
	err := t.taskFunc()
	if err != nil {
		log.Printf("Skipping task %s due to error. This can lead to invalid screen state! Error: %s", t.String(), err)
		return
	}
	if !d.defaultLayer.modified {
		return
	}
	// TODO: Only update canvas after all tasks are applied?
	mr := d.defaultLayer.modifiedRect
	copyImage(d.canvas, mr.Min.X, mr.Min.Y, d.defaultLayer.image, mr, draw.Src)
	d.lastUpdate = time.Now().UnixNano()

	d.defaultLayer.resetModified()
}

func (d *display) flush() {
	if len(d.tasks) == 0 {
		return
	}
	log.Printf("Processing %d pending tasks", len(d.tasks))
	for _, t := range d.tasks {
		d.processSingleTask(t)
	}
	log.Println("All pending tasks were completed")
	d.tasks = nil
}

func (d *display) getCanvas() (image.Image, int64) {
	return d.canvas, d.lastUpdate
}

func (d *display) dispose(layerIdx int) {
	d.scheduleTask("dispose", func() error {
		d.layers.delete(layerIdx)
		return nil
	})
}

func (d *display) copy(srcL, srcX, srcY, srcWidth, srcHeight, dstL, dstX, dstY int, compositeOperation byte) {
	op := compositeOperations[compositeOperation]
	d.scheduleTask("copy", func() error {
		srcLayer := d.layers.get(srcL)
		dstLayer := d.layers.get(dstL)
		dstLayer.Copy(srcLayer, srcX, srcY, srcWidth, srcHeight, dstX, dstY, op)
		return nil
	})
}

func (d *display) draw(layerIdx, x, y int, compositeOperation byte, s *stream) {
	op := compositeOperations[compositeOperation]
	img, err := s.image()

	d.scheduleTask("draw", func() error {
		if err != nil {
			return err
		}
		layer := d.layers.get(layerIdx)
		layer.Draw(x, y, img, op)
		return nil
	})
}

func (d *display) fill(layerIdx int, r, g, b, a, compositeOperation byte) {
	op := compositeOperations[compositeOperation]
	d.scheduleTask("fill", func() error {
		layer := d.layers.get(layerIdx)
		layer.Fill(r, g, b, a, op)
		return nil
	})
}
func (d *display) rect(layerIdx int, x int, y int, width int, height int) {
	d.scheduleTask("rect", func() error {
		layer := d.layers.get(layerIdx)
		layer.Rect(x, y, width, height)
		return nil
	})
}

func (d *display) resize(layerIdx, w, h int) {
	d.scheduleTask("resize", func() error {
		layer := d.layers.get(layerIdx)
		layer.Resize(w, h)
		if layerIdx == 0 {
			d.canvas = image.NewRGBA(layer.image.Bounds())
			copyImage(d.canvas, 0, 0, layer.image, layer.image.Bounds(), draw.Src)
		}
		return nil
	})
}

func (d *display) hideCursor() {
	cr := image.Rect(d.cursorX, d.cursorY, d.cursorX+d.cursor.width, d.cursorY+d.cursor.height)
	copyImage(d.canvas, d.cursorX, d.cursorY, d.defaultLayer.image, cr, draw.Src)
}

func (d *display) moveCursor(x, y int) {
	d.hideCursor()

	d.cursorX = x
	d.cursorY = y

	copyImage(d.canvas, d.cursorX, d.cursorY, d.cursor.image, d.cursor.image.Bounds(), draw.Over)
	d.lastUpdate = time.Now().UnixNano()
}

func (d *display) setCursor(cursorHotspotX, cursorHotspotY, srcL, srcX, srcY, srcWidth, srcHeight int) {
	d.scheduleTask("setCursor", func() error {
		d.hideCursor()

		layer := d.layers.get(srcL)
		d.cursor.Resize(srcWidth, srcHeight)
		d.cursor.Copy(layer, srcX, srcY, srcWidth, srcHeight, 0, 0, draw.Src)
		d.cursorHotspotX = cursorHotspotX
		d.cursorHotspotY = cursorHotspotY

		// TODO: Calculate correct position based on cursorHotspot
		//d.cursorX = cursorHotspotX
		//d.cursorY = cursorHotspotY

		copyImage(d.canvas, d.cursorX, d.cursorY, d.cursor.image, d.cursor.image.Bounds(), draw.Over)
		return nil
	})
}
