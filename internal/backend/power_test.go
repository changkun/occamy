// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package backend

import "testing"

func TestPowerKey(t *testing.T) {
	for _, tt := range []struct {
		name string
		k    Key
		want bool
	}{
		{"Power by code", Key{Down: true, Code: HIDPower}, true},
		{"Power released", Key{Code: HIDPower}, true},
		{"Power with a keysym", Key{Keysym: 'p', Code: HIDPower}, true},
		{"XF86PowerOff", Key{Keysym: 0x1008ff2a}, true},
		{"XF86PowerDown", Key{Keysym: 0x1008ff21}, true},
		{"XF86Sleep", Key{Keysym: 0x1008ff2f}, true},
		{"XF86Suspend", Key{Keysym: 0x1008ffa7}, true},
		{"XF86Hibernate", Key{Keysym: 0x1008ffa8}, true},
		{"XF86Standby", Key{Keysym: 0x1008ff10}, true},
		{"XF86WakeUp", Key{Keysym: 0x1008ff2b}, false},
		{"XF86AudioMute", Key{Keysym: 0x1008ff12}, false},
		{"U+8FF2A", Key{Keysym: 0x0108ff2a}, false},
		{"Menu", Key{Code: 0x65}, false},
		{"International1", Key{Code: 0x87}, false},
		{"a", Key{Keysym: 'a', Code: 0x04}, false},
		{"nothing", Key{}, false},
	} {
		if got := PowerKey(tt.k); got != tt.want {
			t.Errorf("%s: PowerKey(%+v) = %v, want %v", tt.name, tt.k, got, tt.want)
		}
	}
}
