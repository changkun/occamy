// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import CoreGraphics
import Foundation

/// An Event is what a session's events deliver, as the Go core's
/// client.Event: in order, ending with end.
public enum Event: Sendable, Equatable {
    /// A frame closes: a graphical session's screen holds it, and its
    /// dirty rectangles say what changed; a terminal's data came just
    /// before it.
    case frame(Frame)
    /// The screen's new size, in pixels for a graphical session and in
    /// cells for a terminal. It starts a frame that redraws everything.
    case resize(width: Int, height: Int)
    /// Replaces the terminal: reset the emulator and write this to it.
    case termSnapshot(Data)
    /// Escape sequences for the terminal emulator.
    case termData(Data)
    /// The pointer's image, which the local pointer shows.
    case cursor(Cursor)
    /// The remote moved the pointer, in pixels.
    case cursorMove(x: Int, y: Int)
    /// The remote clipboard's new content.
    case clipboard(mime: String, data: Data)
    /// Whether the remote's clipboard takes the text sent for it.
    case clipboardStatus(answering: Bool)
    case bell
    /// The remote's desktop or window title.
    case title(String)
    /// The remote asks the owner for answers, such as a password.
    case prompt(Prompt)
    /// A viewer joined, or was there when this client joined.
    case viewerJoined(Viewer)
    case viewerLeft(id: UInt32)
    /// The session ended; it is the last event.
    case end(End)
}

public struct Frame: Sendable, Equatable {
    /// The frame's ID, from 1 on each connection.
    public var id: UInt32
    /// The last input of this client that the server applied before the
    /// frame, by its sequence number, or 0.
    public var lastSeq: UInt32
    /// When the server read the frame, since the session started.
    public var serverTime: Duration
    /// The rectangles a graphical frame changed, in pixels; empty for a
    /// terminal's.
    public var dirty: [CGRect]
}

/// Cursor is the pointer's image: RGBA with straight alpha, drawn with
/// its hot spot at the pointer. An empty one hides the pointer.
public struct Cursor: Sendable, Equatable {
    public var width: Int
    public var height: Int
    public var hotX: Int
    public var hotY: Int
    public var rgba: Data

    public var hidden: Bool { width == 0 || height == 0 }

    /// image returns the cursor's image, or nil if it is hidden.
    public var image: CGImage? {
        guard !hidden, rgba.count == width * height * 4,
              let provider = CGDataProvider(data: rgba as CFData),
              let space = CGColorSpace(name: CGColorSpace.sRGB)
        else {
            return nil
        }
        return CGImage(
            width: width, height: height, bitsPerComponent: 8, bitsPerPixel: 32,
            bytesPerRow: width * 4, space: space,
            bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.last.rawValue),
            provider: provider, decode: nil, shouldInterpolate: false, intent: .defaultIntent)
    }
}

public struct Prompt: Sendable, Equatable, Identifiable {
    public struct Field: Sendable, Equatable {
        public var label: String
        /// A password: not shown as typed.
        public var secret: Bool

        public init(label: String, secret: Bool) {
            self.label = label
            self.secret = secret
        }
    }

    public var id: UInt32
    public var name: String
    public var instruction: String
    public var fields: [Field]
}

/// A role, as protocol v1 numbers them.
public enum Role: Int32, Sendable {
    case owner = 1
    case viewer = 2
    case readOnly = 3

    /// What the role may do, as the web client's abilities: the owner
    /// alone resizes the remote, answers prompts and shares; a viewer
    /// gives input; a read-only viewer only watches.
    public var input: Bool { self != .readOnly }
    public var resize: Bool { self == .owner }
    public var prompts: Bool { self == .owner }
    public var manage: Bool { self == .owner }
}

/// A session's class, as protocol v1 numbers them.
public enum SessionClass: Int32, Sendable {
    case terminal = 1
    case graphical = 2
}

public struct Viewer: Sendable, Equatable, Identifiable {
    public var id: UInt32
    public var role: Role?
    public var name: String
}

/// End says why a session ended.
public struct End: Sendable, Equatable, Error {
    /// The server's close code, 1000 after close, or 0 if the connection
    /// failed without one.
    public var code: Int
    /// The code's reason, for people.
    public var reason: String
    /// The client's error that ended the session, if any.
    public var error: String?

    public init(code: Int, reason: String, error: String? = nil) {
        self.code = code
        self.reason = reason
        self.error = error
    }
}

/// Pointer buttons.
public struct Buttons: OptionSet, Sendable, Hashable {
    public let rawValue: UInt8
    public init(rawValue: UInt8) { self.rawValue = rawValue }

    public static let left = Buttons(rawValue: 1)
    public static let middle = Buttons(rawValue: 2)
    public static let right = Buttons(rawValue: 4)
    public static let back = Buttons(rawValue: 8)
    public static let forward = Buttons(rawValue: 16)
}

public enum WheelMode: Int32, Sendable {
    case pixels = 0
    case lines = 1
}

/// Input is what a client sends a session. Each takes the next sequence
/// number, which a frame's lastSeq reports once the server applied it.
public enum Input: Sendable, Equatable {
    /// A key press or release: the keysym it produced and the key's HID
    /// usage.
    case key(down: Bool, Key)
    /// Text an input method composed.
    case text(String)
    /// What the terminal emulator produced for a terminal session.
    case termInput(Data)
    /// The pointer on the remote screen, in pixels, and the buttons held.
    case pointer(x: Int, y: Int, buttons: Buttons)
    /// A scroll: positive dx scrolls right and positive dy down.
    case wheel(dx: Int, dy: Int, mode: WheelMode)
    /// Asks for a screen size: pixels for a graphical session, cells for
    /// a terminal.
    case resize(width: Int, height: Int)
    /// The local clipboard, for the remote's.
    case clipboard(mime: String, data: Data)
}
