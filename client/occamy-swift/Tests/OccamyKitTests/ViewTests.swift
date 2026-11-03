// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import Carbon.HIToolbox
import Testing

@testable import OccamyKit

/// within waits up to 10 s for ok.
@MainActor
func within(_ what: String, _ ok: () async throws -> Bool) async throws {
    let deadline = ContinuousClock.now + .seconds(10)
    while try await !ok() {
        guard ContinuousClock.now < deadline else {
            throw InteropError(description: "no \(what) in 10 s")
        }
        try await Task.sleep(for: .milliseconds(10))
    }
}

extension InteropTests {
    /// The views run against the interop server as the app runs them: a
    /// SessionModel on the main actor with its display in a window, which
    /// stays off the screen.
    @MainActor
    @Suite struct ViewTests {
        let interop: Interop
        let pasteboard = NSPasteboard(name: NSPasteboard.Name("occamy-kit-test-\(UUID().uuidString)"))

        init() throws {
            interop = try Interop.server()
            _ = NSApplication.shared
        }

        func open(_ route: String) async throws -> (SessionModel, Task<Void, Never>) {
            let s = try await Session.dial(url: interop.stream(route), token: Interop.token,
                                           options: Options(name: "occamy-kit-test"), timeout: .seconds(10))
            let model = SessionModel(session: s, pasteboard: pasteboard)
            return (model, Task { await model.run() })
        }

        func window(_ view: NSView) -> NSWindow {
            let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 320, height: 200), styleMask: [.borderless],
                             backing: .buffered, defer: false)
            w.isReleasedWhenClosed = false
            w.contentView = view
            w.makeFirstResponder(view)
            return w
        }

        /// key makes a key event of code, at the view's window.
        func key(_ type: NSEvent.EventType, _ code: Int, _ text: String = "", flags: NSEvent.ModifierFlags = [],
                 in w: NSWindow) -> NSEvent {
            NSEvent.keyEvent(with: type, location: .zero, modifierFlags: flags, timestamp: 0, windowNumber: w.windowNumber,
                             context: nil, characters: text, charactersIgnoringModifiers: text, isARepeat: false,
                             keyCode: UInt16(code))!
        }

        /// flags makes a flagsChanged event of the modifier code with raw
        /// modifier flags.
        func flags(_ code: Int, _ raw: UInt64) -> NSEvent {
            let cg = CGEvent(keyboardEventSource: nil, virtualKey: CGKeyCode(code), keyDown: true)!
            cg.type = .flagsChanged
            cg.flags = CGEventFlags(rawValue: raw)
            return NSEvent(cgEvent: cg)!
        }

        @Test func framebuffer() async throws {
            let (model, run) = try await open("/graphical")
            defer { run.cancel() }
            let view = FramebufferNSView(frame: NSRect(x: 0, y: 0, width: 320, height: 200))
            view.model = model
            let w = window(view)
            defer { w.close() }
            #expect(model.session.isPaced)
            try await within("the first frame") { model.started }
            #expect(model.size == CGSize(width: 320, height: 200))
            #expect(model.title == "interop")
            view.layoutSubtreeIfNeeded()
            #expect(view.pictureFrame == view.bounds)

            // The view shows the desktop the right way up, as the remote has
            // it: the layer tree, rendered as the window shows it, is close to
            // the remote's screen, and so is AppKit's capture of the view,
            // which also resamples it to the screen's pixels and colours.
            // Upside down, either is under 10 dB.
            try await within("the desktop") {
                let want = [UInt8](try await interop.screen().pixels)
                return psnr(render(view), want) >= 30 && psnr(capture(view), want) >= 20
            }

            // The pointer, by the view's coordinates from its top left.
            let down = NSEvent.mouseEvent(with: .leftMouseDown, location: NSPoint(x: 10, y: 200 - 20), modifierFlags: [],
                                          timestamp: 0, windowNumber: w.windowNumber, context: nil, eventNumber: 0,
                                          clickCount: 1, pressure: 1)!
            view.mouseDown(with: down)
            view.mouseUp(with: down)
            // Keys, by position: an x, then Command-W, which the app takes,
            // then Command alone, which the remote gets.
            view.keyDown(with: key(.keyDown, kVK_ANSI_X, "x", in: w))
            view.keyUp(with: key(.keyUp, kVK_ANSI_X, "x", in: w))
            view.flagsChanged(with: flags(kVK_Command, 0x100108))
            _ = view.performKeyEquivalent(with: key(.keyDown, kVK_ANSI_W, "w", flags: .command, in: w))
            view.flagsChanged(with: flags(kVK_Command, 0x100))
            view.flagsChanged(with: flags(kVK_Command, 0x100108))
            view.flagsChanged(with: flags(kVK_Command, 0x100))
            model.session.send(.text("bell"))
            let inputs = try await interop.waitInputs { $0.contains { $0.text == "bell" } }
                .filter { $0.type == "key" || ($0.type == "pointer" && $0.buttons != nil) }
            #expect(inputs == [
                Interop.Input(type: "pointer", x: 10, y: 20, buttons: 1),
                Interop.Input(type: "key", down: true, keysym: 0x78, code: 0x1b),
                Interop.Input(type: "key", keysym: 0x78, code: 0x1b),
                Interop.Input(type: "key", down: true, keysym: 0xffeb, code: 0xe3),
                Interop.Input(type: "key", keysym: 0xffeb, code: 0xe3),
            ])
            try await within("the bell") { model.bells > 0 }

            // The clipboard goes both ways, and what came from the remote
            // does not go back.
            pasteboard.clearContents()
            pasteboard.setString("hallo", forType: .string)
            model.syncClipboard()
            try await within("the clipboard's echo") { pasteboard.string(forType: .string) == "hallo" && model.session.screen.size.width > 0 }
            model.session.send(.clipboard(mime: "text/plain", data: Data("über".utf8)))
            try await within("the remote's clipboard") { pasteboard.string(forType: .string) == "über" }
            model.syncClipboard()
            model.session.send(.text("bell"))
            try await within("the bell") { model.bells > 1 }
            let clips = try await interop.inputs().filter { $0.type == "clipboard" }.map(\.data)
            #expect(clips == ["hallo", "über"])

            // The owner answers prompts.
            model.session.send(.text("prompt"))
            try await within("the prompt") { !model.prompts.isEmpty }
            model.answer(model.prompts[0], with: ["ada", "s3cret"])
            #expect(model.prompts.isEmpty)
            try await within("the answer") { model.title == "answered ada,s3cret" }

            // A hidden window gets no frames, and is not paced.
            model.setVisible(false)
            #expect(!model.session.isPaced)
            model.setVisible(true)
            #expect(model.session.isPaced)
            try await within("the demands") {
                try await interop.json("log", as: [String].self).filter { $0 == "hidden" || $0 == "shown" }.suffix(2) == ["hidden", "shown"]
            }

            model.close()
            try await within("the end") { model.ended != nil }
            #expect(model.ended?.code == 1000)
            #expect(Ending(model.ended!).title == "Disconnected")
        }

        @Test func terminal() async throws {
            let (model, run) = try await open("/terminal")
            defer { run.cancel() }
            let display = TerminalDisplay(model: model)
            let w = window(display.view)
            defer { w.close() }
            try await within("the first frame") { model.started }
            #expect(model.size == CGSize(width: 80, height: 24))
            #expect(display.grid == (80, 24))
            // What the emulator produces goes to the remote, which echoes it.
            display.send(source: display.view, data: ArraySlice("hi\r".utf8))
            try await within("the echo") {
                String(data: display.view.getBufferAsData(), encoding: .utf8)?.contains("hi") == true
            }
            // The owner's grid follows the window, once it stays one size.
            w.setContentSize(NSSize(width: 480, height: 300))
            display.view.layoutSubtreeIfNeeded()
            let grid = display.grid
            #expect(grid != (80, 24))
            try await within("the remote's grid") { model.size == CGSize(width: grid.cols, height: grid.rows) }
            model.close()
            try await within("the end") { model.ended != nil }
        }
    }
}

/// render returns the view's layer tree as the window shows it, RGBA
/// with its rows from the top: a flipped view's layer has its geometry
/// flipped, which the window's context does too.
@MainActor
func render(_ view: NSView) -> [UInt8] {
    let w = Int(view.bounds.width)
    let h = Int(view.bounds.height)
    var px = [UInt8](repeating: 0, count: w * h * 4)
    px.withUnsafeMutableBytes { buf in
        let ctx = CGContext(data: buf.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                            space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        if view.isFlipped {
            ctx.translateBy(x: 0, y: CGFloat(h))
            ctx.scaleBy(x: 1, y: -1)
        }
        view.layer!.render(in: ctx)
    }
    return px
}

/// capture returns AppKit's capture of the view, RGBA with its rows from
/// the top.
@MainActor
func capture(_ view: NSView) -> [UInt8] {
    let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds)!
    view.cacheDisplay(in: view.bounds, to: rep)
    let w = Int(view.bounds.width)
    let h = Int(view.bounds.height)
    var px = [UInt8](repeating: 0, count: w * h * 4)
    px.withUnsafeMutableBytes { buf in
        let ctx = CGContext(data: buf.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                            space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        ctx.draw(rep.cgImage!, in: CGRect(x: 0, y: 0, width: w, height: h))
    }
    return px
}
