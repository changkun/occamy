// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import COccamy
import CoreGraphics
import Foundation
import os

/// What dial asks the server for.
public struct Options: Sendable {
    /// The client and its version; nil for the core's default.
    public var name: String?
    /// The screen to ask for, in the remote's pixels; 0 leaves it to the
    /// remote.
    public var width = 0
    public var height = 0
    /// The terminal to ask for, in cells; 0 for the server's default.
    public var cols = 0
    public var rows = 0
    /// Device pixels per point, which only informs the server.
    public var pixelRatio = 1.0
    /// Starts hidden: no frames until view shows the session.
    public var hidden = false

    public init(name: String? = nil, width: Int = 0, height: Int = 0, cols: Int = 0, rows: Int = 0,
                pixelRatio: Double = 1, hidden: Bool = false) {
        self.name = name
        self.width = width
        self.height = height
        self.cols = cols
        self.rows = rows
        self.pixelRatio = pixelRatio
        self.hidden = hidden
    }
}

/// DialError says why dial failed.
public struct DialError: Error, Sendable, LocalizedError {
    /// The server's close code if it closed the connection, such as 4000
    /// for a token it refused; else 0.
    public var closeCode: Int
    /// The server's reason if it closed the connection, such as
    /// "failed" for close code 4006; else empty.
    public var closeReason: String = ""
    public var message: String
    /// The session the server made and the client refused, which the
    /// caller may end through API v1.
    public var sessionID: String?

    public var errorDescription: String? { message }
}

/// SessionError says why a call on a session failed.
public enum SessionError: Error, Sendable, Equatable, LocalizedError {
    /// The session ended.
    case closed
    /// Input from a read-only viewer.
    case readOnly
    /// A prompt reply from a viewer other than the owner.
    case notOwner
    case failed(String)

    public var errorDescription: String? {
        switch self {
        case .closed: "The session ended."
        case .readOnly: "This viewer is read-only."
        case .notOwner: "Only the session's owner answers prompts."
        case let .failed(message): message
        }
    }
}

/// Session is a session of occamyd, through the Go core (occamy.h).
///
/// A thread of its own, since the core's occamy_next blocks, pulls the
/// session's events and yields them to events; for a graphical session
/// it first copies each frame's dirty rectangles to screen. Input goes
/// to the core on a serial queue, in the order it was sent, so that no
/// caller waits on the network. Close ends the session; the core's
/// handle is released once the thread and the queue are done with it.
public final class Session: @unchecked Sendable {
    public let viewerID: UInt32
    public let role: Role
    public let sessionClass: SessionClass
    /// The session's ID, which API v1 names it by.
    public let sessionID: String
    /// The session's events, ending with end. It has one consumer.
    public let events: AsyncStream<Event>
    /// A graphical session's picture as of the last frame events
    /// delivered.
    public let screen = Screen()

    private let handle: occamy_session
    private let continuation: AsyncStream<Event>.Continuation
    private let input = DispatchQueue(label: "de.changkun.occamy.input", qos: .userInteractive)
    private let log = Logger(subsystem: "de.changkun.occamy", category: "session")

    // Pacing: the core acknowledges a frame as shown when it is asked
    // for the next event. While paced, the thread waits after a frame
    // until presented says it was shown, as the web client waits for an
    // animation frame, so that the server sends frames as fast as the
    // app shows them. It waits at most a second, since occamyd closes a
    // viewer that leaves a frame unacknowledged too long.
    private let pace = NSCondition()
    private var paced = false
    private var waiting: UInt32?

    /// dial opens a session on url, a stream route of occamyd such as
    /// wss://host/api/v1/connections/lab/stream, with token, an access,
    /// session or share token. It returns once the server welcomed the
    /// client, or throws a DialError.
    public static func dial(url: URL, token: String, options: Options = Options(),
                            timeout: Duration = .seconds(30)) async throws -> Session {
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Session, Error>) in
            let t = Thread {
                c.resume(with: Result { try Session(url: url, token: token, options: options, timeout: timeout) })
            }
            t.name = "occamy dial"
            t.start()
        }
    }

    private init(url: URL, token: String, options: Options, timeout: Duration) throws {
        var out: occamy_session = 0
        var err = occamy_dial_error()
        defer { occamy_dial_error_free(&err) }
        let ms = Int32(clamping: timeout.components.seconds * 1000 + timeout.components.attoseconds / 1_000_000_000_000_000)
        let res = Self.withOptions(options) { opts in
            occamy_dial(url.absoluteString, token, opts, ms, &out, &err)
        }
        guard res == OCCAMY_OK else {
            throw DialError(closeCode: Int(err.close_code), closeReason: string(err.close_reason) ?? "",
                            message: string(err.message) ?? "occamy: dial failed", sessionID: string(err.session_id))
        }
        handle = out
        var info = occamy_session_info()
        _ = occamy_info(out, &info)
        viewerID = info.viewer_id
        role = Role(rawValue: info.role) ?? .readOnly
        sessionClass = SessionClass(rawValue: info.session_class) ?? .graphical
        sessionID = string(info.session_id) ?? ""
        occamy_free(info.session_id)
        (events, continuation) = AsyncStream.makeStream(of: Event.self)
        let t = Thread { [self] in pump() }
        t.name = "occamy session"
        t.qualityOfService = .userInteractive
        t.start()
    }

    deinit {
        // The thread and the input queue hold the session while they
        // run, so no call on the handle runs or follows.
        occamy_release(handle)
    }

    private static func withOptions<R>(_ o: Options, _ body: (UnsafePointer<occamy_options>) -> R) -> R {
        func call(_ name: UnsafePointer<CChar>?) -> R {
            var opts = occamy_options(
                name: name, width: UInt16(clamping: o.width), height: UInt16(clamping: o.height),
                cols: UInt16(clamping: o.cols), rows: UInt16(clamping: o.rows),
                pixel_ratio: UInt16(clamping: Int((o.pixelRatio * 100).rounded())), hidden: o.hidden)
            return body(&opts)
        }
        if let name = o.name {
            return name.withCString { call($0) }
        }
        return call(nil)
    }

    // MARK: - Events

    private func pump() {
        var ev = occamy_event()
        while occamy_next(handle, -1, &ev) == OCCAMY_OK {
            let event = convert(ev)
            occamy_event_free(&ev)
            if case let .frame(f) = event {
                pace.lock()
                waiting = f.id
                pace.unlock()
            }
            continuation.yield(event)
            if case let .frame(f) = event {
                pace.lock()
                let deadline = Date(timeIntervalSinceNow: 1)
                while paced && waiting == f.id && pace.wait(until: deadline) {}
                waiting = nil
                pace.unlock()
            }
            if case .end = event {
                break
            }
        }
        continuation.finish()
    }

    /// paced says whether the session waits, after a frame, until
    /// presented says it was shown before the server may send the next.
    /// A view that shows frames sets it; one that is hidden clears it.
    public var isPaced: Bool {
        get { pace.withLock { paced } }
        set {
            pace.withLock {
                paced = newValue
                pace.broadcast()
            }
        }
    }

    /// presented says the frame of id is shown.
    public func presented(_ id: UInt32) {
        pace.withLock {
            if waiting == id {
                waiting = nil
                pace.broadcast()
            }
        }
    }

    private func convert(_ ev: occamy_event) -> Event {
        switch ev.kind {
        case OCCAMY_EVENT_FRAME:
            let rects = (0..<ev.rect_count).map { i in
                let r = ev.rects[i]
                return CGRect(x: Int(r.x), y: Int(r.y), width: Int(r.width), height: Int(r.height))
            }
            if !rects.isEmpty, screen.read(from: handle, rects: rects) != OCCAMY_OK {
                log.error("reading the framebuffer: \(self.lastError ?? "", privacy: .public)")
            }
            return .frame(Frame(id: ev.id, lastSeq: ev.last_seq, serverTime: .microseconds(ev.server_time_us), dirty: rects))
        case OCCAMY_EVENT_RESIZE:
            if sessionClass == .graphical {
                screen.resize(width: Int(ev.width), height: Int(ev.height))
            }
            return .resize(width: Int(ev.width), height: Int(ev.height))
        case OCCAMY_EVENT_TERM_SNAPSHOT:
            return .termSnapshot(data(ev))
        case OCCAMY_EVENT_TERM_DATA:
            return .termData(data(ev))
        case OCCAMY_EVENT_CURSOR:
            return .cursor(Cursor(width: Int(ev.width), height: Int(ev.height), hotX: Int(ev.x), hotY: Int(ev.y), rgba: data(ev)))
        case OCCAMY_EVENT_CURSOR_MOVE:
            return .cursorMove(x: Int(ev.x), y: Int(ev.y))
        case OCCAMY_EVENT_CLIPBOARD:
            return .clipboard(mime: string(ev.text) ?? "", data: data(ev))
        case OCCAMY_EVENT_CLIPBOARD_STATUS:
            return .clipboardStatus(answering: ev.code != 0)
        case OCCAMY_EVENT_BELL:
            return .bell
        case OCCAMY_EVENT_TITLE:
            return .title(string(ev.text) ?? "")
        case OCCAMY_EVENT_PROMPT:
            let fields = (0..<ev.field_count).map { i in
                Prompt.Field(label: string(ev.fields[i].label) ?? "", secret: ev.fields[i].secret)
            }
            return .prompt(Prompt(id: ev.id, name: string(ev.text) ?? "", instruction: string(ev.detail) ?? "", fields: fields))
        case OCCAMY_EVENT_VIEWER_JOINED:
            return .viewerJoined(Viewer(id: ev.id, role: Role(rawValue: ev.code), name: string(ev.text) ?? ""))
        case OCCAMY_EVENT_VIEWER_LEFT:
            return .viewerLeft(id: ev.id)
        default: // OCCAMY_EVENT_END, and any kind a newer core adds, which ends what this one knows
            return .end(End(code: Int(ev.code), reason: string(ev.text) ?? "", error: string(ev.detail)))
        }
    }

    // MARK: - Input

    /// send queues input for the session, after what was sent before it.
    /// A failure is logged; submit reports it.
    public func send(_ input: Input) {
        self.input.async { [self] in
            if case let .failure(err) = call(input), err != .closed {
                log.error("sending input: \(err.localizedDescription, privacy: .public)")
            }
        }
    }

    /// submit queues input as send does, and returns its sequence number
    /// once the core took it.
    @discardableResult
    public func submit(_ input: Input) async throws -> UInt32 {
        try await withCheckedThrowingContinuation { c in
            self.input.async { [self] in c.resume(with: call(input)) }
        }
    }

    /// view says whether the session can be seen, and which part of the
    /// screen it shows, in pixels; nil is the whole screen. A hidden
    /// client gets no frames, and one of the current screen when it
    /// shows again.
    public func view(visible: Bool, viewport: CGRect? = nil) {
        let r = viewport.map {
            occamy_rect(x: Int32($0.minX), y: Int32($0.minY), width: Int32($0.width), height: Int32($0.height))
        } ?? occamy_rect()
        input.async { [self] in
            if case let .failure(err) = result(occamy_view(handle, visible, r)), err != .closed {
                log.error("view: \(err.localizedDescription, privacy: .public)")
            }
        }
    }

    /// reply answers the owner's prompt id, with one answer per field.
    public func reply(to prompt: UInt32, answers: [String]) async throws {
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            input.async { [self] in
                let res = withCStrings(answers) { occamy_prompt_reply(handle, prompt, $0, answers.count) }
                c.resume(with: result(res).map { _ in })
            }
        }
    }

    /// close ends the session: events deliver end, and then finish.
    public func close() {
        occamy_close(handle)
    }

    /// lastError is why the session's last call that failed did.
    public var lastError: String? {
        guard let p = occamy_error(handle) else {
            return nil
        }
        defer { occamy_free(p) }
        return String(cString: p)
    }

    private func call(_ input: Input) -> Result<UInt32, SessionError> {
        var seq: UInt32 = 0
        let res: Int32 = switch input {
        case let .key(down, k):
            occamy_key(handle, down, k.keysym, k.usage, &seq)
        case let .text(t):
            occamy_text(handle, t, &seq)
        case let .termInput(d):
            d.withUnsafeBytes { occamy_term_input(handle, $0.baseAddress?.assumingMemoryBound(to: UInt8.self), d.count, &seq) }
        case let .pointer(x, y, buttons):
            occamy_pointer(handle, UInt16(clamping: x), UInt16(clamping: y), buttons.rawValue, &seq)
        case let .wheel(dx, dy, mode):
            occamy_wheel(handle, Int32(clamping: dx), Int32(clamping: dy), mode.rawValue, &seq)
        case let .resize(w, h):
            occamy_resize(handle, UInt16(clamping: w), UInt16(clamping: h), &seq)
        case let .clipboard(mime, d):
            d.withUnsafeBytes { occamy_clipboard(handle, mime, $0.baseAddress?.assumingMemoryBound(to: UInt8.self), d.count, &seq) }
        }
        return result(res).map { seq }
    }

    private func result(_ res: Int32) -> Result<Void, SessionError> {
        switch res {
        case OCCAMY_OK: .success(())
        case OCCAMY_ERR_CLOSED: .failure(.closed)
        case OCCAMY_ERR_READ_ONLY: .failure(.readOnly)
        case OCCAMY_ERR_NOT_OWNER: .failure(.notOwner)
        default: .failure(.failed(lastError ?? "occamy: failed"))
        }
    }
}

private func string(_ p: UnsafePointer<CChar>?) -> String? {
    p.map { String(cString: $0) }
}

private func string(_ p: UnsafeMutablePointer<CChar>?) -> String? {
    p.map { String(cString: $0) }
}

private func data(_ ev: occamy_event) -> Data {
    guard let p = ev.data, ev.data_len > 0 else {
        return Data()
    }
    return Data(bytes: p, count: ev.data_len)
}

/// withCStrings calls body with strings as an array of C strings.
private func withCStrings<R>(_ strings: [String], _ body: (UnsafePointer<UnsafePointer<CChar>?>?) -> R) -> R {
    let copies = strings.map { strdup($0) }
    defer { copies.forEach { free($0) } }
    let pointers = copies.map { UnsafePointer($0) }
    return pointers.withUnsafeBufferPointer { body($0.baseAddress) }
}
