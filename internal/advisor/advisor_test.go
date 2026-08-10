package advisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdvisorOutOfScopeBlocker(t *testing.T) {
	dir := t.TempDir()
	a := New(dir)
	a.Spec = &Spec{ID: "api", Batches: []Batch{{ID: "b1", Files: []string{"internal/api/"}}}}
	var notes []Note
	a.Emit = func(n Note) { notes = append(notes, n) }

	a.Observe(context.Background(), "tool_result", "write", "wrote internal/api/server.go (1024 bytes)", "dev")
	if len(notes) != 0 {
		t.Fatalf("in-scope write flagged: %+v", notes)
	}
	a.Observe(context.Background(), "tool_result", "write", "wrote cmd/other/main.go (1 bytes)", "dev")
	if len(notes) != 1 || notes[0].Level != Blocker || !strings.Contains(notes[0].Text, "out-of-scope") {
		t.Fatalf("out-of-scope write = %+v", notes)
	}
}

func TestAdvisorConventionsAndPlaceholders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n\nfunc main() { panic(\"nope\") }\n// TODO: fix\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := New(dir)
	a.Conventions = []string{`\bpanic\(`}
	var notes []Note
	a.Emit = func(n Note) { notes = append(notes, n) }
	a.Observe(context.Background(), "tool_result", "write", "wrote x.go (42 bytes)", "dev")
	if len(notes) != 2 {
		t.Fatalf("notes = %+v", notes)
	}
	if notes[0].Level != Concern || !strings.Contains(notes[0].Text, "panic") {
		t.Errorf("note 0 = %+v", notes[0])
	}
	// note[1] is the info note when both fire on the same run.
	foundTODO := false
	for _, n := range notes {
		if n.Level == Info && strings.Contains(n.Text, "placeholder") {
			foundTODO = true
		}
	}
	if !foundTODO {
		t.Errorf("missing TODO info note: %+v", notes)
	}
}

func TestAdvisorDisabledAndOtherRoles(t *testing.T) {
	a := New(t.TempDir())
	a.Spec = &Spec{ID: "x"}
	var notes []Note
	a.Emit = func(n Note) { notes = append(notes, n) }
	// Reviewer writes are not dev writes.
	a.Observe(context.Background(), "tool_result", "write", "wrote out/scope.go", "reviewer")
	if len(notes) != 0 {
		t.Fatalf("reviewer write flagged: %+v", notes)
	}
	a.Disable()
	a.Observe(context.Background(), "tool_result", "write", "wrote out/scope.go", "dev")
	if len(notes) != 0 {
		t.Fatalf("disabled advisor flagged: %+v", notes)
	}
}

func TestExtractPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"wrote internal/a.go (1024 bytes)", "internal/a.go"},
		{"staged p123 → internal/a.go (2 hunk(s))", "internal/a.go"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := extractPath(tt.in); got != tt.want {
			t.Errorf("extractPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
