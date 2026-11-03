// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import Observation

/// SessionDisplay shows a session: a FramebufferNSView or a
/// TerminalDisplay. The model hands it the events that change what it
/// shows, in order, and acknowledges a frame once show returned.
@MainActor
public protocol SessionDisplay: AnyObject {
    /// show applies a frame, resize, terminal or cursor event.
    func show(_ event: Event, of session: Session)
}

/// SessionModel runs a session's events on the main actor, as the web
/// client's controller does: it shows the session in its display, keeps
/// the title, the prompts, the viewers and the clipboard, and says how
/// the session ended.
@MainActor
@Observable
public final class SessionModel {
    public let session: Session
    /// The remote's title, if it gave one.
    public private(set) var title: String?
    /// The screen's size, in pixels for a graphical session and in cells
    /// for a terminal; zero until the first frame.
    public private(set) var size = CGSize.zero
    /// Whether the first frame was shown.
    public private(set) var started = false
    /// The prompts waiting for the owner's answer, oldest first.
    public private(set) var prompts: [Prompt] = []
    /// The viewers in the session, this client included.
    public private(set) var viewers: [Viewer] = []
    /// Whether the clipboard goes both ways; off, neither way.
    public var clipboardEnabled = true
    /// Whether the remote's clipboard takes the text sent for it.
    public private(set) var clipboardAnswering = true
    /// How the session ended, once it did.
    public private(set) var ended: End?
    /// How many times the bell rang.
    public private(set) var bells = 0

    /// The display, which the view that shows the session sets.
    @ObservationIgnored public weak var display: SessionDisplay? {
        didSet { session.isPaced = display != nil && visible }
    }

    @ObservationIgnored private let pasteboard: NSPasteboard
    // The clipboard text the remote and this client last agreed on, so
    // that a local clipboard that only echoes the remote's is not sent
    // back, and the pasteboard's change count when it was.
    @ObservationIgnored private var lastClipboard: String?
    @ObservationIgnored private var pasteboardCount = -1
    @ObservationIgnored private var visible = true
    @ObservationIgnored private var lastResize: CGSize = .zero

    public init(session: Session, pasteboard: NSPasteboard = .general) {
        self.session = session
        self.pasteboard = pasteboard
    }

    public var role: Role { session.role }
    public var sessionClass: SessionClass { session.sessionClass }

    /// run handles the session's events until it ends.
    public func run() async {
        for await event in session.events {
            handle(event)
        }
        if ended == nil {
            ended = End(code: 0, reason: "", error: "the session's events stopped")
        }
    }

    func handle(_ event: Event) {
        switch event {
        case let .frame(f):
            display?.show(event, of: session)
            started = true
            session.presented(f.id)
        case let .resize(w, h):
            size = CGSize(width: w, height: h)
            display?.show(event, of: session)
        case .termSnapshot, .termData, .cursor, .cursorMove:
            display?.show(event, of: session)
        case let .clipboard(mime, data):
            guard clipboardEnabled, mime.hasPrefix("text/"), let text = String(data: data, encoding: .utf8) else {
                return
            }
            lastClipboard = text
            pasteboard.clearContents()
            pasteboard.setString(text, forType: .string)
            pasteboardCount = pasteboard.changeCount
        case let .clipboardStatus(answering):
            clipboardAnswering = answering
        case .bell:
            bells += 1
            NSSound.beep()
        case let .title(t):
            title = t.isEmpty ? nil : t
        case let .prompt(p):
            if role.prompts {
                prompts.append(p)
            }
        case let .viewerJoined(v):
            viewers.removeAll { $0.id == v.id }
            viewers.append(v)
        case let .viewerLeft(id):
            viewers.removeAll { $0.id == id }
        case let .end(e):
            ended = e
            prompts = []
        }
    }

    /// terminalTitle sets the title a terminal's escape sequences gave.
    func terminalTitle(_ t: String) {
        title = t.isEmpty ? nil : t
    }

    /// answer answers the owner's prompt, one answer per field.
    public func answer(_ prompt: Prompt, with answers: [String]) {
        prompts.removeAll { $0.id == prompt.id }
        let session = session
        Task { try? await session.reply(to: prompt.id, answers: answers) }
    }

    /// syncClipboard sends the local clipboard's text unless the remote
    /// has it already, as when it came from the remote. The app calls it
    /// when the session's window becomes key, where VNC Viewer does.
    public func syncClipboard() {
        guard clipboardEnabled, role.input, ended == nil, pasteboard.changeCount != pasteboardCount else {
            return
        }
        pasteboardCount = pasteboard.changeCount
        guard let text = pasteboard.string(forType: .string), text != lastClipboard else {
            return
        }
        lastClipboard = text
        session.send(.clipboard(mime: "text/plain", data: Data(text.utf8)))
    }

    /// setVisible says whether the session's window can be seen: a hidden
    /// one gets no frames, and one of the current screen when it shows.
    public func setVisible(_ visible: Bool) {
        guard visible != self.visible else {
            return
        }
        self.visible = visible
        session.view(visible: visible)
        session.isPaced = display != nil && visible
    }

    /// requestResize asks the remote for a screen of width×height, pixels
    /// or cells, if this client is the owner, unless that is the size
    /// last asked for, or the screen's with no other size on its way.
    public func requestResize(width: Int, height: Int) {
        guard role.resize, ended == nil, width > 0, height > 0 else {
            return
        }
        let graphical = sessionClass == .graphical
        let want = CGSize(width: min(width, graphical ? 8192 : 1000), height: min(height, graphical ? 8192 : 500))
        let settled = lastResize == .zero || lastResize == size
        if (settled && want == size) || want == lastResize {
            return
        }
        lastResize = want
        session.send(.resize(width: Int(want.width), height: Int(want.height)))
    }

    /// close leaves the session; for the owner, it runs on for the
    /// server's grace, unless ended through API v1.
    public func close() {
        session.close()
    }
}
