// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// seqStream is the output of seq 1 100000 through a PTY, which turns each
// newline into CR LF: the benchmark baseline's T2.
func seqStream() []byte {
	var b []byte
	for i := 1; i <= 100000; i++ {
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, '\r', '\n')
	}
	return b
}

// sgrStream is coloured log output, with an SGR change every few cells.
func sgrStream() []byte {
	var b strings.Builder
	levels := []string{"\x1b[32mINFO\x1b[0m", "\x1b[1;33mWARN\x1b[0m", "\x1b[1;31mERROR\x1b[0m"}
	for i := range 20000 {
		fmt.Fprintf(&b, "\x1b[2m2026-10-30T10:%02d:%02d\x1b[22m %s \x1b[38;5;%dmworker-%d\x1b[39m "+
			"\x1b[38;2;%d;120;200mrequest\x1b[0m id=\x1b[1m%d\x1b[0m took \x1b[4m%dms\x1b[24m\r\n",
			i/60%60, i%60, levels[i%3], 16+i%200, i%8, i%256, i, i%997)
	}
	return []byte(b.String())
}

func benchmarkWrite(b *testing.B, stream []byte) {
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		term := New(80, 24, Options{})
		// The SSH backend reads in chunks of this order.
		for i := 0; i < len(stream); i += 4096 {
			term.Write(stream[i:min(i+4096, len(stream))])
		}
	}
}

func BenchmarkWriteSeq(b *testing.B) { benchmarkWrite(b, seqStream()) }
func BenchmarkWriteSGR(b *testing.B) { benchmarkWrite(b, sgrStream()) }

// diffCase builds the two screens of a diff benchmark.
type diffCase struct {
	name string
	make func(cols, rows int) (from, to *Screen)
}

// textScreen fills a terminal with coloured text, one line per row.
func textScreen(cols, rows int) *Terminal {
	term := New(cols, rows, Options{})
	for y := range rows {
		fmt.Fprintf(writerOf(term), "\x1b[%d;1H\x1b[3%dm%s line %d\x1b[m %s", y+1, y%8,
			strings.Repeat("word ", cols/10), y, strings.Repeat("x", cols/4))
	}
	return term
}

type termWriter struct{ t *Terminal }

func (w termWriter) Write(p []byte) (int, error) { w.t.Write(p); return len(p), nil }

func writerOf(t *Terminal) termWriter { return termWriter{t} }

var diffCases = []diffCase{
	{"unchanged", func(cols, rows int) (*Screen, *Screen) {
		term := textScreen(cols, rows)
		from := term.Snapshot()
		// A change that leaves the screen as it was.
		term.Write([]byte("\x1b[1;1H\x1b[" + strconv.Itoa(from.Cursor.Y+1) + ";" + strconv.Itoa(from.Cursor.X+1) + "H"))
		return from, term.Snapshot()
	}},
	{"one line", func(cols, rows int) (*Screen, *Screen) {
		term := textScreen(cols, rows)
		from := term.Snapshot()
		term.Write([]byte("\x1b[5;1H\x1b[2Kchanged line"))
		return from, term.Snapshot()
	}},
	{"scroll 1", func(cols, rows int) (*Screen, *Screen) {
		term := textScreen(cols, rows)
		term.Write([]byte(fmt.Sprintf("\x1b[%d;1H", rows)))
		from := term.Snapshot()
		term.Write([]byte("\r\nnew line at the bottom"))
		return from, term.Snapshot()
	}},
	{"scroll screen", func(cols, rows int) (*Screen, *Screen) {
		term := textScreen(cols, rows)
		term.Write([]byte(fmt.Sprintf("\x1b[%d;1H", rows)))
		from := term.Snapshot()
		for i := range rows {
			fmt.Fprintf(writerOf(term), "\r\n%d", 100000+i)
		}
		return from, term.Snapshot()
	}},
	{"repaint", func(cols, rows int) (*Screen, *Screen) {
		a := textScreen(cols, rows).Snapshot()
		term := New(cols, rows, Options{})
		for y := range rows {
			fmt.Fprintf(writerOf(term), "\x1b[%d;1H\x1b[4%dm%s", y+1, y%8, strings.Repeat(string(rune('a'+y%26)), cols))
		}
		return a, term.Snapshot()
	}},
	{"snapshot", func(cols, rows int) (*Screen, *Screen) {
		return nil, textScreen(cols, rows).Snapshot()
	}},
}

func BenchmarkDiff(b *testing.B) {
	for _, size := range [][2]int{{80, 24}, {200, 50}} {
		for _, c := range diffCases {
			from, to := c.make(size[0], size[1])
			b.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], c.name), func(b *testing.B) {
				b.ReportAllocs()
				var n int
				for b.Loop() {
					n = len(Diff(from, to))
				}
				b.ReportMetric(float64(n), "bytes")
			})
		}
	}
}

func BenchmarkSnapshot(b *testing.B) {
	term := textScreen(200, 50)
	b.ReportAllocs()
	for b.Loop() {
		term.Write([]byte("\x1b[10;10Hx"))
		term.Snapshot()
	}
}

// simulateFrames feeds stream to an 80x24 terminal in chunks of chunk
// bytes, as if a frame were sent after each, and returns the bytes of the
// diffs and the number of frames.
func simulateFrames(stream []byte, chunk int) (total, frames int) {
	term := New(80, 24, Options{})
	var last *Screen
	for i := 0; i < len(stream); i += chunk {
		term.Write(stream[i:min(i+chunk, len(stream))])
		s := term.Snapshot()
		total += len(Diff(last, s))
		last = s
		frames++
	}
	return total, frames
}

// TestT2Frames reports what one diff per frame sends for the baseline's
// T2. The arrival rate of the output decides the chunk per frame: at 60
// frames per second, 575 KiB arriving over guacd's 897 ms is about 11 KiB
// a frame, and over the 89.7 ms target about 107 KiB.
func TestT2Frames(t *testing.T) {
	stream := seqStream()
	for _, chunk := range []int{4 << 10, 11 << 10, 16 << 10, 64 << 10, 107 << 10, 256 << 10} {
		total, frames := simulateFrames(stream, chunk)
		t.Logf("%3d KiB a frame: %3d frames, %6d bytes, %4d bytes a frame", chunk>>10, frames, total, total/frames)
		if chunk == 11<<10 && total > 16<<10 {
			t.Errorf("T2 at 60 fps over 897 ms sends %d bytes, more than 16 KiB", total)
		}
	}
}

func BenchmarkT2Frames(b *testing.B) {
	stream := seqStream()
	b.SetBytes(int64(len(stream)))
	var total int
	for b.Loop() {
		total, _ = simulateFrames(stream, 11<<10)
	}
	b.ReportMetric(float64(total), "bytes")
}
