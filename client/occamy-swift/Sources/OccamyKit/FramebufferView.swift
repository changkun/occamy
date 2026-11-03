// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import QuartzCore
import SwiftUI

/// FramebufferNSView shows a graphical session's screen in a CALayer,
/// scaled to fit the view with its aspect ratio kept, and sends the
/// session the local keys, pointer and scrolling.
///
/// Keys go by position, as Keyboard maps them, so the remote's layout
/// decides the character; Command goes as the remote's Super key. The
/// app's own shortcuts, Command-key equivalents, stay with the app
/// unless sendsShortcuts is on, and the remote sees Command only when a
/// key it gets goes with it, or when Command is pressed alone, so that
/// closing the window with Command-W does not press Super on the remote.
@MainActor
public final class FramebufferNSView: NSView, SessionDisplay {
    /// The session's model, whose display the view becomes.
    public weak var model: SessionModel? {
        didSet {
            model?.display = self
            if let model {
                let s = model.session.screen.size
                screenSize = CGSize(width: s.width, height: s.height)
                picture.contents = model.session.screen.image()
                needsLayout = true
            }
        }
    }

    /// Whether the app's own shortcuts go to the remote too.
    public var sendsShortcuts = false

    private let picture = CALayer()
    private var screenSize = CGSize.zero
    private var buttons: Buttons = []
    private var position = (x: 0, y: 0)
    // The keys down, by key code, with what their press sent, which their
    // release sends again.
    private var held: [UInt16: Key] = [:]
    // The Command key down, which goes to the remote once a key goes with
    // it, or as a press of its own once it is released alone.
    private var command: (code: UInt16, key: Key, sent: Bool, shortcut: Bool)?
    // Keys pressed with Command, whose releases AppKit does not deliver.
    private var withCommand: Set<UInt16> = []
    private var scroll = CGPoint.zero
    private var scrollMode = WheelMode.pixels
    private var cursor: Cursor?
    private var tracking: NSTrackingArea?
    private var observers: [NSObjectProtocol] = []

    public override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.backgroundColor = NSColor.black.cgColor
        picture.contentsGravity = .resize
        picture.magnificationFilter = .linear
        picture.minificationFilter = .trilinear
        layer?.addSublayer(picture)
        setAccessibilityRole(.image)
        setAccessibilityLabel("Remote screen")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not supported")
    }

    public override var isFlipped: Bool { true }
    public override var acceptsFirstResponder: Bool { true }
    public override var isOpaque: Bool { true }
    public override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    // MARK: - Showing

    public func show(_ event: Event, of session: Session) {
        switch event {
        case .frame:
            CATransaction.begin()
            CATransaction.setDisableActions(true)
            picture.contents = session.screen.image()
            CATransaction.commit()
        case let .resize(w, h):
            screenSize = CGSize(width: w, height: h)
            needsLayout = true
            window?.invalidateCursorRects(for: self)
        case let .cursor(c):
            cursor = c
            window?.invalidateCursorRects(for: self)
        default:
            break
        }
    }

    /// pictureFrame is where the screen shows: as large as fits, centred.
    public var pictureFrame: CGRect {
        guard screenSize.width > 0, screenSize.height > 0, bounds.width > 0, bounds.height > 0 else {
            return .zero
        }
        let scale = min(bounds.width / screenSize.width, bounds.height / screenSize.height)
        let w = screenSize.width * scale
        let h = screenSize.height * scale
        return CGRect(x: (bounds.width - w) / 2, y: (bounds.height - h) / 2, width: w, height: h)
    }

    public override func layout() {
        super.layout()
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        picture.frame = pictureFrame
        // One remote pixel to one device pixel or more shows sharp.
        let scale = window?.backingScaleFactor ?? 2
        picture.magnificationFilter = pictureFrame.width * scale >= screenSize.width * 2 ? .nearest : .linear
        CATransaction.commit()
        window?.invalidateCursorRects(for: self)
    }

    public override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        observers.forEach(NotificationCenter.default.removeObserver)
        observers = []
        guard let window else {
            return
        }
        // Keys held as the window loses the keyboard would stay down on
        // the remote: their releases go elsewhere.
        observers.append(NotificationCenter.default.addObserver(
            forName: NSWindow.didResignKeyNotification, object: window, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.releaseAll() }
        })
        // The keyboard goes to the remote from the start, as the window
        // has nothing else to type into.
        DispatchQueue.main.async { [weak self] in
            if let self, self.window?.firstResponder === self.window || self.window?.firstResponder == nil {
                self.window?.makeFirstResponder(self)
            }
        }
    }

    // MARK: - Input

    private var canInput: Bool {
        guard let model else {
            return false
        }
        return model.role.input && model.ended == nil
    }

    private func send(_ input: Input) {
        model?.session.send(input)
    }

    /// remote returns where an event happened on the remote screen, in
    /// its pixels, within the screen.
    private func remote(_ event: NSEvent) -> (x: Int, y: Int)? {
        let r = pictureFrame
        guard r.width > 0, r.height > 0 else {
            return nil
        }
        let p = convert(event.locationInWindow, from: nil)
        let x = Int(((p.x - r.minX) / r.width * screenSize.width).rounded(.down))
        let y = Int(((p.y - r.minY) / r.height * screenSize.height).rounded(.down))
        return (min(max(x, 0), Int(screenSize.width) - 1), min(max(y, 0), Int(screenSize.height) - 1))
    }

    private func pointer(_ event: NSEvent) {
        guard canInput, let p = remote(event) else {
            return
        }
        position = p
        send(.pointer(x: p.x, y: p.y, buttons: buttons))
    }

    private func button(_ event: NSEvent) -> Buttons {
        switch event.buttonNumber {
        case 0: .left
        case 1: .right
        case 2: .middle
        case 3: .back
        case 4: .forward
        default: []
        }
    }

    public override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let tracking {
            removeTrackingArea(tracking)
        }
        let t = NSTrackingArea(rect: .zero, options: [.mouseMoved, .activeInKeyWindow, .inVisibleRect, .cursorUpdate], owner: self)
        addTrackingArea(t)
        tracking = t
    }

    public override func mouseMoved(with event: NSEvent) { pointer(event) }
    public override func mouseDragged(with event: NSEvent) { pointer(event) }
    public override func rightMouseDragged(with event: NSEvent) { pointer(event) }
    public override func otherMouseDragged(with event: NSEvent) { pointer(event) }

    public override func mouseDown(with event: NSEvent) {
        window?.makeFirstResponder(self)
        buttons.insert(button(event))
        pointer(event)
    }

    public override func mouseUp(with event: NSEvent) {
        buttons.remove(button(event))
        pointer(event)
    }

    public override func rightMouseDown(with event: NSEvent) { mouseDown(with: event) }
    public override func rightMouseUp(with event: NSEvent) { mouseUp(with: event) }
    public override func otherMouseDown(with event: NSEvent) { mouseDown(with: event) }
    public override func otherMouseUp(with event: NSEvent) { mouseUp(with: event) }

    public override func scrollWheel(with event: NSEvent) {
        guard canInput else {
            return
        }
        // A trackpad's or Magic Mouse's scrolling is precise, in points,
        // which go as pixels; a wheel's goes in lines. Natural scrolling
        // is in the deltas already; AppKit's positive dy scrolls up and
        // the protocol's down.
        let mode: WheelMode = event.hasPreciseScrollingDeltas ? .pixels : .lines
        if mode != scrollMode {
            scroll = .zero
            scrollMode = mode
        }
        scroll.x -= event.scrollingDeltaX
        scroll.y -= event.scrollingDeltaY
        let dx = Int(scroll.x.rounded(.towardZero))
        let dy = Int(scroll.y.rounded(.towardZero))
        guard dx != 0 || dy != 0 else {
            return
        }
        scroll.x -= CGFloat(dx)
        scroll.y -= CGFloat(dy)
        send(.wheel(dx: dx, dy: dy, mode: mode))
    }

    private static let commandKeys: Set<UInt16> = [0x36, 0x37]

    private func key(_ event: NSEvent) -> Key {
        let flags = event.modifierFlags
        return Keyboard.key(
            code: event.keyCode, characters: event.characters, unmodified: event.charactersIgnoringModifiers,
            control: flags.contains(.control) || flags.contains(.command))
    }

    /// pressCommand sends the Command key held, once a key goes with it.
    private func pressCommand() {
        guard var c = command, !c.sent else {
            return
        }
        c.sent = true
        command = c
        send(.key(down: true, c.key))
    }

    public override func keyDown(with event: NSEvent) {
        guard canInput else {
            return
        }
        pressCommand()
        let k = held[event.keyCode] ?? key(event)
        held[event.keyCode] = k
        if command != nil {
            withCommand.insert(event.keyCode)
        }
        send(.key(down: true, k))
    }

    public override func keyUp(with event: NSEvent) {
        guard canInput, let k = held.removeValue(forKey: event.keyCode) else {
            return
        }
        withCommand.remove(event.keyCode)
        send(.key(down: false, k))
    }

    public override func flagsChanged(with event: NSEvent) {
        guard canInput else {
            return
        }
        let code = event.keyCode
        let k = Keyboard.key(code: code, characters: nil, unmodified: nil, control: false)
        if code == Keyboard.capsLock {
            // AppKit reports Caps Lock's toggle; the remote toggles too.
            send(.key(down: true, k))
            send(.key(down: false, k))
            return
        }
        guard let down = Keyboard.modifier(code: code, flags: event.modifierFlags.rawValue) else {
            return
        }
        if Self.commandKeys.contains(code) {
            if down {
                command = (code, k, false, false)
                return
            }
            for c in withCommand {
                if let k = held.removeValue(forKey: c) {
                    send(.key(down: false, k))
                }
            }
            withCommand = []
            if let c = command, c.code == code {
                if !c.sent && !c.shortcut {
                    send(.key(down: true, c.key))
                }
                if c.sent || !c.shortcut {
                    send(.key(down: false, c.key))
                }
                command = nil
            }
            return
        }
        if down {
            held[code] = k
        } else {
            held[code] = nil
        }
        send(.key(down: down, k))
    }

    public override func performKeyEquivalent(with event: NSEvent) -> Bool {
        guard event.type == .keyDown, event.modifierFlags.contains(.command), window?.firstResponder === self, canInput else {
            return super.performKeyEquivalent(with: event)
        }
        if sendsShortcuts {
            keyDown(with: event)
            return true
        }
        // The app's menu may take it; if nothing does, keyDown sends it.
        command?.shortcut = true
        return super.performKeyEquivalent(with: event)
    }

    public override func resignFirstResponder() -> Bool {
        releaseAll()
        return super.resignFirstResponder()
    }

    /// releaseAll releases every key and button held, as the view loses
    /// the keyboard.
    public func releaseAll() {
        guard canInput else {
            held = [:]
            command = nil
            buttons = []
            return
        }
        for k in held.values {
            send(.key(down: false, k))
        }
        held = [:]
        withCommand = []
        if let c = command, c.sent {
            send(.key(down: false, c.key))
        }
        command = nil
        if !buttons.isEmpty {
            buttons = []
            send(.pointer(x: position.x, y: position.y, buttons: []))
        }
    }

    // MARK: - Pointer image

    public override func resetCursorRects() {
        let r = pictureFrame
        guard !r.isEmpty else {
            return
        }
        addCursorRect(r, cursor: currentCursor(points: r.width / screenSize.width))
    }

    private static let invisible = NSCursor(image: NSImage(size: NSSize(width: 1, height: 1)), hotSpot: .zero)

    /// currentCursor returns the remote's pointer image at points a pixel.
    private func currentCursor(points: CGFloat) -> NSCursor {
        guard let cursor else {
            return .arrow
        }
        if cursor.hidden {
            return Self.invisible
        }
        guard let image = cursor.image else {
            return .arrow
        }
        let ns = NSImage(cgImage: image, size: NSSize(width: CGFloat(cursor.width) * points, height: CGFloat(cursor.height) * points))
        return NSCursor(image: ns, hotSpot: NSPoint(x: CGFloat(cursor.hotX) * points, y: CGFloat(cursor.hotY) * points))
    }
}

/// FramebufferView shows a graphical session in SwiftUI.
public struct FramebufferView: NSViewRepresentable {
    public let model: SessionModel
    public var sendsShortcuts: Bool

    public init(model: SessionModel, sendsShortcuts: Bool = false) {
        self.model = model
        self.sendsShortcuts = sendsShortcuts
    }

    public func makeNSView(context: Context) -> FramebufferNSView {
        let v = FramebufferNSView(frame: .zero)
        v.model = model
        v.sendsShortcuts = sendsShortcuts
        return v
    }

    public func updateNSView(_ v: FramebufferNSView, context: Context) {
        v.sendsShortcuts = sendsShortcuts
        if v.model !== model {
            v.model = model
        }
    }

    public static func dismantleNSView(_ v: FramebufferNSView, coordinator: ()) {
        v.releaseAll()
    }
}
