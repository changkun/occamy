// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The key message carries both what a key produced, an X11 keysym, and
// which key it was, a USB HID usage from the keyboard page (0x07)
// (docs/protocol.md, key), as the web client's keyboard.ts makes them.
// AppKit names a key by its virtual key code (NSEvent.keyCode, Carbon's
// kVK_ constants), the key's position, which this table maps to its
// usage: backends that want scancodes translate the usage
// (internal/keymap/keymap.go), so the remote's layout decides the
// character. The keysym comes from the key's text under the user's
// layout, and from the key code for keys without text.

/// A key as the key message sends it.
public struct Key: Equatable, Sendable {
    /// The X11 keysym the key produced, or 0 for one without, such as a
    /// dead key, which goes by its usage alone.
    public var keysym: UInt32
    /// The key's USB HID usage on the keyboard page, or 0 if it has none.
    public var usage: UInt16

    public init(keysym: UInt32, usage: UInt16) {
        self.keysym = keysym
        self.usage = usage
    }
}

/// Keyboard maps AppKit's keys to key messages. It is a pure function
/// of an event's parts, so that it is tested without NSEvent.
public enum Keyboard {
    /// The HID usage of each virtual key code, from Chromium's
    /// dom_code_data.inc, which pairs both with the KeyboardEvent.code the
    /// web client maps.
    public static let usages: [UInt16: UInt16] = [
        0x00: 0x04, // KeyA
        0x01: 0x16, // KeyS
        0x02: 0x07, // KeyD
        0x03: 0x09, // KeyF
        0x04: 0x0b, // KeyH
        0x05: 0x0a, // KeyG
        0x06: 0x1d, // KeyZ
        0x07: 0x1b, // KeyX
        0x08: 0x06, // KeyC
        0x09: 0x19, // KeyV
        0x0a: 0x64, // IntlBackslash, the ISO key left of Z
        0x0b: 0x05, // KeyB
        0x0c: 0x14, // KeyQ
        0x0d: 0x1a, // KeyW
        0x0e: 0x08, // KeyE
        0x0f: 0x15, // KeyR
        0x10: 0x1c, // KeyY
        0x11: 0x17, // KeyT
        0x12: 0x1e, // Digit1
        0x13: 0x1f, // Digit2
        0x14: 0x20, // Digit3
        0x15: 0x21, // Digit4
        0x16: 0x23, // Digit6
        0x17: 0x22, // Digit5
        0x18: 0x2e, // Equal
        0x19: 0x26, // Digit9
        0x1a: 0x24, // Digit7
        0x1b: 0x2d, // Minus
        0x1c: 0x25, // Digit8
        0x1d: 0x27, // Digit0
        0x1e: 0x30, // BracketRight
        0x1f: 0x12, // KeyO
        0x20: 0x18, // KeyU
        0x21: 0x2f, // BracketLeft
        0x22: 0x0c, // KeyI
        0x23: 0x13, // KeyP
        0x24: 0x28, // Enter
        0x25: 0x0f, // KeyL
        0x26: 0x0d, // KeyJ
        0x27: 0x34, // Quote
        0x28: 0x0e, // KeyK
        0x29: 0x33, // Semicolon
        0x2a: 0x31, // Backslash
        0x2b: 0x36, // Comma
        0x2c: 0x38, // Slash
        0x2d: 0x11, // KeyN
        0x2e: 0x10, // KeyM
        0x2f: 0x37, // Period
        0x30: 0x2b, // Tab
        0x31: 0x2c, // Space
        0x32: 0x35, // Backquote
        0x33: 0x2a, // Backspace, which the Mac calls Delete
        0x35: 0x29, // Escape
        0x36: 0xe7, // MetaRight, the right Command key
        0x37: 0xe3, // MetaLeft
        0x38: 0xe1, // ShiftLeft
        0x39: 0x39, // CapsLock
        0x3a: 0xe2, // AltLeft, the left Option key
        0x3b: 0xe0, // ControlLeft
        0x3c: 0xe5, // ShiftRight
        0x3d: 0xe6, // AltRight
        0x3e: 0xe4, // ControlRight
        0x40: 0x6c, // F17
        0x41: 0x63, // NumpadDecimal
        0x43: 0x55, // NumpadMultiply
        0x45: 0x57, // NumpadAdd
        0x47: 0x53, // NumLock, which the Mac calls Clear
        0x48: 0x80, // AudioVolumeUp
        0x49: 0x81, // AudioVolumeDown
        0x4a: 0x7f, // AudioVolumeMute
        0x4b: 0x54, // NumpadDivide
        0x4c: 0x58, // NumpadEnter
        0x4e: 0x56, // NumpadSubtract
        0x4f: 0x6d, // F18
        0x50: 0x6e, // F19
        0x51: 0x67, // NumpadEqual
        0x52: 0x62, // Numpad0
        0x53: 0x59, // Numpad1
        0x54: 0x5a, // Numpad2
        0x55: 0x5b, // Numpad3
        0x56: 0x5c, // Numpad4
        0x57: 0x5d, // Numpad5
        0x58: 0x5e, // Numpad6
        0x59: 0x5f, // Numpad7
        0x5a: 0x6f, // F20
        0x5b: 0x60, // Numpad8
        0x5c: 0x61, // Numpad9
        0x5d: 0x89, // IntlYen
        0x5e: 0x87, // IntlRo
        0x5f: 0x85, // NumpadComma
        0x60: 0x3e, // F5
        0x61: 0x3f, // F6
        0x62: 0x40, // F7
        0x63: 0x3c, // F3
        0x64: 0x41, // F8
        0x65: 0x42, // F9
        0x66: 0x91, // Lang2, the JIS Eisu key
        0x67: 0x44, // F11
        0x68: 0x90, // Lang1, the JIS Kana key
        0x69: 0x68, // F13
        0x6a: 0x6b, // F16
        0x6b: 0x69, // F14
        0x6d: 0x43, // F10
        0x6e: 0x65, // ContextMenu
        0x6f: 0x45, // F12
        0x71: 0x6a, // F15
        0x72: 0x49, // Insert, which the Mac calls Help
        0x73: 0x4a, // Home
        0x74: 0x4b, // PageUp
        0x75: 0x4c, // Delete, the forward delete
        0x76: 0x3d, // F4
        0x77: 0x4d, // End
        0x78: 0x3b, // F2
        0x79: 0x4e, // PageDown
        0x7a: 0x3a, // F1
        0x7b: 0x50, // ArrowLeft
        0x7c: 0x4f, // ArrowRight
        0x7d: 0x51, // ArrowDown
        0x7e: 0x52, // ArrowUp
    ]

    /// The keysyms of keys without text, by key code, as keyboard.ts
    /// gives them for the KeyboardEvent.key of the same key on a Mac.
    static let named: [UInt16: UInt32] = [
        0x24: 0xff0d, // Return
        0x30: 0xff09, // Tab
        0x33: 0xff08, // BackSpace
        0x35: 0xff1b, // Escape
        0x36: 0xffec, // Super_R: Command is the remote's Super key
        0x37: 0xffeb, // Super_L
        0x38: 0xffe1, // Shift_L
        0x39: 0xffe5, // Caps_Lock
        0x3a: 0xffe9, // Alt_L: Option is Alt
        0x3b: 0xffe3, // Control_L
        0x3c: 0xffe2, // Shift_R
        0x3d: 0xffea, // Alt_R
        0x3e: 0xffe4, // Control_R
        0x48: 0x1008ff13, // XF86AudioRaiseVolume
        0x49: 0x1008ff11, // XF86AudioLowerVolume
        0x4a: 0x1008ff12, // XF86AudioMute
        0x66: 0xff2f, // Eisu_toggle
        0x68: 0xff2d, // Kana_Lock
        0x6e: 0xff67, // Menu
        0x72: 0xff63, // Insert
        0x73: 0xff50, // Home
        0x74: 0xff55, // Prior
        0x75: 0xffff, // Delete
        0x77: 0xff57, // End
        0x79: 0xff56, // Next
        0x7b: 0xff51, // Left
        0x7c: 0xff53, // Right
        0x7d: 0xff54, // Down
        0x7e: 0xff52, // Up
    ]

    /// The keypad's keys, whose keysyms are KP_ ones.
    static let keypad: Set<UInt16> = [
        0x41, 0x43, 0x45, 0x47, 0x4b, 0x4c, 0x4e, 0x51,
        0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5b, 0x5c, 0x5f,
    ]

    /// The KP_ keysyms of the keypad's text.
    static let keypadText: [Character: UInt32] = [
        "=": 0xffbd, "*": 0xffaa, "+": 0xffab, ",": 0xffac,
        "-": 0xffad, ".": 0xffae, "/": 0xffaf,
    ]

    /// The device-dependent bits of NSEvent.modifierFlags that say which
    /// modifier key is down, by key code (IOKit's NX_DEVICE*KEYMASK).
    static let modifierBits: [UInt16: UInt] = [
        0x3b: 0x0001, // left Control
        0x38: 0x0002, // left Shift
        0x3c: 0x0004, // right Shift
        0x37: 0x0008, // left Command
        0x36: 0x0010, // right Command
        0x3a: 0x0020, // left Option
        0x3d: 0x0040, // right Option
        0x3e: 0x2000, // right Control
    ]

    /// The function keys F1 to F20 by key code; their keysyms are
    /// F1 (0xffbe) on.
    static let functionKeys: [UInt16: UInt32] = [
        0x7a: 1, 0x78: 2, 0x63: 3, 0x76: 4, 0x60: 5, 0x61: 6, 0x62: 7, 0x64: 8,
        0x65: 9, 0x6d: 10, 0x67: 11, 0x6f: 12, 0x69: 13, 0x6b: 14, 0x71: 15,
        0x6a: 16, 0x40: 17, 0x4f: 18, 0x50: 19, 0x5a: 20,
    ]

    /// The key code of Caps Lock, which toggles: AppKit reports its
    /// toggle, not its press and release.
    public static let capsLock: UInt16 = 0x39

    /// key returns the key message of a key down or up event. characters
    /// is what the key typed under the user's layout with every modifier
    /// applied (NSEvent.characters), and unmodified what it typed with
    /// Shift alone (NSEvent.charactersIgnoringModifiers). With Control or
    /// Command held, the key's text is the unmodified one, as a browser's
    /// KeyboardEvent.key is: Control-C is the keysym c, not ETX.
    public static func key(code: UInt16, characters: String?, unmodified: String?, control: Bool) -> Key {
        let usage = usages[code] ?? 0
        if let n = functionKeys[code] {
            return Key(keysym: 0xffbe + n - 1, usage: usage)
        }
        if keypad.contains(code) {
            switch code {
            case 0x4c: return Key(keysym: 0xff8d, usage: usage) // KP_Enter
            case 0x47: return Key(keysym: 0xff9d, usage: usage) // the Clear key, as keyboard.ts's keypad Clear
            default:
                // The keypad's text is the layout's: a decimal comma on
                // some.
                if let text = unmodified ?? characters, text.count == 1, let c = text.first {
                    if let d = c.wholeNumberValue, c.isASCII {
                        return Key(keysym: 0xffb0 + UInt32(d), usage: usage)
                    }
                    if let k = keypadText[c] {
                        return Key(keysym: k, usage: usage)
                    }
                }
            }
        }
        if let k = named[code] {
            return Key(keysym: k, usage: usage)
        }
        return Key(keysym: keysym(of: control ? unmodified : characters), usage: usage)
    }

    /// keysym returns the X11 keysym of a key's text: Latin-1 as it is,
    /// any other code point 0x01000000 above it, and 0 for text that is
    /// not one character, such as a dead key's empty text, or that is a
    /// control character or one of AppKit's function key characters.
    public static func keysym(of text: String?) -> UInt32 {
        guard let text, text.unicodeScalars.count == 1, let s = text.unicodeScalars.first else {
            return 0
        }
        let cp = s.value
        switch cp {
        case 0x20...0x7e, 0xa0...0xff:
            return cp
        case 0..<0x20, 0x7f..<0xa0:
            return 0 // control characters are not keys' text
        case 0xf700...0xf8ff:
            return 0 // NSEvent's function key characters, which are named keys
        default:
            return 0x0100_0000 + cp
        }
    }

    /// modifier says whether the modifier key of code is down in an
    /// NSEvent.modifierFlags of flags, for a flagsChanged event; nil for
    /// a key that is not a modifier with a side, such as Caps Lock and
    /// Fn.
    public static func modifier(code: UInt16, flags: UInt) -> Bool? {
        guard let bit = modifierBits[code] else {
            return nil
        }
        return flags & bit != 0
    }
}
