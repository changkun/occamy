// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build rdp_windows

package rdp

// TestWindows runs the client against a Windows Remote Desktop server
// on the machine it runs on, and reports what it sees, item by item:
//
//	OCCAMY_RDP_WINDOWS_ADDR=127.0.0.2:3389 \
//	OCCAMY_RDP_WINDOWS_USER=... OCCAMY_RDP_WINDOWS_PASSWORD=... \
//	go test -tags rdp_windows -run TestWindows -v -timeout 20m ./internal/rdp
//
// The Windows RDP workflow (.github/workflows/windows-rdp.yml) sets a
// runner up for it. The test drives the session through its Run dialog
// and reads what the commands leave in OCCAMY_RDP_WINDOWS_SHARE,
// C:\Users\Public by default, so it needs the server's disk, and it
// asks tasklist whether the session has a shell yet.
// OCCAMY_RDP_DUMP names a directory for screenshots and for the
// server's frames, which OCCAMY_RDP_WINDOWS_TRACE asks for. Those hold
// what the session's screen shows and the user's name, but no password:
// the client's frames are not kept.
//
// It is a probe rather than a gate: what it logs is its purpose. Every
// item starts as not run and stays so unless a stage reaches it, and a
// summary ends the log. A stage that fails or panics ends alone.

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

const scancodeLeftWindows = 0xe05b

// The items of the report.
const (
	itemNLA           = "NLA/CredSSP"
	itemDrawing       = "drawing"
	itemGFX           = "GFX negotiation"
	itemClearCodec    = "ClearCodec"
	itemCaches        = "caches"
	itemKeyboard      = "keyboard"
	itemClipFrom      = "clipboard from the server"
	itemClipTo        = "clipboard to the server"
	itemResize        = "resize"
	itemBitmaps       = "GFX off"
	itemBitmapsResize = "resize with GFX off"
	itemTLS           = "TLS alone"
	itemWrongPassword = "a wrong password"
)

// The stages, each of which is also an item.
var windowsStages = []string{"GFX", "Bitmaps", "TLSOnly", "WrongPassword"}

var windowsItems = []string{
	itemNLA, itemDrawing, itemGFX, itemClearCodec, itemCaches, itemKeyboard,
	itemClipFrom, itemClipTo, itemResize, itemBitmaps, itemBitmapsResize,
	itemTLS, itemWrongPassword,
}

func stageItem(name string) string { return "stage " + name }

// findings holds a verdict for each item. An item without one did not
// run.
type findings struct {
	mu       sync.Mutex
	verdicts map[string]string
}

func (f *findings) set(item, verdict string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verdicts[item] = verdict
}

func (f *findings) has(item string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verdicts[item] != ""
}

func (f *findings) seen(t *testing.T, item, format string, args ...any) {
	t.Helper()
	v := "seen: " + fmt.Sprintf(format, args...)
	t.Logf("finding: %s: %s", item, v)
	f.set(item, v)
}

func (f *findings) failed(t *testing.T, item, format string, args ...any) {
	t.Helper()
	v := "failed: " + fmt.Sprintf(format, args...)
	t.Errorf("finding: %s: %s", item, v)
	f.set(item, v)
}

func (f *findings) notRun(t *testing.T, item, format string, args ...any) {
	t.Helper()
	v := "not run: " + fmt.Sprintf(format, args...)
	t.Logf("finding: %s: %s", item, v)
	f.set(item, v)
}

// report logs the verdict of every item.
func (f *findings) report(t *testing.T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := func(item string) {
		v := f.verdicts[item]
		if v == "" {
			v = "not run"
		}
		t.Logf("summary: %s: %s", item, v)
	}
	for _, name := range windowsStages {
		row(stageItem(name))
	}
	for _, item := range windowsItems {
		row(item)
	}
}

// windowsHost is the server under test.
type windowsHost struct {
	addr, user, password string
	share                string
	f                    *findings
}

func TestWindows(t *testing.T) {
	h := windowsHost{
		addr:     os.Getenv("OCCAMY_RDP_WINDOWS_ADDR"),
		user:     os.Getenv("OCCAMY_RDP_WINDOWS_USER"),
		password: os.Getenv("OCCAMY_RDP_WINDOWS_PASSWORD"),
		share:    os.Getenv("OCCAMY_RDP_WINDOWS_SHARE"),
		f:        &findings{verdicts: map[string]string{}},
	}
	if h.addr == "" || h.user == "" || h.password == "" {
		t.Skip("OCCAMY_RDP_WINDOWS_ADDR, _USER and _PASSWORD name no server")
	}
	if h.share == "" {
		h.share = `C:\Users\Public`
	}
	defer h.f.report(t)
	h.stage(t, "GFX", h.gfx)
	h.stage(t, "Bitmaps", h.bitmaps)
	h.stage(t, "TLSOnly", h.tlsOnly)
	h.stage(t, "WrongPassword", h.wrongPassword)
}

// stage runs a stage so that a panic ends it alone.
func (h windowsHost) stage(t *testing.T, name string, run func(*testing.T)) {
	item := stageItem(name)
	passed := t.Run(name, func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("the panic's stack:\n%s", debug.Stack())
				h.f.failed(t, item, "panic: %v", r)
			}
		}()
		run(t)
	})
	switch {
	case h.f.has(item):
	case passed:
		h.f.set(item, "seen: ran to its end")
	default:
		h.f.set(item, "failed: see its findings")
	}
}

// winScreen is a screen that records the clipboards as they come,
// rather than panicking on what the client should have converted, so
// that a finding does not end the session, and that knows when it holds
// its lock, so that a panic between Lock and Unlock leaves it readable.
type winScreen struct {
	*screen
	held   atomic.Bool
	clipMu sync.Mutex
	clips  []string
}

func (s *winScreen) Lock(r image.Rectangle) *image.RGBA {
	img := s.screen.Lock(r)
	s.held.Store(true)
	return img
}

func (s *winScreen) Unlock(r image.Rectangle) {
	s.held.Store(false)
	s.screen.Unlock(r)
}

// release lets go of the lock that a panic left held. Only the session
// takes it that way, and the session has ended.
func (s *winScreen) release() {
	if s.held.Swap(false) {
		s.mu.Unlock()
	}
}

func (s *winScreen) Clipboard(mime string, data []byte) {
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	s.clips = append(s.clips, mime+" "+strconv.Quote(string(data)))
}

func (s *winScreen) clipboards() []string {
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	return slices.Clone(s.clips)
}

func (s *screen) drawCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draws
}

func (s *screen) resizeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resizes
}

// testWriter writes a logger's lines to the test's log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// runPanic is how Run ended when it panicked.
type runPanic struct {
	value any
	stack []byte
}

func (p *runPanic) Error() string { return fmt.Sprintf("panic: %v", p.value) }

// winSession is a connection to the server, running.
type winSession struct {
	h      windowsHost
	t      *testing.T
	name   string // of the screenshots
	stage  string // the stage's item
	isGFX  bool
	c      *Client
	scr    *winScreen
	cancel context.CancelFunc
	done   chan error

	began    time.Time
	drawn    bool
	nextDump time.Duration
	reported bool

	logMu    sync.Mutex
	warnings []string
	traceMu  sync.Mutex
	trace    []byte
}

// Write takes the client's log lines.
func (s *winSession) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	s.t.Log(line)
	if strings.Contains(line, "level=WARN") {
		s.logMu.Lock()
		defer s.logMu.Unlock()
		s.warnings = append(s.warnings, line)
	}
	return len(p), nil
}

func (s *winSession) warned(text string) bool {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return slices.ContainsFunc(s.warnings, func(w string) bool { return strings.Contains(w, text) })
}

// open connects to the server and runs the session. A connection that
// fails fails item and ends the stage.
func (h windowsHost) open(t *testing.T, stage, item string, cfg Config) *winSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &winSession{
		h:        h,
		t:        t,
		name:     strings.ToLower(stage),
		stage:    stageItem(stage),
		isGFX:    cfg.GFX,
		scr:      &winScreen{screen: &screen{}},
		cancel:   cancel,
		done:     make(chan error, 1),
		nextDump: 15 * time.Second,
	}
	cfg.Username, cfg.Password = h.user, h.password
	cfg.TLS.InsecureSkipVerify = true
	cfg.Logger = slog.New(slog.NewTextHandler(s, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if os.Getenv("OCCAMY_RDP_WINDOWS_TRACE") != "" {
		// What the server sends alone: the client's frames carry its
		// credentials.
		cfg.Trace = func(d Direction, p []byte) {
			if d != FromServer {
				return
			}
			s.traceMu.Lock()
			defer s.traceMu.Unlock()
			s.trace = binary.LittleEndian.AppendUint32(s.trace, uint32(len(p))) // #nosec G115 -- a frame
			s.trace = append(s.trace, p...)
		}
	}
	begin := time.Now()
	cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
	defer ccancel()
	c, err := Dial(cctx, h.addr, cfg)
	if err != nil {
		cancel()
		var ce *CredSSPError
		h.f.failed(t, item, "connecting failed after %v, CredSSPError %v: %v", time.Since(begin).Round(time.Millisecond), errors.As(err, &ce), err)
		t.FailNow()
	}
	s.c = c
	s.began = time.Now()
	w, h2 := c.Size()
	note := fmt.Sprintf("connected in %v, protocol %#x, desktop %d×%d, finalized %v, Unicode keys %v",
		time.Since(begin).Round(time.Millisecond), c.Protocol(), w, h2, c.Finalized(), c.UnicodeInput())
	t.Logf("finding: %s", note)
	if item == itemNLA {
		if c.Protocol()&(ProtocolHybrid|ProtocolHybridEx) != 0 {
			h.f.seen(t, item, "%s", note)
		} else {
			h.f.failed(t, item, "the server chose no NLA: %s", note)
		}
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.scr.release()
				s.done <- &runPanic{value: r, stack: debug.Stack()}
			}
		}()
		s.done <- c.Run(ctx, s.scr)
	}()
	t.Cleanup(s.stop)
	return s
}

// ended reports once how Run ended.
func (s *winSession) ended(err error) {
	if s.reported {
		return
	}
	s.reported = true
	var p *runPanic
	switch {
	case errors.As(err, &p):
		s.t.Logf("the panic's stack:\n%s", p.stack)
		s.h.f.failed(s.t, s.stage, "panic in the session: %v", p.value)
	case errors.Is(err, context.Canceled):
	default:
		s.t.Errorf("finding: the session ended: %v", err)
	}
}

// stop ends the session, and reports what can be read of it only then.
func (s *winSession) stop() {
	s.cancel()
	select {
	case err := <-s.done:
		s.ended(err)
	case <-time.After(30 * time.Second):
		s.t.Errorf("finding: Run did not return 30 s after the cancel")
		return
	}
	s.report("at the end")
	if s.isGFX {
		s.judgeGFX()
	}
	s.saveTrace()
}

// judgeGFX gives the verdicts on GFX, whose state is the reader's until
// Run has returned.
func (s *winSession) judgeGFX() {
	st, f := s.c.Stats(), s.h.f
	if s.c.gfx.version == 0 {
		f.failed(s.t, itemGFX, "no capability confirmed; GFX PDUs %v", sorted(st.GFX))
		f.notRun(s.t, itemClearCodec, "no GFX was confirmed")
		f.notRun(s.t, itemCaches, "no GFX was confirmed")
		return
	}
	f.seen(s.t, itemGFX, "capability version %#x, flags %#x; GFX PDUs %v", s.c.gfx.version, s.c.gfx.flags, sorted(st.GFX))
	warned := s.warned("disable_gfx")
	f.seen(s.t, itemClearCodec, "%d bitmaps skipped, warning logged %v", st.Ignored["GFX ClearCodec bitmaps"], warned)
	f.seen(s.t, itemCaches, "%d PDUs skipped, warning logged %v", st.Ignored["GFX cache PDUs"], warned)
}

// alive reports whether Run still runs.
func (s *winSession) alive() bool {
	select {
	case err := <-s.done:
		s.done <- err // for stop
		s.ended(err)
		return false
	default:
		return true
	}
}

// report logs what the client has received.
func (s *winSession) report(when string) {
	st := s.c.Stats()
	s.t.Logf("finding: stats %s: GFX %v; bitmaps %v; ignored %v; fast path %d, slow path %d, %d bytes, %d pointers",
		when, sorted(st.GFX), sorted(st.Bitmaps), sorted(st.Ignored), st.FastPath, st.SlowPath, st.Bytes, st.Pointers)
	if img := s.scr.snapshot(); img != nil {
		black, colours := coverage(img)
		s.t.Logf("finding: screen %s: %v, %.1f%% black, %d colours, %d draws, %d copies, %d resizes",
			when, img.Rect.Size(), black, colours, s.scr.drawCount(), s.scr.copyCount(), s.scr.resizeCount())
	}
}

// sorted prints m in the order of its keys.
func sorted(m map[string]int) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		fmt.Fprintf(&b, "%q:%d ", k, m[k])
	}
	return strings.TrimSpace(b.String())
}

// coverage returns the share of img that is black, in percent, and how
// many colours it shows, at most 4,096 of them counted.
func coverage(img *image.RGBA) (black float64, colours int) {
	seen := map[uint32]bool{}
	n := 0
	for i := 0; i+3 < len(img.Pix); i += 4 {
		p := img.Pix[i : i+3 : i+3]
		if p[0] == 0 && p[1] == 0 && p[2] == 0 {
			n++
		}
		if len(seen) < 4096 {
			seen[uint32(p[0])<<16|uint32(p[1])<<8|uint32(p[2])] = true
		}
	}
	return 100 * float64(n) / float64(len(img.Pix)/4), len(seen)
}

// dump saves the screen as name.png.
func (s *winSession) dump(name string) {
	dir := os.Getenv("OCCAMY_RDP_DUMP")
	img := s.scr.snapshot()
	if dir == "" || img == nil {
		return
	}
	f, err := os.Create(filepath.Join(dir, s.name+"-"+name+".png"))
	if err != nil {
		s.t.Log(err)
		return
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		s.t.Log(err)
	}
}

// saveTrace saves the server's frames, each after its length in 4
// bytes, in the dump directory.
func (s *winSession) saveTrace() {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	dir := os.Getenv("OCCAMY_RDP_DUMP")
	if dir == "" || len(s.trace) == 0 {
		return
	}
	f, err := os.Create(filepath.Join(dir, s.name+".trace.gz"))
	if err != nil {
		s.t.Log(err)
		return
	}
	defer f.Close()
	z := gzip.NewWriter(f)
	defer z.Close()
	if _, err := z.Write(s.trace); err != nil {
		s.t.Log(err)
	}
}

// watch notes the first draw and keeps a record of the session's first
// minutes.
func (s *winSession) watch() {
	elapsed := time.Since(s.began)
	if !s.drawn && s.scr.drawCount() > 0 {
		s.drawn = true
		s.t.Logf("finding: the first draw after %v", elapsed.Round(time.Millisecond))
	}
	if elapsed >= s.nextDump {
		s.dump(fmt.Sprintf("after-%03ds", int(s.nextDump.Seconds())))
		s.report(fmt.Sprintf("after %v", s.nextDump))
		s.nextDump *= 2
	}
}

// processes returns the names of what the session's user runs.
func (h windowsHost) processes() ([]string, error) {
	out, err := exec.Command("tasklist", "/fi", "username eq "+h.user, "/fo", "csv", "/nh").Output() // #nosec G204 -- the user the workflow made
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(string(out), "\n") {
		name, _, found := strings.Cut(strings.TrimSpace(line), ",")
		if found {
			names = append(names, strings.ToLower(strings.Trim(name, `"`)))
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// waitDesktop waits until the session's user has a shell, and says
// whether it came. Until then keys may go nowhere, so that what they do
// says nothing of the client. A first logon makes the user's profile,
// which takes a while.
func (s *winSession) waitDesktop(limit time.Duration) bool {
	begin := time.Now()
	var names []string
	var lastErr error
	for time.Since(begin) < limit {
		if !s.alive() {
			return false
		}
		s.watch()
		var err error
		names, err = s.h.processes()
		if err != nil && (lastErr == nil || err.Error() != lastErr.Error()) {
			s.t.Logf("tasklist: %v", err)
		}
		lastErr = err
		if slices.Contains(names, "explorer.exe") {
			s.t.Logf("finding: the shell runs after %v, among %v", time.Since(s.began).Round(time.Millisecond), names)
			return true
		}
		time.Sleep(2 * time.Second)
	}
	s.t.Logf("finding: no shell after %v; the user runs %v", limit, names)
	return false
}

// settle waits for the screen to show something and stop changing, or
// for limit.
func (s *winSession) settle(name string, limit time.Duration) {
	begin := time.Now()
	lastDraws, stillSince := -1, time.Now()
	for time.Since(begin) < limit {
		if !s.alive() {
			return
		}
		s.watch()
		draws := s.scr.drawCount()
		if draws != lastDraws {
			lastDraws, stillSince = draws, time.Now()
		}
		if draws > 0 && time.Since(stillSince) > 8*time.Second {
			if img := s.scr.snapshot(); img != nil {
				if black, _ := coverage(img); black < 99 {
					break
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	s.t.Logf("finding: %s settled after %v", name, time.Since(begin).Round(time.Millisecond))
	s.dump(name)
	s.report(name)
}

// key taps the key of scancode sc.
func (s *winSession) key(sc uint16) {
	s.t.Helper()
	if err := s.c.Key(sc, true); err != nil {
		s.t.Errorf("key %#x: %v", sc, err)
	}
	if err := s.c.Key(sc, false); err != nil {
		s.t.Errorf("key %#x: %v", sc, err)
	}
}

// typeText types text with Unicode keyboard events.
func (s *winSession) typeText(text string) {
	s.t.Helper()
	for _, u := range utf16.Encode([]rune(text)) {
		if err := s.c.SendInput(UnicodeEvent(u, true), UnicodeEvent(u, false)); err != nil {
			s.t.Errorf("typing: %v", err)
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// run types command into the session's Run dialog.
func (s *winSession) run(command string) {
	s.t.Helper()
	if err := s.c.Key(scancodeLeftWindows, true); err != nil {
		s.t.Errorf("the Windows key: %v", err)
	}
	r, _ := ScancodeForKeysym('r')
	s.key(r)
	if err := s.c.Key(scancodeLeftWindows, false); err != nil {
		s.t.Errorf("the Windows key: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	s.typeText(command)
	time.Sleep(300 * time.Millisecond)
	s.key(0x1c) // Return
}

// waitFor polls cond until it holds or d has passed.
func waitFor(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// waitFile returns what the file holds once it has something, or "".
func waitFile(path string, d time.Duration) string {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
	}
	return ""
}

// command runs a command of the session that writes to the shared file
// name, attempts times, until the file comes, and returns what it holds.
func (s *winSession) command(name string, attempts int, format string) string {
	path := filepath.Join(s.h.share, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.t.Logf("removing %s: %v", path, err)
	}
	for i := range attempts {
		if !s.alive() {
			return ""
		}
		s.run(fmt.Sprintf(format, path))
		if got := waitFile(path, 15*time.Second); got != "" {
			return got
		}
		s.t.Logf("no %s after attempt %d", name, i+1)
		s.dump(fmt.Sprintf("%s-attempt-%d", strings.TrimSuffix(name, ".txt"), i+1))
	}
	return ""
}

// resize asks for each size in turn.
func (s *winSession) resize(item string, sizes ...image.Point) {
	var notes []string
	for _, want := range sizes {
		if !s.alive() {
			return
		}
		begin := time.Now()
		s.c.RequestSize(want.X, want.Y)
		ok := waitFor(30*time.Second, func() bool {
			w, h := s.c.Size()
			return image.Pt(w, h) == want
		})
		took := time.Since(begin).Round(time.Millisecond)
		s.settle(fmt.Sprintf("resize-%dx%d", want.X, want.Y), 20*time.Second)
		if !ok {
			w, h := s.c.Size()
			s.h.f.failed(s.t, item, "the desktop stays %d×%d, 30 s after asking for %v", w, h, want)
			return
		}
		notes = append(notes, fmt.Sprintf("%v after %v", want, took))
	}
	s.h.f.seen(s.t, item, "%s", strings.Join(notes, ", "))
}

// gfx is the main stage: NLA, a session under GFX, the keyboard, the
// clipboard both ways, resizing, and what Windows draws a window with.
func (h windowsHost) gfx(t *testing.T) {
	s := h.open(t, "GFX", itemNLA, Config{GFX: true, Resize: true, Clipboard: ClipboardFromServer | ClipboardToServer})
	desktop := s.waitDesktop(4 * time.Minute)
	s.settle("logon", time.Minute)
	if !s.alive() {
		return
	}
	names, err := h.processes()
	t.Logf("the user runs %v (%v)", names, err)
	if img := s.scr.snapshot(); img == nil || s.scr.drawCount() == 0 {
		h.f.failed(t, itemDrawing, "nothing drawn after the logon")
	} else if black, colours := coverage(img); black >= 99 {
		h.f.failed(t, itemDrawing, "%d draws, but %.1f%% black", s.scr.drawCount(), black)
	} else {
		h.f.seen(t, itemDrawing, "%d draws, %.1f%% black, %d colours, a shell %v; see gfx-logon.png", s.scr.drawCount(), black, colours, desktop)
	}

	// The keyboard: a command typed in the Run dialog, the session's
	// identity in a file that the test reads. A file proves the keys;
	// no file proves little until the session has a shell.
	got := s.command("occamy-input.txt", 4, `cmd /c "(whoami & echo %%SESSIONNAME%% & query session) > %s"`)
	keyboard := got != ""
	switch {
	case keyboard:
		h.f.seen(t, itemKeyboard, "the Run dialog's command ran: %q", got)
		s.dump("keyboard")
	case !s.alive():
		return
	case desktop:
		h.f.failed(t, itemKeyboard, "a shell ran, and the Run dialog's command left no file; see gfx-occamy-input-attempt-*.png")
	default:
		h.f.notRun(t, itemKeyboard, "no logged-on desktop came; see gfx-occamy-input-attempt-*.png")
	}
	s.report("after the keyboard")

	if keyboard {
		h.clipboard(s)
	} else {
		h.f.notRun(t, itemClipFrom, "it needs the keyboard")
		h.f.notRun(t, itemClipTo, "it needs the keyboard")
	}

	// Display Control: a larger desktop, then the first size again.
	s.resize(itemResize, image.Pt(1280, 800), image.Pt(1024, 768))

	if !keyboard || !s.alive() {
		return
	}
	// A window with text, which Windows draws in ClearCodec if anything.
	s.run("notepad")
	time.Sleep(3 * time.Second)
	s.typeText("The quick brown fox jumps over the lazy dog. 0123456789\r")
	s.typeText("Text that the client must decode to show.\r")
	time.Sleep(3 * time.Second)
	s.settle("notepad", 20*time.Second)
	if !s.alive() {
		return
	}

	// A window of many colours, in case that is drawn in another codec.
	s.run(`powershell -NoProfile -Command "Get-Process | Format-Table -AutoSize | Out-Host; Start-Sleep 20"`)
	s.settle("powershell", 20*time.Second)
}

// clipboard copies text each way.
func (h windowsHost) clipboard(s *winSession) {
	t := s.t
	const fromWindows = "occamy-from-windows"
	s.run(`cmd /c echo ` + fromWindows + `| clip`)
	if waitFor(20*time.Second, func() bool { return strings.Contains(strings.Join(s.scr.clipboards(), " "), fromWindows) }) {
		h.f.seen(t, itemClipFrom, "%v", s.scr.clipboards())
	} else {
		h.f.failed(t, itemClipFrom, "nothing with %q after 20 s, %v", fromWindows, s.scr.clipboards())
	}
	if !s.alive() {
		return
	}

	const toWindows = "occamy-to-windows"
	if err := s.c.SetClipboard([]byte(toWindows)); err != nil {
		h.f.failed(t, itemClipTo, "SetClipboard: %v", err)
		return
	}
	time.Sleep(2 * time.Second)
	got := s.command("occamy-clip.txt", 2, `powershell -NoProfile -Command "Get-Clipboard | Out-File -Encoding ascii %s"`)
	if strings.Contains(got, toWindows) {
		h.f.seen(t, itemClipTo, "the session's clipboard holds %q", got)
	} else {
		h.f.failed(t, itemClipTo, "the session's clipboard holds %q, want %q", got, toWindows)
	}
}

// bitmaps connects to the session again with GFX off, as disable_gfx
// does: the fallback for what the client does not decode under GFX.
func (h windowsHost) bitmaps(t *testing.T) {
	s := h.open(t, "Bitmaps", itemBitmaps, Config{Resize: true, Clipboard: ClipboardFromServer | ClipboardToServer})
	s.settle("reconnect", time.Minute)
	if !s.alive() {
		return
	}
	got := s.command("occamy-bitmaps.txt", 3, `cmd /c "echo keys under bitmaps> %s"`)
	if got != "" && s.alive() {
		s.run("notepad")
		time.Sleep(3 * time.Second)
		s.typeText("Under bitmap updates, with GFX off.\r")
		s.settle("notepad", 20*time.Second)
	}
	s.resize(itemBitmapsResize, image.Pt(1280, 800))
	if !s.alive() {
		return
	}
	st := s.c.Stats()
	if img := s.scr.snapshot(); img == nil || s.scr.drawCount() == 0 {
		h.f.failed(t, itemBitmaps, "nothing drawn")
	} else if black, colours := coverage(img); black >= 99 {
		h.f.failed(t, itemBitmaps, "%d draws, but %.1f%% black", s.scr.drawCount(), black)
	} else {
		h.f.seen(t, itemBitmaps, "%d draws, %.1f%% black, %d colours; bitmaps %v; ignored %v; keys worked %v", s.scr.drawCount(), black, colours, sorted(st.Bitmaps), sorted(st.Ignored), got != "")
	}
}

// tlsOnly offers TLS alone to a server that requires NLA.
func (h windowsHost) tlsOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := Dial(ctx, h.addr, Config{Username: h.user, Password: h.password, Protocols: ProtocolSSL, TLS: TLS{InsecureSkipVerify: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil))})
	if err != nil {
		h.f.seen(t, itemTLS, "refused: %v", err)
		return
	}
	defer c.Close()
	h.f.seen(t, itemTLS, "accepted, protocol %#x", c.Protocol())
}

// wrongPassword checks that NLA fails with an error, at once. Once
// only, so that no lockout policy takes the user away.
func (h windowsHost) wrongPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	begin := time.Now()
	c, err := Dial(ctx, h.addr, Config{Username: h.user, Password: h.password + "x", TLS: TLS{InsecureSkipVerify: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil))})
	if err == nil {
		c.Close()
		h.f.failed(t, itemWrongPassword, "it connected")
		return
	}
	var ce *CredSSPError
	if errors.As(err, &ce) {
		h.f.seen(t, itemWrongPassword, "refused after %v: %v", time.Since(begin).Round(time.Millisecond), err)
	} else {
		h.f.failed(t, itemWrongPassword, "refused after %v, but not as a CredSSPError: %v", time.Since(begin).Round(time.Millisecond), err)
	}
}
