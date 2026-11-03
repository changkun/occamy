// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import Foundation
import Testing

@testable import OccamyKit

/// repository is the repository's root, which holds this package.
let repository = URL(filePath: #filePath)
    .deletingLastPathComponent() // OccamyKitTests
    .deletingLastPathComponent() // Tests
    .deletingLastPathComponent() // occamy-swift
    .deletingLastPathComponent() // client
    .deletingLastPathComponent()

/// InteropTests holds the tests that run against the interop server. Its
/// desktop keeps one record of input, of the last desktop started, so
/// they run one at a time.
@Suite(.serialized) enum InteropTests {}

struct InteropError: Error, CustomStringConvertible {
    var description: String
}

/// Interop runs client/internal/webinterop, the stream code of occamyd in
/// miniature, for the tests: /graphical serves a scripted desktop and
/// /terminal an echoing terminal, both to the token "interop". It is
/// built into a temporary directory once, and exits when this process
/// does, which closes its stdin.
final class Interop: Sendable {
    static let token = "interop"

    let url: URL
    private let process: Process
    private let stdin: Pipe

    private init() throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "occamy-kit-test-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let bin = dir.appending(path: "webinterop")
        let build = Process()
        build.executableURL = URL(filePath: "/usr/bin/env")
        build.arguments = ["go", "build", "-o", bin.path, "changkun.de/x/occamy/client/internal/webinterop"]
        build.currentDirectoryURL = repository
        try build.run()
        build.waitUntilExit()
        guard build.terminationStatus == 0 else {
            throw InteropError(description: "building the interop server failed: \(build.terminationStatus)")
        }
        process = Process()
        process.executableURL = bin
        stdin = Pipe()
        let stdout = Pipe()
        process.standardInput = stdin
        process.standardOutput = stdout
        try process.run()
        // The server prints its URL on its first line.
        var line = Data()
        let deadline = Date(timeIntervalSinceNow: 30)
        while !line.contains(UInt8(ascii: "\n")) {
            guard Date() < deadline, let b = try stdout.fileHandleForReading.read(upToCount: 1), !b.isEmpty else {
                throw InteropError(description: "the interop server printed no URL")
            }
            line.append(b)
        }
        guard let s = String(data: line, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
              let u = URL(string: s)
        else {
            throw InteropError(description: "the interop server printed \(line)")
        }
        url = u
        try? FileManager.default.removeItem(at: dir)
    }

    private static let shared = Result { try Interop() }

    static func server() throws -> Interop {
        try shared.get()
    }

    /// stream is the stream URL of a route.
    func stream(_ route: String) -> URL {
        var c = URLComponents(url: url, resolvingAgainstBaseURL: false)!
        c.scheme = "ws"
        c.path = route
        return c.url!
    }

    func json<T: Decodable>(_ route: String, as: T.Type) async throws -> T {
        let (data, _) = try await URLSession.shared.data(from: url.appending(path: route))
        return try JSONDecoder().decode(T.self, from: data)
    }

    struct Image: Decodable {
        var width: Int
        var height: Int
        var pixels: Data
    }

    /// An input as /inputs reports it.
    struct Input: Decodable, Equatable {
        var type: String
        var down: Bool?
        var keysym: UInt32?
        var code: UInt16?
        var x: Int?
        var y: Int?
        var buttons: UInt8?
        var dx: Int?
        var dy: Int?
        var mode: UInt8?
        var text: String?
        var mime: String?
        var data: String?
    }

    /// screen is the interop desktop's framebuffer.
    func screen() async throws -> Image {
        try await json("screen", as: Image.self)
    }

    /// inputs is the input the interop desktop got.
    func inputs() async throws -> [Input] {
        try await json("inputs", as: [Input].self)
    }

    /// waitInputs waits up to 5 s for the inputs to satisfy ok.
    func waitInputs(_ ok: @Sendable ([Input]) -> Bool) async throws -> [Input] {
        let deadline = ContinuousClock.now + .seconds(5)
        while true {
            let in_ = try await inputs()
            if ok(in_) || ContinuousClock.now > deadline {
                return in_
            }
            try await Task.sleep(for: .milliseconds(20))
        }
    }
}

/// Recorder collects a session's events, so that a test waits for one
/// with a deadline.
final class Recorder: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [Event] = []
    private var next = 0
    private var finished = false
    private var task: Task<Void, Never>?

    init(_ session: Session) {
        task = Task { [weak self] in
            for await ev in session.events {
                self?.lock.withLock { self?.events.append(ev) }
            }
            self?.lock.withLock { self?.finished = true }
        }
    }

    var isFinished: Bool { lock.withLock { finished } }

    /// until returns the next event for which ok holds, after the last
    /// one it returned, waiting up to 10 s; it throws at an end ok does
    /// not want.
    @discardableResult
    func until(_ what: String = "event", _ ok: (Event) -> Bool) async throws -> Event {
        let deadline = ContinuousClock.now + .seconds(10)
        while ContinuousClock.now < deadline {
            let found: Event? = lock.withLock {
                while next < events.count {
                    let ev = events[next]
                    next += 1
                    if ok(ev) {
                        return ev
                    }
                    if case .end = ev {
                        return ev
                    }
                }
                return nil
            }
            if let found {
                if case let .end(e) = found, !ok(found) {
                    throw InteropError(description: "the session ended waiting for \(what): \(e)")
                }
                return found
            }
            try await Task.sleep(for: .milliseconds(5))
        }
        throw InteropError(description: "no \(what) in 10 s")
    }
}

/// psnr returns the peak signal-to-noise ratio of b against a, of the
/// same size, in dB; infinity for equal ones.
func psnr(_ a: [UInt8], _ b: [UInt8]) -> Double {
    guard a.count == b.count, !a.isEmpty else {
        return 0
    }
    var sum = 0.0
    for i in a.indices where i % 4 != 3 {
        let d = Double(a[i]) - Double(b[i])
        sum += d * d
    }
    let mse = sum / Double(a.count / 4 * 3)
    return mse == 0 ? .infinity : 10 * log10(255 * 255 / mse)
}
