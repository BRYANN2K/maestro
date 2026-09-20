package rlm

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPrimeKernelPersistenceAndHostBoundary(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip(err)
	}
	k := New(t.TempDir(), func(_ context.Context, data map[string]any) (any, error) {
		if data["type"] != "allowed" {
			return nil, errors.New("not authorized")
		}
		return map[string]any{"value": 42}, nil
	})
	defer k.Close()
	for _, cell := range []struct {
		code, want string
		fail       bool
	}{
		{"large_context = list(range(10000)); len(large_context)", "10000", false},
		{"sum(large_context[:10])", "45", false},
		{"await rlm.host_request('allowed')", "42", false},
		{"await rlm.spawn('bypass approved plan',name='bad')", "not authorized", true},
	} {
		output, err := k.Execute(context.Background(), cell.code)
		if cell.fail {
			if err == nil || !strings.Contains(err.Error(), cell.want) {
				t.Fatalf("error=%v output=%s", err, output)
			}
		} else if err != nil || !strings.Contains(output, cell.want) {
			t.Fatalf("output=%q err=%v", output, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := k.Execute(ctx, "while True: pass"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	output, err := k.Execute(context.Background(), "'large_context' in globals()")
	if err != nil || !strings.Contains(output, "False") {
		t.Fatalf("cancelled kernel survived: %s %v", output, err)
	}
}
