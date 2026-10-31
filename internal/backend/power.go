// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package backend

// HIDPower is the USB HID usage of the Power key on the keyboard page.
// Backends drop it, with the keys of the power keysyms: the remote
// applies a power key to the whole machine, which may shut it down or
// put it to sleep for every user, beyond what a viewer's input to one
// session should reach. Sleep and Wake are usages of the Generic Desktop
// page, which keys do not carry.
const HIDPower = 0x66

// The X11 keysyms of keys that act on the machine's power state
// (XF86keysym.h), which a client may send for a key without a code.
// Text cannot type them: the keysyms of characters end at 0x0110ffff.
const (
	keysymStandby   = 0x1008ff10 // XF86Standby
	keysymPowerDown = 0x1008ff21 // XF86PowerDown
	keysymPowerOff  = 0x1008ff2a // XF86PowerOff
	keysymSleep     = 0x1008ff2f // XF86Sleep
	keysymSuspend   = 0x1008ffa7 // XF86Suspend
	keysymHibernate = 0x1008ffa8 // XF86Hibernate
)

// PowerKey reports whether backends drop k, pressed or released: the
// Power key by its code, or a key whose keysym acts on the machine's
// power state (XF86PowerOff, XF86PowerDown, XF86Sleep, XF86Suspend,
// XF86Hibernate or XF86Standby).
func PowerKey(k Key) bool {
	switch k.Keysym {
	case keysymStandby, keysymPowerDown, keysymPowerOff, keysymSleep, keysymSuspend, keysymHibernate:
		return true
	}
	return k.Code == HIDPower
}
