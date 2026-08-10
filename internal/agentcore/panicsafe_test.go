package agentcore

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeGoRecoversPanic(t *testing.T) {
	var mu sync.Mutex
	var got struct {
		name  string
		panic any
	}
	done := make(chan struct{})
	old := PanicHook
	PanicHook = func(name string, r any, stack []byte) {
		mu.Lock()
		got.name, got.panic = name, r
		mu.Unlock()
		close(done)
	}
	defer func() { PanicHook = old }()

	ran := make(chan struct{})
	safeGo("boom-run", func() {
		close(ran)
		panic("kaboom")
	})
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("fn never ran")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("panic not reported to hook")
	}
	mu.Lock()
	defer mu.Unlock()
	if got.name != "boom-run" {
		t.Errorf("name = %q", got.name)
	}
	if got.panic != "kaboom" {
		t.Errorf("panic = %v", got.panic)
	}
}

func TestSafeGoRunsNormal(t *testing.T) {
	done := make(chan struct{})
	safeGo("ok-run", func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fn did not run")
	}
}

func TestSafeGoStackContainsFn(t *testing.T) {
	var mu sync.Mutex
	var stack []byte
	done := make(chan struct{})
	old := PanicHook
	PanicHook = func(name string, r any, s []byte) {
		mu.Lock()
		stack = append([]byte(nil), s...)
		mu.Unlock()
		close(done)
	}
	defer func() { PanicHook = old }()

	safeGo("trace-run", func() { panic("x") })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("panic not reported")
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Contains(stack, []byte("TestSafeGoStackContainsFn")) {
		t.Errorf("stack missing caller: %s", stack)
	}
	if !strings.Contains(string(stack), "panicsafe") {
		t.Errorf("stack missing panicsafe frame: %s", stack)
	}
}
