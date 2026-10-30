// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package session_test

import (
	"context"
	"errors"
	"image"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/session"
)

// start starts a session in the bubble and ends it when the test ends.
func start(t *testing.T, cfg session.Config) (*session.Session, *session.Viewer) {
	t.Helper()
	s, owner, err := session.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, owner
}

func ended(t *testing.T, s *session.Session) session.End {
	t.Helper()
	select {
	case <-s.Done():
		return s.End()
	default:
		t.Fatal("session has not ended")
		return session.End{}
	}
}

func TestGraphicalSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		screen := backendtest.NewScreen(128, 96, 10*time.Millisecond)
		s, owner := start(t, session.Config{Backend: screen, TileSize: 32})
		fb := s.Framebuffer()

		synctest.Wait()
		if got := s.Size(); got != image.Pt(128, 96) {
			t.Fatalf("size %v, want 128x96", got)
		}
		p := owner.Take()
		if p.Resize == nil || *p.Resize != image.Pt(128, 96) {
			t.Errorf("owner's pending resize %v", p.Resize)
		}

		// Nobody can see the screen yet, so the backend paints no motion.
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if screen.Frames() != 0 {
			t.Fatalf("%d motion frames with no viewer visible", screen.Frames())
		}
		changed := s.Changed()
		if err := owner.View(true, image.Rectangle{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
		synctest.Wait()
		if screen.Frames() != 2 {
			t.Errorf("%d motion frames in 25 ms at 10 ms while visible, want 2", screen.Frames())
		}
		select {
		case <-changed:
		default:
			t.Error("motion did not signal a change")
		}

		// The swatch tile changes after the key, above the input's clock.
		if err := owner.Input(7, backend.Key{Down: true, Keysym: 0x61}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		seq, clock := owner.LastInput()
		if seq != 7 {
			t.Errorf("last input %d, want 7", seq)
		}
		if v := fb.Version(0); v <= clock {
			t.Errorf("swatch tile at version %d, not above the input's clock %d", v, clock)
		}
		if !owner.Take().Input {
			t.Error("pending state does not report the input")
		}
	})
}

func TestReadOnlyInputDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		screen := backendtest.NewScreen(64, 48, 0)
		s, owner := start(t, session.Config{Backend: screen})
		ro, err := s.Join(session.RoleReadOnly)
		if err != nil {
			t.Fatal(err)
		}
		if err := ro.Input(1, backend.Key{Down: true, Keysym: 0x62}); err != nil {
			t.Fatal(err)
		}
		if err := owner.Input(1, backend.Key{Down: true, Keysym: 0x63}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := s.DroppedInputs(); got != 1 {
			t.Errorf("%d inputs dropped, want 1", got)
		}
		if got := screen.Inputs(); len(got) != 1 || got[0].(backend.Key).Keysym != 0x63 {
			t.Errorf("backend received %v, want only the owner's key", got)
		}
	})
}

func TestDemandFollowsVisibility(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		screen := backendtest.NewScreen(64, 48, 0)
		s, owner := start(t, session.Config{Backend: screen})
		v, _ := s.Join(session.RoleViewer)

		steps := []struct {
			who     *session.Viewer
			visible bool
		}{
			{owner, true},  // visible
			{v, true},      // still visible
			{owner, false}, // the viewer still sees it
			{v, false},     // nobody sees it
			{v, true},      // visible again
		}
		for _, st := range steps {
			if err := st.who.View(st.visible, image.Rectangle{}); err != nil {
				t.Fatal(err)
			}
		}
		v.Leave() // the last visible viewer leaves
		synctest.Wait()
		var got []bool
		for _, d := range screen.Demands() {
			got = append(got, d.Visible)
		}
		if want := []bool{true, false, true, false}; !slices.Equal(got, want) {
			t.Errorf("demands %v, want %v", got, want)
		}
	})
}

func TestViewersSeeEachOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, owner := start(t, session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		owner.Take()
		v, _ := s.Join(session.RoleViewer)
		if p := owner.Take(); len(p.Joined) != 1 || p.Joined[0] != (session.ViewerInfo{ID: v.ID(), Role: session.RoleViewer}) {
			t.Errorf("owner saw joins %v", p.Joined)
		}
		if p := v.Take(); len(p.Joined) != 1 || p.Joined[0].Role != session.RoleOwner {
			t.Errorf("new viewer saw %v, want the owner", p.Joined)
		}
		v.Leave()
		if p := owner.Take(); len(p.Left) != 1 || p.Left[0] != v.ID() {
			t.Errorf("owner saw leaves %v", p.Left)
		}
		if err := v.Input(1, backend.Key{}); !errors.Is(err, session.ErrLeft) {
			t.Errorf("input after leaving returned %v", err)
		}
	})
}

func TestOwnerGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, owner := start(t, session.Config{Backend: backendtest.NewScreen(64, 48, 0), OwnerGrace: time.Minute})
		v, _ := s.Join(session.RoleViewer)
		owner.Leave()

		// The owner comes back within the grace period.
		time.Sleep(50 * time.Second)
		back, err := s.Join(session.RoleOwner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Join(session.RoleOwner); !errors.Is(err, session.ErrOwnerPresent) {
			t.Errorf("second owner join returned %v", err)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		select {
		case <-s.Done():
			t.Fatalf("session ended while its owner was back: %v", s.End())
		default:
		}

		// It leaves again and stays away.
		back.Leave()
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		if got := ended(t, s); got.Reason != session.OwnerLeft {
			t.Errorf("ended for %v, want owner left", got.Reason)
		}
		if p := v.Take(); p.End == nil || p.End.Reason != session.OwnerLeft {
			t.Errorf("viewer's pending end %v", p.End)
		}
		if _, err := s.Join(session.RoleViewer); !errors.Is(err, session.ErrEnded) {
			t.Errorf("join after the end returned %v", err)
		}
	})
}

func TestIdleTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, owner := start(t, session.Config{Backend: backendtest.NewScreen(64, 48, 0), IdleTimeout: 10 * time.Minute})
		time.Sleep(9 * time.Minute)
		if err := owner.Input(1, backend.Key{Down: true, Keysym: 0x61}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(9 * time.Minute)
		synctest.Wait()
		select {
		case <-s.Done():
			t.Fatal("input did not reset the idle timeout")
		default:
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if got := ended(t, s); got.Reason != session.Idle {
			t.Errorf("ended for %v, want idle", got.Reason)
		}
	})
}

func TestBackendEnds(t *testing.T) {
	failure := errors.New("connection reset")
	for _, tc := range []struct {
		err    error
		reason session.Reason
	}{{nil, session.RemoteEnded}, {failure, session.Failed}} {
		synctest.Test(t, func(t *testing.T) {
			screen := backendtest.NewScreen(64, 48, 0)
			s, owner := start(t, session.Config{Backend: screen})
			screen.End(tc.err)
			synctest.Wait()
			got := ended(t, s)
			if got.Reason != tc.reason || !errors.Is(got.Err, tc.err) {
				t.Errorf("ended with %+v, want %v", got, tc.reason)
			}
			if p := owner.Take(); p.End == nil {
				t.Error("owner not told of the end")
			}
		})
	}
}

func TestCloseAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, owner := start(t, session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		s.Close()
		if got := ended(t, s); got.Reason != session.Closed {
			t.Errorf("ended for %v, want closed", got.Reason)
		}
		if err := owner.Input(1, backend.Key{}); !errors.Is(err, session.ErrEnded) {
			t.Errorf("input after close returned %v", err)
		}
		s.Close() // twice is fine
	})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s, _, err := session.Start(ctx, session.Config{Backend: backendtest.NewScreen(64, 48, 0)})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		synctest.Wait()
		if got := ended(t, s); got.Reason != session.Shutdown {
			t.Errorf("ended for %v, want shutdown", got.Reason)
		}
	})
}

func TestSmallStateBroadcast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &scripted{class: backend.Graphical}
		s, owner := start(t, session.Config{Backend: b})
		synctest.Wait()
		sink := b.sink()
		cur := image.NewRGBA(image.Rect(4, 4, 6, 6))
		sink.Cursor(cur, image.Pt(1, 1))
		sink.CursorMove(image.Pt(10, 20))
		sink.Title("desktop")
		sink.Bell()
		sink.Bell()
		sink.Clipboard("text/plain", []byte("hi"))

		p := owner.Take()
		if p.Cursor == nil || p.Cursor.Image.Rect != image.Rect(0, 0, 2, 2) || p.Cursor.Hot != image.Pt(1, 1) {
			t.Errorf("cursor %+v", p.Cursor)
		}
		if p.CursorPos == nil || *p.CursorPos != image.Pt(10, 20) || p.Title == nil || *p.Title != "desktop" ||
			p.Bells != 2 || p.Clipboard == nil || string(p.Clipboard.Data) != "hi" {
			t.Errorf("pending %+v", p)
		}

		// A late joiner starts from the current cursor, position and title.
		late, _ := s.Join(session.RoleViewer)
		p = late.Take()
		if p.Cursor == nil || p.CursorPos == nil || *p.CursorPos != image.Pt(10, 20) || p.Title == nil || *p.Title != "desktop" {
			t.Errorf("late joiner's pending %+v", p)
		}
		if p.Bells != 0 || p.Clipboard != nil {
			t.Errorf("late joiner got past events: %+v", p)
		}
		select {
		case <-late.Wake():
		default:
			t.Error("late joiner not woken")
		}
	})
}

func TestPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &scripted{class: backend.Graphical}
		s, owner := start(t, session.Config{Backend: b})
		v, _ := s.Join(session.RoleViewer)
		synctest.Wait()

		replies := make(chan string, 1)
		go func() {
			r, err := b.sink().Prompt(context.Background(), backend.Prompt{Fields: []backend.PromptField{{Label: "Password", Secret: true}}})
			if err != nil {
				replies <- err.Error()
				return
			}
			replies <- r.Answers[0]
		}()
		synctest.Wait()
		p := owner.Take()
		if len(p.Prompts) != 1 {
			t.Fatalf("owner got %d prompts, want 1", len(p.Prompts))
		}
		if len(v.Take().Prompts) != 0 {
			t.Error("a viewer received the owner's prompt")
		}
		id := p.Prompts[0].ID
		if err := v.Answer(id, backend.PromptReply{Answers: []string{"wrong"}}); !errors.Is(err, session.ErrNotOwner) {
			t.Errorf("viewer's answer returned %v", err)
		}

		// The owner leaves before answering and gets the prompt again on
		// its return.
		owner.Leave()
		back, err := s.Join(session.RoleOwner)
		if err != nil {
			t.Fatal(err)
		}
		p = back.Take()
		if len(p.Prompts) != 1 || p.Prompts[0].ID != id {
			t.Fatalf("returning owner got prompts %+v", p.Prompts)
		}
		if err := back.Answer(id, backend.PromptReply{Answers: []string{"secret"}}); err != nil {
			t.Fatal(err)
		}
		if got := <-replies; got != "secret" {
			t.Errorf("backend got %q, want secret", got)
		}

		// A prompt still open when the session ends fails.
		go func() {
			_, err := b.sink().Prompt(context.Background(), backend.Prompt{})
			replies <- err.Error()
		}()
		synctest.Wait()
		s.Close()
		if got := <-replies; got != session.ErrEnded.Error() {
			t.Errorf("open prompt returned %q at the end, want %q", got, session.ErrEnded)
		}
	})
}

func TestTerminalSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		term := &recordingTerminal{}
		b := backendtest.NewTerminal(80, 24, 0)
		s, owner := start(t, session.Config{Backend: b, Terminal: term})
		synctest.Wait()
		if s.Framebuffer() != nil {
			t.Error("terminal session has a framebuffer")
		}
		changed := s.Changed()
		if err := owner.Input(1, backend.TermInput{Data: []byte("ls\r")}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Error("terminal output did not signal a change")
		}
		if err := owner.Input(2, backend.Resize{Width: 100, Height: 30}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		written, sizes := term.state()
		if written != "ls\r\n" {
			t.Errorf("terminal got %q, want %q", written, "ls\r\n")
		}
		if want := []image.Point{{80, 24}, {100, 30}}; !slices.Equal(sizes, want) {
			t.Errorf("terminal sizes %v, want %v", sizes, want)
		}
		if p := owner.Take(); p.Resize == nil || *p.Resize != image.Pt(100, 30) {
			t.Errorf("pending resize %v", p.Resize)
		}
	})
}

func TestStartErrors(t *testing.T) {
	if _, _, err := session.Start(context.Background(), session.Config{}); err == nil {
		t.Error("started without a backend")
	}
	if _, _, err := session.Start(context.Background(), session.Config{Backend: backendtest.NewTerminal(80, 24, 0)}); err == nil {
		t.Error("started a terminal backend without a Terminal")
	}
}

// scripted is a backend whose sink the test drives directly.
type scripted struct {
	class backend.Class
	mu    sync.Mutex
	s     backend.Sink
}

func (b *scripted) Class() backend.Class { return b.class }

func (b *scripted) Run(ctx context.Context, sink backend.Sink) error {
	b.mu.Lock()
	b.s = sink
	b.mu.Unlock()
	sink.Resize(64, 48)
	<-ctx.Done()
	return ctx.Err()
}

func (b *scripted) Input(backend.InputEvent) error { return nil }
func (b *scripted) Demand(backend.Demand)          {}

func (b *scripted) sink() backend.Sink {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.s
}

type recordingTerminal struct {
	mu      sync.Mutex
	written []byte
	sizes   []image.Point
}

func (r *recordingTerminal) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written = append(r.written, p...)
}

func (r *recordingTerminal) Resize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sizes = append(r.sizes, image.Pt(cols, rows))
}

func (r *recordingTerminal) state() (string, []image.Point) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.written), slices.Clone(r.sizes)
}
