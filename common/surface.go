// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package common

import (
	"sync"
	"time"

	"github.com/changkun/occamy/lib"
)

const (
	// SurfaceQueueSize The maximum number of updates to allow within the bitmap queue.
	SurfaceQueueSize = 256
	// SurfaceHeatCellSize Heat map cell size in pixels. Each side of each heat map cell will consist
	// of this many pixels.
	SurfaceHeatCellSize = 64
	// SurfaceHeatCellHistorySize The number of entries to collect within each heat map cell. Collected
	// history entries are used to determine the framerate of the region associated
	// with that cell.
	SurfaceHeatCellHistorySize = 5
	// SurfaceNegligibleWidth The width of an update which should be considered negible and thus
	// trivial overhead compared ot the cost of two updates.
	SurfaceNegligibleWidth = 64
	// SurfaceNegligibleHeight The height of an update which should be considered negible and thus
	// trivial overhead compared ot the cost of two updates.
	SurfaceNegligibleHeight = 64
	// SurfaceDataFactor The proportional increase in cost contributed by transfer and processing of
	// image data, compared to processing an equivalent amount of client-side
	// data.
	SurfaceDataFactor = 16
	// SurfaceBaseCost The base cost of every update. Each update should be considered to have
	// this starting cost, plus any additional cost estimated from its
	// content.
	SurfaceBaseCost = 4096
	// SurfaceNegligibleIncrease An increase in cost is negligible if it is less than
	// 1/GUAC_SURFACE_NEGLIGIBLE_INCREASE of the old cost.
	SurfaceNegligibleIncrease = 4
	// SurfaceFillPatternFactor If combining an update because it appears to be follow a fill pattern,
	// the combined cost must not exceed
	// GUAC_SURFACE_FILL_PATTERN_FACTOR * (total uncombined cost).
	SurfaceFillPatternFactor = 3
	// SurfaceJpegImageQuality The JPEG image quality ('quantization') setting to use. Range 0-100 where
	// 100 is the highest quality/largest file size, and 0 is the lowest
	// quality/smallest file size.
	SurfaceJpegImageQuality = 90
	// SurfaceJpegFramerate The framerate which, if exceeded, indicates that JPEG is preferred.
	SurfaceJpegFramerate = 3
	// SurfaceJpegMinBitmapSize Minimum JPEG bitmap size (area). If the bitmap is smaller than this threshold,
	// it should be compressed as a PNG image to avoid the JPEG compression tax.
	SurfaceJpegMinBitmapSize = 4096
	// SurfaceJpegBlockSize The JPEG compression min block size. This defines the optimal rectangle block
	// size factor for JPEG compression. Usually 8x8 would suffice, but use 16 to
	// reduce the occurrence of ringing artifacts further.
	SurfaceJpegBlockSize = 16
)

// SurfaceHeatCell is a representation of a cell in the refresh heat map. This cell is used to keep
// track of how often an area on a surface is refreshed.
type SurfaceHeatCell struct {
	history [SurfaceHeatCellHistorySize]time.Time
	oldest  int
}

// SurfaceBitmapRect is a representation of a bitmap update, having a rectangle of image data (stored
// elsewhere) and a flushed/not-flushed state.
type SurfaceBitmapRect struct {
	flushed int
	rect    *Rect
}

type Surface struct {
	layer             *Layer
	client            *lib.Client
	socket            *lib.Socket
	x, y, z           int
	opacity           int
	parent            *Layer
	width             int
	height            int
	stride            int
	buffer            []byte
	locationDirty     int
	opacityDirty      int
	dirty             int
	dirtyRect         Rect
	realized          int
	clipped           int
	clipRect          Rect
	bitmapQueueLength int
	bitmapQueue       [SurfaceQueueSize]SurfaceBitmapRect
	heatMap           *SurfaceHeatCell

	mu sync.Mutex
}

func NewSurface(c *lib.Client, s *lib.Socket, l *Layer, w, h int) *Surface {

}
