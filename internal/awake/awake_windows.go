// Package awake asks the system not to sleep while a run is going (SPEC
// section 9, DECISIONS.md D64).
package awake

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

var setThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// Hold asks Windows not to go to sleep by itself, and returns the function
// that lets it sleep again. It doesn't keep the display on, and it can't
// stop a sleep the owner asks for, such as closing a laptop's lid. Windows
// ties the request to a thread, so one goroutine, locked to its thread,
// makes it and later withdraws it.
func Hold() (release func()) {
	held, done := make(chan struct{}), make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, _, _ = setThreadExecutionState.Call(esContinuous | esSystemRequired)
		close(held)
		<-done
		_, _, _ = setThreadExecutionState.Call(esContinuous)
	}()
	<-held
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
