package orchestrator

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

func TestModifiedFiles(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@maestro.local")
	run("config", "user.name", "Maestro Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")

	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Clean tree → no files.
	if got := orch.ModifiedFiles(context.Background()); len(got) != 0 {
		t.Fatalf("clean tree stats = %+v", got)
	}

	// Modify + add untracked.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n\nline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats := orch.ModifiedFiles(context.Background())
	found := map[string]git.NumStat{}
	for _, s := range stats {
		found[s.Path] = s
	}
	readme, ok := found["README.md"]
	if !ok || readme.Additions != 2 {
		t.Errorf("README.md stat = %+v (present %v)", readme, ok)
	}
	ng, ok := found["new.go"]
	if !ok || !ng.Untracked || ng.Additions != 1 {
		t.Errorf("new.go stat = %+v (present %v)", ng, ok)
	}

	// Revert → files disappear.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "new.go")); err != nil {
		t.Fatal(err)
	}
	if got := orch.ModifiedFiles(context.Background()); len(got) != 0 {
		t.Errorf("stats after revert = %+v", got)
	}
}

func TestModifiedFilesSnapshotSurvivesWorkspaceSwitch(t *testing.T) {
	dirA := newTestRepo(t)
	dirB := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(dirA, "only-a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "only-b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orch := newTestOrch(t, dirA, &fakeRunner{})
	snapshotA := orch.SnapshotWorkspace()
	orch.installWorkspace(dirB, git.New(dirB), spec.NewStore(filepath.Join(dirB, "specs")))

	if orch.WorkspaceIsCurrent(snapshotA) {
		t.Fatal("snapshot from previous workspace still reported current")
	}
	stats := orch.ModifiedFilesFor(context.Background(), snapshotA)
	if !hasModifiedPath(stats, "only-a.txt") || hasModifiedPath(stats, "only-b.txt") {
		t.Fatalf("old snapshot crossed workspace boundary: %+v", stats)
	}
}

func TestModifiedFilesConcurrentWorkspaceSwitch(t *testing.T) {
	dirA := newTestRepo(t)
	dirB := filepath.Join(t.TempDir(), "session-worktree")
	gitRun(t, dirA, "worktree", "add", "-b", "session-b", dirB)
	var err error
	dirB, err = canonicalProjectDir(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "only-a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "only-b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orch := newTestOrch(t, dirA, &fakeRunner{})
	dirA = orch.WorkDirDisplay()
	baseSession := orch.Session()
	baseSession.ID = "workspace-a"
	baseSession.Revision = 0
	baseSession.Phase = session.PhaseChat
	baseSession.Branch = "main"
	baseSession.Worktree = dirA
	baseSession.WorkspaceRef = "refs/heads/main"
	worktreeSession := baseSession
	worktreeSession.ID = "workspace-b"
	worktreeSession.Revision = 0
	worktreeSession.Branch = "session-b"
	worktreeSession.Worktree = dirB
	worktreeSession.WorkspaceRef = "refs/heads/session-b"
	if err := orch.sessions.Save(context.Background(), baseSession); err != nil {
		t.Fatal(err)
	}
	if err := orch.sessions.Save(context.Background(), worktreeSession); err != nil {
		t.Fatal(err)
	}

	const switches = 20
	start := make(chan struct{})
	switchErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < switches; i++ {
			id := "workspace-a"
			if i%2 == 0 {
				id = "workspace-b"
			}
			if err := orch.LoadSession(context.Background(), id); err != nil {
				switchErr <- err
				return
			}
		}
	}()

	close(start)
	defer wg.Wait()
	for i := 0; i < switches; i++ {
		snapshot := orch.SnapshotWorkspace()
		stats := orch.ModifiedFilesFor(context.Background(), snapshot)
		switch snapshot.WorkDir() {
		case dirA:
			if !hasModifiedPath(stats, "only-a.txt") || hasModifiedPath(stats, "only-b.txt") {
				t.Fatalf("workspace A scan mixed routes: %+v", stats)
			}
		case dirB:
			if !hasModifiedPath(stats, "only-b.txt") || hasModifiedPath(stats, "only-a.txt") {
				t.Fatalf("workspace B scan mixed routes: %+v", stats)
			}
		default:
			t.Fatalf("unexpected workspace snapshot %q", snapshot.WorkDir())
		}
	}
	wg.Wait()
	select {
	case err := <-switchErr:
		t.Fatalf("switch session: %v", err)
	default:
	}
}

func hasModifiedPath(stats []git.NumStat, path string) bool {
	for _, stat := range stats {
		if stat.Path == path {
			return true
		}
	}
	return false
}
