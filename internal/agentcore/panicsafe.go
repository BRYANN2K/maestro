package agentcore

import (
	"fmt"
	"os"
	"runtime/debug"
)

// PanicHook is a test hook invoked when safeGo recovers a panic.
var PanicHook func(name string, r any, stack []byte)

// safeGo runs fn on a goroutine with panic containment: a panic is recovered,
// logged to stderr, and optionally reported to PanicHook (tests). The program
// never dies silently from a stray panic at a goroutine boundary.
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				if PanicHook != nil {
					PanicHook(name, r, stack)
				}
				fmt.Fprintf(os.Stderr, "maestro: panic in %s: %v\n%s", name, r, stack)
			}
		}()
		fn()
	}()
}
