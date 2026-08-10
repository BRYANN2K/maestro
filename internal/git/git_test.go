package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// initRepo creates a git repository in t.TempDir() with a first commit and
// returns the directory. Tests needing a clean repo for the first commit
// should use initRepo and add a second commit before relying on diffs.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", "main")
	run(t, dir, "config", "user.email", "test@maestro.local")
	run(t, dir, "config", "user.name", "Maestro Test")
	writeFile(t, dir, "README.md", "# repo\n")
	run(t, dir, "add", "README.md")
	run(t, dir, "commit", "-m", "initial commit")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", name, err)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	got, err := c.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("CurrentBranch = %q, want main", got)
	}
}

func TestCurrentBranchInUnbornRepository(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-b", "main")

	got, err := New(dir).CurrentBranch(t.Context())
	if err != nil {
		t.Fatalf("CurrentBranch in unborn repository: %v", err)
	}
	if got != "main" {
		t.Fatalf("CurrentBranch in unborn repository = %q, want main", got)
	}
}

func TestRepositoryRootPreservesTrailingSpace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows normalizes trailing spaces in directory names")
	}
	dir := filepath.Join(t.TempDir(), "repo with trailing space ")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-b", "main")
	want, err := canonicalPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RepositoryRoot(context.Background(), dir)
	if err != nil {
		t.Fatalf("RepositoryRoot: %v", err)
	}
	if got != want {
		t.Fatalf("RepositoryRoot = %q, want %q", got, want)
	}
}

func TestProjectRootDoesNotEscapeIntoHomeRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	run(t, home, "init", "-b", "main")
	child := filepath.Join(home, "Documents", "new-project")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := canonicalPath(child)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProjectRoot(t.Context(), child)
	if err != nil {
		t.Fatalf("ProjectRoot: %v", err)
	}
	if got != want {
		t.Fatalf("ProjectRoot = %q, want child project %q", got, want)
	}
	if _, err := RepositoryIdentity(t.Context(), child); err == nil || !strings.Contains(err.Error(), "ambient ancestor") {
		t.Fatalf("RepositoryIdentity error = %v, want ambient ancestor rejection", err)
	}
	if _, err := NewProject(child).CurrentBranch(t.Context()); err == nil {
		t.Fatal("confined project client discovered the home repository")
	}
}

func TestCurrentBranchRejectsDetachedHEAD(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "switch", "--detach", "HEAD")

	branch, err := New(dir).CurrentBranch(context.Background())
	if !errors.Is(err, ErrDetachedHEAD) {
		t.Fatalf("CurrentBranch error = %v, want ErrDetachedHEAD", err)
	}
	if branch != "" {
		t.Fatalf("CurrentBranch = %q on detached HEAD, want empty", branch)
	}
}

func TestBranch(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	if err := c.Branch(ctx, "feat-x"); err != nil {
		t.Fatalf("Branch: %v", err)
	}
	got, _ := c.CurrentBranch(ctx)
	if got != "feat-x" {
		t.Errorf("after Branch, HEAD = %q, want feat-x", got)
	}
}

func TestBranchInvalid(t *testing.T) {
	c := New(initRepo(t))
	ctx := context.Background()
	for _, name := range []string{"", "bad name", "-leading", "a..b", "foo.lock", "head@{1}", "@"} {
		if err := c.Branch(ctx, name); err == nil {
			t.Errorf("Branch(%q) should fail", name)
		}
	}
}

func TestDeleteBranchIfOIDCompareAndSwap(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := t.Context()
	if err := c.Branch(ctx, "feat-cas"); err != nil {
		t.Fatal(err)
	}
	oid, err := c.BranchOID(ctx, "feat-cas")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteBranchIfOID(ctx, "feat-cas", oid); err != nil {
		t.Fatalf("DeleteBranchIfOID unchanged: %v", err)
	}
	if _, err := c.BranchOID(ctx, "feat-cas"); err == nil {
		t.Fatal("CAS-deleted branch still exists")
	}
}

func TestDeleteBranchIfOIDPreservesConcurrentCommit(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := t.Context()
	if err := c.Branch(ctx, "feat-concurrent"); err != nil {
		t.Fatal(err)
	}
	originalOID, err := c.BranchOID(ctx, "feat-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "concurrent.txt", "preserve me\n")
	run(t, dir, "add", "concurrent.txt")
	run(t, dir, "commit", "-m", "concurrent commit")
	concurrentOID, err := c.BranchOID(ctx, "feat-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if concurrentOID == originalOID {
		t.Fatal("test did not advance branch")
	}
	if err := c.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteBranchIfOID(ctx, "feat-concurrent", originalOID); err == nil {
		t.Fatal("CAS deletion accepted a concurrently advanced branch")
	}
	got, err := c.BranchOID(ctx, "feat-concurrent")
	if err != nil || got != concurrentOID {
		t.Fatalf("concurrent branch = %q, %v; want %q", got, err, concurrentOID)
	}
}

func TestDeleteBranchIfOIDRejectsInvalidExpectedOID(t *testing.T) {
	c := New(initRepo(t))
	if err := c.DeleteBranchIfOID(t.Context(), "main", "HEAD"); err == nil {
		t.Fatal("DeleteBranchIfOID accepted a symbolic expected OID")
	}
}

func TestWorktreeAddRemove(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	wt := filepath.Join(t.TempDir(), "wt name\n雪")
	if err := c.WorktreeAdd(ctx, wt, "feat-wt"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	registered, err := c.HasWorktree(ctx, wt)
	if err != nil || !registered {
		t.Fatalf("HasWorktree = %v, %v; want true", registered, err)
	}
	if registered, err := c.HasWorktree(ctx, t.TempDir()); err != nil || registered {
		t.Fatalf("HasWorktree(unrelated) = %v, %v; want false", registered, err)
	}
	if err := c.WorktreeRemove(ctx, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still exists: %v", err)
	}
}

func TestDiffNameStatusAndUnified(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	writeFile(t, dir, "lib.go", "package lib\n")
	writeFile(t, dir, "other.txt", "hello\n")
	run(t, dir, "add", "lib.go", "other.txt")
	run(t, dir, "commit", "-m", "add lib")

	changes, err := c.DiffNameStatus(ctx, "HEAD~1")
	if err != nil {
		t.Fatalf("DiffNameStatus: %v", err)
	}
	types := map[string]string{}
	for _, ch := range changes {
		types[ch.Path] = ch.Type
	}
	if types["lib.go"] != "A" {
		t.Errorf("lib.go change type = %q, want A; got %+v", types["lib.go"], changes)
	}
	if types["other.txt"] != "A" {
		t.Errorf("other.txt change type = %q, want A", types["other.txt"])
	}

	unified, err := c.DiffUnified(ctx, "HEAD~1")
	if err != nil {
		t.Fatalf("DiffUnified: %v", err)
	}
	if !strings.Contains(unified, "diff --git") || !strings.Contains(unified, "other.txt") {
		t.Errorf("unified diff missing expected markers: %q", unified)
	}

	run(t, dir, "rm", "-f", "other.txt")
	workingChanges, err := c.DiffNameStatus(ctx, "HEAD")
	if err != nil {
		t.Fatalf("DiffNameStatus vs HEAD: %v", err)
	}
	if len(workingChanges) != 1 || workingChanges[0].Path != "other.txt" || workingChanges[0].Type != "D" {
		t.Errorf("working-tree diff = %+v", workingChanges)
	}
}

func TestWorktreeDiffIncludesUntrackedWithoutChangingIndex(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	writeFile(t, dir, ".gitignore", "ignored.txt\n")
	run(t, dir, "add", ".gitignore")
	run(t, dir, "commit", "-m", "add ignore rules")
	writeFile(t, dir, "README.md", "# changed\n")
	writeFile(t, dir, "fresh.go", "package fresh\nfunc  New( ){ }\n")
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), []byte{0x00, 0xff, 0x01, 0xfe}, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "ignored.txt", "must not be reviewed\n")

	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")

	diff, err := c.WorktreeDiff(ctx, "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	for _, want := range []string{"README.md", "fresh.go", "func  New( ){ }", "payload.bin", "GIT binary patch", "new file mode 100755"} {
		if !strings.Contains(diff, want) {
			t.Errorf("worktree diff missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "ignored.txt") || strings.Contains(diff, "must not be reviewed") {
		t.Fatalf("worktree diff included ignored file:\n%s", diff)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("WorktreeDiff modified the real Git index")
	}
	if statusAfter := run(t, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"); statusAfter != statusBefore {
		t.Fatalf("WorktreeDiff changed checkout status:\nbefore %q\nafter  %q", statusBefore, statusAfter)
	}
}

func TestWorktreeDiffFromSubdirectoryCoversRepositoryRoot(t *testing.T) {
	dir := initRepo(t)
	nested := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "sibling.txt", "must be reviewed\n")

	c := New(nested)
	root, err := c.RepositoryRoot(context.Background())
	if err != nil {
		t.Fatalf("RepositoryRoot: %v", err)
	}
	wantRoot, err := canonicalPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != wantRoot {
		t.Fatalf("RepositoryRoot = %q, want %q", root, wantRoot)
	}
	diff, err := c.WorktreeDiff(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	if !strings.Contains(diff, "sibling.txt") || !strings.Contains(diff, "must be reviewed") {
		t.Fatalf("subdirectory worktree diff omitted root sibling:\n%s", diff)
	}
}

func TestWorktreeDiffRejectsDirtySubmodule(t *testing.T) {
	submoduleSource := initRepo(t)
	dir := initRepo(t)
	run(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", submoduleSource, "modules/dependency")
	run(t, dir, "commit", "-am", "add submodule")
	writeFile(t, filepath.Join(dir, "modules", "dependency"), "README.md", "dirty submodule\n")

	_, err := New(dir).WorktreeDiff(context.Background(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), "dirty submodule") || !strings.Contains(err.Error(), "modules/dependency") {
		t.Fatalf("WorktreeDiff error = %v, want explicit dirty-submodule refusal", err)
	}
}

func TestWorktreeDiffIncludesCleanSubmoduleCommitChange(t *testing.T) {
	submoduleSource := initRepo(t)
	dir := initRepo(t)
	run(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", submoduleSource, "modules/dependency")
	run(t, dir, "commit", "-am", "add submodule")
	submoduleDir := filepath.Join(dir, "modules", "dependency")
	run(t, submoduleDir, "config", "user.email", "test@maestro.local")
	run(t, submoduleDir, "config", "user.name", "Maestro Test")
	writeFile(t, submoduleDir, "README.md", "new committed state\n")
	run(t, submoduleDir, "add", "README.md")
	run(t, submoduleDir, "commit", "-m", "advance submodule")

	diff, err := New(dir).WorktreeDiff(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	if !strings.Contains(diff, "modules/dependency") || !strings.Contains(diff, "Subproject commit") {
		t.Fatalf("worktree diff omitted clean submodule commit change:\n%s", diff)
	}
}

func TestStatus(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	clean, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if clean.Dirty || len(clean.Files) != 0 {
		t.Errorf("clean repo status = %+v, want clean", clean)
	}

	writeFile(t, dir, "new.go", "package x\n")
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Dirty {
		t.Error("expected dirty status")
	}
	found := false
	for _, f := range st.Files {
		if f.Path == "new.go" && f.Worktree == '?' {
			found = true
		}
	}
	if !found {
		t.Errorf("untracked new.go missing from status: %+v", st.Files)
	}
}

func TestPathBearingGitOutputIsNULSafe(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	oldPath := "nested/old name\t雪\nline.txt"
	newPath := "nested/new name\té\nline.txt"
	modifiedPath := "tracked space\t日\nfile.txt"
	untrackedPath := "untracked dir/fresh name\tç\nfile.txt"
	dashPath := "-leading option.txt"

	writeFile(t, dir, oldPath, "rename me\n")
	writeFile(t, dir, modifiedPath, "before\n")
	run(t, dir, "add", "--", oldPath, modifiedPath)
	run(t, dir, "commit", "-m", "add unusual paths")

	if err := os.Rename(filepath.Join(dir, oldPath), filepath.Join(dir, newPath)); err != nil {
		t.Fatalf("rename unusual path: %v", err)
	}
	writeFile(t, dir, modifiedPath, "after\n")
	writeFile(t, dir, untrackedPath, "untracked\n")
	writeFile(t, dir, dashPath, "not an option\n")
	run(t, dir, "add", "-A", "--", oldPath, newPath)

	status, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantStatus := map[string]StatusEntry{
		newPath:       {Path: newPath, OldPath: oldPath, IndexState: 'R', Worktree: ' '},
		modifiedPath:  {Path: modifiedPath, IndexState: ' ', Worktree: 'M'},
		untrackedPath: {Path: untrackedPath, IndexState: '?', Worktree: '?'},
		dashPath:      {Path: dashPath, IndexState: '?', Worktree: '?'},
	}
	if len(status.Files) != len(wantStatus) {
		t.Fatalf("Status files = %+v, want %d exact entries", status.Files, len(wantStatus))
	}
	for _, entry := range status.Files {
		want, ok := wantStatus[entry.Path]
		if !ok {
			t.Errorf("unexpected status entry %+v", entry)
			continue
		}
		if entry != want {
			t.Errorf("status entry for %q = %+v, want %+v", entry.Path, entry, want)
		}
	}

	changes, err := c.DiffNameStatus(ctx, "HEAD")
	if err != nil {
		t.Fatalf("DiffNameStatus: %v", err)
	}
	wantChanges := map[string]FileChange{
		newPath:      {Path: newPath, OldPath: oldPath, Type: "R"},
		modifiedPath: {Path: modifiedPath, Type: "M"},
	}
	if len(changes) != len(wantChanges) {
		t.Fatalf("DiffNameStatus = %+v, want %d exact entries", changes, len(wantChanges))
	}
	for _, change := range changes {
		want, ok := wantChanges[change.Path]
		if !ok || change != want {
			t.Errorf("change for %q = %+v, want %+v", change.Path, change, want)
		}
	}

	all, err := c.AllChanges(ctx)
	if err != nil {
		t.Fatalf("AllChanges: %v", err)
	}
	wantAll := map[string]string{
		newPath:       "R",
		modifiedPath:  "M",
		untrackedPath: "A",
		dashPath:      "A",
	}
	if len(all) != len(wantAll) {
		t.Fatalf("AllChanges = %+v, want %d exact entries", all, len(wantAll))
	}
	for _, change := range all {
		if wantAll[change.Path] != change.Type {
			t.Errorf("AllChanges entry = %+v, want type %q", change, wantAll[change.Path])
		}
	}

	untracked, err := c.UntrackedFiles(ctx)
	if err != nil {
		t.Fatalf("UntrackedFiles: %v", err)
	}
	wantUntracked := map[string]bool{dashPath: true, untrackedPath: true}
	if len(untracked) != len(wantUntracked) {
		t.Fatalf("UntrackedFiles = %q, want %d exact paths", untracked, len(wantUntracked))
	}
	for _, path := range untracked {
		if !wantUntracked[path] {
			t.Errorf("unexpected untracked path %q", path)
		}
	}

	stats, err := c.DiffNumStat(ctx, "HEAD")
	if err != nil {
		t.Fatalf("DiffNumStat: %v", err)
	}
	wantStats := map[string]bool{newPath: true, modifiedPath: true}
	if len(stats) != len(wantStats) {
		t.Fatalf("DiffNumStat = %+v, want %d exact entries", stats, len(wantStats))
	}
	for _, stat := range stats {
		if !wantStats[stat.Path] {
			t.Errorf("unexpected numstat path %q", stat.Path)
		}
	}

	// Add must place -- before user-controlled paths so a leading dash is data.
	if err := c.Add(ctx, dashPath); err != nil {
		t.Fatalf("Add leading-dash path: %v", err)
	}
}

func TestCommitValidation(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	long := strings.Repeat("x", 73)
	tests := []struct {
		name    string
		message string
		ok      bool
	}{
		{"valid", "feat: add thing", true},
		{"valid with body", "feat: add thing\n\nBody here.", true},
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"too long", long, false},
		{"trailing space", "feat: add thing ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeFile(t, dir, "staged.txt", tt.name+"\n")
			if err := c.Add(ctx, "staged.txt"); err != nil {
				t.Fatalf("Add: %v", err)
			}
			err := c.Commit(ctx, tt.message)
			if tt.ok && err != nil {
				t.Errorf("Commit(%q) = %v, want nil", tt.message, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("Commit(%q) should fail", tt.message)
			}
		})
	}
}

func TestAddAndCommit(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()

	writeFile(t, dir, "a.go", "package a\n")
	if err := c.Add(ctx, "a.go"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := c.Commit(ctx, "feat: add a.go"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Dirty {
		t.Errorf("repo should be clean after commit, got %+v", st)
	}
}

func TestCommitHash(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	hash, err := c.CommitHash(context.Background())
	if err != nil {
		t.Fatalf("CommitHash: %v", err)
	}
	if len(hash) != 7 {
		t.Errorf("CommitHash = %q, want 7 chars", hash)
	}
}

func TestIsRepo(t *testing.T) {
	if !New(initRepo(t)).IsRepo(context.Background()) {
		t.Error("initRepo dir should be a repo")
	}
	if New(t.TempDir()).IsRepo(context.Background()) {
		t.Error("plain dir should not be a repo")
	}
}

func TestParseNameStatus(t *testing.T) {
	changes, err := parseNameStatusZ([]byte("A\x00new name\t雪\n.go\x00M\x00mod.go\x00D\x00old.go\x00R100\x00from name\n.go\x00to name\t.go\x00"))
	if err != nil {
		t.Fatalf("parseNameStatusZ: %v", err)
	}
	if len(changes) != 4 {
		t.Fatalf("parsed %d changes, want 4: %+v", len(changes), changes)
	}
	if changes[0].Path != "new name\t雪\n.go" {
		t.Errorf("added path = %q", changes[0].Path)
	}
	if changes[3].Type != "R" || changes[3].OldPath != "from name\n.go" || changes[3].Path != "to name\t.go" {
		t.Errorf("rename change = %+v", changes[3])
	}
}

func TestNULParsersRejectTruncatedRecords(t *testing.T) {
	if _, err := parseNameStatusZ([]byte("M\x00file")); err == nil {
		t.Error("parseNameStatusZ should reject output without final NUL")
	}
	if _, err := parseStatusZ([]byte("R  new\x00")); err == nil {
		t.Error("parseStatusZ should reject a rename without its source path")
	}
	if _, err := parseNumStatZ([]byte("1\t2\t\x00old\x00")); err == nil {
		t.Error("parseNumStatZ should reject a rename without its destination path")
	}
}

func TestDiffNumStat(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	// Modify README.md: +2/-1.
	writeFile(t, dir, "README.md", "# repo\n\nnew line 1\nnew line 2\n")
	// Delete nothing; add an untracked file is not part of the diff.
	stats, err := c.DiffNumStat(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("DiffNumStat: %v", err)
	}
	if len(stats) != 1 || stats[0].Path != "README.md" {
		t.Fatalf("stats = %+v", stats)
	}
	if stats[0].Additions != 3 || stats[0].Removals != 0 {
		t.Errorf("numstat = %+v, want +3/-0", stats[0])
	}
}

func TestUntrackedFiles(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	writeFile(t, dir, "fresh.go", "package main\n")
	paths, err := c.UntrackedFiles(context.Background())
	if err != nil {
		t.Fatalf("UntrackedFiles: %v", err)
	}
	if len(paths) != 1 || paths[0] != "fresh.go" {
		t.Errorf("untracked = %v", paths)
	}
	// Commit it and the list empties.
	run(t, dir, "add", "fresh.go")
	run(t, dir, "commit", "-m", "add fresh")
	paths, _ = c.UntrackedFiles(context.Background())
	if len(paths) != 0 {
		t.Errorf("untracked after commit = %v", paths)
	}
}
