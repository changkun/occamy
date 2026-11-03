// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/protocol"
)

// webStep is how long the page has for each step of a test, such as the
// remote taking a size: xrdp's first session takes a while on a busy host.
const webStep = time.Minute

// TestWebScreen opens the demo desktops that take any size in the web
// client, in Chrome, at device pixel ratios of 2, 1 and 1.5. The remote
// must take the window's size in CSS pixels at each, when the session
// opens and when the window changes: its programs draw in its pixels and
// do not scale, so a screen of device pixels shows them at half their
// size at a ratio of 2. The hello still states the ratio. A window of an
// odd width opens at that width at a ratio of 2, where a size in device
// pixels could only be even.
func TestWebScreen(t *testing.T) {
	addr := endpointV1(t)
	browser := startChrome(t)
	for _, c := range []struct{ id, name string }{
		{"tigervnc", "TigerVNC"},
		{"rdp", "RDP"},
	} {
		for _, w := range []struct{ ratio, width, height float64 }{
			{2, 1280, 800},
			{1, 1280, 800},
			{1.5, 1280, 800},
			{2, 1281, 801},
		} {
			t.Run(fmt.Sprintf("%s-ratio-%g-%gx%g", c.id, w.ratio, w.width, w.height), func(t *testing.T) {
				p := browser.open(t, addr, w.ratio, w.width, w.height)
				p.until(t, "the sign-in form", fmt.Sprintf(webSignIn, v1User, v1Password))
				p.until(t, "the connection in the list", fmt.Sprintf(webOpen, c.name))

				opened := p.awaitScreen(t, w.width)
				if opened.Ratio != w.ratio {
					t.Fatalf("the page runs at a device pixel ratio of %g, want %g", opened.Ratio, w.ratio)
				}
				hello := p.hello(t)
				if int(hello.Width) != opened.Width || int(hello.Height) != opened.Height {
					t.Errorf("the hello asks for %d×%d, want the %d×%d of the window", hello.Width, hello.Height, opened.Width, opened.Height)
				}
				if want := uint16(w.ratio * 100); hello.Capabilities.PixelRatio != want {
					t.Errorf("the hello states a pixel ratio of %d, want %d", hello.Capabilities.PixelRatio, want)
				}

				p.resize(t, w.ratio, 1100, 700)
				p.awaitScreen(t, 1100)

				p.until(t, "the End button", webEnd)
				p.until(t, "the connection list after End", `document.querySelector('ul.connections') !== null`)
			})
		}
	}
}

// The scripts that drive the page. Each is run until it says true, so one
// that acts does so once, when what it acts on has appeared.
const (
	webSignIn = `(() => {
		const form = document.querySelector('form.sign-in')
		if (form === null) return false
		form.elements.username.value = %q
		form.elements.password.value = %q
		form.requestSubmit()
		return true
	})()`
	webOpen = `(() => {
		const b = [...document.querySelectorAll('ul.connections button')].find((b) => b.querySelector('span.name')?.textContent === %q)
		b?.click()
		return b !== undefined
	})()`
	webEnd = `(() => {
		const b = [...document.querySelectorAll('button')].find((b) => b.textContent === 'End session')
		b?.click()
		return b !== undefined
	})()`
	webScreenState = `(() => {
		const c = document.querySelector('canvas.screen')
		if (c === null) return null
		const stage = c.parentElement.getBoundingClientRect()
		return {width: c.width, height: c.height, stageWidth: stage.width, stageHeight: stage.height, ratio: devicePixelRatio}
	})()`
)

// webScreen is a graphical session as the page shows it: the remote's
// screen, to whose size the canvas's bitmap keeps, and the stage in CSS
// pixels that it fills.
type webScreen struct {
	Width, Height           int
	StageWidth, StageHeight float64
	Ratio                   float64
}

// awaitScreen waits for the remote's screen to fill a stage as wide as
// width in CSS pixels, one remote pixel to a CSS pixel.
func (p *webPage) awaitScreen(t *testing.T, width float64) webScreen {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), webStep)
	defer cancel()
	var s webScreen
	for {
		if err := p.eval(ctx, webScreenState, &s); err != nil && ctx.Err() == nil {
			t.Fatal(err)
		}
		if s.StageWidth == width && s.Width == int(math.Round(s.StageWidth)) && s.Height == int(math.Round(s.StageHeight)) {
			return s
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the remote's screen is %d×%d for a stage of %g×%g CSS pixels at a device pixel ratio of %g, after %v",
				s.Width, s.Height, s.StageWidth, s.StageHeight, s.Ratio, webStep)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// until runs script, until it says true.
func (p *webPage) until(t *testing.T, what, script string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), webStep)
	defer cancel()
	for {
		var done bool
		if err := p.eval(ctx, script, &done); err != nil && ctx.Err() == nil {
			t.Fatal(err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no %s after %v", what, webStep)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// hello returns the hello of the stream the page opened last.
func (p *webPage) hello(t *testing.T) protocol.Hello {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	sockets := p.sockets[p.session]
	if len(sockets) == 0 {
		t.Fatal("the page opened no stream")
	}
	u, err := url.Parse(sockets[len(sockets)-1])
	if err != nil {
		t.Fatal(err)
	}
	h, err := protocol.DecodeHelloQuery(u.Query().Get(protocol.HelloParameter))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// chrome is a headless Chrome that a test drives over the DevTools
// protocol, as what the page does at a device pixel ratio can only be
// seen in a browser.
type chrome struct {
	ws   *websocket.Conn
	done chan struct{} // closed when the connection to Chrome ends

	mu      sync.Mutex
	next    int
	pending map[int]chan cdpMessage
	sockets map[string][]string // the WebSocket URLs each page opened, by session
}

// cdpMessage is a reply or an event of the DevTools protocol.
type cdpMessage struct {
	ID        int             `json:"id"`
	Method    string          `json:"method"`
	SessionID string          `json:"sessionId"`
	Params    json.RawMessage `json:"params"`
	Result    json.RawMessage `json:"result"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var devToolsURL = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// chromePath returns the Chrome that OCCAMY_E2E_CHROME names, or else the
// first that it finds. A host without one skips the test, but for CI,
// where that would hide the test; its runners have Chrome.
func chromePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("OCCAMY_E2E_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if os.Getenv("CI") == "true" {
		t.Fatal("no Chrome to run the web client in; OCCAMY_E2E_CHROME names one")
	}
	t.Skip("no Chrome to run the web client in; OCCAMY_E2E_CHROME names one")
	return ""
}

// startChrome starts a headless Chrome, which ends with the test.
func startChrome(t *testing.T) *chrome {
	t.Helper()
	profile, err := os.MkdirTemp("", "occamy-e2e-chrome")
	if err != nil {
		t.Fatal(err)
	}
	// No sandbox, as CI's runners do not allow Chrome's; it opens only
	// the stack under test.
	cmd := exec.Command(chromePath(t), "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--no-first-run", "--no-default-browser-check", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	cmd.WaitDelay = 5 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &chrome{done: make(chan struct{}), pending: map[int]chan cdpMessage{}, sockets: map[string][]string{}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if c.ws != nil {
			_ = c.call(ctx, "", "Browser.close", nil, nil)
			c.ws.CloseNow()
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		// Its helpers may still be leaving, and this is only a temporary
		// profile.
		_ = os.RemoveAll(profile)
	})

	endpoint := make(chan string, 1)
	go func() {
		lines := bufio.NewScanner(stderr)
		for lines.Scan() {
			if m := devToolsURL.FindStringSubmatch(lines.Text()); m != nil {
				endpoint <- m[1]
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), webStep)
	defer cancel()
	select {
	case u := <-endpoint:
		ws, _, err := websocket.Dial(ctx, u, nil) //nolint:bodyclose // coder/websocket: the response body need not be closed
		if err != nil {
			t.Fatal(err)
		}
		// A page's tiles come through as events, base64 and all.
		ws.SetReadLimit(-1)
		c.ws = ws
		go c.read()
	case <-ctx.Done():
		t.Fatalf("Chrome did not start within %v", webStep)
	}
	return c
}

// read takes Chrome's messages until the connection ends.
func (c *chrome) read() {
	defer close(c.done)
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		var m cdpMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		c.mu.Lock()
		if reply, ok := c.pending[m.ID]; ok && m.ID != 0 {
			delete(c.pending, m.ID)
			reply <- m
		} else if m.Method == "Network.webSocketCreated" {
			var created struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(m.Params, &created) == nil {
				c.sockets[m.SessionID] = append(c.sockets[m.SessionID], created.URL)
			}
		}
		c.mu.Unlock()
	}
}

// call sends method to session, or to the browser if session is empty, and
// reads its result into out unless out is nil.
func (c *chrome) call(ctx context.Context, session, method string, params, out any) error {
	reply := make(chan cdpMessage, 1)
	c.mu.Lock()
	c.next++
	id := c.next
	c.pending[id] = reply
	c.mu.Unlock()
	req, err := json.Marshal(struct {
		ID        int    `json:"id"`
		Method    string `json:"method"`
		SessionID string `json:"sessionId,omitempty"`
		Params    any    `json:"params,omitempty"`
	}{id, method, session, params})
	if err != nil {
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, req); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m := <-reply:
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(m.Result, out)
	case <-c.done:
		return fmt.Errorf("%s: the connection to Chrome ended", method)
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// webPage is a page of its own, in a browser context with no sign-in.
type webPage struct {
	*chrome
	session string
}

// open opens the web client that addr serves, in a window of width × height
// CSS pixels at a device pixel ratio, and closes it when the test ends.
func (c *chrome) open(t *testing.T, addr string, ratio, width, height float64) *webPage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), webStep)
	defer cancel()
	var browser struct {
		ID string `json:"browserContextId"`
	}
	if err := c.call(ctx, "", "Target.createBrowserContext", nil, &browser); err != nil {
		t.Fatal(err)
	}
	var target struct {
		ID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "browserContextId": browser.ID}, &target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.call(ctx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": browser.ID}, nil)
	})
	var attached struct {
		ID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.ID, "flatten": true}, &attached); err != nil {
		t.Fatal(err)
	}
	p := &webPage{chrome: c, session: attached.ID}
	p.resize(t, ratio, width, height)
	for _, step := range []struct {
		method string
		params any
	}{
		{"Network.enable", nil},
		{"Page.navigate", map[string]any{"url": "http://" + addr + "/static/"}},
	} {
		if err := c.call(ctx, p.session, step.method, step.params, nil); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// resize makes the window width × height CSS pixels at a device pixel ratio.
func (p *webPage) resize(t *testing.T, ratio, width, height float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), webStep)
	defer cancel()
	metrics := map[string]any{"width": width, "height": height, "deviceScaleFactor": ratio, "mobile": false}
	if err := p.call(ctx, p.session, "Emulation.setDeviceMetricsOverride", metrics, nil); err != nil {
		t.Fatal(err)
	}
}

// eval runs script in the page and reads what it returns into out.
func (p *webPage) eval(ctx context.Context, script string, out any) error {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := p.call(ctx, p.session, "Runtime.evaluate", map[string]any{"expression": script, "returnByValue": true}, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		return errors.New(r.Exception.Text)
	}
	return json.Unmarshal(r.Result.Value, out)
}
