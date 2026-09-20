package tui

import "testing"

func TestStreamingAccumulatorsPreserveAndResetVisibleSnapshots(t *testing.T) {
	message := &Message{Text: "prefix "}
	message.appendText("one")
	message.appendText(" two")
	if message.Text != "prefix one two" {
		t.Fatalf("stream text = %q", message.Text)
	}
	message.Text = "replacement "
	message.appendText("three")
	if message.Text != "replacement three" {
		t.Fatalf("stream text after direct replacement = %q", message.Text)
	}

	thinking := &thinkingState{Detail: "reason "}
	thinking.appendDetail("one")
	thinking.appendDetail(" two")
	if thinking.Detail != "reason one two" {
		t.Fatalf("thinking detail = %q", thinking.Detail)
	}
	thinking.Detail = "replacement "
	thinking.appendDetail("three")
	if thinking.Detail != "replacement three" {
		t.Fatalf("thinking detail after direct replacement = %q", thinking.Detail)
	}
}

func TestStreamingAccumulatorAllocationsDoNotScalePerDelta(t *testing.T) {
	allocations := testing.AllocsPerRun(10, func() {
		message := &Message{}
		for i := 0; i < 4096; i++ {
			message.appendText("one streamed token ")
		}
		if len(message.Text) == 0 {
			t.Fatal("stream accumulator produced no text")
		}
	})
	if allocations > 64 {
		t.Fatalf("4096 streamed deltas allocated %.0f times; want bounded geometric growth", allocations)
	}
}
