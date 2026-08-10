package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseWorktreePorcelainZPreservesAdversarialPaths(t *testing.T) {
	oid := strings.Repeat("a", 40)
	raw := []byte("worktree /tmp/a path\nwith-tab\tend\x00HEAD " + oid + "\x00branch refs/heads/feat/weird\x00locked reason\nline\x00\x00" +
		"worktree /tmp/detached\x00HEAD " + oid + "\x00detached\x00prunable gitdir file points to non-existent location\x00\x00")
	got, err := parseWorktreePorcelainZ(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "/tmp/a path\nwith-tab\tend" || got[0].LockReason != "reason\nline" || got[0].Ref != "refs/heads/feat/weird" {
		t.Fatalf("parsed records = %+v", got)
	}
	if !got[1].Detached || !got[1].Prunable || got[1].PrunableReason == "" {
		t.Fatalf("detached record = %+v", got[1])
	}
}

func TestValidateBranchNameUsesGitGrammar(t *testing.T) {
	c := New(initRepo(t))
	for _, name := range []string{"feature/good", "release-2026.08"} {
		if err := c.ValidateBranchName(t.Context(), name); err != nil {
			t.Errorf("ValidateBranchName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", "-option", "bad..name", "bad.lock", "bad@{thing", "bad//name", "bad name", "bad\nname"} {
		if err := c.ValidateBranchName(t.Context(), name); err == nil {
			t.Errorf("ValidateBranchName(%q) succeeded", name)
		}
	}
}

func TestListWorkspacesReportsDirtyLockedAndDetached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("worktree path permission and removal semantics differ on Windows")
	}
	dir := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked\nworkspace")
	run(t, dir, "worktree", "add", "-b", "feature/list", linked, "HEAD")
	writeFile(t, linked, "dirty.txt", "not committed\n")
	workspaces, err := New(dir).ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := findWorkspace(workspaces, linked)
	if !ok || !workspace.Healthy || !workspace.Dirty || workspace.Ref != "refs/heads/feature/list" {
		t.Fatalf("dirty workspace = %+v found=%v", workspace, ok)
	}
	os.Remove(filepath.Join(linked, "dirty.txt"))
	run(t, dir, "worktree", "lock", "--reason", "held by test", linked)
	workspaces, err = New(dir).ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace, _ = findWorkspace(workspaces, linked)
	if workspace.Healthy || !workspace.Locked || workspace.LockReason != "held by test" {
		t.Fatalf("locked workspace = %+v", workspace)
	}
	run(t, dir, "worktree", "unlock", linked)
	run(t, linked, "switch", "--detach", "HEAD")
	workspaces, err = New(dir).ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace, _ = findWorkspace(workspaces, linked)
	if workspace.Healthy || !workspace.Detached {
		t.Fatalf("detached workspace = %+v", workspace)
	}
}

func TestListWorkspacesReportsPrunableMissingCheckout(t *testing.T) {
	dir := initRepo(t)
	linked := filepath.Join(t.TempDir(), "stale")
	run(t, dir, "worktree", "add", "-b", "feature/stale", linked, "HEAD")
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	workspaces, err := New(dir).ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := findWorkspace(workspaces, linked)
	if !ok || workspace.Healthy || !workspace.Prunable || workspace.DisabledReason == "" {
		t.Fatalf("prunable workspace = %+v found=%v", workspace, ok)
	}
}

func TestWorkspaceDirtyUsesBoundedNormalUntrackedStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	envPath := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MAESTRO_TEST_GIT_ARGS\"\nprintf '%s\\n%s\\n' \"$GIT_OPTIONAL_LOCKS\" \"$GIT_TERMINAL_PROMPT\" > \"$MAESTRO_TEST_GIT_ENV\"\nprintf 'x'\n"
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAESTRO_TEST_GIT_ARGS", argsPath)
	t.Setenv("MAESTRO_TEST_GIT_ENV", envPath)
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	dirty, err := workspaceDirty(t.Context(), t.TempDir())
	if err != nil || !dirty {
		t.Fatalf("workspaceDirty = %v, %v", dirty, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(args)
	if !strings.Contains(text, "core.fsmonitor=false") || !strings.Contains(text, "core.hooksPath=") ||
		!strings.Contains(text, "--untracked-files=normal") || strings.Contains(text, "--untracked-files=all") || !strings.Contains(text, "--no-renames") {
		t.Fatalf("git status arguments = %q", text)
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "0\n0\n" {
		t.Fatalf("git metadata environment = %q, want optional locks and prompts disabled", env)
	}
}

func TestWorkspaceReadOnlyGitCommandHasPortableSecurityEnvelope(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	cmd := workspaceReadOnlyGitCommand(t.Context(), t.TempDir(), "status", "--porcelain=v1")
	wantArgs := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=", "status", "--porcelain=v1"}
	if len(cmd.Args) != len(wantArgs)+1 {
		t.Fatalf("git args = %q", cmd.Args)
	}
	for i, want := range wantArgs {
		if got := cmd.Args[i+1]; got != want {
			t.Fatalf("git arg %d = %q, want %q (all: %q)", i, got, want, cmd.Args)
		}
	}
	environment := map[string]string{}
	for _, item := range cmd.Env {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			environment[name] = value
		}
	}
	if environment["GIT_OPTIONAL_LOCKS"] != "0" || environment["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("git environment locks=%q prompt=%q, want both 0", environment["GIT_OPTIONAL_LOCKS"], environment["GIT_TERMINAL_PROMPT"])
	}
}

func TestListWorkspacesDoesNotRunConfiguredFSMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fsmonitor fixture")
	}
	dir := initRepo(t)
	fixtureDir := t.TempDir()
	sentinel := filepath.Join(fixtureDir, "fsmonitor-ran")
	hook := filepath.Join(fixtureDir, "fsmonitor")
	script := "#!/bin/sh\n: > \"$MAESTRO_TEST_FS_MONITOR_SENTINEL\"\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAESTRO_TEST_FS_MONITOR_SENTINEL", sentinel)
	run(t, dir, "config", "core.fsmonitor", hook)

	// Prove that the fixture really is executable by an ordinary status call;
	// the assertion below is meaningful only if Git would otherwise run it.
	if _, err := New(dir).run(t.Context(), "status", "--porcelain=v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("configured fsmonitor fixture did not run: %v", err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	// Command-scope config supplied through the environment is also lower
	// priority than Maestro's explicit final -c override.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", hook)

	if _, err := New(dir).ListWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace metadata refresh executed core.fsmonitor: %v", err)
	}
}

func TestWorkspaceDirtyHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := workspaceDirty(ctx, t.TempDir())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("workspaceDirty cancellation = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("workspaceDirty cancellation took %s", elapsed)
	}
}

func TestCreateLinkedWorktreeUsesExplicitHeadAndCASCleanup(t *testing.T) {
	dir := initRepo(t)
	client := New(dir)
	start, err := client.HeadOID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "source-only.txt", "dirty source\n")
	parent := t.TempDir()
	target := filepath.Join(parent, "managed")
	created, err := client.CreateLinkedWorktree(t.Context(), target, "feature/managed", start)
	if err != nil {
		t.Fatal(err)
	}
	if created.Head != start || created.Ref != "refs/heads/feature/managed" || created.Dirty || !created.Healthy {
		t.Fatalf("created = %+v", created)
	}
	if _, err := os.Stat(filepath.Join(target, "source-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("dirty source change leaked into linked worktree: %v", err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("managed worktree mode = %v, %v", info, err)
	}
	if _, err := client.CreateLinkedWorktree(t.Context(), filepath.Join(parent, "other"), "feature/managed", start); err == nil {
		t.Fatal("branch collision succeeded")
	}
	if err := client.CleanupCreatedWorktree(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("cleanup retained target: %v", err)
	}
	if _, err := client.run(t.Context(), "rev-parse", "--verify", "refs/heads/feature/managed"); err == nil {
		t.Fatal("cleanup retained branch")
	}
}

func TestCleanupCreatedWorktreePreservesConcurrentChanges(t *testing.T) {
	dir := initRepo(t)
	client := New(dir)
	start, err := client.HeadOID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "persistent")
	created, err := client.CreateLinkedWorktree(t.Context(), target, "feature/persistent", start)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, target, "user.txt", "keep me\n")
	if err := client.CleanupCreatedWorktree(context.Background(), created); err == nil {
		t.Fatal("cleanup removed a dirty workspace")
	}
	if _, err := os.Stat(filepath.Join(target, "user.txt")); err != nil {
		t.Fatalf("concurrent file was not preserved: %v", err)
	}
}

func TestCreateLinkedWorktreeRejectsSubmoduleRepository(t *testing.T) {
	subject := initRepo(t)
	super := initRepo(t)
	path := filepath.Join(super, "nested")
	run(t, super, "-c", "protocol.file.allow=always", "submodule", "add", subject, "nested")
	run(t, super, "commit", "-am", "add submodule")
	client := New(path)
	head, err := client.HeadOID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateLinkedWorktree(t.Context(), filepath.Join(t.TempDir(), "target"), "feature/forbidden", head); err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("submodule create error = %v", err)
	}
}
