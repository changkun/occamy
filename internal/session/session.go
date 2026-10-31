// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package session turns a backend connection into a session that any
// number of viewers share (specs/008-native-architecture/session-core.md).
//
// One goroutine owns each session. It runs the backend, applies viewers'
// input in arrival order according to their roles, tells the backend
// whether anyone can see the screen, and ends the session when its owner
// leaves for good, when nobody gives input for too long, or when the
// remote ends it. The backend draws into the session's framebuffer, or
// feeds its terminal, directly.
//
// A viewer's writer, which encodes frames for one connection, reads the
// screen from the session and everything else from its Viewer: small
// state such as the cursor and the title, which only ever holds the
// latest value, so a slow viewer never queues stale messages.
package session

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/framebuffer"
	"changkun.de/x/occamy/internal/terminal"
)

// Role is what a viewer may do in a session.
type Role int

const (
	// RoleOwner opened the session. It may give input and answer prompts,
	// and the session ends with it.
	RoleOwner Role = iota + 1
	// RoleViewer may give input.
	RoleViewer
	// RoleReadOnly may only watch; its input is dropped.
	RoleReadOnly
)

func (r Role) String() string {
	switch r {
	case RoleOwner:
		return "owner"
	case RoleViewer:
		return "viewer"
	case RoleReadOnly:
		return "read-only"
	}
	return fmt.Sprintf("Role(%d)", int(r))
}

// Reason is why a session ended.
type Reason int

const (
	// Closed: the owner closed the session.
	Closed Reason = iota + 1
	// OwnerLeft: the owner left and did not return within the grace period.
	OwnerLeft
	// Idle: nobody gave input for the idle timeout.
	Idle
	// RemoteEnded: the remote ended the session normally.
	RemoteEnded
	// Failed: the backend failed; End.Err says why.
	Failed
	// Shutdown: occamyd is shutting down.
	Shutdown
	// Internal: occamyd failed on its own, as when the backend panicked;
	// End.Err is a PanicError.
	Internal
)

func (r Reason) String() string {
	switch r {
	case Closed:
		return "closed"
	case OwnerLeft:
		return "owner left"
	case Idle:
		return "idle"
	case RemoteEnded:
		return "remote ended"
	case Failed:
		return "failed"
	case Shutdown:
		return "shutdown"
	case Internal:
		return "internal error"
	}
	return fmt.Sprintf("Reason(%d)", int(r))
}

// End is how a session ended.
type End struct {
	Reason Reason
	Err    error // the backend's error, for Failed, or a PanicError
}

// PanicError is a panic that a session or a stream recovered from, with
// the stack where it happened, or one that a backend recovered on a
// goroutine of its own and returned from Run. A client's message must not
// crash occamyd, whatever it makes a backend do.
type PanicError = backend.PanicError

// Recovered returns the PanicError of r, the value of recover, or nil
// if r is nil.
func Recovered(r any) error { return backend.Recovered(r) }

// Errors from sessions and viewers.
var (
	// ErrEnded reports a call on a session that has ended.
	ErrEnded = errors.New("session: ended")
	// ErrOwnerPresent reports a join as owner while the owner is present.
	ErrOwnerPresent = errors.New("session: owner already present")
	// ErrNotOwner reports an owner's action by another role.
	ErrNotOwner = errors.New("session: not the owner")
	// ErrLeft reports a call on a viewer that has left.
	ErrLeft = errors.New("session: viewer left")
	// ErrViewerLimit reports a join beyond Config.MaxViewers.
	ErrViewerLimit = errors.New("session: viewer limit reached")
)

// Terminal is the session's terminal state for a terminal backend: the
// headless emulator that its output feeds
// (specs/008-native-architecture/native-ssh-backend.md, Screen diffs).
// The session serialises its own calls to Write and Resize, which may
// come from any of the backend's goroutines; Write's slice is borrowed
// for the call. Viewers' writers call Snapshot concurrently, so the
// emulator still needs its own lock for them. *terminal.Terminal is one.
type Terminal interface {
	Write(p []byte)
	Resize(cols, rows int)
	// Snapshot returns the current screen, which never changes after.
	Snapshot() *terminal.Screen
}

// Config describes a session. Zero fields take the defaults.
type Config struct {
	// Backend is the connection the session runs. Required.
	Backend backend.Backend
	// Terminal holds the screen of a terminal backend. If nil, the
	// session makes a terminal.Terminal of 80x24 until the backend sizes
	// it, whose answers to the application's queries go back to the
	// backend as terminal input.
	Terminal Terminal
	// TileSize is the framebuffer's tile side for a graphical backend,
	// 64 if zero.
	TileSize int
	// OwnerGrace is how long the session waits for its owner to come
	// back, 60 s if zero.
	OwnerGrace time.Duration
	// IdleTimeout ends a session without input for that long, 30 min if
	// zero.
	IdleTimeout time.Duration
	// MaxViewers is the most viewers the session takes besides its
	// owner, who can always join; no limit if zero.
	MaxViewers int
	// Logger receives the session's log, slog.Default() if nil.
	Logger *slog.Logger
}

const (
	defaultTileSize    = 64
	defaultOwnerGrace  = 60 * time.Second
	defaultIdleTimeout = 30 * time.Minute
)

// Session is a backend connection shared by its viewers.
type Session struct {
	cfg     Config
	class   backend.Class
	backend backend.Backend
	fb      *framebuffer.Framebuffer // graphical sessions
	log     *slog.Logger

	cmds   chan func()
	ending chan struct{} // closed when the session starts to end
	done   chan struct{} // closed when it has ended
	end    End

	// outMu serialises Resize and the calls to cfg.Terminal, so that the
	// screen, s.size and what viewers were told agree.
	outMu sync.Mutex

	// Owned by the owner goroutine.
	idle   *time.Timer
	grace  *time.Timer
	graceC <-chan time.Time

	mu        sync.Mutex
	viewers   []*Viewer
	owner     *Viewer // nil while the owner is away
	nextID    int
	size      image.Point
	cursor    *Cursor
	cursorPos image.Point
	title     string
	visible   bool // the demand last sent to the backend
	prompts   map[uint32]*promptRequest
	nextPrmt  uint32
	termWake  chan struct{} // closed at the next terminal output
	replies   [][]byte      // the terminal's answers, for the backend

	replyWake chan struct{} // holds a token while replies is not empty

	dropped atomic.Int64
}

// Start starts a session on cfg.Backend and returns it with the owner's
// viewer. The session ends when ctx is done, among other reasons.
func Start(ctx context.Context, cfg Config) (*Session, *Viewer, error) {
	if cfg.Backend == nil {
		return nil, nil, errors.New("session: no backend")
	}
	if cfg.TileSize <= 0 {
		cfg.TileSize = defaultTileSize
	}
	if cfg.OwnerGrace <= 0 {
		cfg.OwnerGrace = defaultOwnerGrace
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Session{
		cfg:       cfg,
		class:     cfg.Backend.Class(),
		backend:   cfg.Backend,
		log:       cfg.Logger,
		cmds:      make(chan func()),
		ending:    make(chan struct{}),
		done:      make(chan struct{}),
		prompts:   map[uint32]*promptRequest{},
		replyWake: make(chan struct{}, 1),
	}
	switch s.class {
	case backend.Graphical:
		s.fb = framebuffer.New(cfg.TileSize)
	case backend.Terminal:
		if s.cfg.Terminal == nil {
			s.cfg.Terminal = terminal.New(80, 24, terminal.Options{Reply: s.reply})
		}
	default:
		return nil, nil, fmt.Errorf("session: backend of class %v", s.class)
	}
	owner := s.addViewer(RoleOwner)
	s.owner = owner
	go s.run(ctx)
	return s, owner, nil
}

// Class is the kind of content the session shows.
func (s *Session) Class() backend.Class { return s.class }

// Framebuffer is the screen of a graphical session, nil for a terminal.
func (s *Session) Framebuffer() *framebuffer.Framebuffer { return s.fb }

// Screen returns the current screen of a terminal session, nil for a
// graphical one.
func (s *Session) Screen() *terminal.Screen {
	if s.fb != nil {
		return nil
	}
	return s.cfg.Terminal.Snapshot()
}

// reply queues the terminal's answer to the application, such as a
// cursor position report, for the backend's input. It is called from
// the sink's TermData on a backend goroutine, which must not wait for
// the backend, so the owner goroutine delivers it.
func (s *Session) reply(p []byte) {
	s.mu.Lock()
	s.replies = append(s.replies, p)
	s.mu.Unlock()
	select {
	case s.replyWake <- struct{}{}:
	default:
	}
}

// sendReplies gives the backend the terminal's queued answers, in order.
func (s *Session) sendReplies() {
	s.mu.Lock()
	replies := s.replies
	s.replies = nil
	s.mu.Unlock()
	for _, p := range replies {
		if err := s.backend.Input(backend.TermInput{Data: p}); err != nil {
			s.log.Debug("terminal reply dropped", "err", err)
		}
	}
}

// Changed returns a channel that is closed at the next change to the
// screen or the terminal. Call it before reading the screen, so that a
// change in between is not missed.
func (s *Session) Changed() <-chan struct{} {
	if s.fb != nil {
		return s.fb.Changed()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.termWake == nil {
		s.termWake = make(chan struct{})
	}
	return s.termWake
}

// Size is the remote screen's size, in pixels or character cells.
func (s *Session) Size() image.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Done is closed when the session has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// End reports how the session ended. It is valid once Done is closed.
func (s *Session) End() End {
	<-s.done
	return s.end
}

// Viewers describes the session's viewers, the owner first if present,
// then the others in the order they joined.
func (s *Session) Viewers() []ViewerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := make([]ViewerInfo, 0, len(s.viewers))
	for _, v := range s.viewers {
		vs = append(vs, ViewerInfo{ID: v.id, Role: v.role})
	}
	slices.SortStableFunc(vs, func(a, b ViewerInfo) int {
		return cmp.Compare(rank(a.Role), rank(b.Role))
	})
	return vs
}

// rank orders the owner before other roles.
func rank(r Role) int {
	if r == RoleOwner {
		return 0
	}
	return 1
}

// DroppedInputs is the number of inputs dropped from read-only viewers.
func (s *Session) DroppedInputs() int64 { return s.dropped.Load() }

// Join adds a viewer with the given role. Joining as RoleOwner is how the
// owner comes back within the grace period.
func (s *Session) Join(role Role) (*Viewer, error) {
	var v *Viewer
	err := s.do(func() error {
		if role == RoleOwner {
			if s.owner != nil {
				return ErrOwnerPresent
			}
			v = s.addViewer(RoleOwner)
			s.setOwner(v)
			s.ownerAway(false)
			return nil
		}
		if role != RoleViewer && role != RoleReadOnly {
			return fmt.Errorf("session: join as %v", role)
		}
		s.mu.Lock()
		others := len(s.viewers)
		if s.owner != nil {
			others--
		}
		s.mu.Unlock()
		if s.cfg.MaxViewers > 0 && others >= s.cfg.MaxViewers {
			return ErrViewerLimit
		}
		v = s.addViewer(role)
		return nil
	})
	return v, err
}

// Close ends the session at the owner's request.
func (s *Session) Close() {
	_ = s.do(func() error {
		s.finish(End{Reason: Closed})
		return nil
	})
	<-s.done
}

// do runs f on the owner goroutine and returns its error, or ErrEnded.
// If f panics, the session ends, and so does the call.
func (s *Session) do(f func() error) error {
	errc := make(chan error, 1)
	select {
	case s.cmds <- func() {
		defer func() {
			if r := recover(); r != nil {
				errc <- ErrEnded
				panic(r) // for step, which ends the session
			}
		}()
		errc <- f()
	}:
		return <-errc
	case <-s.ending:
		return ErrEnded
	}
}

// run is the owner goroutine.
func (s *Session) run(ctx context.Context) {
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		defer func() {
			if err := Recovered(recover()); err != nil {
				runDone <- err
			}
		}()
		runDone <- s.backend.Run(bctx, sink{s})
	}()

	s.idle = time.NewTimer(s.cfg.IdleTimeout)
	defer s.idle.Stop()
	defer s.ownerAway(false)

	for s.end.Reason == 0 {
		s.step(ctx, &runDone)
	}

	// Prompts and commands waiting on the session give up first, then
	// the backend stops and the viewers learn why.
	close(s.ending)
	cancel()
	if runDone != nil {
		// The session ended for another reason; the backend's error on
		// the way out matters only for the log.
		var p PanicError
		switch err := <-runDone; {
		case errors.As(err, &p):
			s.log.Error("backend panicked", "panic", p.Value, "stack", string(p.Stack))
		case err != nil && !errors.Is(err, context.Canceled):
			s.log.Debug("backend stopped", "err", err)
		}
	}
	s.mu.Lock()
	viewers := slices.Clone(s.viewers)
	s.mu.Unlock()
	for _, v := range viewers {
		v.update(func(p *Pending) { p.End = &s.end })
	}
	if s.fb != nil {
		// Run has returned, so no backend goroutine draws any more: a
		// Lock still held was left by a panic, or forgotten, and the
		// writers waiting for its rows would wait forever. Closing
		// releases it; they read nothing more, and find the end.
		s.fb.Close()
	}
	s.log.Info("session ended", "reason", s.end.Reason.String(), "err", s.end.Err)
	close(s.done)
}

// step waits for what happens next on the owner goroutine and handles
// it. A panic, of a command or of the backend's Run, ends the session
// with Internal: what a client sent must not crash occamyd.
func (s *Session) step(ctx context.Context, runDone *chan error) {
	defer func() {
		if err := Recovered(recover()); err != nil {
			s.panicked(err)
		}
	}()
	select {
	case f := <-s.cmds:
		f()
	case <-s.replyWake:
		s.sendReplies()
	case err := <-*runDone:
		*runDone = nil
		var p PanicError
		switch {
		case errors.As(err, &p):
			s.panicked(err)
		case err == nil:
			s.finish(End{Reason: RemoteEnded})
		case ctx.Err() != nil:
			s.finish(End{Reason: Shutdown})
		default:
			s.finish(End{Reason: Failed, Err: err})
		}
	case <-s.idle.C:
		s.finish(End{Reason: Idle})
	case <-s.graceC:
		s.finish(End{Reason: OwnerLeft})
	case <-ctx.Done():
		s.finish(End{Reason: Shutdown})
	}
}

// panicked ends the session for err, a PanicError, and logs its stack.
func (s *Session) panicked(err error) {
	var p PanicError
	if errors.As(err, &p) {
		s.log.Error("session panicked", "panic", p.Value, "stack", string(p.Stack))
	}
	s.finish(End{Reason: Internal, Err: err})
}

// ownerAway starts the grace period for an owner who left, or stops it.
// It runs on the owner goroutine.
func (s *Session) ownerAway(away bool) {
	if s.grace != nil {
		s.grace.Stop()
		s.grace, s.graceC = nil, nil
	}
	if away {
		s.grace = time.NewTimer(s.cfg.OwnerGrace)
		s.graceC = s.grace.C
	}
}

// finish records why the session ends; run then ends it. It runs on the
// owner goroutine.
func (s *Session) finish(e End) {
	if s.end.Reason == 0 {
		s.end = e
	}
}

// addViewer creates a viewer and tells the others. It runs on the owner
// goroutine, or in Start before it.
func (s *Session) addViewer(role Role) *Viewer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	v := &Viewer{s: s, id: s.nextID, role: role, wake: make(chan struct{}, 1)}
	// A new viewer starts from the session's current small state. A
	// terminal's title and cursor are on its screen, and a terminal has
	// no pointer.
	if s.class == backend.Graphical {
		title := s.title
		v.pending.Title = &title
		pos := s.cursorPos
		v.pending.CursorPos = &pos
	} else if s.title != "" {
		title := s.title
		v.pending.Title = &title
	}
	if s.size != (image.Point{}) {
		size := s.size
		v.pending.Resize = &size
	}
	if s.cursor != nil {
		v.pending.Cursor = s.cursor
	}
	for _, o := range s.viewers {
		o.update(func(p *Pending) { p.Joined = append(p.Joined, ViewerInfo{ID: v.id, Role: role}) })
		v.pending.Joined = append(v.pending.Joined, ViewerInfo{ID: o.id, Role: o.role})
	}
	s.viewers = append(s.viewers, v)
	v.wake <- struct{}{}
	return v
}

// removeViewer runs on the owner goroutine.
func (s *Session) removeViewer(v *Viewer) {
	s.mu.Lock()
	i := slices.Index(s.viewers, v)
	if i < 0 {
		s.mu.Unlock()
		return
	}
	s.viewers = slices.Delete(s.viewers, i, i+1)
	for _, o := range s.viewers {
		o.update(func(p *Pending) { p.Left = append(p.Left, v.id) })
	}
	isOwner := s.owner == v
	if isOwner {
		s.owner = nil
	}
	s.mu.Unlock()
	if isOwner {
		s.ownerAway(true)
	}
	s.updateDemand()
}

// updateDemand tells the backend whether any viewer can see the screen,
// when that changes. It runs on the owner goroutine.
func (s *Session) updateDemand() {
	s.mu.Lock()
	visible := false
	for _, v := range s.viewers {
		v.mu.Lock()
		visible = visible || v.visible
		v.mu.Unlock()
	}
	changed := visible != s.visible
	s.visible = visible
	s.mu.Unlock()
	if changed {
		s.backend.Demand(backend.Demand{Visible: visible})
	}
}

// broadcast applies f to every viewer's pending state.
func (s *Session) broadcast(f func(p *Pending)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked(f)
}

// broadcastLocked is broadcast with s.mu held, for callers that store the
// same state for late joiners in the same critical section.
func (s *Session) broadcastLocked(f func(p *Pending)) {
	for _, v := range s.viewers {
		v.update(f)
	}
}

type promptRequest struct {
	id     uint32
	prompt backend.Prompt
	reply  chan backend.PromptReply
}

// setOwner makes v the owner and gives it the prompts still unanswered.
// Both happen under one hold of s.mu, so that a prompt raised meanwhile
// reaches the owner once, either here or from sink.Prompt. It runs on the
// owner goroutine.
func (s *Session) setOwner(v *Viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owner = v
	for _, r := range s.prompts {
		s.owner.update(func(p *Pending) { p.Prompts = append(p.Prompts, PromptRequest{ID: r.id, Prompt: r.prompt}) })
	}
}

// sink is the backend.Sink a session gives its backend.
type sink struct{ s *Session }

func (k sink) Resize(width, height int) {
	s := k.s
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.fb != nil {
		s.fb.Resize(width, height)
	} else {
		s.cfg.Terminal.Resize(width, height)
		s.wakeTerminal()
	}
	size := image.Pt(width, height)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = size
	s.broadcastLocked(func(p *Pending) { p.Resize = &size })
}

func (k sink) Lock(r image.Rectangle) *image.RGBA { return k.s.fb.Lock(r) }
func (k sink) Unlock(r image.Rectangle)           { k.s.fb.Unlock(r) }

func (k sink) CopyRect(dst image.Rectangle, src image.Point) { k.s.fb.CopyRect(dst, src) }

func (k sink) Cursor(img *image.RGBA, hot image.Point) {
	c := &Cursor{Hot: hot}
	if img != nil {
		c.Image = image.NewRGBA(img.Rect.Sub(img.Rect.Min))
		draw.Draw(c.Image, c.Image.Rect, img, img.Rect.Min, draw.Src)
	}
	s := k.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = c
	s.broadcastLocked(func(p *Pending) { p.Cursor = c })
}

func (k sink) CursorMove(pos image.Point) {
	s := k.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursorPos = pos
	s.broadcastLocked(func(p *Pending) { p.CursorPos = &pos })
}

func (k sink) TermData(data []byte) {
	s := k.s
	s.termWrite(data)
	s.wakeTerminal()
}

// termWrite feeds the terminal under outMu, which a panic in the
// emulator releases too: the backend's other goroutines, such as SSH's
// reader of the other stream, still feed and resize the terminal until
// they stop.
func (s *Session) termWrite(data []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.cfg.Terminal.Write(data)
}

func (k sink) Clipboard(mime string, data []byte) {
	c := &Clipboard{MIME: mime, Data: slices.Clone(data)}
	k.s.broadcast(func(p *Pending) { p.Clipboard = c })
}

func (k sink) Bell() { k.s.broadcast(func(p *Pending) { p.Bells++ }) }

func (k sink) Title(title string) {
	s := k.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.title = title
	s.broadcastLocked(func(p *Pending) { p.Title = &title })
}

func (k sink) Prompt(ctx context.Context, pr backend.Prompt) (backend.PromptReply, error) {
	s := k.s
	s.mu.Lock()
	s.nextPrmt++
	r := &promptRequest{id: s.nextPrmt, prompt: pr, reply: make(chan backend.PromptReply, 1)}
	s.prompts[r.id] = r
	if s.owner != nil {
		s.owner.update(func(p *Pending) { p.Prompts = append(p.Prompts, PromptRequest{ID: r.id, Prompt: pr}) })
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.prompts, r.id)
		s.mu.Unlock()
	}()
	select {
	case reply := <-r.reply:
		return reply, nil
	case <-ctx.Done():
		return backend.PromptReply{}, ctx.Err()
	case <-s.ending:
		return backend.PromptReply{}, ErrEnded
	}
}

func (s *Session) wakeTerminal() {
	s.mu.Lock()
	if s.termWake != nil {
		close(s.termWake)
		s.termWake = nil
	}
	s.mu.Unlock()
}
