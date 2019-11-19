package common

// Rect is a simple representation of a rectangle,
// having a defined corner and dimensions.
type Rect struct {
	X, Y, Width, Height int
}

// NewRect creates a rect with the given coordinates
// and dimensions.
func NewRect(x, y, width, height int) Rect {
	return Rect{x, y, width, height}
}

// Set updates rect parameter
func (r *Rect) Set(x, y, width, height int) {
	r.X = x
	r.Y = y
	r.Width = width
	r.Height = height
}

// ExpandToGrid expands the rectangle to fit an NxN grid
// return true if success otherwise false.
func (r *Rect) ExpandToGrid(cellSize int, max *Rect) bool {
	// invalid cellSize received
	if cellSize <= 0 {
		return false
	}

	// nothing to do
	if cellSize == 1 {
		return true
	}

	// calculate how much the rect must be adjusted to fit within
	// the given cell size
	dw := cellSize - r.Width%cellSize
	dh := cellSize - r.Height%cellSize

	dx := dw / 2
	dy := dh / 2

	// set initial extents of adjusted rect
	top := r.Y - dy
	left := r.X - dx
	bottom := top + r.Height + dh
	right := left + r.Width + dw

	// the max rect
	maxleft := max.X
	maxtop := max.Y
	maxright := maxleft + max.Width
	maxbottom := maxtop + max.Height

	// if the adjusted rectangle has sides beyond the max rectangle,
	// or is larger in any direction; shift or adjust the rectangle
	// while trying to fit in the grid

	// adjust left/right
	if right > maxright {
		// shift to left
		dw = right - maxright
		right -= dw
		left -= dw

		// clamp left if too far
		if left < maxleft {
			left = maxleft
		}
	} else if left < maxleft {
		// shift to right
		dw = maxleft - left
		left += dw
		right += dw

		// clamp right if too far
		if right > maxright {
			right = maxright
		}
	}

	if bottom > maxbottom {
		// shift up
		dh = bottom - maxbottom
		bottom -= dh
		top -= dh

		// clamp top if too far
		if top < maxtop {
			top = maxtop
		}
	} else if top < maxtop {
		// shift down
		dh = maxtop - top
		top += dh
		bottom += dh

		if bottom > maxbottom {
			bottom = maxbottom
		}
	}

	r.Set(left, top, right-left, bottom-top)
	return true
}

// Extend the given rect such that it contains
// at least the specified minimum rect.
func (r *Rect) Extend(min *Rect) {
	// Calculate extents of existing dirty rect
	left := r.X
	top := r.Y
	right := left + r.Width
	bottom := top + r.Height

	// Calculate missing extents of given new rect
	minleft := min.X
	mintop := min.Y
	minright := minleft + min.Width
	minbottom := mintop + min.Height

	//  Update minimums
	if minleft < left {
		left = minleft
	}
	if mintop < top {
		top = mintop
	}
	if minright > right {
		right = minright
	}
	if minbottom > bottom {
		bottom = minbottom
	}

	r.Set(left, top, right-left, bottom-top)
}

// Constrain collapses the given rect such that
// it exists only within the given maximum rect.
func (r *Rect) Constrain(max *Rect) {
	// Calculate extents of existing dirty rect
	left := r.X
	top := r.Y
	right := left + r.Width
	bottom := top + r.Height

	// Calculate missing extents of given new rect
	maxleft := max.X
	maxtop := max.Y
	maxright := maxleft + max.Width
	maxbottom := maxtop + max.Height

	// Update maximums
	if maxleft > left {
		left = maxleft
	}
	if maxtop > top {
		top = maxtop
	}
	if maxright < right {
		right = maxright
	}
	if maxbottom < bottom {
		bottom = maxbottom
	}

	r.Set(left, top, right-left, bottom-top)
}

// RectIntersection ...
type RectIntersection int

// RectIntersection consts
const (
	RectIntersectionEmpty RectIntersection = iota
	RectIntersectionPartial
	RectIntersectionCompleteInside
)

// IsIntersects checks whether a rectangle intersects
// another. Return true if no intersection
func (r *Rect) IsIntersects(other *Rect) RectIntersection {
	if (other.X+other.Width < r.X) ||
		(r.X+r.Width < other.X) ||
		(other.Y+other.Height < r.Y) ||
		(r.Y+r.Height < other.Y) {
		return RectIntersectionEmpty
	} else if (other.X <= r.X) &&
		(other.X+other.Width >= r.X+r.Width) &&
		(other.Y <= r.Y) &&
		(other.Y+other.Height >= r.Y+r.Height) {
		return RectIntersectionCompleteInside
	}

	return RectIntersectionPartial
}

// ClipAndSplit a rectangle into rectangles which
// are not covered by the hole rectangle.
// Return false if no splits were done, and true vise versa
func (r *Rect) ClipAndSplit(hole, split *Rect) bool {
	if r.IsIntersects(hole) == RectIntersectionEmpty {
		return false
	}

	var top, left, bottom, right int

	if r.Y < hole.Y { // top
		top = r.Y
		left = r.X
		bottom = hole.Y
		right = r.X + r.Width

		split.Set(left, top, right-left, bottom-top)

		// re-initialize original rect
		top = hole.Y
		bottom = r.Y + r.Height
		r.Set(left, top, right-left, bottom-top)
		return true
	} else if r.X < hole.X { // left
		top = r.Y
		left = r.X
		bottom = r.Y + r.Height
		right = hole.X

		split.Set(left, top, right-left, bottom-top)

		// re-initialize original rect
		left = hole.X
		right = r.X + r.Width
		r.Set(left, top, right-left, bottom-top)
		return true
	} else if r.Y+r.Height > hole.Y+hole.Height { // bottom
		top = hole.Y + hole.Height
		left = r.X
		bottom = r.Y + r.Height
		right = r.X + r.Width
		r.Set(left, top, right-left, bottom-top)

		top = r.Y
		bottom = hole.Y + hole.Height
		r.Set(left, top, right-left, bottom-top)
		return true
	} else if r.X+r.Width > hole.X+hole.Width { // right
		top = r.Y
		left = hole.X + hole.Width
		bottom = r.Y + r.Height
		right = r.X + r.Width
		split.Set(left, top, right-left, bottom-top)

		left = r.X
		right = hole.X + hole.Width
		r.Set(left, top, right-left, bottom-top)
		return true
	}
	return false
}
