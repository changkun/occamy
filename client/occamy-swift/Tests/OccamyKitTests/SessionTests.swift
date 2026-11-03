// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import Foundation
import Testing

@testable import OccamyKit

extension InteropTests {
    @Suite struct SessionTests {
        let interop: Interop

        init() throws {
            interop = try Interop.server()
        }

        func dial(_ route: String, _ options: Options = Options(name: "occamy-kit-test")) async throws -> Session {
            try await Session.dial(url: interop.stream(route), token: Interop.token, options: options, timeout: .seconds(10))
        }

        /// waitScreen waits for frames until the session's screen is the
        /// desktop's: its gradients go lossy, so close to it rather than
        /// equal.
        func waitScreen(_ s: Session, _ events: Recorder) async throws {
            while true {
                let want = try await interop.screen()
                let got = s.screen.rgba()
                if got.width == want.width && got.height == want.height && psnr(got.pixels, [UInt8](want.pixels)) >= 30 {
                    return
                }
                try await events.until("frame") { if case .frame = $0 { true } else { false } }
            }
        }

        @Test func graphical() async throws {
            let s = try await dial("/graphical", Options(name: "occamy-kit-test", pixelRatio: 2))
            let events = Recorder(s)
            defer { s.close() }
            #expect(s.viewerID != 0)
            #expect(s.role == .owner)
            #expect(s.sessionClass == .graphical)
            #expect(s.sessionID == "interop")

            // The first frame redraws the whole screen after its resize.
            guard case let .resize(w, h) = try await events.until("resize", { if case .resize = $0 { true } else { false } }) else {
                return
            }
            guard case let .frame(first) = try await events.until("frame", { if case .frame = $0 { true } else { false } }) else {
                return
            }
            #expect(first.dirty.contains(CGRect(x: 0, y: 0, width: w, height: h)))
            #expect(s.screen.size.width == w && s.screen.size.height == h)
            try await waitScreen(s, events)

            // A key shows in the swatch, and its frame reports its sequence
            // number once the server applied it; the swatch may go lossy,
            // within a few levels of its colour.
            let x = Key(keysym: 0x78, usage: 0x1b)
            let seq = try await s.submit(.key(down: true, x))
            #expect(seq != 0)
            try await events.until("the key's frame") { if case let .frame(f) = $0 { f.lastSeq >= seq } else { false } }
            let px = s.screen.rgba().pixels
            let swatch: [Int] = [0x78, 0x00, 0x40, 0xff] // backendtest.SwatchColor('x')
            #expect(zip(px[0..<4], swatch).allSatisfy { abs(Int($0) - $1) <= 4 }, "the swatch is \(px[0..<4])")
            s.send(.key(down: false, x))

            // Pointer, wheel and text reach the remote, in order.
            s.send(.pointer(x: 10, y: 20, buttons: [.left, .right]))
            s.send(.wheel(dx: 0, dy: 120, mode: .pixels))
            s.send(.text("title hello"))
            try await events.until("title") { if case .title("hello") = $0 { true } else { false } }
            s.send(.text("bell"))
            try await events.until("bell") { if case .bell = $0 { true } else { false } }
            let inputs = try await interop.waitInputs { $0.contains { $0.type == "text" && $0.text == "bell" } }
            #expect(inputs.contains(Interop.Input(type: "key", down: true, keysym: 0x78, code: 0x1b)))
            #expect(inputs.contains(Interop.Input(type: "key", keysym: 0x78, code: 0x1b)))
            #expect(inputs.contains(Interop.Input(type: "pointer", x: 10, y: 20, buttons: 5)))
            #expect(inputs.contains(Interop.Input(type: "wheel", dy: 120)))
            #expect(inputs.contains(Interop.Input(type: "text", text: "title hello")))

            // The desktop echoes the clipboard.
            s.send(.clipboard(mime: "text/plain", data: Data("über".utf8)))
            let cb = try await events.until("clipboard") { if case .clipboard = $0 { true } else { false } }
            #expect(cb == .clipboard(mime: "text/plain", data: Data("über".utf8)))

            // A resize request resizes the screen, and the screen follows.
            s.send(.resize(width: 400, height: 300))
            try await events.until("resize") { if case .resize(400, 300) = $0 { true } else { false } }
            try await events.until("frame") { if case .frame = $0 { true } else { false } }
            #expect(s.screen.size.width == 400 && s.screen.size.height == 300)
            try await waitScreen(s, events)

            // A prompt is answered once, by the owner.
            s.send(.text("prompt"))
            guard case let .prompt(p) = try await events.until("prompt", { if case .prompt = $0 { true } else { false } }) else {
                return
            }
            #expect(p.name == "interop")
            #expect(p.instruction == "Log in")
            #expect(p.fields == [Prompt.Field(label: "User", secret: false), Prompt.Field(label: "Password", secret: true)])
            try await s.reply(to: p.id, answers: ["ada", "s3cret"])
            try await events.until("answer") { if case .title("answered ada,s3cret") = $0 { true } else { false } }

            // A viewport off the screen is refused.
            s.view(visible: false)
            s.view(visible: true, viewport: CGRect(x: 0, y: 0, width: 200, height: 100))
            // Closed, the session delivers end, then finishes.
            s.close()
            guard case let .end(e) = try await events.until("end", { if case .end = $0 { true } else { false } }) else {
                return
            }
            #expect(e.code == 1000)
            await #expect(throws: SessionError.closed) { try await s.submit(.key(down: true, x)) }
        }

        @Test func remoteEnds() async throws {
            let s = try await dial("/graphical")
            let events = Recorder(s)
            try await events.until("frame") { if case .frame = $0 { true } else { false } }
            s.send(.text("end"))
            guard case let .end(e) = try await events.until("end", { if case .end = $0 { true } else { false } }) else {
                return
            }
            #expect(e.code == 4005)
            #expect(Ending(e).title == "Session ended")
            let deadline = ContinuousClock.now + .seconds(5)
            while !events.isFinished && ContinuousClock.now < deadline {
                try await Task.sleep(for: .milliseconds(10))
            }
            #expect(events.isFinished)
        }

        @Test func terminal() async throws {
            let s = try await dial("/terminal")
            let events = Recorder(s)
            defer { s.close() }
            #expect(s.sessionClass == .terminal)
            try await events.until("resize") { if case .resize(80, 24) = $0 { true } else { false } }
            try await events.until("snapshot") { if case .termSnapshot = $0 { true } else { false } }
            try await events.until("frame") { if case let .frame(f) = $0 { f.dirty.isEmpty } else { false } }
            let seq = try await s.submit(.termInput(Data("hi\r".utf8)))
            #expect(seq != 0)
            var echoed = Data()
            try await events.until("echo") {
                if case let .termData(d) = $0 {
                    echoed.append(d)
                }
                return echoed.range(of: Data("hi".utf8)) != nil
            }
            s.send(.resize(width: 100, height: 30))
            try await events.until("resize") { if case .resize(100, 30) = $0 { true } else { false } }
        }

        @Test func dialErrors() async throws {
            do {
                _ = try await Session.dial(url: interop.stream("/graphical"), token: "wrong", timeout: .seconds(10))
                Issue.record("dialled with a wrong token")
            } catch let e as DialError {
                #expect(e.closeCode == 4000)
                #expect(Ending(End(code: e.closeCode, reason: "", error: nil)).signIn)
            }
            do {
                _ = try await Session.dial(url: URL(string: "ws://127.0.0.1:1/graphical")!, token: "t", timeout: .seconds(10))
                Issue.record("dialled a closed port")
            } catch let e as DialError {
                #expect(e.closeCode == 0)
                #expect(e.message.contains("dial"))
            }
        }
    }
}
