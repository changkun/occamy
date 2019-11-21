// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

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
