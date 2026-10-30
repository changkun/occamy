// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package backendtest_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// run starts b on sink and returns a function that stops it and returns
// Run's error.
func run(t *testing.T, b backend.Backend, sink backend.Sink) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx, sink) }()
	return func() error {
		cancel()
		return <-done
	}
}

func at(img *image.RGBA, p image.Point) color.RGBA {
	return img.RGBAAt(p.X, p.Y)
}

func TestScreenPaintsOnlyWhileVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		s := backendtest.NewScreen(64, 48, 10*time.Millisecond)
		stop := run(t, s, sink)

		synctest.Wait()
		if got := sink.Size(); got != image.Pt(64, 48) {
			t.Fatalf("size %v, want 64x48", got)
		}
		fb := sink.Pixels()
		if got, want := at(fb, image.Pt(5, 5)), backendtest.SwatchColor(0); got != want {
			t.Errorf("swatch %v, want %v", got, want)
		}
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if n := s.Frames(); n != 0 {
			t.Fatalf("%d motion frames before any viewer could see", n)
		}

		s.Demand(backend.Demand{Visible: true})
		time.Sleep(35 * time.Millisecond)
		synctest.Wait()
		if n := s.Frames(); n != 3 {
			t.Fatalf("%d motion frames in 35 ms at 10 ms, want 3", n)
		}
		if got, want := at(sink.Pixels(), image.Pt(10, 40)), backendtest.MotionColor(3); got != want {
			t.Errorf("motion area %v, want %v", got, want)
		}
		if got, want := at(sink.Pixels(), image.Pt(5, 5)), backendtest.SwatchColor(0); got != want {
			t.Errorf("motion painted over the swatch: %v, want %v", got, want)
		}

		s.Demand(backend.Demand{Visible: false})
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if n := s.Frames(); n != 3 {
			t.Errorf("%d motion frames after the viewers left, want 3", n)
		}
		if got := s.Demands(); len(got) != 2 || !got[0].Visible || got[1].Visible {
			t.Errorf("demands %v", got)
		}
		if err := stop(); !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	})
}

func TestScreenKeyPaintsSwatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		s := backendtest.NewScreen(64, 48, 0)
		stop := run(t, s, sink)
		defer stop()

		synctest.Wait()
		sink.TakeDamage()
		for _, ev := range []backend.InputEvent{
			backend.Key{Down: true, Keysym: 0x61, Code: 0x04},
			backend.Key{Down: false, Keysym: 0x61, Code: 0x04},
			backend.Pointer{X: 3, Y: 4, Buttons: backend.ButtonLeft},
		} {
			if err := s.Input(ev); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		if got, want := at(sink.Pixels(), image.Pt(31, 31)), backendtest.SwatchColor(0x61); got != want {
			t.Errorf("swatch %v, want %v", got, want)
		}
		if got := sink.TakeDamage(); len(got) != 1 || got[0] != backendtest.Swatch {
			t.Errorf("damage %v, want only the swatch", got)
		}
		if got := s.Inputs(); len(got) != 3 {
			t.Errorf("recorded %d inputs, want 3", len(got))
		}
	})
}

func TestEnd(t *testing.T) {
	failed := errors.New("connection reset")
	for _, want := range []error{nil, failed} {
		synctest.Test(t, func(t *testing.T) {
			s := backendtest.NewScreen(64, 48, 0)
			done := make(chan error, 1)
			go func() { done <- s.Run(context.Background(), &backendtest.Sink{}) }()
			s.End(want)
			s.End(errors.New("ignored"))
			if got := <-done; !errors.Is(got, want) {
				t.Errorf("Run returned %v, want %v", got, want)
			}
		})
	}
}

func TestInputQueueFull(t *testing.T) {
	s := backendtest.NewScreen(64, 48, 0)
	var err error
	n := 0
	for ; err == nil && n < 1000; n++ {
		err = s.Input(backend.Key{Down: true, Keysym: 0x61})
	}
	if !errors.Is(err, backendtest.ErrInputFull) {
		t.Fatalf("Input returned %v after %d inputs, want ErrInputFull", err, n)
	}
	if got := len(s.Inputs()); got != n {
		t.Errorf("recorded %d inputs, want %d", got, n)
	}
}

func TestTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		term := backendtest.NewTerminal(80, 24, 10*time.Millisecond)
		if term.Class() != backend.Terminal {
			t.Fatalf("class %v", term.Class())
		}
		stop := run(t, term, sink)
		defer stop()

		synctest.Wait()
		if got := sink.Size(); got != image.Pt(80, 24) {
			t.Fatalf("size %v, want 80x24", got)
		}
		if err := term.Input(backend.TermInput{Data: []byte("ls\r")}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(25 * time.Millisecond)
		synctest.Wait()
		if got, want := string(sink.Term()), "ls\r\n1\r\n2\r\n"; got != want {
			t.Errorf("output %q, want %q", got, want)
		}
		if err := term.Input(backend.Resize{Width: 100, Height: 30}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := sink.Size(); got != image.Pt(100, 30) {
			t.Errorf("size %v after Resize, want 100x30", got)
		}
	})
}

func TestSinkCopyRectOverlapping(t *testing.T) {
	sink := &backendtest.Sink{}
	sink.Resize(4, 4)
	for y := range 4 {
		r := image.Rect(0, y, 4, y+1)
		img := sink.Lock(r)
		for x := range 4 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(y), A: 0xff})
		}
		sink.Unlock(r)
	}
	// Scroll up by one row, as a terminal or a document view does.
	sink.CopyRect(image.Rect(0, 0, 4, 3), image.Pt(0, 1))
	fb := sink.Pixels()
	for y, want := range []uint8{1, 2, 3, 3} {
		if got := fb.RGBAAt(2, y).R; got != want {
			t.Errorf("row %d holds row %d, want %d", y, got, want)
		}
	}
	if got := sink.Copies(); len(got) != 1 || got[0].Src != image.Pt(0, 1) {
		t.Errorf("copies %v", got)
	}
	if got := len(sink.TakeDamage()); got != 4 {
		t.Errorf("%d damage rectangles, want 4", got)
	}
	if got := len(sink.TakeDamage()); got != 0 {
		t.Errorf("%d damage rectangles after taking them, want 0", got)
	}
}

func TestSinkContract(t *testing.T) {
	for name, f := range map[string]func(*backendtest.Sink){
		"lock before resize": func(s *backendtest.Sink) { s.Lock(image.Rect(0, 0, 1, 1)) },
		"lock outside":       func(s *backendtest.Sink) { s.Resize(4, 4); s.Lock(image.Rect(2, 2, 5, 5)) },
		"lock empty":         func(s *backendtest.Sink) { s.Resize(4, 4); s.Lock(image.Rectangle{}) },
		"unlock other": func(s *backendtest.Sink) {
			s.Resize(4, 4)
			s.Lock(image.Rect(0, 0, 2, 2))
			s.Unlock(image.Rect(0, 0, 1, 1))
		},
		"copy from outside": func(s *backendtest.Sink) { s.Resize(4, 4); s.CopyRect(image.Rect(0, 0, 2, 2), image.Pt(3, 3)) },
		"resize to zero":    func(s *backendtest.Sink) { s.Resize(0, 4) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			f(&backendtest.Sink{})
		})
	}
}

func TestSinkKeepsCopies(t *testing.T) {
	sink := &backendtest.Sink{}
	cur := image.NewRGBA(image.Rect(0, 0, 2, 2))
	cur.SetRGBA(0, 0, color.RGBA{R: 1, A: 0xff})
	sink.Cursor(cur, image.Pt(1, 1))
	cur.SetRGBA(0, 0, color.RGBA{R: 2, A: 0xff})
	if img, hot := sink.CursorImage(); img.RGBAAt(0, 0).R != 1 || hot != image.Pt(1, 1) {
		t.Errorf("cursor not copied: %v at %v", img.RGBAAt(0, 0), hot)
	}
	sink.Cursor(nil, image.Point{})
	if img, _ := sink.CursorImage(); img != nil {
		t.Error("nil cursor not hidden")
	}

	data := []byte("hello")
	sink.Clipboard("text/plain", data)
	data[0] = 'j'
	if mime, got := sink.ClipboardContent(); mime != "text/plain" || string(got) != "hello" {
		t.Errorf("clipboard %q %q", mime, got)
	}

	sink.CursorMove(image.Pt(7, 8))
	sink.Bell()
	sink.Title("desk")
	if sink.CursorPos() != image.Pt(7, 8) || sink.Bells() != 1 || sink.TitleText() != "desk" {
		t.Errorf("pos %v, bells %d, title %q", sink.CursorPos(), sink.Bells(), sink.TitleText())
	}
}

func TestSinkPrompt(t *testing.T) {
	p := backend.Prompt{Name: "vnc", Fields: []backend.PromptField{{Label: "Password", Secret: true}}}
	sink := &backendtest.Sink{}
	if _, err := sink.Prompt(context.Background(), p); !errors.Is(err, backendtest.ErrNoAnswer) {
		t.Errorf("Prompt without Answer returned %v", err)
	}

	sink.Answer = func(q backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: []string{"secret"}}, nil
	}
	reply, err := sink.Prompt(context.Background(), p)
	if err != nil || len(reply.Answers) != 1 || reply.Answers[0] != "secret" {
		t.Errorf("Prompt returned %v, %v", reply, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sink.Prompt(ctx, p); !errors.Is(err, context.Canceled) {
		t.Errorf("Prompt with a done context returned %v", err)
	}
	if got := len(sink.Prompts()); got != 3 {
		t.Errorf("%d prompts recorded, want 3", got)
	}
}
