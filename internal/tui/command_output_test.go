package tui

import (
	"sync"
	"testing"
)

func TestCommandOutputDrainIsThreadSafeAndDestructive(t *testing.T) {
	out := NewCommandOutput()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = out.Write([]byte("x"))
		}()
	}
	wg.Wait()
	if got := len(out.Drain()); got != 20 {
		t.Fatalf("captured bytes = %d, want 20", got)
	}
	if got := out.Drain(); got != "" {
		t.Fatalf("second drain = %q, want empty", got)
	}
}
