package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestVetAndTestChecksInstallSeparateDeadlines(t *testing.T) {
	if maxReviewVetDuration <= 0 {
		t.Fatalf("go vet deadline = %s, want positive", maxReviewVetDuration)
	}
	if maxReviewTestDuration <= maxReviewVetDuration {
		t.Fatalf("go test deadline = %s, want longer than go vet deadline %s", maxReviewTestDuration, maxReviewVetDuration)
	}

	tests := []struct {
		name      string
		timeout   time.Duration
		run       func(context.Context, string, reviewCommandRunner) []ReviewItem
		wantArgs  []string
		wantItems []ReviewItem
	}{
		{
			name:     "vet",
			timeout:  maxReviewVetDuration,
			run:      vetCheckWithRunner,
			wantArgs: []string{"vet", "./..."},
		},
		{
			name:      "test",
			timeout:   maxReviewTestDuration,
			run:       testCheckWithRunner,
			wantArgs:  []string{"test", "./...", "-count=1"},
			wantItems: []ReviewItem{{Level: "pass", Message: "go test ./... passes"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotDeadline time.Time
			var gotArgs []string
			runner := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
				if dir != "workspace" {
					t.Fatalf("runner dir = %q, want workspace", dir)
				}
				if name != "go" {
					t.Fatalf("runner command = %q, want go", name)
				}
				var ok bool
				gotDeadline, ok = ctx.Deadline()
				if !ok {
					t.Fatal("runner context has no deadline")
				}
				gotArgs = append([]string(nil), args...)
				return nil, nil
			}

			before := time.Now()
			gotItems := tt.run(context.Background(), "workspace", runner)
			after := time.Now()
			if gotDeadline.Before(before.Add(tt.timeout)) || gotDeadline.After(after.Add(tt.timeout)) {
				t.Fatalf("deadline = %s, want between %s and %s", gotDeadline, before.Add(tt.timeout), after.Add(tt.timeout))
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("runner args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
			if !reflect.DeepEqual(gotItems, tt.wantItems) {
				t.Fatalf("items = %#v, want %#v", gotItems, tt.wantItems)
			}
		})
	}
}

func TestRunReviewCommandCheckFailureDiagnostics(t *testing.T) {
	tests := []struct {
		name        string
		ctx         func() context.Context
		timeout     time.Duration
		runner      reviewCommandRunner
		wantMessage string
	}{
		{
			name:    "own deadline",
			ctx:     context.Background,
			timeout: 0,
			runner: func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			wantMessage: "go test timed out after 0s",
		},
		{
			name: "parent cancellation",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			timeout: time.Hour,
			runner: func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			wantMessage: "go test cancelled: context canceled",
		},
		{
			name:    "command output",
			ctx:     context.Background,
			timeout: time.Hour,
			runner: func(context.Context, string, string, ...string) ([]byte, error) {
				return []byte("compile failed\n"), errors.New("exit status 1")
			},
			wantMessage: "go test: compile failed",
		},
		{
			name:    "empty command output",
			ctx:     context.Background,
			timeout: time.Hour,
			runner: func(context.Context, string, string, ...string) ([]byte, error) {
				return nil, errors.New("cannot start command")
			},
			wantMessage: "go test: cannot start command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, failed := runReviewCommandCheck(tt.ctx(), tt.timeout, "workspace", "go test", "go", tt.runner, "test", "./...")
			if !failed {
				t.Fatal("check unexpectedly passed")
			}
			if item.Level != "fail" || item.Message != tt.wantMessage {
				t.Fatalf("item = %#v, want fail %q", item, tt.wantMessage)
			}
		})
	}
}
