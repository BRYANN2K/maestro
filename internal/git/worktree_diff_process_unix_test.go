//go:build unix

package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWorktreeDiffUsesTenGitProcessesWithoutSubmodules(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "changed.txt", "complete evidence\n")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	countPath := filepath.Join(t.TempDir(), "git-calls")
	shim := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nprintf 'call\\n' >> \"$MAESTRO_GIT_CALLS\"\nexec \"$MAESTRO_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAESTRO_GIT_CALLS", countPath)
	t.Setenv("MAESTRO_REAL_GIT", realGit)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if _, err := New(dir).WorktreeDiff(t.Context(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	// Root and object-store resolution, submodule inspection, read-tree, bounded
	// path/change inventories, two filter gates, add, and diff. The added calls
	// enforce the no-external-filter and bounded-input security boundary.
	if calls := strings.Count(string(data), "call\n"); calls != 10 {
		t.Fatalf("WorktreeDiff git processes = %d, want 10", calls)
	}
}

func TestWorktreeDiffAndSubmoduleSafetyDisableConfiguredFSMonitor(t *testing.T) {
	submoduleSource := initRepo(t)
	dir := initRepo(t)
	run(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", submoduleSource, "modules/dependency")
	run(t, dir, "commit", "-am", "add submodule")
	writeFile(t, dir, "fresh.txt", "review without repository hooks\n")

	marker := filepath.Join(t.TempDir(), "fsmonitor-invoked")
	t.Setenv("MAESTRO_TEST_FS_MONITOR_SENTINEL", marker)
	hook := filepath.Join(t.TempDir(), "fsmonitor")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n: > \"$MAESTRO_TEST_FS_MONITOR_SENTINEL\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	submodule := filepath.Join(dir, "modules", "dependency")
	for _, repository := range []string{dir, submodule} {
		run(t, repository, "config", "core.fsmonitor", hook)
		// Prove the configured fixture is active in both the root repository and
		// the checked-out submodule before asserting Maestro suppresses it.
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		run(t, repository, "status", "--porcelain=v1")
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("configured fsmonitor did not run in %q: %v", repository, err)
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	// An inherited command-scope setting must not outrank Maestro's explicit
	// per-command override either.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", hook)

	diff, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	if !strings.Contains(diff, "fresh.txt") || !strings.Contains(diff, "review without repository hooks") {
		t.Fatalf("WorktreeDiff omitted the root change:\n%s", diff)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WorktreeDiff or recursive submodule safety invoked core.fsmonitor: %v", err)
	}
}

func TestWorktreeDiffUsesPrivateObjectStoreFromLinkedWorktreeWithQuotedPath(t *testing.T) {
	repository := filepath.Join(t.TempDir(), `repo:with"quote`)
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repository, "init", "-b", "main")
	run(t, repository, "config", "user.email", "test@maestro.local")
	run(t, repository, "config", "user.name", "Maestro Test")
	writeFile(t, repository, "README.md", "# repo\n")
	run(t, repository, "add", "README.md")
	run(t, repository, "commit", "-m", "initial commit")

	linked := filepath.Join(t.TempDir(), `linked:with"quote`)
	run(t, repository, "worktree", "add", "-b", "evidence", linked)
	payload := []byte("unique linked-worktree payload: " + linked + "\x00\xff")
	path := filepath.Join(linked, "isolated.bin")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	objectID := strings.TrimSpace(run(t, linked, "hash-object", "isolated.bin"))
	objects := strings.TrimSuffix(run(t, linked, "rev-parse", "--path-format=absolute", "--git-path", "objects"), "\n")
	looseObject := filepath.Join(objects, objectID[:2], objectID[2:])
	if _, err := os.Stat(looseObject); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test blob unexpectedly existed before WorktreeDiff: %v", err)
	}

	diff, err := New(linked).WorktreeDiff(t.Context(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	if !strings.Contains(diff, "isolated.bin") || !strings.Contains(diff, "GIT binary patch") {
		t.Fatalf("binary patch missing from linked-worktree diff:\n%s", diff)
	}
	if _, err := os.Stat(looseObject); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WorktreeDiff wrote blob %s into real object store: %v", objectID, err)
	}
}

func TestWorktreeDiffReadsRepositoryObjectAlternates(t *testing.T) {
	source := initRepo(t)
	parent := t.TempDir()
	shared := filepath.Join(parent, "shared:clone")
	run(t, parent, "clone", "--shared", source, shared)
	writeFile(t, shared, "README.md", "changed through alternate object store\n")

	diff, err := New(shared).WorktreeDiff(t.Context(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff with objects/info/alternates: %v", err)
	}
	if !strings.Contains(diff, "changed through alternate object store") {
		t.Fatalf("diff omitted change from shared clone:\n%s", diff)
	}
}

func TestWorktreeDiffRejectsCleanAndProcessFiltersWithoutInvokingThem(t *testing.T) {
	for _, kind := range []string{"clean", "process"} {
		t.Run(kind, func(t *testing.T) {
			dir := initRepo(t)
			marker := filepath.Join(t.TempDir(), "filter-invoked")
			t.Setenv("MAESTRO_FILTER_MARKER", marker)
			filter := filepath.Join(t.TempDir(), "filter.sh")
			script := "#!/bin/sh\n: > \"$MAESTRO_FILTER_MARKER\"\nexit 1\n"
			if err := os.WriteFile(filter, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, dir, "config", "filter.evil."+kind, filter)
			writeFile(t, dir, ".gitattributes", "*.payload filter=evil\n")
			writeFile(t, dir, "untrusted.payload", "must never reach a filter\n")

			_, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
			if err == nil || !strings.Contains(err.Error(), "content filter") || !strings.Contains(err.Error(), "untrusted.payload") {
				t.Fatalf("WorktreeDiff error = %v, want explicit content-filter refusal", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("configured %s filter was invoked: %v", kind, err)
			}
		})
	}
}

func TestWorktreeDiffRejectsSubmoduleFilterWithoutInvokingIt(t *testing.T) {
	source := initRepo(t)
	writeFile(t, source, ".gitattributes", "*.payload filter=evil\n")
	writeFile(t, source, "tracked.payload", "submodule payload\n")
	run(t, source, "add", ".gitattributes", "tracked.payload")
	run(t, source, "commit", "-m", "add filtered path")

	dir := initRepo(t)
	run(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", source, "modules/dependency")
	run(t, dir, "commit", "-am", "add submodule")
	marker := filepath.Join(t.TempDir(), "submodule-filter-invoked")
	t.Setenv("MAESTRO_FILTER_MARKER", marker)
	filter := filepath.Join(t.TempDir(), "filter.sh")
	if err := os.WriteFile(filter, []byte("#!/bin/sh\n: > \"$MAESTRO_FILTER_MARKER\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	submodule := filepath.Join(dir, "modules", "dependency")
	run(t, submodule, "config", "filter.evil.clean", filter)

	_, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), "content filter") || !strings.Contains(err.Error(), "tracked.payload") {
		t.Fatalf("WorktreeDiff error = %v, want submodule content-filter refusal", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured submodule filter was invoked: %v", err)
	}
}

func TestWorktreeDiffRejectsOversizeAndAggregateChangedFilesBeforeAdd(t *testing.T) {
	t.Run("per-file", func(t *testing.T) {
		dir := initRepo(t)
		writeSparseFile(t, filepath.Join(dir, "too-large.bin"), maxWorktreeDiffFileBytes+1)

		_, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
		if err == nil || !strings.Contains(err.Error(), "per-file review snapshot limit") {
			t.Fatalf("WorktreeDiff error = %v, want per-file preflight refusal", err)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		dir := initRepo(t)
		partSize := maxWorktreeDiffAggregateBytes/3 + 1
		if partSize > maxWorktreeDiffFileBytes {
			t.Fatalf("test part %d exceeds per-file limit", partSize)
		}
		for i := range 3 {
			writeSparseFile(t, filepath.Join(dir, "aggregate-"+string(rune('a'+i))+".bin"), partSize)
		}

		_, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
		if err == nil || !strings.Contains(err.Error(), "aggregate review snapshot limit") {
			t.Fatalf("WorktreeDiff error = %v, want aggregate preflight refusal", err)
		}
	})
}

func TestWorktreeDiffRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := initRepo(t)
	pipe := filepath.Join(dir, "untrusted.pipe")
	writeFile(t, dir, "untrusted.pipe", "regular before replacement\n")
	run(t, dir, "add", "untrusted.pipe")
	run(t, dir, "commit", "-m", "track replaceable path")
	if err := os.Remove(pipe); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), "unsupported special file mode") || !strings.Contains(err.Error(), "untrusted.pipe") {
		t.Fatalf("WorktreeDiff error = %v, want non-blocking FIFO refusal", err)
	}
}

func TestWorktreeDiffStillIncludesSymlink(t *testing.T) {
	dir := initRepo(t)
	if err := os.Symlink("README.md", filepath.Join(dir, "readme-link")); err != nil {
		t.Fatal(err)
	}

	diff, err := New(dir).WorktreeDiff(t.Context(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff symlink: %v", err)
	}
	if !strings.Contains(diff, "new file mode 120000") || !strings.Contains(diff, "+README.md") {
		t.Fatalf("symlink missing from patch:\n%s", diff)
	}
}
