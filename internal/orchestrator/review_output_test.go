package orchestrator

import (
	"bytes"
	"strings"
	"testing"
)

func TestBoundedReviewOutputDrainsAndCapsNoisyCommand(t *testing.T) {
	w := &boundedReviewOutput{}
	input := []byte(strings.Repeat("x", maxReviewCommandOutputBytes*4))
	n, err := w.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("write = %d, %v; want %d, nil", n, err, len(input))
	}
	out := w.Bytes()
	if len(out) > maxReviewCommandOutputBytes+64 {
		t.Fatalf("bounded output retained %d bytes", len(out))
	}
	if !bytes.Contains(out, []byte("command output truncated")) {
		t.Fatalf("bounded output omitted truncation marker: %q", out[len(out)-64:])
	}
}
