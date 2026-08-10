package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

func newWorkspaceOrchestrator(t *testing.T, dir, sessionsDir string) *Orchestrator {
	t.Helper()
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: sessionsDir, In: strings.NewReader("n\n"),
		Out: &bytes.Buffer{}, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return orch
}

func TestCreateWorkspaceUsesManagedPathAndFreshPersistentSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	orch := newWorkspaceOrchestrator(t, dir, sessionsDir)
	if err := os.WriteFile(filepath.Join(dir, "dirty-source.txt"), []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := orch.CreateWorkspace(t.Context(), "feature/safe-workspace")
	if err != nil {
		t.Fatal(err)
	}
	wantParent, err := canonicalProjectDir(filepath.Join(home, ".maestro", "worktrees", created.Project))
	if err != nil {
		t.Fatal(err)
	}
	if !pathContains(wantParent, created.Worktree) || created.WorkspaceRef != "refs/heads/feature/safe-workspace" || created.ManagedWorktree || created.Phase != session.PhaseChat {
		t.Fatalf("created session = %+v", created)
	}
	if _, err := os.Stat(filepath.Join(created.Worktree, "dirty-source.txt")); !os.IsNotExist(err) {
		t.Fatalf("dirty source leaked into workspace: %v", err)
	}
	if info, err := os.Stat(created.Worktree); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("managed path mode = %v, %v", info, err)
	}
	if orch.WorkDirDisplay() != created.Worktree || orch.Session().ID != created.ID {
		t.Fatalf("workspace not published: dir=%q session=%+v", orch.WorkDirDisplay(), orch.Session())
	}
	active, err := orch.sessions.Active(t.Context(), created.Project)
	if err != nil || active != created.ID {
		t.Fatalf("active session = %q, %v", active, err)
	}
}

func TestSelectWorkspaceRejectsLockedAndDetachedThenLoadsHealthy(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	orch := newWorkspaceOrchestrator(t, dir, sessionsDir)
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, dir, "worktree", "add", "-b", "feature/select", linked, "HEAD")
	gitRun(t, dir, "worktree", "lock", linked)
	if _, err := orch.SelectWorkspace(t.Context(), linked); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("locked selection error = %v", err)
	}
	gitRun(t, dir, "worktree", "unlock", linked)
	gitRun(t, linked, "switch", "--detach", "HEAD")
	if _, err := orch.SelectWorkspace(t.Context(), linked); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("detached selection error = %v", err)
	}
	gitRun(t, linked, "switch", "feature/select")
	selected, err := orch.SelectWorkspace(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if filepathKey(selected.Worktree) != filepathKey(linked) || selected.WorkspaceRef != "refs/heads/feature/select" || selected.ManagedWorktree || filepathKey(orch.WorkDirDisplay()) != filepathKey(linked) {
		t.Fatalf("selected = %+v route=%q", selected, orch.WorkDirDisplay())
	}
}

func TestWorkspaceChangePhaseGuardPrecedesMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orch := newWorkspaceOrchestrator(t, newTestRepo(t), filepath.Join(t.TempDir(), "sessions"))
	orch.sess.Phase = session.PhaseBuild
	before := orch.Session()
	if _, err := orch.CreateWorkspace(t.Context(), "feature/blocked"); err == nil || !strings.Contains(err.Error(), "chat or propose") {
		t.Fatalf("phase error = %v", err)
	}
	if orch.Session().ID != before.ID {
		t.Fatal("blocked workspace change mutated the session")
	}
	if _, err := os.Stat(filepath.Join(home, ".maestro", "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("blocked workspace change created managed storage: %v", err)
	}
}

func TestCreateWorkspaceRejectsSymlinkedManagedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	redirect := t.TempDir()
	if err := os.Symlink(redirect, filepath.Join(home, ".maestro")); err != nil {
		t.Fatal(err)
	}
	orch := newWorkspaceOrchestrator(t, newTestRepo(t), filepath.Join(t.TempDir(), "sessions"))
	if _, err := orch.CreateWorkspace(t.Context(), "feature/no-symlink"); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlinked root error = %v", err)
	}
	if entries, err := os.ReadDir(redirect); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target was mutated: entries=%v err=%v", entries, err)
	}
}

func TestLinkedWorktreesShareProjectSessionNamespace(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	first := newWorkspaceOrchestrator(t, dir, sessionsDir)
	if err := first.RenameSession(t.Context(), "Shared repository session"); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "other-checkout")
	gitRun(t, dir, "worktree", "add", "-b", "feature/other", linked, "HEAD")
	second := newWorkspaceOrchestrator(t, linked, sessionsDir)
	if second.sess.Project != first.sess.Project || second.sess.ID != first.sess.ID || second.sess.Title != "Shared repository session" {
		t.Fatalf("main session=%+v linked session=%+v", first.sess, second.sess)
	}
}

func TestFreshSessionsPersistExactCheckoutAcrossLinkedWorktrees(t *testing.T) {
	dir := newTestRepo(t)
	linked := filepath.Join(t.TempDir(), "linked-checkout")
	gitRun(t, dir, "worktree", "add", "-b", "feature/linked-session", linked, "HEAD")
	linked, err := canonicalProjectDir(linked)
	if err != nil {
		t.Fatal(err)
	}
	mainDir, err := canonicalProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	mainOrch := newWorkspaceOrchestrator(t, mainDir, filepath.Join(t.TempDir(), "main-sessions"))
	linkedOrch := newWorkspaceOrchestrator(t, linked, filepath.Join(t.TempDir(), "linked-sessions"))
	if mainOrch.sess.Worktree != mainDir || mainOrch.sess.WorkspaceRef != "refs/heads/main" {
		t.Fatalf("main fresh identity = %+v", mainOrch.sess)
	}
	if linkedOrch.sess.Worktree != linked || linkedOrch.sess.WorkspaceRef != "refs/heads/feature/linked-session" {
		t.Fatalf("linked fresh identity = %+v", linkedOrch.sess)
	}
	for _, orch := range []*Orchestrator{mainOrch, linkedOrch} {
		persisted, loadErr := orch.sessions.Load(t.Context(), orch.sess.Project, orch.sess.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if persisted.Worktree != orch.sess.Worktree || persisted.WorkspaceRef != orch.sess.WorkspaceRef || persisted.Revision == 0 {
			t.Fatalf("fresh identity not durable: memory=%+v disk=%+v", orch.sess, persisted)
		}
	}
}

func TestSharedSessionAlwaysRoutesToItsPersistedWorktree(t *testing.T) {
	dir := newTestRepo(t)
	linked := filepath.Join(t.TempDir(), "linked-checkout")
	gitRun(t, dir, "worktree", "add", "-b", "feature/other-checkout", linked, "HEAD")
	mainDir, err := canonicalProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	mainOrch := newWorkspaceOrchestrator(t, mainDir, sessionsDir)
	fromLinked := newWorkspaceOrchestrator(t, linked, sessionsDir)
	if fromLinked.sess.ID != mainOrch.sess.ID || fromLinked.WorkDirDisplay() != mainDir {
		t.Fatalf("shared session teleported to caller checkout: main=%+v linked=%+v route=%q", mainOrch.sess, fromLinked.sess, fromLinked.WorkDirDisplay())
	}
	if err := fromLinked.Chat(t.Context(), "keep this conversation on its exact checkout"); err != nil {
		t.Fatal(err)
	}
	persisted, err := fromLinked.sessions.Load(t.Context(), fromLinked.sess.Project, fromLinked.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Worktree != mainDir || persisted.WorkspaceRef != "refs/heads/main" || len(persisted.Conversation) != 2 {
		t.Fatalf("chat routing identity = %+v", persisted)
	}
	path := filepath.Join(sessionsDir, persisted.Project, persisted.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := mainOrch.Chat(t.Context(), "this process still holds a stale lifecycle snapshot"); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("stale orchestrator Chat error = %v, want session.ErrConflict", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("stale orchestrator changed the durable session")
	}
}

func TestLegacySessionWithoutIdentityFailsClosedWhenWorktreeIsAmbiguous(t *testing.T) {
	dir := newTestRepo(t)
	linked := filepath.Join(t.TempDir(), "linked-checkout")
	gitRun(t, dir, "worktree", "add", "-b", "feature/legacy-ambiguity", linked, "HEAD")
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	viewer := newWorkspaceOrchestrator(t, dir, sessionsDir)
	project := viewer.sess.Project
	store := viewer.sessions
	legacy := session.New(project)
	legacy.ID = "legacy-no-workspace"
	created, err := store.Commit(t.Context(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(t.Context(), project, created.ID); err != nil {
		t.Fatal(err)
	}
	summaries, err := viewer.ListSessionSummaries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	for _, summary := range summaries {
		if summary.ID == created.ID {
			disabled = summary.Disabled && strings.Contains(summary.DisabledReason, "ambiguous")
		}
	}
	if !disabled {
		t.Fatalf("ambiguous legacy session was not disabled: %+v", summaries)
	}
	path := filepath.Join(sessionsDir, project, created.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(t.Context(), Options{ProjectDir: linked, SessionsDir: sessionsDir, In: strings.NewReader(""), Out: &bytes.Buffer{}, Runner: &fakeRunner{}})
	if err == nil || !strings.Contains(err.Error(), "no workspace identity") {
		t.Fatalf("ambiguous legacy startup error = %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("ambiguous legacy startup mutated the session record")
	}
}

func TestLegacySessionWithoutIdentityMigratesOnlyUniqueWorktree(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	project := projectSessionKey(dir)
	store := session.NewStore(sessionsDir)
	legacy := session.New(project)
	legacy.ID = "legacy-unique-workspace"
	created, err := store.Commit(t.Context(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(t.Context(), project, created.ID); err != nil {
		t.Fatal(err)
	}
	orch := newWorkspaceOrchestrator(t, dir, sessionsDir)
	want, err := canonicalProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if orch.sess.Worktree != want || orch.sess.WorkspaceRef != "refs/heads/main" || orch.sess.Revision <= created.Revision {
		t.Fatalf("unique legacy migration = before %+v after %+v", created, orch.sess)
	}
}

func TestAcceptOwnershipSeparatesPersistentAndManagedWorktrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := newTestRepo(t)
	orch := newWorkspaceOrchestrator(t, dir, filepath.Join(t.TempDir(), "sessions"))
	persistent, err := orch.CreateWorkspace(t.Context(), "feature/persistent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Propose(t.Context(), "Add persistent workspace behavior"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if orch.sess.Worktree != persistent.Worktree || orch.sess.ManagedWorktree {
		t.Fatalf("accept stay changed persistent ownership: %+v", orch.sess)
	}

	other := newWorkspaceOrchestrator(t, dir, filepath.Join(t.TempDir(), "other-sessions"))
	if _, err := other.Propose(t.Context(), "Add managed accept worktree behavior"); err != nil {
		t.Fatal(err)
	}
	name := "accept-managed-" + filepath.Base(t.TempDir())
	if _, err := other.Accept(t.Context(), BranchChoice{Kind: "worktree", Name: name}); err != nil {
		t.Fatal(err)
	}
	if other.sess.Worktree == "" || !other.sess.ManagedWorktree {
		t.Fatalf("accept worktree ownership = %+v", other.sess)
	}
}

func TestMergeRetainsExternallySelectedWorkspace(t *testing.T) {
	dir := newTestRepo(t)
	linked := filepath.Join(t.TempDir(), "external")
	gitRun(t, dir, "worktree", "add", "-b", "feature/external", linked, "HEAD")
	if err := os.WriteFile(filepath.Join(linked, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, linked, "add", "feature.txt")
	gitRun(t, linked, "commit", "-m", "feature")
	orch := newWorkspaceOrchestrator(t, dir, filepath.Join(t.TempDir(), "sessions"))
	orch.sess.Branch = "feature/external"
	orch.sess.BaseBranch = "main"
	orch.sess.Worktree = linked
	orch.sess.ManagedWorktree = false
	orch.installWorkspace(linked, git.New(linked), spec.NewStore(filepath.Join(linked, "specs")))
	hooks := t.TempDir()
	if err := orch.mergeBranch(context.Background(), hooks); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(linked); err != nil {
		t.Fatalf("persistent worktree was removed: %v", err)
	}
	registered, err := git.New(dir).HasWorktree(t.Context(), linked)
	if err != nil || !registered {
		t.Fatalf("persistent worktree registration = %v, %v", registered, err)
	}
}
