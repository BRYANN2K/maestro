package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointRewindRestoresExactCodeStateAndRecovery(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	writeFile(t, dir, ".gitignore", "ignored.txt\n")
	writeFile(t, dir, "tracked.txt", "base\n")
	run(t, dir, "add", ".gitignore", "tracked.txt")
	run(t, dir, "commit", "-m", "add fixtures")

	// Capture a partially staged tracked file and pre-existing untracked data.
	writeFile(t, dir, "tracked.txt", "staged\n")
	run(t, dir, "add", "tracked.txt")
	writeFile(t, dir, "tracked.txt", "worktree\n")
	writeFile(t, dir, "captured-new.txt", "new file staged\n")
	run(t, dir, "add", "captured-new.txt")
	writeFile(t, dir, "captured-new.txt", "new file worktree\n")
	initialBinary := []byte{0x00, 0xff, 'm', 'a', 'e', 's', 't', 'r', 'o'}
	if err := os.WriteFile(filepath.Join(dir, "untracked binary.dat"), initialBinary, 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(dir, "untracked-link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "ignored.txt", "ignored-at-checkpoint\n")
	wantStatus := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	wantIndex := run(t, dir, "show", ":tracked.txt")
	wantNewIndex := run(t, dir, "show", ":captured-new.txt")

	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))
	cp, err := store.Create(ctx, c, `{"session":"checkpoint"}`, SpecRev("spec-v1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cp.SnapshotVersion != checkpointSnapshotVersion || cp.IndexTree == "" || cp.WorktreeTree == "" {
		t.Fatalf("incomplete checkpoint: %+v", cp)
	}
	if cp.SpecRev != SpecRev("spec-v1") {
		t.Errorf("spec rev = %q", cp.SpecRev)
	}

	// Replace every captured state and add a post-checkpoint untracked file.
	writeFile(t, dir, "tracked.txt", "later\n")
	run(t, dir, "add", "tracked.txt")
	if err := os.Remove(filepath.Join(dir, "captured-new.txt")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-u", "--", "captured-new.txt")
	if err := os.WriteFile(filepath.Join(dir, "untracked binary.dat"), []byte("later"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "empty.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "empty.txt/post-checkpoint.txt", "directory replaced file\n")
	if err := os.Remove(filepath.Join(dir, "untracked-link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "untracked-link", "not a symlink\n")
	writeFile(t, dir, "created-after.txt", "later\n")
	writeFile(t, dir, "ignored.txt", "ignored-after-checkpoint\n")
	laterStatus := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")

	result, err := store.Rewind(ctx, c, cp.ID, true, `{"session":"later"}`, "later-rev")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if result.Checkpoint.ID != cp.ID || result.Recovery.ID == "" || result.Recovery.RecoveryFor != cp.ID {
		t.Fatalf("rewind result = %+v", result)
	}
	if _, err := store.Load(ctx, result.Recovery.ID); err != nil {
		t.Fatalf("recovery checkpoint is not loadable: %v", err)
	}
	if got := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"); got != wantStatus {
		t.Errorf("status after rewind = %q, want %q", got, wantStatus)
	}
	if got := run(t, dir, "show", ":tracked.txt"); got != wantIndex {
		t.Errorf("index content = %q, want %q", got, wantIndex)
	}
	if got := run(t, dir, "show", ":captured-new.txt"); got != wantNewIndex {
		t.Errorf("new-file index content = %q, want %q", got, wantNewIndex)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "tracked.txt")); err != nil || string(got) != "worktree\n" {
		t.Errorf("tracked worktree = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "captured-new.txt")); err != nil || string(got) != "new file worktree\n" {
		t.Errorf("captured staged creation = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "untracked binary.dat")); err != nil || !bytes.Equal(got, initialBinary) {
		t.Errorf("untracked binary = %v, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "untracked binary.dat")); err != nil || info.Mode().Perm() != 0o751 {
		t.Errorf("untracked mode = %v, %v", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "empty.txt")); err != nil || len(got) != 0 {
		t.Errorf("empty untracked file = %q, %v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(dir, "untracked-link")); err != nil || got != "tracked.txt" {
		t.Errorf("untracked symlink = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "created-after.txt")); !os.IsNotExist(err) {
		t.Errorf("post-checkpoint untracked file still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "ignored.txt")); err != nil || string(got) != "ignored-after-checkpoint\n" {
		t.Errorf("ignored file should be untouched, got %q, %v", got, err)
	}

	// The automatic backup is itself a complete, recoverable snapshot.
	if err := store.RestoreCode(ctx, c, result.Recovery.ID); err != nil {
		t.Fatalf("RestoreCode(recovery): %v", err)
	}
	if got := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"); got != laterStatus {
		t.Errorf("status after recovery restore = %q, want %q", got, laterStatus)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "tracked.txt")); err != nil || string(got) != "later\n" {
		t.Errorf("recovered tracked file = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "created-after.txt")); err != nil || string(got) != "later\n" {
		t.Errorf("recovered untracked creation = %q, %v", got, err)
	}
}

func TestCheckpointRewindCleanSnapshotRemovesLaterTrackedAndUntrackedChanges(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))
	cp, err := store.Create(ctx, c, "{}", "rev")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "README.md", "changed\n")
	writeFile(t, dir, "new.txt", "new\n")
	writeFile(t, dir, "staged-new.txt", "staged later\n")
	run(t, dir, "add", "staged-new.txt")
	if _, err := store.Rewind(ctx, c, cp.ID, true, "{}", "rev"); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if got := run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("status = %q, want clean", got)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || string(got) != "# repo\n" {
		t.Errorf("README = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new.txt still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "staged-new.txt")); !os.IsNotExist(err) {
		t.Errorf("staged-new.txt still exists: %v", err)
	}
}

func TestCheckpointRewindRejectsChangedHEADWithoutMutationOrRecovery(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))
	cp, err := store.Create(ctx, c, "{}", "rev")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "README.md", "new commit\n")
	run(t, dir, "add", "README.md")
	run(t, dir, "commit", "-m", "advance head")
	writeFile(t, dir, "after.txt", "preserve me\n")
	wantStatus := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")

	if _, err := store.Rewind(ctx, c, cp.ID, true, "{}", "rev"); err == nil || !strings.Contains(err.Error(), "differs from current HEAD") {
		t.Fatalf("Rewind error = %v", err)
	}
	if got := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"); got != wantStatus {
		t.Errorf("failed rewind mutated repository: got %q, want %q", got, wantStatus)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("failed preflight created a recovery checkpoint: len=%d, err=%v", len(list), err)
	}
}

func TestCheckpointRewindRejectsAnotherWorktreeWithoutMutation(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()
	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))
	cp, err := store.Create(ctx, New(dir), "{}", "rev")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other-worktree")
	run(t, dir, "worktree", "add", "-b", "other", other)
	writeFile(t, other, "README.md", "must survive\n")

	if _, err := store.Rewind(ctx, New(other), cp.ID, true, "{}", "rev"); err == nil || !strings.Contains(err.Error(), "different repository or worktree") {
		t.Fatalf("Rewind error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(other, "README.md")); err != nil || string(got) != "must survive\n" {
		t.Errorf("wrong-worktree rewind mutated file: %q, %v", got, err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("wrong-worktree preflight created recovery: len=%d, err=%v", len(list), err)
	}
}

func TestCheckpointStoreMustBeOutsideWorktree(t *testing.T) {
	dir := initRepo(t)
	storeDir := filepath.Join(dir, ".maestro-checkpoints")
	store := NewCheckpointStore(storeDir)
	if _, err := store.Create(context.Background(), New(dir), "{}", "rev"); err == nil || !strings.Contains(err.Error(), "outside the Git worktree") {
		t.Fatalf("Create error = %v", err)
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("failed checkpoint should not create in-worktree state: %v", err)
	}
}

func TestCheckpointRewindRejectsLegacyCodeSnapshot(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))
	legacy := Checkpoint{ID: "cp-legacy", Code: "legacy diff", Conv: "{}", Created: time.Now()}
	if err := store.save(legacy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "README.md", "must remain\n")
	if _, err := store.Rewind(ctx, c, legacy.ID, true, "{}", ""); err == nil || !strings.Contains(err.Error(), "legacy snapshot") {
		t.Fatalf("Rewind error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(got) != "must remain\n" {
		t.Errorf("legacy rewind mutated file: %q", got)
	}
}

func TestCheckpointListBySpec(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))

	first, err := store.Create(ctx, c, "{}", "rev-a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := store.Create(ctx, c, "{}", "rev-a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("checkpoint IDs must be unique")
	}
	if _, err := store.Create(ctx, c, "{}", "rev-b"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := store.ListBySpec(ctx, "rev-a")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListBySpec = %d, %v", len(list), err)
	}
	all, _ := store.List(ctx)
	if len(all) != 3 {
		t.Errorf("all = %d", len(all))
	}
	if all[0].Created.Before(all[1].Created) {
		t.Error("list should be newest first")
	}
}

func TestCheckpointLoadMissingAndRejectsTraversal(t *testing.T) {
	store := NewCheckpointStore(filepath.Join(t.TempDir(), "cps"))
	if _, err := store.Load(context.Background(), "cp-none"); err == nil {
		t.Error("loading a missing checkpoint should fail")
	}
	if _, err := store.Load(context.Background(), "../cp-secret"); err == nil {
		t.Error("checkpoint traversal should fail")
	}
}

func TestSpecRev(t *testing.T) {
	if SpecRev("x") == SpecRev("y") {
		t.Error("different specs must hash differently")
	}
	if SpecRev("x") == "" || len(SpecRev("x")) != 24 {
		t.Errorf("rev = %q", SpecRev("x"))
	}
}
