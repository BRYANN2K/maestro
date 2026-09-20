package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStipulateArtifactRejectsTruncation(t *testing.T) {
	root := newTestRepo(t)
	o := newTestOrch(t, root, &fakeRunner{})
	defer o.Close()
	dir := filepath.Join(root, ".workflow", "changes", "sample")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := o.WorkflowArtifact(t.Context(), "sample", 2)
	if err == nil || text != "" {
		t.Fatalf("silently truncated: bytes=%d err=%v", len(text), err)
	}
}
