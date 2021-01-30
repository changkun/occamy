// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package mainthread

import (
	"runtime"
	"sync"
)

var funcQ = make(chan funcData, runtime.GOMAXPROCS(0))

func init() {
	runtime.LockOSThread()
}

type funcData struct {
	fn   func()
	done chan struct{}
}

// Init initializes the functionality for running arbitrary subsequent
// functions on a main system thread.
//
// Init must be called in the main package.
func Init(main func()) {
	done := donePool.Get().(chan struct{})
	defer donePool.Put(done)

	go func() {
		defer func() {
			done <- struct{}{}
		}()
		main()
	}()

	for {
		select {
		case f := <-funcQ:
			func() {
				defer func() {
					f.done <- struct{}{}
				}()
				f.fn()
			}()
		case <-done:
			return
		}
	}
}

// Call calls f on the main thread and blocks until f finishes.
func Call(f func()) {
	done := donePool.Get().(chan struct{})
	defer donePool.Put(done)

	funcQ <- funcData{fn: f, done: done}
	<-done
}

var donePool = sync.Pool{
	New: func() interface{} { return make(chan struct{}) },
}
