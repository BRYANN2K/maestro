package proposals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		target string
		hunks  int
		shape  string
	}{
		{"identical", "a\nb\n", "a\nb\n", 0, ""},
		{"append", "a\n", "a\nb\n", 1, "+"},
		{"replace", "a\nb\n", "a\nc\n", 1, "-+"},
		{"insert middle", "a\nc\n", "a\nb\nc\n", 1, "+"},
		{"delete", "a\nb\nc\n", "a\nc\n", 1, "-"},
		{"new file", "", "x\n", 1, "+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hunks := lineDiff(splitLines(tt.base), splitLines(tt.target))
			if len(hunks) != tt.hunks {
				t.Fatalf("hunks = %d, want %d: %+v", len(hunks), tt.hunks, hunks)
			}
			var b strings.Builder
			for _, h := range hunks {
				for range h.OldLines {
					b.WriteString("-")
				}
				for range h.NewLines {
					b.WriteString("+")
				}
			}
			if b.String() != tt.shape {
				t.Errorf("hunk shape = %q, want %q", b.String(), tt.shape)
			}
		})
	}
}

func TestProposalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prop, err := store.Stage(path, "one\nTWO\nthree\nfour\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(prop.Hunks) == 0 {
		t.Fatal("expected hunks")
	}
	if _, err := os.Stat(filepath.Join(store.dir, prop.ID+".json")); err != nil {
		t.Errorf("proposal file missing: %v", err)
	}

	if err := store.Accept(prop); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "one\nTWO\nthree\nfour\n" {
		t.Errorf("file after accept = %q", data)
	}
	// Proposal file removed after accept.
	if _, err := os.Stat(filepath.Join(store.dir, prop.ID+".json")); !os.IsNotExist(err) {
		t.Error("proposal file should be removed after accept")
	}
}

func TestAcceptVerifiedBindsExactCompleteStagedContent(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	path := filepath.Join(dir, "MAESTRO.md")
	prop, err := store.Stage(path, "first\nsecond\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptHunk(&prop, 0); err == nil || !strings.Contains(err.Error(), "atomic contract") {
		t.Fatalf("atomic AcceptHunk = %v", err)
	}
	if err := store.RejectHunk(&prop, 0); err == nil || !strings.Contains(err.Error(), "atomic contract") {
		t.Fatalf("atomic RejectHunk = %v", err)
	}
	validated := ""
	if err := store.AcceptVerified(prop, func(content []byte) error {
		validated = string(content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if validated != "first\nsecond\n" {
		t.Fatalf("validated content = %q", validated)
	}

	tampered, err := store.Stage(path, "changed\nagain\n")
	if err != nil {
		t.Fatal(err)
	}
	tampered.Hunks[0].NewLines[0] = "injected"
	if err := store.AcceptVerified(tampered, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("tampered verified accept = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("tampered accept changed file: %q, %v", data, err)
	}
}

func TestProposalDiscard(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prop, err := store.Stage(path, "changed\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	store.Discard(prop)
	data, _ := os.ReadFile(path)
	if string(data) != "keep\n" {
		t.Errorf("file after discard = %q, want untouched", data)
	}
}

func TestProposalStaleRejection(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prop, err := store.Stage(path, "one\nCHANGED\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	// Someone else edits the file after staging.
	if err := os.WriteFile(path, []byte("one\nEXTERNAL\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = store.Accept(prop)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("Accept after external edit = %v, want stale error", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one\nEXTERNAL\n" {
		t.Errorf("file corrupted: %q", data)
	}
}

func TestProposalNewFile(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	path := filepath.Join(dir, "nested", "new.go")
	prop, err := store.Stage(path, "package x\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := store.Accept(prop); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "package x\n" {
		t.Errorf("new file = %q, %v", data, err)
	}
}

func TestAcceptHunksSequentially(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prop, err := store.Stage(path, "ONE\ntwo\nthree\nFOUR\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(prop.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2: %+v", len(prop.Hunks), prop.Hunks)
	}
	if err := store.AcceptHunk(&prop, 0); err != nil {
		t.Fatalf("AcceptHunk first: %v", err)
	}
	if err := store.AcceptHunk(&prop, 0); err != nil {
		t.Fatalf("AcceptHunk second: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got, want := string(data), "ONE\ntwo\nthree\nFOUR\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestWorkspaceStoreRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaceProposalStore(filepath.Join(t.TempDir(), "proposals"), func() string { return root })
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if _, err := store.Stage(outside, "nope\n"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("absolute escape error = %v", err)
	}
	if _, err := store.Stage("../outside.txt", "nope\n"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("relative escape error = %v", err)
	}
}

func TestWorkspaceStoreRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	store := NewWorkspaceProposalStore(filepath.Join(t.TempDir(), "proposals"), func() string { return root })
	if _, err := store.Stage(filepath.Join("linked", "outside.txt"), "nope\n"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestPending(t *testing.T) {
	dir := t.TempDir()
	store := NewProposalStore(filepath.Join(dir, ".proposals"))
	pending, err := store.Pending()
	if err != nil || len(pending) != 0 {
		t.Fatalf("Pending = %v, %v", pending, err)
	}
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prop, err := store.Stage(path, "b\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	pending, _ = store.Pending()
	if len(pending) != 1 || pending[0] != prop.ID {
		t.Errorf("Pending = %v", pending)
	}
}
