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
// summary ends the log. A stage that fails or panics ends alone. It
// first replays the connection's security stages under several limits,
// and runs the session under the first that passes and that the client
// can take as it is, naming that limit in every verdict.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"maps"
	"net"
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
	itemRounds        = "CredSSP rounds"
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
var windowsStages = []string{"CredSSPRounds", "TLSOnly", "GFX", "Bitmaps", "WrongPassword"}

var windowsItems = []string{
	itemNLA, itemRounds, itemDrawing, itemGFX, itemClearCodec, itemCaches, itemKeyboard,
	itemClipFrom, itemClipTo, itemResize, itemBitmaps, itemBitmapsResize,
	itemTLS, itemWrongPassword,
}

func stageItem(name string) string { return "stage " + name }

// findings holds a verdict for each item. An item without one did not
// run.
type findings struct {
	mu       sync.Mutex
	verdicts map[string]string
	attempts []string // how each replay went
	limit    string   // what the verdicts come under, if anything
}

func (f *findings) set(item, verdict string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verdicts[item] = verdict
}

func (f *findings) attempt(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, line)
}

func (f *findings) setLimit(limit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = limit
}

// verdict words a verdict, with the limit it came under.
func (f *findings) verdict(word, format string, args ...any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.limit != "" {
		word += " under " + f.limit
	}
	return word + ": " + fmt.Sprintf(format, args...)
}

func (f *findings) has(item string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verdicts[item] != ""
}

func (f *findings) seen(t *testing.T, item, format string, args ...any) {
	t.Helper()
	v := f.verdict("seen", format, args...)
	t.Logf("finding: %s: %s", item, v)
	f.set(item, v)
}

func (f *findings) failed(t *testing.T, item, format string, args ...any) {
	t.Helper()
	v := f.verdict("failed", format, args...)
	t.Errorf("finding: %s: %s", item, v)
	f.set(item, v)
}

func (f *findings) notRun(t *testing.T, item, format string, args ...any) {
	t.Helper()
	v := f.verdict("not run", format, args...)
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
	for _, line := range f.attempts {
		t.Logf("summary: attempt %s", line)
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
	lim                  *sessionLimit
}

// sessionLimit is what the replays leave for the session stages: how the
// client, as it is, gets past round 1.
type sessionLimit struct {
	name      string // "" if it needs no limit
	protocols uint32 // to offer, if not the client's default
	godebug   string // the process's GODEBUG setting, if any
	// pause is how long the connection under the client holds back each
	// of its first two writes of application data, the second of which
	// is NEGOTIATE when the server asked for a retry.
	pause time.Duration
	why   string // why the stages cannot run, if they cannot
}

func TestWindows(t *testing.T) {
	h := windowsHost{
		addr:     os.Getenv("OCCAMY_RDP_WINDOWS_ADDR"),
		user:     os.Getenv("OCCAMY_RDP_WINDOWS_USER"),
		password: os.Getenv("OCCAMY_RDP_WINDOWS_PASSWORD"),
		share:    os.Getenv("OCCAMY_RDP_WINDOWS_SHARE"),
		f:        &findings{verdicts: map[string]string{}},
		lim:      &sessionLimit{why: "the attempts did not finish"},
	}
	if h.addr == "" || h.user == "" || h.password == "" {
		t.Skip("OCCAMY_RDP_WINDOWS_ADDR, _USER and _PASSWORD name no server")
	}
	if h.share == "" {
		h.share = `C:\Users\Public`
	}
	defer h.f.report(t)
	h.stage(t, "CredSSPRounds", h.credSSPRounds)
	h.stage(t, "TLSOnly", h.tlsOnly)
	if why := h.lim.why; why != "" {
		h.f.failed(t, itemNLA, "%s", why)
		for _, item := range []string{
			itemDrawing, itemGFX, itemClearCodec, itemCaches, itemKeyboard, itemClipFrom, itemClipTo,
			itemResize, itemBitmaps, itemBitmapsResize, itemWrongPassword,
		} {
			h.f.notRun(t, item, "%s", why)
		}
		for _, name := range []string{"GFX", "Bitmaps", "WrongPassword"} {
			h.f.notRun(t, stageItem(name), "%s", why)
		}
		return
	}
	if h.lim.godebug != "" {
		t.Setenv("GODEBUG", h.lim.godebug)
	}
	h.f.setLimit(h.lim.name)
	t.Logf("finding: the session stages run under %q", h.lim.name)
	h.stage(t, "GFX", h.gfx)
	h.stage(t, "Bitmaps", h.bitmaps)
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
		h.f.set(item, h.f.verdict("seen", "ran to its end"))
	default:
		h.f.set(item, h.f.verdict("failed", "see its findings"))
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
	c, rec, err := h.connect(cctx, cfg)
	if rec != nil {
		rec.log(func(format string, args ...any) {
			t.Helper()
			t.Logf("the client's TLS: "+format, args...)
		})
		h.limitHeld(t, rec)
	}
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

// connect is Dial under the session's limit, over a connection that
// records the hellos.
func (h windowsHost) connect(ctx context.Context, cfg Config) (*Client, *wireRecorder, error) {
	host, _, err := net.SplitHostPort(h.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("rdp: %w", err)
	}
	cfg.TLS.ServerName = host
	if cfg.Protocols == 0 {
		cfg.Protocols = h.lim.protocols
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", h.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("rdp: %w", err)
	}
	rec := &wireRecorder{Conn: conn, pause: h.lim.pause}
	c, err := Connect(ctx, rec, cfg)
	return c, rec, err
}

// limitHeld checks, from what the client sent, that a limit that a
// GODEBUG setting makes is in force.
func (h windowsHost) limitHeld(t *testing.T, rec *wireRecorder) {
	t.Helper()
	if h.lim.godebug == "" {
		return
	}
	offered := 0
	for _, m := range rec.hellos() {
		if !m.client {
			continue
		}
		offered++
		if m.postQuantum() {
			t.Errorf("finding: %s is not in force: %v", h.lim.godebug, m)
			return
		}
	}
	if offered == 0 {
		t.Errorf("finding: no ClientHello was recorded, so %s is unproven", h.lim.godebug)
		return
	}
	t.Logf("finding: %s is in force: the client offered no post-quantum group", h.lim.godebug)
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

// replayAttempt is an attempt at the connection's security stages, on a
// connection of its own, that differs from the client in one thing, so
// that a failure has a place and a cause: Connect's error is the same
// for all of them.
type replayAttempt struct {
	label     string
	protocols uint32            // to offer, if not the client's default
	limit     func(*tls.Config) // what it changes of the client's TLS
	version   int               // of the first TSRequest, if not the client's
	pause     time.Duration     // between the TLS handshake and NEGOTIATE
	listen    time.Duration     // reading there, for what comes unasked
	// client is how the client itself takes the same limit, as it is;
	// nil if that needs a change to it.
	client *sessionLimit
}

var replayAttempts = []replayAttempt{
	{label: "default", client: &sessionLimit{}},
	{label: "hybrid", protocols: ProtocolHybrid, client: &sessionLimit{name: "hybrid (PROTOCOL_HYBRID alone)", protocols: ProtocolHybrid}},
	{
		label: "tls13-classic",
		limit: func(c *tls.Config) {
			// What GODEBUG=tlsmlkem=0 leaves of the default groups.
			c.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384, tls.CurveP521}
		},
		client: &sessionLimit{name: "tls13-classic (GODEBUG=tlsmlkem=0)", godebug: "tlsmlkem=0"},
	},
	{label: "tls13-pause-500ms", pause: 500 * time.Millisecond, client: &sessionLimit{
		name:  "tls13-pause-500ms (the test's connection holds back the client's first application data)",
		pause: 500 * time.Millisecond,
	}},
	{label: "tls13-listen", listen: time.Second},
	// A session cache is what makes Go offer psk_key_exchange_modes and
	// session_ticket, as OpenSSL does by default.
	{label: "tls13-tickets", limit: func(c *tls.Config) { c.ClientSessionCache = tls.NewLRUClientSessionCache(1) }},
	{label: "tls13-tickets-listen", listen: time.Second, limit: func(c *tls.Config) { c.ClientSessionCache = tls.NewLRUClientSessionCache(1) }},
	{label: "tls13-p384-tickets", limit: func(c *tls.Config) {
		c.CurvePreferences = []tls.CurveID{tls.CurveP384}
		c.ClientSessionCache = tls.NewLRUClientSessionCache(1)
	}},
	{label: "tls13-p384", limit: func(c *tls.Config) { c.CurvePreferences = []tls.CurveID{tls.CurveP384} }},
	{label: "tls12", limit: func(c *tls.Config) { c.MaxVersion = tls.VersionTLS12 }},
	{label: "credssp-v2", version: 2},
}

// How far a replay came.
const (
	reachedNothing = iota
	reachedTLS
	reachedChallenge // past round 1
	reachedProof     // the server proved its key in round 2
	reachedCredentials
)

// credSSPRounds runs the replays and leaves for the session stages the
// first way past round 1 that the client can take as it is.
func (h windowsHost) credSSPRounds(t *testing.T) {
	var chosen *sessionLimit
	var others []string
	for i, a := range replayAttempts {
		if i > 0 {
			// So that the server's events tell the attempts apart.
			time.Sleep(3 * time.Second)
		}
		began := time.Now()
		reached, note := h.replay(t, a)
		line := fmt.Sprintf("%s: began %s, took %v: %s", a.label, began.UTC().Format("15:04:05.000"), time.Since(began).Round(time.Millisecond), note)
		t.Logf("attempt %s", line)
		h.f.attempt(line)
		if a.label == "default" {
			if reached >= reachedProof {
				h.f.seen(t, itemRounds, "%s", note)
			} else {
				h.f.failed(t, itemRounds, "%s", note)
			}
		}
		if reached < reachedChallenge {
			continue
		}
		switch {
		case a.client == nil:
			others = append(others, a.label)
		case chosen == nil:
			chosen = a.client
		}
	}
	switch {
	case chosen != nil:
		*h.lim = *chosen
	case len(others) > 0:
		h.lim.why = fmt.Sprintf("only %s passed round 1, and the client cannot take that without a change", strings.Join(others, ", "))
	default:
		h.lim.why = "no attempt passed round 1"
	}
}

// replay repeats the client's connection as far as the server's answer
// to the credentials, with the client's own messages, and returns how
// far it came. It logs what the server sends, and of what the client
// sends only the structure: an AUTHENTICATE_MESSAGE holds proofs of the
// password, and the TSCredentials hold the password.
func (h windowsHost) replay(t *testing.T, a replayAttempt) (reached int, note string) {
	logf := func(format string, args ...any) {
		t.Helper()
		t.Logf("replay %s: "+format, append([]any{a.label}, args...)...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", h.addr)
	if err != nil {
		return reachedNothing, fmt.Sprintf("dialing: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Log(err)
	}
	rec := &wireRecorder{Conn: conn}
	defer rec.log(logf)

	protocols := a.protocols
	if protocols == 0 {
		protocols = ProtocolHybrid | ProtocolHybridEx
	}
	tr := newTransport(rec, nil)
	if err := tr.write(connectionRequest("", protocols)); err != nil {
		return reachedNothing, fmt.Sprintf("the connection request: %v", err)
	}
	f, _, err := tr.readFrame(nil)
	if err != nil {
		return reachedNothing, fmt.Sprintf("the connection confirm: %v", err)
	}
	protocol, err := parseConnectionConfirm(f.data)
	logf("connection confirm % x: protocol %#x, %v", f.data, protocol, err)
	if err != nil {
		return reachedNothing, fmt.Sprintf("the connection confirm: %v", err)
	}

	host, _, err := net.SplitHostPort(h.addr)
	if err != nil {
		return reachedNothing, err.Error()
	}
	policy, err := newTLSPolicy(TLS{ServerName: host, InsecureSkipVerify: true}, host)
	if err != nil {
		return reachedNothing, err.Error()
	}
	cfg := policy.config(conn.RemoteAddr().String(), slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if a.limit != nil {
		a.limit(cfg)
	}
	tc := tls.Client(rec, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		return reachedNothing, fmt.Sprintf("protocol %#x; the TLS handshake: %v", protocol, err)
	}
	tr.upgrade(tc)
	st := tc.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return reachedNothing, "TLS without a certificate"
	}
	cert := st.PeerCertificates[0]
	negotiated := fmt.Sprintf("protocol %#x, %s, %s, group %v", protocol, tls.VersionName(st.Version), tls.CipherSuiteName(st.CipherSuite), st.CurveID)
	logf("%s; certificate %v, %v bytes of public key info", negotiated, cert.PublicKeyAlgorithm, len(cert.RawSubjectPublicKeyInfo))
	pub, err := subjectPublicKey(cert)
	if err != nil {
		return reachedTLS, fmt.Sprintf("%s; %v", negotiated, err)
	}

	version := credSSPVersion
	if a.version != 0 {
		version = a.version
	}
	time.Sleep(a.pause)
	if a.listen > 0 {
		// Whether the server speaks, or objects, before NEGOTIATE.
		if err := tc.SetReadDeadline(time.Now().Add(a.listen)); err != nil {
			t.Log(err)
		}
		var b [1]byte
		n, err := tc.Read(b[:])
		logf("listening for %v before NEGOTIATE: %d bytes of data, %d bytes on the wire since the client's Finished, %v", a.listen, n, rec.afterFinished(), err)
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			return reachedTLS, fmt.Sprintf("%s; before NEGOTIATE: %v", negotiated, err)
		}
		if err := tc.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Log(err)
		}
	}
	nc := &ntlmClient{user: h.user, password: h.password}
	negotiate := nc.negotiateMessage()
	logf("round 1: TSRequest version %d, NEGOTIATE % x", version, negotiate)
	if err := writeTSRequest(tc, &tsRequest{Version: version, NegoTokens: []negoToken{{negotiate}}}); err != nil {
		return reachedTLS, fmt.Sprintf("%s; round 1: writing: %v", negotiated, err)
	}
	resp, err := readTSRequest(tr.r)
	if err != nil {
		return reachedTLS, fmt.Sprintf("%s; round 1: no answer to NEGOTIATE: %v", negotiated, err)
	}
	if len(resp.NegoTokens) == 0 {
		return reachedTLS, fmt.Sprintf("%s; round 1: TSRequest version %d without a token", negotiated, resp.Version)
	}
	challenge := resp.NegoTokens[0].Token
	logf("round 1: TSRequest version %d, CHALLENGE % x", resp.Version, challenge)
	ch, err := parseChallenge(challenge)
	if err != nil {
		return reachedChallenge, fmt.Sprintf("%s; round 1: the CHALLENGE: %v", negotiated, err)
	}
	logf("round 1: CHALLENGE flags %#08x, target info %v, version field %v", ch.flags, pairShapes(ch.targetInfo), ch.flags&ntlmVersion != 0)
	if a.version != 0 {
		return reachedChallenge, fmt.Sprintf("%s; round 1 answered with TSRequest version %d; it stops there, for the replay proves a key at version 5 and later alone", negotiated, resp.Version)
	}

	authenticate, sess, err := nc.authenticate(challenge)
	if err != nil {
		return reachedChallenge, fmt.Sprintf("%s; round 2: the client's answer: %v", negotiated, err)
	}
	logf("round 2: AUTHENTICATE %s", authenticateShape(authenticate))
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	version = min(resp.Version, credSSPVersion)
	err = writeTSRequest(tc, &tsRequest{
		Version:     version,
		NegoTokens:  []negoToken{{authenticate}},
		PubKeyAuth:  sess.seal(bindingHash(clientServerHashMagic, nonce, pub)),
		ClientNonce: nonce,
	})
	if err != nil {
		return reachedChallenge, fmt.Sprintf("%s; round 2: writing: %v", negotiated, err)
	}
	resp, err = readTSRequest(tr.r)
	if err != nil {
		return reachedChallenge, fmt.Sprintf("%s; round 2: no answer to AUTHENTICATE: %v", negotiated, err)
	}
	got, err := sess.unseal(resp.PubKeyAuth)
	if err != nil {
		return reachedChallenge, fmt.Sprintf("%s; round 2: the server's proof: %v", negotiated, err)
	}
	if !hmac.Equal(got, bindingHash(serverClientHashMagic, nonce, pub)) {
		return reachedChallenge, fmt.Sprintf("%s; round 2: the server proved another key", negotiated)
	}

	// The credentials go only to the server that has proved its key.
	pw := utf16le(h.password)
	defer clear(pw)
	creds, err := asn1.Marshal(tsPasswordCreds{utf16le(""), utf16le(h.user), pw})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(creds)
	ts, err := asn1.Marshal(tsCredentials{CredType: 1, Credentials: creds})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(ts)
	if err := writeTSRequest(tc, &tsRequest{Version: version, AuthInfo: sess.seal(ts)}); err != nil {
		return reachedProof, fmt.Sprintf("%s; both rounds answered; the credentials: writing: %v", negotiated, err)
	}
	logf("%d bytes on the wire since the client's Finished", rec.afterFinished())
	if protocol&ProtocolHybridEx == 0 {
		time.Sleep(time.Second) // for the server to log on, with no answer to wait for
		return reachedCredentials, fmt.Sprintf("%s; both rounds answered, the server's proof matches; the credentials are written, and PROTOCOL_HYBRID has no answer to them", negotiated)
	}
	if err := readEarlyUserAuth(tr.r); err != nil {
		return reachedProof, fmt.Sprintf("%s; both rounds answered; the Early User Authorization Result: %v", negotiated, err)
	}
	return reachedCredentials, fmt.Sprintf("%s; both rounds answered, the server's proof matches, and the server authorizes the user", negotiated)
}

// The TLS record types that wireRecorder and parseHellos look at.
const (
	recordChangeCipherSpec = 20
	recordHandshake        = 22
	recordApplicationData  = 23
)

// wireRecorder keeps in memory what crosses a connection until the
// client's first application data, which comes after its part of the
// handshake and before anything of CredSSP. It logs the fields of the
// hellos alone.
type wireRecorder struct {
	net.Conn
	mu             sync.Mutex
	sent, received []byte
	done           bool
	pause          time.Duration // see sessionLimit
	paused         int
	after          int // bytes read since recording stopped
}

func (r *wireRecorder) afterFinished() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.after
}

func (r *wireRecorder) Write(p []byte) (int, error) {
	application := len(p) > 0 && p[0] == recordApplicationData
	r.mu.Lock()
	if application {
		r.done = true
	}
	if !r.done {
		r.sent = append(r.sent, p...)
	}
	hold := application && r.paused < 2
	if hold {
		r.paused++
	}
	r.mu.Unlock()
	if hold {
		time.Sleep(r.pause)
	}
	return r.Conn.Write(p)
}

func (r *wireRecorder) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	r.mu.Lock()
	if !r.done {
		r.received = append(r.received, p[:n]...)
	} else {
		r.after += n
	}
	r.mu.Unlock()
	return n, err
}

// hellos returns what each side said in its hellos, the client's first.
func (r *wireRecorder) hellos() []tlsHello {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(parseHellos(r.sent), parseHellos(r.received)...)
}

func (r *wireRecorder) log(logf func(string, ...any)) {
	for _, m := range r.hellos() {
		logf("%v", m)
	}
}

// helloRetryRandom is the random of a ServerHello that is a
// HelloRetryRequest, RFC 8446 section 4.1.3.
var helloRetryRandom, _ = hex.DecodeString("cf21ad74e59a6111be1d8c021e65b891c2a211167abb8c5e079e09e2c8a8339c")

// tlsHello is what a ClientHello or a ServerHello offers or selects.
type tlsHello struct {
	client     bool
	retry      bool // a HelloRetryRequest
	legacy     uint16
	versions   []uint16
	suites     []uint16
	groups     []tls.CurveID
	shares     []tls.CurveID
	signatures []tls.SignatureScheme
	extensions []uint16
}

// mapped returns the elements of s through f.
func mapped[T, U any](s []T, f func(T) U) []U {
	out := make([]U, len(s))
	for i, v := range s {
		out[i] = f(v)
	}
	return out
}

func (m tlsHello) String() string {
	versions, suites := mapped(m.versions, tls.VersionName), mapped(m.suites, tls.CipherSuiteName)
	switch {
	case m.client:
		return fmt.Sprintf("ClientHello: legacy version %s, versions %q, groups %v, key shares %v, signature algorithms %v, ciphers %q, extensions %d",
			tls.VersionName(m.legacy), versions, m.groups, m.shares, m.signatures, suites, m.extensions)
	case m.retry:
		return fmt.Sprintf("HelloRetryRequest: version %q, cipher %q, group wanted %v, extensions %d", versions, suites, m.shares, m.extensions)
	}
	return fmt.Sprintf("ServerHello: legacy version %s, version %q, cipher %q, key share %v, extensions %d",
		tls.VersionName(m.legacy), versions, suites, m.shares, m.extensions)
}

// postQuantum reports whether the hello offers a post-quantum group.
func (m tlsHello) postQuantum() bool {
	return slices.ContainsFunc(m.groups, func(g tls.CurveID) bool { return strings.Contains(g.String(), "MLKEM") })
}

// helloReader reads the big-endian fields of a hello; past its end it
// gives zeros and says so in bad.
type helloReader struct {
	b   []byte
	bad bool
}

func (c *helloReader) more() bool { return len(c.b) > 0 && !c.bad }

func (c *helloReader) bytes(n int) []byte {
	if c.bad || n > len(c.b) {
		c.bad = true
		return nil
	}
	p := c.b[:n]
	c.b = c.b[n:]
	return p
}

func (c *helloReader) u8() int {
	if p := c.bytes(1); p != nil {
		return int(p[0])
	}
	return 0
}

func (c *helloReader) u16() uint16 {
	if p := c.bytes(2); p != nil {
		return binary.BigEndian.Uint16(p)
	}
	return 0
}

func (c *helloReader) sub8() *helloReader  { return &helloReader{b: c.bytes(c.u8())} }
func (c *helloReader) sub16() *helloReader { return &helloReader{b: c.bytes(int(c.u16()))} }

// parseHellos returns the hellos in one direction of a connection, which
// may start with X.224 frames. It stops at the first record that is
// neither those nor in the clear.
func parseHellos(stream []byte) []tlsHello {
	var handshake []byte
	for len(stream) >= 5 {
		n := 0
		switch stream[0] {
		case 3: // TPKT
			n = int(binary.BigEndian.Uint16(stream[2:]))
		case recordChangeCipherSpec, recordHandshake:
			n = 5 + int(binary.BigEndian.Uint16(stream[3:]))
		}
		if n < 4 || n > len(stream) {
			break
		}
		if stream[0] == recordHandshake {
			handshake = append(handshake, stream[5:n]...)
		}
		stream = stream[n:]
	}
	var out []tlsHello
	c := helloReader{b: handshake}
	for c.more() {
		typ := c.u8()
		size := c.u8()<<16 | int(c.u16())
		body := helloReader{b: c.bytes(size)}
		if !c.bad && (typ == 1 || typ == 2) {
			out = append(out, parseHello(&body, typ == 1))
		}
	}
	return out
}

func parseHello(c *helloReader, client bool) tlsHello {
	m := tlsHello{client: client, legacy: c.u16()}
	random := c.bytes(32)
	m.retry = !client && bytes.Equal(random, helloRetryRandom)
	c.sub8() // the session ID
	if client {
		for s := c.sub16(); s.more(); {
			m.suites = append(m.suites, s.u16())
		}
		c.sub8() // the compression methods
	} else {
		m.suites = []uint16{c.u16()}
		c.u8() // the compression method
	}
	for e := c.sub16(); e.more(); {
		id := e.u16()
		data := e.sub16()
		m.extensions = append(m.extensions, id)
		switch id {
		case 43: // supported_versions
			if !client {
				m.versions = append(m.versions, data.u16())
				break
			}
			for v := data.sub8(); v.more(); {
				m.versions = append(m.versions, v.u16())
			}
		case 10: // supported_groups
			for g := data.sub16(); g.more(); {
				m.groups = append(m.groups, tls.CurveID(g.u16()))
			}
		case 13: // signature_algorithms
			for s := data.sub16(); s.more(); {
				m.signatures = append(m.signatures, tls.SignatureScheme(s.u16()))
			}
		case 51: // key_share
			if !client {
				m.shares = append(m.shares, tls.CurveID(data.u16()))
				break
			}
			for s := data.sub16(); s.more(); {
				m.shares = append(m.shares, tls.CurveID(s.u16()))
				s.sub16() // the key
			}
		}
	}
	return m
}

// pairShapes prints AV pairs by ID and length.
func pairShapes(pairs []avPair) string {
	var b strings.Builder
	for _, p := range pairs {
		fmt.Fprintf(&b, "%#x:%d ", p.id, len(p.value))
	}
	return strings.TrimSpace(b.String())
}

// authenticateShape prints the structure of an AUTHENTICATE_MESSAGE and
// nothing of its responses or keys.
func authenticateShape(m []byte) string {
	if len(m) < ntlmAuthenticateLen {
		return fmt.Sprintf("%d bytes", len(m))
	}
	field := func(off int) string {
		return fmt.Sprintf("%d@%d", binary.LittleEndian.Uint16(m[off:]), binary.LittleEndian.Uint32(m[off+4:]))
	}
	pairs := "unreadable"
	if nt, err := ntlmField(m, 20); err == nil && len(nt) >= 44 {
		if p, err := parseAVPairs(nt[44:]); err == nil {
			pairs = pairShapes(p)
		}
	}
	return fmt.Sprintf("%d bytes: LM %s, NT %s, domain %s, user %s, workstation %s, session key %s; flags %#08x, version % x, MIC set %v; NT target info %s",
		len(m), field(12), field(20), field(28), field(36), field(44), field(52),
		binary.LittleEndian.Uint32(m[60:]), m[64:72], !slices.Equal(m[ntlmMICOffset:ntlmMICOffset+16], make([]byte, 16)), pairs)
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
	c, _, err := h.connect(ctx, Config{Username: h.user, Password: h.password + "x", TLS: TLS{InsecureSkipVerify: true},
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
