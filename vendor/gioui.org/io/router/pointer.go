// SPDX-License-Identifier: Unlicense OR MIT

package router

import (
	"image"

	"gioui.org/f32"
	"gioui.org/internal/ops"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
)

type pointerQueue struct {
	hitTree  []hitNode
	areas    []areaNode
	cursors  []cursorNode
	cursor   pointer.CursorName
	handlers map[event.Tag]*pointerHandler
	pointers []pointerInfo

	scratch []event.Tag

	semantic struct {
		idsAssigned bool
		lastID      SemanticID
		// contentIDs maps semantic content to a list of semantic IDs
		// previously assigned. It is used to maintain stable IDs across
		// frames.
		contentIDs map[semanticContent][]semanticID
	}
}

type hitNode struct {
	next int
	area int

	// For handler nodes.
	tag  event.Tag
	pass bool
}

type cursorNode struct {
	name pointer.CursorName
	area int
}

type pointerInfo struct {
	id       pointer.ID
	pressed  bool
	handlers []event.Tag
	// last tracks the last pointer event received,
	// used while processing frame events.
	last pointer.Event

	// entered tracks the tags that contain the pointer.
	entered []event.Tag
}

type pointerHandler struct {
	area      int
	active    bool
	wantsGrab bool
	types     pointer.Type
	// min and max horizontal/vertical scroll
	scrollRange image.Rectangle
}

type areaOp struct {
	kind areaKind
	rect f32.Rectangle
}

type areaNode struct {
	trans f32.Affine2D
	area  areaOp

	// Tree indices, with -1 being the sentinel.
	parent     int
	firstChild int
	lastChild  int
	sibling    int

	semantic struct {
		valid   bool
		id      SemanticID
		content semanticContent
	}
}

type areaKind uint8

// collectState represents the state for pointerCollector.
type collectState struct {
	t f32.Affine2D
	// nodePlusOne is the current node index, plus one to
	// make the zero value collectState the initial state.
	nodePlusOne int
	pass        int
}

// pointerCollector tracks the state needed to update an pointerQueue
// from pointer ops.
type pointerCollector struct {
	q         *pointerQueue
	state     collectState
	nodeStack []int
}

type semanticContent struct {
	tag      event.Tag
	label    string
	desc     string
	class    semantic.ClassOp
	gestures SemanticGestures
	selected bool
	disabled bool
}

type semanticID struct {
	id   SemanticID
	used bool
}

const (
	areaRect areaKind = iota
	areaEllipse
)

func (c *pointerCollector) resetState() {
	c.state = collectState{}
}

func (c *pointerCollector) setTrans(t f32.Affine2D) {
	c.state.t = t
}

func (c *pointerCollector) clip(op ops.ClipOp) {
	kind := areaRect
	if op.Shape == ops.Ellipse {
		kind = areaEllipse
	}
	c.pushArea(kind, frect(op.Bounds))
}

func (c *pointerCollector) pushArea(kind areaKind, bounds f32.Rectangle) {
	parentID := c.currentArea()
	areaID := len(c.q.areas)
	areaOp := areaOp{kind: kind, rect: bounds}
	if parentID != -1 {
		parent := &c.q.areas[parentID]
		if parent.firstChild == -1 {
			parent.firstChild = areaID
		}
		if siblingID := parent.lastChild; siblingID != -1 {
			c.q.areas[siblingID].sibling = areaID
		}
		parent.lastChild = areaID
	}
	an := areaNode{
		trans:      c.state.t,
		area:       areaOp,
		parent:     parentID,
		sibling:    -1,
		firstChild: -1,
		lastChild:  -1,
	}

	c.q.areas = append(c.q.areas, an)
	c.nodeStack = append(c.nodeStack, c.state.nodePlusOne-1)
	c.addHitNode(hitNode{
		area: areaID,
		pass: true,
	})
}

// frect converts a rectangle to a f32.Rectangle.
func frect(r image.Rectangle) f32.Rectangle {
	return f32.Rectangle{
		Min: fpt(r.Min), Max: fpt(r.Max),
	}
}

// fpt converts an point to a f32.Point.
func fpt(p image.Point) f32.Point {
	return f32.Point{
		X: float32(p.X), Y: float32(p.Y),
	}
}

func (c *pointerCollector) popArea() {
	n := len(c.nodeStack)
	c.state.nodePlusOne = c.nodeStack[n-1] + 1
	c.nodeStack = c.nodeStack[:n-1]
}

func (c *pointerCollector) pass() {
	c.state.pass++
}

func (c *pointerCollector) popPass() {
	c.state.pass--
}

func (c *pointerCollector) currentArea() int {
	if i := c.state.nodePlusOne - 1; i != -1 {
		n := c.q.hitTree[i]
		return n.area
	}
	return -1
}

func (c *pointerCollector) addHitNode(n hitNode) {
	n.next = c.state.nodePlusOne - 1
	c.q.hitTree = append(c.q.hitTree, n)
	c.state.nodePlusOne = len(c.q.hitTree) - 1 + 1
}

func (c *pointerCollector) inputOp(op pointer.InputOp, events *handlerEvents) {
	areaID := c.currentArea()
	area := &c.q.areas[areaID]
	area.semantic.content.tag = op.Tag
	if op.Types&(pointer.Press|pointer.Release) != 0 {
		area.semantic.content.gestures |= ClickGesture
	}
	area.semantic.valid = area.semantic.content.gestures != 0
	c.addHitNode(hitNode{
		area: areaID,
		tag:  op.Tag,
		pass: c.state.pass > 0,
	})
	h, ok := c.q.handlers[op.Tag]
	if !ok {
		h = new(pointerHandler)
		c.q.handlers[op.Tag] = h
		// Cancel handlers on (each) first appearance, but don't
		// trigger redraw.
		events.AddNoRedraw(op.Tag, pointer.Event{Type: pointer.Cancel})
	}
	h.active = true
	h.area = areaID
	h.wantsGrab = h.wantsGrab || op.Grab
	h.types = h.types | op.Types
	h.scrollRange = op.ScrollBounds
}

func (c *pointerCollector) semanticLabel(lbl string) {
	areaID := c.currentArea()
	area := &c.q.areas[areaID]
	area.semantic.valid = true
	area.semantic.content.label = lbl
}

func (c *pointerCollector) semanticDesc(desc string) {
	areaID := c.currentArea()
	area := &c.q.areas[areaID]
	area.semantic.valid = true
	area.semantic.content.desc = desc
}

func (c *pointerCollector) semanticClass(class semantic.ClassOp) {
	areaID := c.currentArea()
	area := &c.q.areas[areaID]
	area.semantic.valid = true
	area.semantic.content.class = class
}

func (c *pointerCollector) semanticSelected(selected bool) {
	areaID := c.currentArea()
	area := &c.q.areas[areaID]
	area.semantic.valid = true
	area.semantic.content.selected = selected
}

func (c *pointerCollector) semanticDisabled(disabled bool) {
	areaID := c.currentArea()
	area := &c.q.areas[areaID]
	area.semantic.valid = true
	area.semantic.content.disabled = disabled
}

func (c *pointerCollector) cursor(name pointer.CursorName) {
	c.q.cursors = append(c.q.cursors, cursorNode{
		name: name,
		area: len(c.q.areas) - 1,
	})
}

func (c *pointerCollector) reset(q *pointerQueue) {
	q.reset()
	c.resetState()
	c.nodeStack = c.nodeStack[:0]
	c.q = q
	// Add implicit root area for semantic descriptions to hang onto.
	c.pushArea(areaRect, f32.Rect(-1e6, -1e6, 1e6, 1e6))
	// Make it semantic to ensure a single semantic root.
	c.q.areas[0].semantic.valid = true
}

func (q *pointerQueue) assignSemIDs() {
	if q.semantic.idsAssigned {
		return
	}
	q.semantic.idsAssigned = true
	for i, a := range q.areas {
		if a.semantic.valid {
			q.areas[i].semantic.id = q.semanticIDFor(a.semantic.content)
		}
	}
}

func (q *pointerQueue) AppendSemantics(nodes []SemanticNode) []SemanticNode {
	q.assignSemIDs()
	nodes = q.appendSemanticChildren(nodes, 0)
	nodes = q.appendSemanticArea(nodes, 0, 0)
	return nodes
}

func (q *pointerQueue) appendSemanticArea(nodes []SemanticNode, parentID SemanticID, nodeIdx int) []SemanticNode {
	areaIdx := nodes[nodeIdx].areaIdx
	a := q.areas[areaIdx]
	childStart := len(nodes)
	nodes = q.appendSemanticChildren(nodes, a.firstChild)
	childEnd := len(nodes)
	for i := childStart; i < childEnd; i++ {
		nodes = q.appendSemanticArea(nodes, a.semantic.id, i)
	}
	n := &nodes[nodeIdx]
	n.ParentID = parentID
	n.Children = nodes[childStart:childEnd]
	return nodes
}

func (q *pointerQueue) appendSemanticChildren(nodes []SemanticNode, areaIdx int) []SemanticNode {
	if areaIdx == -1 {
		return nodes
	}
	a := q.areas[areaIdx]
	if semID := a.semantic.id; semID != 0 {
		cnt := a.semantic.content
		nodes = append(nodes, SemanticNode{
			ID: semID,
			Desc: SemanticDesc{
				Bounds: f32.Rectangle{
					Min: a.trans.Transform(a.area.rect.Min),
					Max: a.trans.Transform(a.area.rect.Max),
				},
				Label:       cnt.label,
				Description: cnt.desc,
				Class:       cnt.class,
				Gestures:    cnt.gestures,
				Selected:    cnt.selected,
				Disabled:    cnt.disabled,
			},
			areaIdx: areaIdx,
		})
	} else {
		nodes = q.appendSemanticChildren(nodes, a.firstChild)
	}
	return q.appendSemanticChildren(nodes, a.sibling)
}

func (q *pointerQueue) semanticIDFor(content semanticContent) SemanticID {
	ids := q.semantic.contentIDs[content]
	for i, id := range ids {
		if !id.used {
			ids[i].used = true
			return id.id
		}
	}
	// No prior assigned ID; allocate a new one.
	q.semantic.lastID++
	id := semanticID{id: q.semantic.lastID, used: true}
	if q.semantic.contentIDs == nil {
		q.semantic.contentIDs = make(map[semanticContent][]semanticID)
	}
	q.semantic.contentIDs[content] = append(q.semantic.contentIDs[content], id)
	return id.id
}

func (q *pointerQueue) SemanticAt(pos f32.Point) (SemanticID, bool) {
	q.assignSemIDs()
	for i := len(q.hitTree) - 1; i >= 0; i-- {
		n := &q.hitTree[i]
		hit := q.hit(n.area, pos)
		if !hit {
			continue
		}
		area := q.areas[n.area]
		if area.semantic.id != 0 {
			return area.semantic.id, true
		}
	}
	return 0, false
}

func (q *pointerQueue) opHit(handlers *[]event.Tag, pos f32.Point) {
	// Track whether we're passing through hits.
	pass := true
	idx := len(q.hitTree) - 1
	for idx >= 0 {
		n := &q.hitTree[idx]
		hit := q.hit(n.area, pos)
		if !hit {
			idx--
			continue
		}
		pass = pass && n.pass
		if pass {
			idx--
		} else {
			idx = n.next
		}
		if n.tag != nil {
			if _, exists := q.handlers[n.tag]; exists {
				*handlers = addHandler(*handlers, n.tag)
			}
		}
	}
}

func (q *pointerQueue) invTransform(areaIdx int, p f32.Point) f32.Point {
	if areaIdx == -1 {
		return p
	}
	return q.areas[areaIdx].trans.Invert().Transform(p)
}

func (q *pointerQueue) hit(areaIdx int, p f32.Point) bool {
	for areaIdx != -1 {
		a := &q.areas[areaIdx]
		p := a.trans.Invert().Transform(p)
		if !a.area.Hit(p) {
			return false
		}
		areaIdx = a.parent
	}
	return true
}

func (q *pointerQueue) reset() {
	if q.handlers == nil {
		q.handlers = make(map[event.Tag]*pointerHandler)
	}
	for _, h := range q.handlers {
		// Reset handler.
		h.active = false
		h.wantsGrab = false
		h.types = 0
	}
	q.hitTree = q.hitTree[:0]
	q.areas = q.areas[:0]
	q.cursors = q.cursors[:0]
	q.semantic.idsAssigned = false
	for k, ids := range q.semantic.contentIDs {
		for i := len(ids) - 1; i >= 0; i-- {
			if !ids[i].used {
				ids = append(ids[:i], ids[i+1:]...)
			} else {
				ids[i].used = false
			}
		}
		if len(ids) > 0 {
			q.semantic.contentIDs[k] = ids
		} else {
			delete(q.semantic.contentIDs, k)
		}
	}
}

func (q *pointerQueue) Frame(events *handlerEvents) {
	for k, h := range q.handlers {
		if !h.active {
			q.dropHandler(nil, k)
			delete(q.handlers, k)
		}
		if h.wantsGrab {
			for _, p := range q.pointers {
				if !p.pressed {
					continue
				}
				for i, k2 := range p.handlers {
					if k2 == k {
						// Drop other handlers that lost their grab.
						dropped := q.scratch[:0]
						dropped = append(dropped, p.handlers[:i]...)
						dropped = append(dropped, p.handlers[i+1:]...)
						for _, tag := range dropped {
							q.dropHandler(events, tag)
						}
						break
					}
				}
			}
		}
	}
	for i := range q.pointers {
		p := &q.pointers[i]
		q.deliverEnterLeaveEvents(p, events, p.last)
	}
}

func (q *pointerQueue) dropHandler(events *handlerEvents, tag event.Tag) {
	if events != nil {
		events.Add(tag, pointer.Event{Type: pointer.Cancel})
	}
	for i := range q.pointers {
		p := &q.pointers[i]
		for i := len(p.handlers) - 1; i >= 0; i-- {
			if p.handlers[i] == tag {
				p.handlers = append(p.handlers[:i], p.handlers[i+1:]...)
			}
		}
		for i := len(p.entered) - 1; i >= 0; i-- {
			if p.entered[i] == tag {
				p.entered = append(p.entered[:i], p.entered[i+1:]...)
			}
		}
	}
}

// pointerOf returns the pointerInfo index corresponding to the pointer in e.
func (q *pointerQueue) pointerOf(e pointer.Event) int {
	for i, p := range q.pointers {
		if p.id == e.PointerID {
			return i
		}
	}
	q.pointers = append(q.pointers, pointerInfo{id: e.PointerID})
	return len(q.pointers) - 1
}

func (q *pointerQueue) Push(e pointer.Event, events *handlerEvents) {
	if e.Type == pointer.Cancel {
		q.pointers = q.pointers[:0]
		for k := range q.handlers {
			q.dropHandler(events, k)
		}
		return
	}
	pidx := q.pointerOf(e)
	p := &q.pointers[pidx]
	p.last = e

	switch e.Type {
	case pointer.Press:
		q.deliverEnterLeaveEvents(p, events, e)
		p.pressed = true
		q.deliverEvent(p, events, e)
	case pointer.Move:
		if p.pressed {
			e.Type = pointer.Drag
		}
		q.deliverEnterLeaveEvents(p, events, e)
		q.deliverEvent(p, events, e)
	case pointer.Release:
		q.deliverEvent(p, events, e)
		p.pressed = false
		q.deliverEnterLeaveEvents(p, events, e)
	case pointer.Scroll:
		q.deliverEnterLeaveEvents(p, events, e)
		q.deliverScrollEvent(p, events, e)
	default:
		panic("unsupported pointer event type")
	}

	if !p.pressed && len(p.entered) == 0 {
		// No longer need to track pointer.
		q.pointers = append(q.pointers[:pidx], q.pointers[pidx+1:]...)
	}
}

func (q *pointerQueue) deliverEvent(p *pointerInfo, events *handlerEvents, e pointer.Event) {
	foremost := true
	if p.pressed && len(p.handlers) == 1 {
		e.Priority = pointer.Grabbed
		foremost = false
	}
	for _, k := range p.handlers {
		h := q.handlers[k]
		if e.Type&h.types == 0 {
			continue
		}
		e := e
		if foremost {
			foremost = false
			e.Priority = pointer.Foremost
		}
		e.Position = q.invTransform(h.area, e.Position)
		events.Add(k, e)
	}
}

func (q *pointerQueue) deliverScrollEvent(p *pointerInfo, events *handlerEvents, e pointer.Event) {
	foremost := true
	if p.pressed && len(p.handlers) == 1 {
		e.Priority = pointer.Grabbed
		foremost = false
	}
	var sx, sy = e.Scroll.X, e.Scroll.Y
	for _, k := range p.handlers {
		if sx == 0 && sy == 0 {
			return
		}
		h := q.handlers[k]
		// Distribute the scroll to the handler based on its ScrollRange.
		sx, e.Scroll.X = setScrollEvent(sx, h.scrollRange.Min.X, h.scrollRange.Max.X)
		sy, e.Scroll.Y = setScrollEvent(sy, h.scrollRange.Min.Y, h.scrollRange.Max.Y)
		e := e
		if foremost {
			foremost = false
			e.Priority = pointer.Foremost
		}
		e.Position = q.invTransform(h.area, e.Position)
		events.Add(k, e)
	}
}

func (q *pointerQueue) deliverEnterLeaveEvents(p *pointerInfo, events *handlerEvents, e pointer.Event) {
	q.scratch = q.scratch[:0]
	q.opHit(&q.scratch, e.Position)
	if p.pressed {
		// Filter out non-participating handlers.
		for i := len(q.scratch) - 1; i >= 0; i-- {
			if _, found := searchTag(p.handlers, q.scratch[i]); !found {
				q.scratch = append(q.scratch[:i], q.scratch[i+1:]...)
			}
		}
	} else {
		p.handlers = append(p.handlers[:0], q.scratch...)
	}
	hits := q.scratch
	if e.Source != pointer.Mouse && !p.pressed && e.Type != pointer.Press {
		// Consider non-mouse pointers leaving when they're released.
		hits = nil
	}
	// Deliver Leave events.
	for _, k := range p.entered {
		if _, found := searchTag(hits, k); found {
			continue
		}
		h := q.handlers[k]
		e.Type = pointer.Leave

		if e.Type&h.types != 0 {
			e.Position = q.invTransform(h.area, e.Position)
			events.Add(k, e)
		}
	}
	// Deliver Enter events and update cursor.
	q.cursor = pointer.CursorDefault
	for _, k := range hits {
		h := q.handlers[k]
		for i := len(q.cursors) - 1; i >= 0; i-- {
			if c := q.cursors[i]; c.area == h.area {
				q.cursor = c.name
				break
			}
		}
		if _, found := searchTag(p.entered, k); found {
			continue
		}
		e.Type = pointer.Enter

		if e.Type&h.types != 0 {
			e.Position = q.invTransform(h.area, e.Position)
			events.Add(k, e)
		}
	}
	p.entered = append(p.entered[:0], hits...)
}

func searchTag(tags []event.Tag, tag event.Tag) (int, bool) {
	for i, t := range tags {
		if t == tag {
			return i, true
		}
	}
	return 0, false
}

// addHandler adds tag to the slice if not present.
func addHandler(tags []event.Tag, tag event.Tag) []event.Tag {
	for _, t := range tags {
		if t == tag {
			return tags
		}
	}
	return append(tags, tag)
}

func (op *areaOp) Hit(pos f32.Point) bool {
	pos = pos.Sub(op.rect.Min)
	size := op.rect.Size()
	switch op.kind {
	case areaRect:
		return 0 <= pos.X && pos.X < size.X &&
			0 <= pos.Y && pos.Y < size.Y
	case areaEllipse:
		rx := size.X / 2
		ry := size.Y / 2
		xh := pos.X - rx
		yk := pos.Y - ry
		// The ellipse function works in all cases because
		// 0/0 is not <= 1.
		return (xh*xh)/(rx*rx)+(yk*yk)/(ry*ry) <= 1
	default:
		panic("invalid area kind")
	}
}

func setScrollEvent(scroll float32, min, max int) (left, scrolled float32) {
	if v := float32(max); scroll > v {
		return scroll - v, v
	}
	if v := float32(min); scroll < v {
		return scroll - v, v
	}
	return 0, scroll
}
