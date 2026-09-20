package agentcore

import (
	"context"
	"testing"
	"time"
)

func TestSendStreamEventUnblocksOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ch := make(chan StreamEvent, 1)
	ch <- NewEvent(nil, RoleOrchestrator, EvTextDelta, TextDelta{Text: "fill"})

	done := make(chan bool, 1)
	go func() {
		done <- sendStreamEvent(ctx, ch, NewEvent(nil, RoleOrchestrator, EvTextDelta, TextDelta{Text: "blocked"}))
	}()
	cancel()

	select {
	case sent := <-done:
		if sent {
			t.Fatal("event was sent after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked provider send ignored cancellation")
	}
}
