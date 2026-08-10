package orchestrator

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchPreservesMultiwordPositionalText(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "add an API"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{
			name: "edit positional text",
			cmd:  Command{Cmd: "edit", Args: []string{"keep", "the", "public", "API"}},
			want: "keep the public API",
		},
		{
			name: "edit message flag",
			cmd:  Command{Cmd: "edit", Flags: map[string]string{"m": "retain compatibility"}},
			want: "retain compatibility",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := orch.Dispatch(ctx, tt.cmd); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(orch.sess.Draft.Body, tt.want) {
				t.Fatalf("draft body %q does not contain %q", orch.sess.Draft.Body, tt.want)
			}
		})
	}
}

func TestFlagAndArgsKeepsStructuralPositionals(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		from int
		want string
	}{
		{
			name: "answer skips question id",
			cmd:  Command{Args: []string{"Q-001", "and", "document it"}, Flags: map[string]string{"m": "yes"}},
			from: 1,
			want: "yes and document it",
		},
		{
			name: "message plus loose words",
			cmd:  Command{Args: []string{"auth"}, Flags: map[string]string{"m": "add"}},
			want: "add auth",
		},
		{
			name: "positionals only",
			cmd:  Command{Args: []string{"keep", "compatibility"}},
			want: "keep compatibility",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flagAndArgs(tt.cmd, "m", tt.from); got != tt.want {
				t.Fatalf("flagAndArgs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDispatchRenameAndResumeExactSession(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	out := orch.out.(*bytes.Buffer)

	if err := orch.Dispatch(ctx, Command{Cmd: "rename", Args: []string{"Security", "review"}}); err != nil {
		t.Fatal(err)
	}
	if orch.sess.Title != "Security review" || !strings.Contains(out.String(), "session renamed: Security review") {
		t.Fatalf("renamed session = %#v, output %q", orch.sess, out.String())
	}

	id := orch.sess.ID
	out.Reset()
	if err := orch.Dispatch(ctx, Command{Cmd: "resume", Args: []string{id}}); err != nil {
		t.Fatal(err)
	}
	if orch.sess.ID != id || !strings.Contains(out.String(), "Session "+id) {
		t.Fatalf("resume selected %q, output %q", orch.sess.ID, out.String())
	}
}

func TestDispatchCoachLifecycle(t *testing.T) {
	t.Setenv("MAESTRO_LEARN_DIR", filepath.Join(t.TempDir(), "coach"))
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	ctx := context.Background()
	out := orch.out.(*bytes.Buffer)

	if err := orch.Dispatch(ctx, Command{Cmd: "learn", Args: []string{"guided"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "coach: mode guided") {
		t.Fatalf("mode output = %q", out.String())
	}

	out.Reset()
	if err := orch.Dispatch(ctx, Command{Cmd: "learn", Args: []string{"next"}}); err != nil {
		t.Fatal(err)
	}
	state, err := orch.CoachState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingLessonID == "" || !strings.Contains(out.String(), "coach lesson "+state.PendingLessonID) {
		t.Fatalf("pending = %#v, output %q", state, out.String())
	}
	if !strings.HasPrefix(out.String(), "Next (2 min): ") || strings.Count(out.String(), "Next") != 1 ||
		strings.Count(out.String(), "2 min") != 1 || strings.Count(out.String(), "Why now:") != 1 ||
		strings.Count(out.String(), "Done when:") != 1 {
		t.Fatalf("coach output is not focus-first or duplicates context: %q", out.String())
	}

	out.Reset()
	if err := orch.Dispatch(ctx, Command{Cmd: "learn", Args: []string{"done"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "coach: completed "+state.PendingLessonID) {
		t.Fatalf("done output = %q", out.String())
	}

	out.Reset()
	if err := orch.Dispatch(ctx, Command{Cmd: "learn", Args: []string{"later"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "coach: snoozed for 24h0m0s") {
		t.Fatalf("later output = %q", out.String())
	}

	out.Reset()
	if err := orch.Dispatch(ctx, Command{Cmd: "learn", Args: []string{"status"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "coach: mode guided · 1 completed lesson(s)") {
		t.Fatalf("status output = %q", out.String())
	}
}

func TestDispatchWorkspaceListAndUsage(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	out := orch.out.(*bytes.Buffer)

	if err := orch.Dispatch(ctx, Command{Cmd: "git", Args: []string{"list"}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "main") || !strings.Contains(got, dir) {
		t.Fatalf("workspace list = %q", got)
	}
	if err := orch.Dispatch(ctx, Command{Cmd: "git"}); err == nil || !strings.Contains(err.Error(), "/git create") {
		t.Fatalf("git usage error = %v", err)
	}
}

func TestTerminalSafeLineRemovesControlSequences(t *testing.T) {
	got := terminalSafeLine("a\n\t\x1b[31m\u202Eb")
	if strings.ContainsAny(got, "\n\t\x1b") || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("terminalSafeLine = %q", got)
	}
}
