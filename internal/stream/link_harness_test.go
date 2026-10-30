// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// The link itself: a message takes its serialisation time plus the delay,
// and later messages queue behind it.
func TestLink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink(1e6, 20*time.Millisecond) // 1 MB/s
		defer l.close()
		ctx := context.Background()
		start := time.Now()
		for range 3 {
			if err := l.server().WriteMessage(ctx, make([]byte, 10_000)); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 3 {
			if _, err := l.client().ReadMessage(ctx); err != nil {
				t.Fatal(err)
			}
			want := time.Duration(i+1)*10*time.Millisecond + 20*time.Millisecond
			if got := time.Since(start); got != want {
				t.Errorf("message %d arrived after %v, want %v", i, got, want)
			}
		}
		start = time.Now()
		if err := l.client().WriteMessage(ctx, []byte("ack")); err != nil {
			t.Fatal(err)
		}
		if _, err := l.server().ReadMessage(ctx); err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 20*time.Millisecond {
			t.Errorf("client message took %v, want 20ms", got)
		}
	})
}
