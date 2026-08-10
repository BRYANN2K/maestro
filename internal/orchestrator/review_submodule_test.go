package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeFingerprintRejectsDirtySubmodule(t *testing.T) {
	submoduleSource := newTestRepo(t)
	dir := newTestRepo(t)
	gitRun(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", submoduleSource, "modules/dependency")
	gitRun(t, dir, "commit", "-am", "add submodule")
	orch := newTestOrch(t, dir, &fakeRunner{})

	if _, err := orch.worktreeFingerprint(t.Context()); err != nil {
		t.Fatalf("clean fingerprint: %v", err)
	}
	path := filepath.Join(dir, "modules", "dependency", "README.md")
	if err := os.WriteFile(path, []byte("dirty submodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := orch.worktreeFingerprint(t.Context())
	if err == nil || !strings.Contains(err.Error(), "dirty submodule") {
		t.Fatalf("fingerprint error = %v, want dirty-submodule refusal", err)
	}
}
