package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// fakeRunner simulates a dev sub-agent: it records prompts and writes a file
// into the current workdir (resolved at run time) so git has a diff.
type fakeRunner struct {
	Wd      func() string
	prompts []string
	files   []string
}

func (*fakeRunner) maestroReadOnlySkillRunner() {}
func (*fakeRunner) maestroPrivateLearnRunner()  {}

func (f *fakeRunner) Run(ctx context.Context, role agentcore.Role, taskPrompt string) (agentcore.AgentResult, error) {
	f.prompts = append(f.prompts, taskPrompt)
	if role == agentcore.RoleDev {
		dir := "."
		if f.Wd != nil {
			dir = f.Wd()
		}
		name := fmt.Sprintf("implemented_%d.go", len(f.prompts))
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package main\n"), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		f.files = append(f.files, name)
	}
	return agentcore.AgentResult{Role: string(role), OK: true, Summary: "fake round done"}, nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newTestRepo creates a git repo with a minimal Go module.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@maestro.local")
	gitRun(t, dir, "config", "user.name", "Maestro Test")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	write("go.mod", "module testrepo\n\ngo 1.26\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("README.md", "# repo\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "initial commit")
	return dir
}

func newTestOrch(t *testing.T, dir string, runner Runner) *Orchestrator {
	t.Helper()
	t.Setenv("MAESTRO_MEMORY_DIR", filepath.Join(t.TempDir(), "mem"))
	t.Setenv("MAESTRO_CHECKPOINTS_DIR", filepath.Join(t.TempDir(), "cps"))
	var out bytes.Buffer
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In:          strings.NewReader("n\n"), // decline any confirmation by default
		Out:         &out,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if fr, ok := runner.(*fakeRunner); ok {
		fr.Wd = func() string { return orch.WorkDirDisplay() }
	}
	return orch
}

func cmdOf(cmd string, flags map[string]string) Command {
	return Command{Cmd: cmd, Flags: flags}
}

func TestEventStreamIsLosslessAndMonotonic(t *testing.T) {
	orch := &Orchestrator{Stream: make(chan agentcore.StreamEvent, 256)}
	const count = 1000
	done := make(chan struct{})
	go func() {
		for i := 0; i < count; i++ {
			orch.emit(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "x"}))
		}
		close(done)
	}()
	for want := uint64(1); want <= count; want++ {
		select {
		case ev := <-orch.Stream:
			if ev.Seq != want {
				t.Fatalf("event seq = %d, want %d", ev.Seq, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after event %d", want-1)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not finish")
	}
}

func TestFullHeadlessPipeline(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add a postgres API with DATABASE_URL support"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if orch.Phase() != session.PhasePropose {
		t.Fatalf("phase = %q, want propose", orch.Phase())
	}

	if _, err := orch.Accept(ctx, BranchChoice{Kind: "branch", Name: "feat-pg"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if orch.Phase() != session.PhaseSpec {
		t.Fatalf("phase = %q, want spec", orch.Phase())
	}
	branch := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current"))
	if branch != "feat-pg" {
		t.Errorf("branch = %q, want feat-pg", branch)
	}
	specID := orch.ActiveSpec().ID
	if !strings.HasPrefix(specID, "add-a-postgres-api-with-database-url") {
		t.Errorf("spec id = %q", specID)
	}
	for _, f := range []string{"spec.md", "design.md", "tasks.md"} {
		if _, err := os.Stat(filepath.Join(dir, "specs", specID, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}

	if err := orch.Dispatch(ctx, cmdOf("build", nil)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if orch.Phase() != session.PhaseBuild {
		t.Fatalf("phase = %q, want build", orch.Phase())
	}
	if len(runner.prompts) != 1 || !strings.Contains(runner.prompts[0], "SPEC") {
		t.Errorf("dev prompt = %d prompts, first missing spec content", len(runner.prompts))
	}
	for _, instruction := range []string{
		"Never modify\nspec.md or design.md",
		"update only its\ncheckbox in tasks.md from [ ] to [x]",
		"never rewrite task text or mark an\nunverified task complete",
	} {
		if !strings.Contains(runner.prompts[0], instruction) {
			t.Errorf("dev prompt missing task-integrity instruction %q:\n%s", instruction, runner.prompts[0])
		}
	}

	verdict, err := orch.Review(ctx)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
	if verdict.VerdictLevel() == "fail" {
		t.Errorf("verdict should not fail: %+v", verdict.Items)
	}

	if err := orch.Dispatch(ctx, cmdOf("fix", nil)); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if orch.Phase() != session.PhaseBuild {
		t.Fatalf("phase = %q, want build after fix", orch.Phase())
	}
	if len(runner.prompts) != 2 {
		t.Errorf("fix should have re-run the dev round: %d prompts", len(runner.prompts))
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatalf("Review after fix: %v", err)
	}

	draftPath, draftContent, err := orch.DocsDraft(ctx)
	if err != nil {
		t.Fatalf("DocsDraft: %v", err)
	}
	if strings.TrimSpace(draftContent) == "" {
		t.Fatal("DocsDraft returned empty content")
	}
	if _, err := os.Stat(draftPath); !os.IsNotExist(err) {
		t.Fatalf("DocsDraft wrote before acceptance: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("drafting docs advanced phase to %q", orch.Phase())
	}

	if err := orch.Docs(ctx); err != nil {
		t.Fatalf("Docs: %v", err)
	}
	if orch.Phase() != session.PhaseDocs {
		t.Fatalf("phase = %q, want docs", orch.Phase())
	}
	adrDir := filepath.Join(dir, "docs-archive", "adr")
	entries, err := os.ReadDir(adrDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ADR dir = %v, %v", entries, err)
	}

	if err := orch.Archive(ctx, ArchiveOptions{Yes: true, Merge: true}); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if orch.Phase() != session.PhaseChat {
		t.Fatalf("phase = %q, want chat after archive", orch.Phase())
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID)); !os.IsNotExist(err) {
		t.Error("spec folder should be archived")
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID, "spec.md")); err != nil {
		t.Errorf("archived spec missing: %v", err)
	}
	log := gitRun(t, dir, "log", "--oneline", "-3")
	if !strings.Contains(log, "feat(add-a-postgres-api-with…):") || !strings.Contains(log, "postgres") {
		t.Errorf("commit message missing from log:\n%s", log)
	}
	if branch := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); branch != "main" {
		t.Errorf("after merge, branch = %q, want main", branch)
	}
}

func TestProposeEditCancel(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add auth"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if err := orch.Edit(ctx, "scope to OAuth only"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if !strings.Contains(orch.Session().Draft.Body, "OAuth only") {
		t.Error("edit note missing from draft")
	}
	if err := orch.Dispatch(ctx, cmdOf("accept", map[string]string{"branch": "feat-auth"})); err != nil {
		t.Fatalf("accept after edit: %v", err)
	}
	if orch.ActiveSpec().Category != "feat" {
		t.Errorf("category = %q", orch.ActiveSpec().Category)
	}

	orch2 := newTestOrch(t, dir, &fakeRunner{})
	if err := orch2.Dispatch(ctx, cmdOf("propose", map[string]string{"m": "fix the timeout bug"})); err != nil {
		t.Fatalf("Propose 2: %v", err)
	}
	if orch2.ActiveSpec() != nil {
		t.Error("second propose should not touch the accepted spec")
	}
	if err := orch2.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if orch2.Phase() != session.PhaseChat {
		t.Errorf("phase = %q, want chat", orch2.Phase())
	}
}

func TestResumeRestoresPhaseAndDraft(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	ctx := context.Background()

	mk := func() *Orchestrator {
		t.Helper()
		var out bytes.Buffer
		orch, err := New(ctx, Options{
			ProjectDir:  dir,
			SessionsDir: sessionsDir,
			In:          strings.NewReader(""),
			Out:         &out,
			Runner:      &fakeRunner{},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return orch
	}

	orch := mk()
	if _, err := orch.Propose(ctx, "Add retries"); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	restored := mk() // new process, same sessions dir
	if restored.Phase() != session.PhasePropose {
		t.Errorf("restored phase = %q, want propose", restored.Phase())
	}
	if restored.Session().Draft == nil || restored.Session().Draft.ID != "add-retries" {
		t.Errorf("restored draft = %+v", restored.Session().Draft)
	}
	if _, err := restored.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("accept on restored session: %v", err)
	}
	if restored.Phase() != session.PhaseSpec {
		t.Errorf("phase = %q, want spec", restored.Phase())
	}
}

func TestAcceptRequiresIsolationForDirtyCheckout(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add retries"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "user.txt")
	wantStatus := gitRun(t, dir, "status", "--porcelain=v1")

	for _, choice := range []BranchChoice{{Kind: "stay"}, {Kind: "branch", Name: "feat-unsafe"}} {
		if _, err := orch.Accept(ctx, choice); err == nil || !strings.Contains(err.Error(), "--worktree") {
			t.Fatalf("Accept(%s) error = %v, want isolation guidance", choice.Kind, err)
		}
		if branch := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); branch != "main" {
			t.Fatalf("failed accept switched branch to %q", branch)
		}
		if got := gitRun(t, dir, "status", "--porcelain=v1"); got != wantStatus {
			t.Fatalf("failed accept changed user state: got %q, want %q", got, wantStatus)
		}
	}

	if _, err := orch.Accept(ctx, BranchChoice{Kind: "worktree", Name: "feat-isolated"}); err != nil {
		t.Fatalf("Accept(worktree): %v", err)
	}
	if got := gitRun(t, dir, "status", "--porcelain=v1"); got != wantStatus {
		t.Fatalf("worktree accept changed base checkout: got %q, want %q", got, wantStatus)
	}
}

func TestAcceptRejectsDetachedHEAD(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add retries"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "switch", "--detach", "HEAD")

	for _, choice := range []BranchChoice{
		{Kind: "stay"},
		{Kind: "branch", Name: "feat-detached"},
		{Kind: "worktree", Name: "feat-detached-worktree"},
	} {
		if _, err := orch.Accept(ctx, choice); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
			t.Fatalf("Accept(%s) error = %v, want detached HEAD rejection", choice.Kind, err)
		}
	}
	if orch.Phase() != session.PhasePropose || orch.Session().Draft == nil {
		t.Fatalf("failed accept mutated proposal state: phase=%q draft=%+v", orch.Phase(), orch.Session().Draft)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD")); got != "HEAD" {
		t.Fatalf("failed accept moved detached HEAD to %q", got)
	}
}

func TestProjectSessionKeySeparatesSameBasename(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	projectA := filepath.Join(rootA, "api")
	projectB := filepath.Join(rootB, "api")
	if err := os.Mkdir(projectA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(projectB, 0o755); err != nil {
		t.Fatal(err)
	}
	if projectSessionKey(projectA) == projectSessionKey(projectB) {
		t.Fatalf("same-basename repositories share session key %q", projectSessionKey(projectA))
	}
}

func TestNewCanonicalizesGitSubdirectoryToRepositoryRoot(t *testing.T) {
	dir := newTestRepo(t)
	nested := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root-sibling.txt"), []byte("review me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orch, err := New(t.Context(), Options{
		ProjectDir:  nested,
		SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New from subdirectory: %v", err)
	}
	wantRoot, err := canonicalProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := orch.WorkDirDisplay(); got != wantRoot {
		t.Fatalf("work dir = %q, want repository root %q", got, wantRoot)
	}
	if orch.baseDir != wantRoot || orch.specsDir != filepath.Join(wantRoot, "specs") {
		t.Fatalf("root routing = base %q specs %q, want %q", orch.baseDir, orch.specsDir, wantRoot)
	}
	if orch.Session().Project != projectSessionKey(wantRoot) {
		t.Fatalf("session project = %q, want root-scoped %q", orch.Session().Project, projectSessionKey(wantRoot))
	}
	diff, err := orch.workspaceRoute().git.WorktreeDiff(t.Context(), "HEAD")
	if err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	if !strings.Contains(diff, "root-sibling.txt") {
		t.Fatalf("root sibling omitted from review diff:\n%s", diff)
	}
}

func TestNewDoesNotResumeHomeRepositorySessionFromChildProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAESTRO_MEMORY_DIR", filepath.Join(t.TempDir(), "memory"))
	t.Setenv("MAESTRO_CHECKPOINTS_DIR", filepath.Join(t.TempDir(), "checkpoints"))
	gitRun(t, home, "init", "-b", "main")
	sessionsDir := filepath.Join(t.TempDir(), "sessions")

	homeOrch, err := New(t.Context(), Options{
		ProjectDir: home, SessionsDir: sessionsDir, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New home project: %v", err)
	}
	if err := homeOrch.RenameSession(t.Context(), "Home repository session"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}

	child := filepath.Join(home, "Documents", "new-python-tool")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	childOrch, err := New(t.Context(), Options{
		ProjectDir: child, SessionsDir: sessionsDir, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New child project: %v", err)
	}
	wantChild, err := canonicalProjectDir(child)
	if err != nil {
		t.Fatal(err)
	}
	if childOrch.WorkDirDisplay() != wantChild {
		t.Fatalf("child work dir = %q, want %q", childOrch.WorkDirDisplay(), wantChild)
	}
	if childOrch.Session().ID == homeOrch.Session().ID || childOrch.Session().Project == homeOrch.Session().Project {
		t.Fatalf("child resumed home session: home=%+v child=%+v", homeOrch.Session(), childOrch.Session())
	}
	if childOrch.Session().Title != "" || childOrch.Session().Worktree != "" || childOrch.Session().WorkspaceRef != "" {
		t.Fatalf("child inherited home session state: %+v", childOrch.Session())
	}
}

func TestNewStartsInPlainAndUnbornWorkspaces(t *testing.T) {
	t.Run("plain directory", func(t *testing.T) {
		dir := t.TempDir()
		orch := newTestOrch(t, dir, &fakeRunner{})
		if orch.Session().Worktree != "" || orch.Session().WorkspaceRef != "" {
			t.Fatalf("plain workspace received Git identity: worktree=%q ref=%q", orch.Session().Worktree, orch.Session().WorkspaceRef)
		}
	})

	t.Run("unborn Git repository", func(t *testing.T) {
		dir := t.TempDir()
		gitRun(t, dir, "init", "-b", "main")
		orch := newTestOrch(t, dir, &fakeRunner{})
		wantDir, err := canonicalProjectDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if orch.Session().Worktree != wantDir || orch.Session().WorkspaceRef != "refs/heads/main" {
			t.Fatalf("unborn workspace identity = worktree %q ref %q, want %q and refs/heads/main", orch.Session().Worktree, orch.Session().WorkspaceRef, wantDir)
		}
	})
}

func TestSessionListAndLoadSelectedSession(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	project := orch.sess.Project
	first := session.New(project)
	first.ID = "100"
	first.DraftPrompt = "first session"
	second := session.New(project)
	second.ID = "200"
	second.DraftPrompt = "second session"
	for _, saved := range []session.Session{first, second} {
		if err := orch.sessions.Save(context.Background(), saved); err != nil {
			t.Fatal(err)
		}
	}
	if got := orch.SessionList(); !slices.Contains(got, "200") || !slices.Contains(got, "100") || !slices.Contains(got, orch.sess.ID) {
		t.Fatalf("SessionList = %v, want saved sessions plus current %q", got, orch.sess.ID)
	}
	if err := orch.LoadSession(context.Background(), "100"); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got := orch.Session(); got.ID != "100" || got.DraftPrompt != "first session" {
		t.Fatalf("loaded session = %+v", got)
	}
}

func TestLoadSessionRejectsUnregisteredWorktreeWithoutMutation(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	before := orch.Session()
	tampered := session.New(before.Project)
	tampered.ID = "tampered"
	tampered.Worktree = t.TempDir()
	tampered.Branch = "outside"
	if err := orch.sessions.Save(context.Background(), tampered); err != nil {
		t.Fatal(err)
	}
	if err := orch.LoadSession(context.Background(), tampered.ID); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("LoadSession error = %v", err)
	}
	if got := orch.Session(); got.ID != before.ID || orch.WorkDirDisplay() != orch.baseDir {
		t.Fatalf("failed load mutated live state: session=%+v dir=%q", got, orch.WorkDirDisplay())
	}
}

func TestFeatureStoresSeparateSameBasenameRepositories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_MEMORY_DIR", "")
	t.Setenv("MAESTRO_CHECKPOINTS_DIR", "")
	root := t.TempDir()
	initRepo := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "init", "-b", "main")
		gitRun(t, dir, "config", "user.email", "test@maestro.local")
		gitRun(t, dir, "config", "user.name", "Maestro Test")
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testrepo\n\ngo 1.26\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "add", ".")
		gitRun(t, dir, "commit", "-m", "initial")
	}
	dirA := filepath.Join(root, "one", "api")
	dirB := filepath.Join(root, "two", "api")
	initRepo(dirA)
	initRepo(dirB)
	newOrch := func(dir string) *Orchestrator {
		t.Helper()
		o, err := New(context.Background(), Options{
			ProjectDir: dir, SessionsDir: filepath.Join(root, "sessions"),
			In: strings.NewReader(""), Out: &bytes.Buffer{}, Runner: &fakeRunner{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	orchA, orchB := newOrch(dirA), newOrch(dirB)
	if err := orchA.Remember(context.Background(), "private to A", nil); err != nil {
		t.Fatal(err)
	}
	if facts := orchB.features.memory.All(context.Background()); len(facts) != 0 {
		t.Fatalf("same-basename repository leaked memory: %+v", facts)
	}
}

func TestResumeRejectsUnregisteredWorktree(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	ctx := context.Background()
	newOrchestrator := func() *Orchestrator {
		t.Helper()
		o, err := New(ctx, Options{
			ProjectDir: dir, SessionsDir: sessionsDir,
			In: strings.NewReader(""), Out: &bytes.Buffer{}, Runner: &fakeRunner{},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return o
	}

	orch := newOrchestrator()
	wantDir := orch.WorkDirDisplay()
	orch.sess.Worktree = t.TempDir()
	orch.sess.Branch = "tampered"
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}
	_, err := New(ctx, Options{
		ProjectDir: dir, SessionsDir: sessionsDir,
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Runner: &fakeRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unsafe restore error = %v, want unregistered worktree", err)
	}
	if orch.WorkDirDisplay() != wantDir {
		t.Fatalf("failed restore mutated original orchestrator route: %q", orch.WorkDirDisplay())
	}
}

func TestProposeRecoversCompletedArchivePhase(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.sess.Phase = session.PhaseArchive
	orch.sess.SpecID = "already-archived"
	orch.spec = nil
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Propose(context.Background(), "add pod discovery"); err != nil {
		t.Fatalf("propose after completed archive: %v", err)
	}
	if orch.Phase() != session.PhasePropose || orch.Session().Draft == nil || orch.Session().SpecID != "" {
		t.Fatalf("archive recovery state: phase=%q spec=%q draft=%+v", orch.Phase(), orch.Session().SpecID, orch.Session().Draft)
	}
}

func TestStructuredRunnersAreSilentCopies(t *testing.T) {
	native := &nativeRunner{}
	gotNative, ok := silentStructuredRunner(native).(*nativeRunner)
	if !ok || !gotNative.silent || native.silent {
		t.Fatalf("native planning runner was not isolated: original=%+v copy=%+v", native, gotNative)
	}
	legacy := &legacyRunner{}
	gotLegacy, ok := silentStructuredRunner(legacy).(*legacyRunner)
	if !ok || !gotLegacy.silent || legacy.silent {
		t.Fatalf("legacy planning runner was not isolated: original=%+v copy=%+v", legacy, gotLegacy)
	}
}

func TestHITLItems(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Configure the required environment variable DATABASE_URL in .env before startup"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	items, err := orch.HITLItems(ctx)
	if err != nil {
		t.Fatalf("HITLItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("HITL items = %+v, want env items", items)
	}
	found := false
	for _, it := range items {
		if strings.Contains(it.Item, "DATABASE_URL") {
			found = true
		}
	}
	if !found {
		t.Errorf("HITL items missing DATABASE_URL: %+v", items)
	}

	// With .env filled, the item disappears.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DATABASE_URL=postgres://x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .env: %v", err)
	}
	items2, _ := orch.HITLItems(ctx)
	for _, it := range items2 {
		if strings.Contains(it.Item, "DATABASE_URL") {
			t.Errorf("DATABASE_URL item should be gone after .env fill: %+v", items2)
		}
	}
}

func TestWorktreePipeline(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "worktree", Name: "feat-health"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	wt := orch.Session().Worktree
	if wt == "" {
		t.Fatal("worktree not recorded in session")
	}
	if _, err := os.Stat(filepath.Join(wt, "go.mod")); err != nil {
		t.Fatalf("worktree missing repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "specs", "add-a-health-endpoint", spec.FileSpec)); err != nil {
		t.Fatalf("worktree must own the active spec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "add-a-health-endpoint", spec.FileSpec)); !os.IsNotExist(err) {
		t.Fatalf("base checkout must not receive an uncommitted worktree spec: %v", err)
	}
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if err := orch.Archive(ctx, ArchiveOptions{Yes: true, Merge: true}); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree should be removed after merge: %v", err)
	}
	mainBranch := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current"))
	if mainBranch != "main" {
		t.Errorf("main branch = %q", mainBranch)
	}
}

func TestArchiveMergesBackToRecordedBaseBranch(t *testing.T) {
	dir := newTestRepo(t)
	gitRun(t, dir, "branch", "-m", "trunk")
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "branch", Name: "feat-health"}); err != nil {
		t.Fatal(err)
	}
	if got := orch.Session().BaseBranch; got != "trunk" {
		t.Fatalf("base branch = %q, want trunk", got)
	}
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatal(err)
	}
	if err := orch.Archive(ctx, ArchiveOptions{Yes: true, Merge: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); got != "trunk" {
		t.Fatalf("archive returned to %q, want trunk", got)
	}
}

func TestArchiveRefusesAutomaticMergeWithoutRecordedBaseBranch(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "branch", Name: "feat-legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatal(err)
	}
	orch.sess.BaseBranch = "" // simulate a session persisted before base branches were recorded
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))

	err := orch.Archive(ctx, ArchiveOptions{Yes: true, Merge: true})
	if err == nil || !strings.Contains(err.Error(), "base branch") || !strings.Contains(err.Error(), "merge manually") {
		t.Fatalf("Archive error = %v, want manual merge guidance", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("failed preflight advanced phase to %q", orch.Phase())
	}
	if got := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); got != "feat-legacy" {
		t.Fatalf("failed preflight switched to %q", got)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("failed preflight created a commit: HEAD %q, want %q", got, headBefore)
	}
}

func TestArchivePreflightsDirtyWorktreeMergeTarget(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "worktree", Name: "feat-health-dirty-base"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user-local.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := orch.Archive(ctx, ArchiveOptions{Yes: true, Merge: true})
	if err == nil || !strings.Contains(err.Error(), "merge checkout has uncommitted changes") {
		t.Fatalf("Archive error = %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("failed preflight advanced phase to %q", orch.Phase())
	}
	if _, err := os.Stat(orch.Session().Worktree); err != nil {
		t.Fatalf("failed preflight removed active worktree: %v", err)
	}
}

func TestArchiveMergeConflictAbortsCleanlyAndFinalizesSession(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "branch", Name: "feat-conflict"}); err != nil {
		t.Fatal(err)
	}

	baseWorktree := filepath.Join(t.TempDir(), "base")
	gitRun(t, dir, "worktree", "add", baseWorktree, "main")
	if err := os.WriteFile(filepath.Join(baseWorktree, "main.go"), []byte("package main\n\nfunc main() { println(\"base\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, baseWorktree, "add", "main.go")
	gitRun(t, baseWorktree, "commit", "-m", "base change")
	gitRun(t, dir, "worktree", "remove", baseWorktree)

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(\"feature\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatal(err)
	}
	err := orch.Archive(ctx, ArchiveOptions{Yes: true, Merge: true})
	if err == nil || !strings.Contains(err.Error(), "archive committed on branch feat-conflict") {
		t.Fatalf("Archive error = %v", err)
	}
	if orch.Phase() != session.PhaseChat {
		t.Fatalf("merge conflict left phase %q", orch.Phase())
	}
	if got := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); got != "main" {
		t.Fatalf("merge conflict left branch %q", got)
	}
	if got := gitRun(t, dir, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("merge conflict left dirty checkout: %q", got)
	}
	if log := gitRun(t, dir, "log", "feat-conflict", "-1", "--pretty=%s"); !strings.Contains(log, "add-a-health-endpoint") {
		t.Fatalf("archive commit missing from feature branch: %s", log)
	}
}

func TestInvalidTransitions(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	if err := orch.Dispatch(ctx, cmdOf("build", nil)); err == nil {
		t.Error("build without spec should fail")
	}
	if err := orch.Dispatch(ctx, cmdOf("review", nil)); err == nil {
		t.Error("review without spec should fail")
	}
	if err := orch.Dispatch(ctx, cmdOf("cancel", nil)); err == nil {
		t.Error("cancel without proposal should fail")
	}
	if err := orch.Dispatch(ctx, cmdOf("bogus", nil)); err == nil {
		t.Error("unknown command should fail")
	}
	if err := orch.Dispatch(ctx, cmdOf("learn", nil)); err == nil {
		t.Error("learn should be unimplemented")
	}
}

func TestAcceptStayDoesNotRecordPhantomBranch(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Keep this checkout"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if got := orch.Session().Branch; got != "" {
		t.Fatalf("stay branch = %q, want empty", got)
	}
}

func TestAcceptRollsBackBranchWhenSpecWriteFails(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Broken draft"); err != nil {
		t.Fatal(err)
	}
	orch.sess.Draft.ID = "INVALID ID"
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "branch", Name: "feat-rollback"}); err == nil {
		t.Fatal("accept should fail for an invalid spec")
	}
	if branch := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); branch != "main" {
		t.Fatalf("branch after rollback = %q", branch)
	}
	branches := gitRun(t, dir, "branch", "--list", "feat-rollback")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("failed accept left branch behind: %q", branches)
	}
	if orch.Phase() != session.PhasePropose || orch.Session().Draft == nil {
		t.Fatalf("failed accept lost proposal state: phase=%q draft=%+v", orch.Phase(), orch.Session().Draft)
	}
}

func TestDecodeGeneratedProposal(t *testing.T) {
	raw := "```json\n{\"title\":\"API\",\"category\":\"feat\",\"body\":\"# Goal\",\"design\":\"# Design\",\"tasks\":\"- [ ] Build\"}\n```"
	var got generatedProposal
	if err := decodeJSONObject(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "API" || got.Design != "# Design" || got.Tasks != "- [ ] Build" {
		t.Fatalf("decoded proposal = %+v", got)
	}
}

func TestCommitMessage(t *testing.T) {
	orch := &Orchestrator{spec: &spec.Spec{ID: "api-go", Title: "API", Category: "feat", Body: "# x\n\nAdd a postgres API.\n"}}
	msg := orch.commitMessage()
	if msg != "feat(api-go): Add a postgres API." {
		t.Errorf("commit message = %q", msg)
	}
}

func TestCommitMessagePreservesUsefulGoalWithLongUnicodeID(t *testing.T) {
	orch := &Orchestrator{spec: &spec.Spec{
		ID:       "conserve-les-espaces-de-greeting-name-très-long",
		Category: "feat",
		Body:     "# Goal\n\nPermettre de personnaliser le message d’accueil via GREETING_NAME sans ambiguïté.\n",
	}}
	msg := orch.commitMessage()
	if got := len([]rune(msg)); got > 72 {
		t.Fatalf("commit message has %d runes, want <= 72: %q", got, msg)
	}
	if !strings.Contains(msg, "Permettre de personnaliser") {
		t.Fatalf("commit message lost its useful goal: %q", msg)
	}
	if !utf8.ValidString(msg) {
		t.Fatalf("commit message is invalid UTF-8: %q", msg)
	}
}

func TestScopedToolsWireAsk(t *testing.T) {
	dir := t.TempDir()
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Unwired: the ask tool reports a clear error instead of hanging.
	tools := orch.scopedTools(agentcore.RoleOrchestrator)
	for _, forbidden := range []string{"write", "bash"} {
		if _, exists := tools[forbidden]; exists {
			t.Fatalf("orchestrator chat exposed mutating tool %q", forbidden)
		}
	}
	ask, ok := tools["ask"]
	if !ok {
		t.Fatal("ask tool missing from scoped tools")
	}
	_, err = ask.Run(context.Background(), map[string]any{"question": "q?", "options": []any{"a"}})
	if err == nil || !strings.Contains(err.Error(), "no interactive picker") {
		t.Errorf("unwired ask error = %v", err)
	}
	// Wired: the callback is invoked and the answer flows back.
	orch.SetAsk(func(ctx context.Context, question string, options []string, recommended int) (int, error) {
		return 0, nil
	})
	tools = orch.scopedTools(agentcore.RoleOrchestrator)
	ask = tools["ask"]
	out, err := ask.Run(context.Background(), map[string]any{"question": "q?", "options": []any{"a", "b"}})
	if err != nil {
		t.Fatalf("wired ask: %v", err)
	}
	if !strings.Contains(out, "a") {
		t.Errorf("wired ask output = %q", out)
	}
}
