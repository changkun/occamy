// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The following code is modified from
// https://github.com/deluan/bring
// Authored by Deluan Quintao released under MIT license.

package guac

import "gioui.org/io/pointer"

// Mouse buttons recognized by guacd
type MouseButton int

const (
	MouseLeft MouseButton = 1 << iota
	MouseMiddle
	MouseRight
	MouseUp
	MouseDown
)

var MouseToGioButton = map[pointer.Buttons]MouseButton{
	pointer.ButtonPrimary:   MouseLeft,
	pointer.ButtonTertiary:  MouseMiddle,
	pointer.ButtonSecondary: MouseRight,
}

// Keys recognized by guacd. ASCII symbols from 32 to 126 do not need mapping.
type KeyCode int32

const (
	KeyAgain KeyCode = 1024 + iota
	KeyAllCandidates
	KeyAlphanumeric
	KeyLeftAlt
	KeyRightAlt
	KeyAttn
	KeyAltGraph
	KeyArrowDown
	KeyArrowLeft
	KeyArrowRight
	KeyArrowUp
	KeyBackspace
	KeyCapsLock
	KeyCancel
	KeyClear
	KeyConvert
	KeyCopy
	KeyCrsel
	KeyCrSel
	KeyCodeInput
	KeyCompose
	KeyLeftControl
	KeyRightControl
	KeyContextMenu
	KeyDelete
	KeyDown
	KeyEnd
	KeyEnter
	KeyEraseEof
	KeyEscape
	KeyExecute
	KeyExsel
	KeyExSel
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyF13
	KeyF14
	KeyF15
	KeyF16
	KeyF17
	KeyF18
	KeyF19
	KeyF20
	KeyF21
	KeyF22
	KeyF23
	KeyF24
	KeyFind
	KeyGroupFirst
	KeyGroupLast
	KeyGroupNext
	KeyGroupPrevious
	KeyFullWidth
	KeyHalfWidth
	KeyHangulMode
	KeyHankaku
	KeyHanjaMode
	KeyHelp
	KeyHiragana
	KeyHiraganaKatakana
	KeyHome
	KeyHyper
	KeyInsert
	KeyJapaneseHiragana
	KeyJapaneseKatakana
	KeyJapaneseRomaji
	KeyJunjaMode
	KeyKanaMode
	KeyKanjiMode
	KeyKatakana
	KeyLeft
	KeyMeta
	KeyModeChange
	KeyNumLock
	KeyPageDown
	KeyPageUp
	KeyPause
	KeyPlay
	KeyPreviousCandidate
	KeyPrintScreen
	KeyRedo
	KeyRight
	KeyRomanCharacters
	KeyScroll
	KeySelect
	KeySeparator
	KeyLeftShift
	KeyRightShift
	KeySingleCandidate
	KeySuper
	KeyTab
	KeyUIKeyInputDownArrow
	KeyUIKeyInputEscape
	KeyUIKeyInputLeftArrow
	KeyUIKeyInputRightArrow
	KeyUIKeyInputUpArrow
	KeyUp
	KeyUndo
	KeyWin
	KeyZenkaku
	KeyZenkakuHankaku
)

// KeyCodes mapped to X11 keysyms (used by guacd)
type keySym []int

var keySyms map[KeyCode]keySym

func init() {
	keySyms = make(map[KeyCode]keySym)
	keySyms[KeyAgain] = keySym{0xFF66}
	keySyms[KeyAllCandidates] = keySym{0xFF3D}
	keySyms[KeyAlphanumeric] = keySym{0xFF30}
	keySyms[KeyLeftAlt] = keySym{0xFFE9}
	keySyms[KeyRightAlt] = keySym{0xFFE9, 0xFE03}
	keySyms[KeyAttn] = keySym{0xFD0E}
	keySyms[KeyAltGraph] = keySym{0xFE03}
	keySyms[KeyArrowDown] = keySym{0xFF54}
	keySyms[KeyArrowLeft] = keySym{0xFF51}
	keySyms[KeyArrowRight] = keySym{0xFF53}
	keySyms[KeyArrowUp] = keySym{0xFF52}
	keySyms[KeyBackspace] = keySym{0xFF08}
	keySyms[KeyCapsLock] = keySym{0xFFE5}
	keySyms[KeyCancel] = keySym{0xFF69}
	keySyms[KeyClear] = keySym{0xFF0B}
	keySyms[KeyConvert] = keySym{0xFF21}
	keySyms[KeyCopy] = keySym{0xFD15}
	keySyms[KeyCrsel] = keySym{0xFD1C}
	keySyms[KeyCrSel] = keySym{0xFD1C}
	keySyms[KeyCodeInput] = keySym{0xFF37}
	keySyms[KeyCompose] = keySym{0xFF20}
	keySyms[KeyLeftControl] = keySym{0xFFE3}
	keySyms[KeyRightControl] = keySym{0xFFE3, 0xFFE4}
	keySyms[KeyContextMenu] = keySym{0xFF67}
	keySyms[KeyDelete] = keySym{0xFFFF}
	keySyms[KeyDown] = keySym{0xFF54}
	keySyms[KeyEnd] = keySym{0xFF57}
	keySyms[KeyEnter] = keySym{0xFF0D}
	keySyms[KeyEraseEof] = keySym{0xFD06}
	keySyms[KeyEscape] = keySym{0xFF1B}
	keySyms[KeyExecute] = keySym{0xFF62}
	keySyms[KeyExsel] = keySym{0xFD1D}
	keySyms[KeyExSel] = keySym{0xFD1D}
	keySyms[KeyF1] = keySym{0xFFBE}
	keySyms[KeyF2] = keySym{0xFFBF}
	keySyms[KeyF3] = keySym{0xFFC0}
	keySyms[KeyF4] = keySym{0xFFC1}
	keySyms[KeyF5] = keySym{0xFFC2}
	keySyms[KeyF6] = keySym{0xFFC3}
	keySyms[KeyF7] = keySym{0xFFC4}
	keySyms[KeyF8] = keySym{0xFFC5}
	keySyms[KeyF9] = keySym{0xFFC6}
	keySyms[KeyF10] = keySym{0xFFC7}
	keySyms[KeyF11] = keySym{0xFFC8}
	keySyms[KeyF12] = keySym{0xFFC9}
	keySyms[KeyF13] = keySym{0xFFCA}
	keySyms[KeyF14] = keySym{0xFFCB}
	keySyms[KeyF15] = keySym{0xFFCC}
	keySyms[KeyF16] = keySym{0xFFCD}
	keySyms[KeyF17] = keySym{0xFFCE}
	keySyms[KeyF18] = keySym{0xFFCF}
	keySyms[KeyF19] = keySym{0xFFD0}
	keySyms[KeyF20] = keySym{0xFFD1}
	keySyms[KeyF21] = keySym{0xFFD2}
	keySyms[KeyF22] = keySym{0xFFD3}
	keySyms[KeyF23] = keySym{0xFFD4}
	keySyms[KeyF24] = keySym{0xFFD5}
	keySyms[KeyFind] = keySym{0xFF68}
	keySyms[KeyGroupFirst] = keySym{0xFE0C}
	keySyms[KeyGroupLast] = keySym{0xFE0E}
	keySyms[KeyGroupNext] = keySym{0xFE08}
	keySyms[KeyGroupPrevious] = keySym{0xFE0A}
	keySyms[KeyFullWidth] = keySym(nil)
	keySyms[KeyHalfWidth] = keySym(nil)
	keySyms[KeyHangulMode] = keySym{0xFF31}
	keySyms[KeyHankaku] = keySym{0xFF29}
	keySyms[KeyHanjaMode] = keySym{0xFF34}
	keySyms[KeyHelp] = keySym{0xFF6A}
	keySyms[KeyHiragana] = keySym{0xFF25}
	keySyms[KeyHiraganaKatakana] = keySym{0xFF27}
	keySyms[KeyHome] = keySym{0xFF50}
	keySyms[KeyHyper] = keySym{0xFFED, 0xFFED, 0xFFEE}
	keySyms[KeyInsert] = keySym{0xFF63}
	keySyms[KeyJapaneseHiragana] = keySym{0xFF25}
	keySyms[KeyJapaneseKatakana] = keySym{0xFF26}
	keySyms[KeyJapaneseRomaji] = keySym{0xFF24}
	keySyms[KeyJunjaMode] = keySym{0xFF38}
	keySyms[KeyKanaMode] = keySym{0xFF2D}
	keySyms[KeyKanjiMode] = keySym{0xFF21}
	keySyms[KeyKatakana] = keySym{0xFF26}
	keySyms[KeyLeft] = keySym{0xFF51}
	keySyms[KeyMeta] = keySym{0xFFE7, 0xFFE7, 0xFFE8}
	keySyms[KeyModeChange] = keySym{0xFF7E}
	keySyms[KeyNumLock] = keySym{0xFF7F}
	keySyms[KeyPageDown] = keySym{0xFF56}
	keySyms[KeyPageUp] = keySym{0xFF55}
	keySyms[KeyPause] = keySym{0xFF13}
	keySyms[KeyPlay] = keySym{0xFD16}
	keySyms[KeyPreviousCandidate] = keySym{0xFF3E}
	keySyms[KeyPrintScreen] = keySym{0xFF61}
	keySyms[KeyRedo] = keySym{0xFF66}
	keySyms[KeyRight] = keySym{0xFF53}
	keySyms[KeyRomanCharacters] = keySym(nil)
	keySyms[KeyScroll] = keySym{0xFF14}
	keySyms[KeySelect] = keySym{0xFF60}
	keySyms[KeySeparator] = keySym{0xFFAC}
	keySyms[KeyLeftShift] = keySym{0xFFE1}
	keySyms[KeyRightShift] = keySym{0xFFE1, 0xFFE2}
	keySyms[KeySingleCandidate] = keySym{0xFF3C}
	keySyms[KeySuper] = keySym{0xFFEB, 0xFFEB, 0xFFEC}
	keySyms[KeyTab] = keySym{0xFF09}
	keySyms[KeyUIKeyInputDownArrow] = keySym{0xFF54}
	keySyms[KeyUIKeyInputEscape] = keySym{0xFF1B}
	keySyms[KeyUIKeyInputLeftArrow] = keySym{0xFF51}
	keySyms[KeyUIKeyInputRightArrow] = keySym{0xFF53}
	keySyms[KeyUIKeyInputUpArrow] = keySym{0xFF52}
	keySyms[KeyUp] = keySym{0xFF52}
	keySyms[KeyUndo] = keySym{0xFF65}
	keySyms[KeyWin] = keySym{0xFFEB}
	keySyms[KeyZenkaku] = keySym{0xFF28}
	keySyms[KeyZenkakuHankaku] = keySym{0xFF2}

	for ch := 32; ch < 127; ch++ {
		keySyms[KeyCode(ch)] = keySym{ch}
	}
}
