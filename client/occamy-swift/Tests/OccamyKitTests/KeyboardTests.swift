// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import Carbon.HIToolbox
import Foundation
import Testing

@testable import OccamyKit

/// The HID-to-XT table that the VNC and RDP backends send keys by,
/// internal/keymap's set1, read from its source: each entry is
/// `0xUU: 0xXX, // Name`, where Name is KeyboardEvent.code's name of the
/// key, or a description of it.
private let set1: [UInt16: String] = {
    let source = (try? String(contentsOf: repository.appending(path: "internal/keymap/keymap.go"), encoding: .utf8)) ?? ""
    guard let table = source.firstMatch(of: /var set1 = \[0xe8\]uint16\{\n([^}]*)\n\}/)?.1 else {
        return [:]
    }
    var out: [UInt16: String] = [:]
    for m in table.matches(of: /\t0x([0-9a-f]{2}): 0x[0-9a-f]{2,4}, +\/\/ (\w+)/) {
        out[UInt16(m.1, radix: 16)!] = String(m.2)
    }
    return out
}()

@Suite struct KeyboardTests {
    @Test func readsTheBackendsTable() {
        #expect(set1.count >= 148)
    }

    // Each virtual key code, by Carbon's name, and the key the backends'
    // table names its usage: the table maps every key a Mac keyboard has
    // to one the backends send.
    static let names: [(Int, String)] = [
        (kVK_ANSI_A, "KeyA"), (kVK_ANSI_S, "KeyS"), (kVK_ANSI_D, "KeyD"), (kVK_ANSI_F, "KeyF"),
        (kVK_ANSI_H, "KeyH"), (kVK_ANSI_G, "KeyG"), (kVK_ANSI_Z, "KeyZ"), (kVK_ANSI_X, "KeyX"),
        (kVK_ANSI_C, "KeyC"), (kVK_ANSI_V, "KeyV"), (kVK_ISO_Section, "IntlBackslash"), (kVK_ANSI_B, "KeyB"),
        (kVK_ANSI_Q, "KeyQ"), (kVK_ANSI_W, "KeyW"), (kVK_ANSI_E, "KeyE"), (kVK_ANSI_R, "KeyR"),
        (kVK_ANSI_Y, "KeyY"), (kVK_ANSI_T, "KeyT"), (kVK_ANSI_1, "Digit1"), (kVK_ANSI_2, "Digit2"),
        (kVK_ANSI_3, "Digit3"), (kVK_ANSI_4, "Digit4"), (kVK_ANSI_6, "Digit6"), (kVK_ANSI_5, "Digit5"),
        (kVK_ANSI_Equal, "Equal"), (kVK_ANSI_9, "Digit9"), (kVK_ANSI_7, "Digit7"), (kVK_ANSI_Minus, "Minus"),
        (kVK_ANSI_8, "Digit8"), (kVK_ANSI_0, "Digit0"), (kVK_ANSI_RightBracket, "BracketRight"),
        (kVK_ANSI_O, "KeyO"), (kVK_ANSI_U, "KeyU"), (kVK_ANSI_LeftBracket, "BracketLeft"), (kVK_ANSI_I, "KeyI"),
        (kVK_ANSI_P, "KeyP"), (kVK_Return, "Enter"), (kVK_ANSI_L, "KeyL"), (kVK_ANSI_J, "KeyJ"),
        (kVK_ANSI_Quote, "Quote"), (kVK_ANSI_K, "KeyK"), (kVK_ANSI_Semicolon, "Semicolon"),
        (kVK_ANSI_Backslash, "Backslash"), (kVK_ANSI_Comma, "Comma"), (kVK_ANSI_Slash, "Slash"),
        (kVK_ANSI_N, "KeyN"), (kVK_ANSI_M, "KeyM"), (kVK_ANSI_Period, "Period"), (kVK_Tab, "Tab"),
        (kVK_Space, "Space"), (kVK_ANSI_Grave, "Backquote"), (kVK_Delete, "Backspace"), (kVK_Escape, "Escape"),
        (kVK_RightCommand, "MetaRight"), (kVK_Command, "MetaLeft"), (kVK_Shift, "ShiftLeft"),
        (kVK_CapsLock, "CapsLock"), (kVK_Option, "AltLeft"), (kVK_Control, "ControlLeft"),
        (kVK_RightShift, "ShiftRight"), (kVK_RightOption, "AltRight"), (kVK_RightControl, "ControlRight"),
        (kVK_F17, "F17"), (kVK_ANSI_KeypadDecimal, "NumpadDecimal"), (kVK_ANSI_KeypadMultiply, "NumpadMultiply"),
        (kVK_ANSI_KeypadPlus, "NumpadAdd"), (kVK_ANSI_KeypadClear, "NumLock"), (kVK_VolumeUp, "AudioVolumeUp"),
        (kVK_VolumeDown, "AudioVolumeDown"), (kVK_Mute, "AudioVolumeMute"), (kVK_ANSI_KeypadDivide, "NumpadDivide"),
        (kVK_ANSI_KeypadEnter, "NumpadEnter"), (kVK_ANSI_KeypadMinus, "NumpadSubtract"), (kVK_F18, "F18"),
        (kVK_F19, "F19"), (kVK_ANSI_KeypadEquals, "NumpadEqual"), (kVK_ANSI_Keypad0, "Numpad0"),
        (kVK_ANSI_Keypad1, "Numpad1"), (kVK_ANSI_Keypad2, "Numpad2"), (kVK_ANSI_Keypad3, "Numpad3"),
        (kVK_ANSI_Keypad4, "Numpad4"), (kVK_ANSI_Keypad5, "Numpad5"), (kVK_ANSI_Keypad6, "Numpad6"),
        (kVK_ANSI_Keypad7, "Numpad7"), (kVK_F20, "F20"), (kVK_ANSI_Keypad8, "Numpad8"),
        (kVK_ANSI_Keypad9, "Numpad9"), (kVK_JIS_Yen, "IntlYen"), (kVK_JIS_Underscore, "IntlRo"),
        (kVK_JIS_KeypadComma, "NumpadComma"), (kVK_F5, "F5"), (kVK_F6, "F6"), (kVK_F7, "F7"), (kVK_F3, "F3"),
        (kVK_F8, "F8"), (kVK_F9, "F9"), (kVK_JIS_Eisu, "Lang2"), (kVK_F11, "F11"), (kVK_JIS_Kana, "Lang1"),
        (kVK_F13, "F13"), (kVK_F16, "F16"), (kVK_F14, "F14"), (kVK_F10, "F10"), (kVK_ContextualMenu, "ContextMenu"),
        (kVK_F12, "F12"), (kVK_F15, "F15"), (kVK_Help, "Insert"), (kVK_Home, "Home"), (kVK_PageUp, "PageUp"),
        (kVK_ForwardDelete, "Delete"), (kVK_F4, "F4"), (kVK_End, "End"), (kVK_F2, "F2"), (kVK_PageDown, "PageDown"),
        (kVK_F1, "F1"), (kVK_LeftArrow, "ArrowLeft"), (kVK_RightArrow, "ArrowRight"), (kVK_DownArrow, "ArrowDown"),
        (kVK_UpArrow, "ArrowUp"),
    ]

    @Test(arguments: names)
    func usage(code: Int, name: String) throws {
        let usage = try #require(Keyboard.usages[UInt16(code)], "no usage for \(name)")
        #expect(set1[usage] == name)
    }

    @Test func mapsEveryKeyOnce() {
        #expect(Keyboard.usages.count == 119)
        #expect(Set(Keyboard.usages.values).count == Keyboard.usages.count)
        // Fn has no usage: the keyboard handles it.
        #expect(Keyboard.usages[UInt16(kVK_Function)] == nil)
    }

    struct Case: Sendable, CustomTestStringConvertible {
        var code: Int
        var characters: String?
        var unmodified: String?
        var control = false
        var keysym: UInt32
        var usage: UInt16
        var testDescription: String { "\(code) \(characters.debugDescription)" }
    }

    @Test(arguments: [
        Case(code: kVK_ANSI_A, characters: "a", unmodified: "a", keysym: 0x61, usage: 0x04),
        Case(code: kVK_ANSI_A, characters: "A", unmodified: "A", keysym: 0x41, usage: 0x04),
        // An AZERTY a is the key QWERTY calls KeyQ.
        Case(code: kVK_ANSI_Q, characters: "a", unmodified: "a", keysym: 0x61, usage: 0x14),
        // Control-C is the keysym c, not ETX; so is Command-C.
        Case(code: kVK_ANSI_C, characters: "\u{3}", unmodified: "c", control: true, keysym: 0x63, usage: 0x06),
        Case(code: kVK_ANSI_C, characters: "c", unmodified: "c", control: true, keysym: 0x63, usage: 0x06),
        // Option composes: Option-O is ø; Option-E is a dead key, with no
        // text, which goes by its usage alone.
        Case(code: kVK_ANSI_O, characters: "ø", unmodified: "o", keysym: 0xf8, usage: 0x12),
        Case(code: kVK_ANSI_E, characters: "", unmodified: "e", keysym: 0, usage: 0x08),
        Case(code: kVK_ANSI_E, characters: "€", unmodified: "e", keysym: 0x0100_20ac, usage: 0x08),
        Case(code: kVK_ANSI_Grave, characters: "😀", unmodified: "`", keysym: 0x0101_f600, usage: 0x35),
        Case(code: kVK_Space, characters: " ", unmodified: " ", keysym: 0x20, usage: 0x2c),
        Case(code: kVK_ANSI_1, characters: "1", unmodified: "1", keysym: 0x31, usage: 0x1e),
        // Keys without text, whose characters AppKit gives as control or
        // function key characters.
        Case(code: kVK_Return, characters: "\r", unmodified: "\r", keysym: 0xff0d, usage: 0x28),
        Case(code: kVK_Tab, characters: "\t", unmodified: "\t", keysym: 0xff09, usage: 0x2b),
        Case(code: kVK_Delete, characters: "\u{7f}", unmodified: "\u{7f}", keysym: 0xff08, usage: 0x2a),
        Case(code: kVK_Escape, characters: "\u{1b}", unmodified: "\u{1b}", keysym: 0xff1b, usage: 0x29),
        Case(code: kVK_ForwardDelete, characters: "\u{f728}", unmodified: "\u{f728}", keysym: 0xffff, usage: 0x4c),
        Case(code: kVK_UpArrow, characters: "\u{f700}", unmodified: "\u{f700}", keysym: 0xff52, usage: 0x52),
        Case(code: kVK_LeftArrow, characters: "\u{f702}", unmodified: "\u{f702}", keysym: 0xff51, usage: 0x50),
        Case(code: kVK_PageDown, characters: "\u{f72d}", unmodified: "\u{f72d}", keysym: 0xff56, usage: 0x4e),
        Case(code: kVK_Help, characters: "\u{f746}", unmodified: "\u{f746}", keysym: 0xff63, usage: 0x49),
        Case(code: kVK_F1, characters: "\u{f704}", unmodified: "\u{f704}", keysym: 0xffbe, usage: 0x3a),
        Case(code: kVK_F12, characters: "\u{f70f}", unmodified: "\u{f70f}", keysym: 0xffc9, usage: 0x45),
        Case(code: kVK_F20, characters: "\u{f717}", unmodified: "\u{f717}", keysym: 0xffd1, usage: 0x6f),
        Case(code: kVK_ContextualMenu, characters: "\u{10}", unmodified: "\u{10}", keysym: 0xff67, usage: 0x65),
        // Modifiers, which flagsChanged reports without text.
        Case(code: kVK_Shift, keysym: 0xffe1, usage: 0xe1),
        Case(code: kVK_RightShift, keysym: 0xffe2, usage: 0xe5),
        Case(code: kVK_Control, keysym: 0xffe3, usage: 0xe0),
        Case(code: kVK_RightControl, keysym: 0xffe4, usage: 0xe4),
        Case(code: kVK_Option, keysym: 0xffe9, usage: 0xe2),
        Case(code: kVK_RightOption, keysym: 0xffea, usage: 0xe6),
        Case(code: kVK_Command, keysym: 0xffeb, usage: 0xe3),
        Case(code: kVK_RightCommand, keysym: 0xffec, usage: 0xe7),
        Case(code: kVK_CapsLock, keysym: 0xffe5, usage: 0x39),
        // The keypad's keys have KP_ keysyms, by the layout's text.
        Case(code: kVK_ANSI_Keypad1, characters: "1", unmodified: "1", keysym: 0xffb1, usage: 0x59),
        Case(code: kVK_ANSI_Keypad0, characters: "0", unmodified: "0", keysym: 0xffb0, usage: 0x62),
        Case(code: kVK_ANSI_KeypadEnter, characters: "\u{3}", unmodified: "\u{3}", keysym: 0xff8d, usage: 0x58),
        Case(code: kVK_ANSI_KeypadPlus, characters: "+", unmodified: "+", keysym: 0xffab, usage: 0x57),
        Case(code: kVK_ANSI_KeypadDecimal, characters: ".", unmodified: ".", keysym: 0xffae, usage: 0x63),
        Case(code: kVK_ANSI_KeypadDecimal, characters: ",", unmodified: ",", keysym: 0xffac, usage: 0x63),
        Case(code: kVK_ANSI_KeypadEquals, characters: "=", unmodified: "=", keysym: 0xffbd, usage: 0x67),
        Case(code: kVK_ANSI_KeypadClear, characters: "\u{f739}", unmodified: "\u{f739}", keysym: 0xff9d, usage: 0x53),
        // A key no table knows has neither.
        Case(code: 0x7f, characters: nil, unmodified: nil, keysym: 0, usage: 0),
    ])
    func key(_ c: Case) {
        let k = Keyboard.key(code: UInt16(c.code), characters: c.characters, unmodified: c.unmodified, control: c.control)
        #expect(k == Key(keysym: c.keysym, usage: c.usage))
    }

    @Test func keysyms() {
        #expect(Keyboard.keysym(of: "a") == 0x61)
        #expect(Keyboard.keysym(of: "ÿ") == 0xff)
        #expect(Keyboard.keysym(of: "я") == 0x0100_044f)
        #expect(Keyboard.keysym(of: "ab") == 0)
        #expect(Keyboard.keysym(of: "") == 0)
        #expect(Keyboard.keysym(of: nil) == 0)
        #expect(Keyboard.keysym(of: "\u{0}") == 0)
        #expect(Keyboard.keysym(of: "\u{85}") == 0)
        // A character with a combining mark is two code points.
        #expect(Keyboard.keysym(of: "e\u{301}") == 0)
    }

    @Test func modifiers() {
        // NSEvent.modifierFlags: Shift (1 << 17) with the left Shift's
        // device bit, then the right's.
        #expect(Keyboard.modifier(code: UInt16(kVK_Shift), flags: 0x20102) == true)
        #expect(Keyboard.modifier(code: UInt16(kVK_RightShift), flags: 0x20102) == false)
        #expect(Keyboard.modifier(code: UInt16(kVK_RightShift), flags: 0x20104) == true)
        #expect(Keyboard.modifier(code: UInt16(kVK_Command), flags: 0x100) == false)
        #expect(Keyboard.modifier(code: UInt16(kVK_RightCommand), flags: 0x100110) == true)
        #expect(Keyboard.modifier(code: UInt16(kVK_RightControl), flags: 0x42100) == true)
        #expect(Keyboard.modifier(code: UInt16(kVK_RightOption), flags: 0x80140) == true)
        // Caps Lock toggles, and Fn is the keyboard's.
        #expect(Keyboard.modifier(code: UInt16(kVK_CapsLock), flags: 0x10100) == nil)
        #expect(Keyboard.modifier(code: UInt16(kVK_Function), flags: 0x800100) == nil)
    }
}
