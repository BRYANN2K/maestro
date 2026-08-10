package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gitpkg "github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

func TestRewindRestoresCode(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add a feature"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// Simulate a dev round that writes code.
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "implemented_1.go")); err != nil {
		t.Fatalf("implemented file missing: %v", err)
	}
	cps := orch.CheckpointList(ctx)
	if len(cps) == 0 {
		t.Fatal("no checkpoint created")
	}
	var checkpointSession session.Session
	if err := json.Unmarshal([]byte(cps[0].Conv), &checkpointSession); err != nil {
		t.Fatalf("checkpoint session: %v", err)
	}
	if checkpointSession.Phase != session.PhaseSpec {
		t.Errorf("build checkpoint phase = %q, want pre-build phase %q", checkpointSession.Phase, session.PhaseSpec)
	}
	// Rewind the code only.
	if err := orch.Rewind(ctx, cps[0].ID, true, false); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "implemented_1.go")); !os.IsNotExist(err) {
		t.Errorf("implemented file should be reverted: %v", err)
	}
	// Conversation untouched: still in build phase with the active spec.
	if orch.Phase() != "build" || orch.ActiveSpec() == nil {
		t.Errorf("conversation should be untouched: phase=%s spec=%v", orch.Phase(), orch.ActiveSpec() != nil)
	}
}

func TestRewindRestoresValidatedConversationAndPersistsIt(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	orch.appendConversation("user", "captured question")
	orch.appendConversation("assistant", "captured answer")
	orch.sess.ModelRole = "captured-model"
	orch.sess.DraftPrompt = "captured draft"
	orch.sess.PermQueue = []session.Permission{{ID: "captured", Type: "tool", Status: "pending"}}
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}
	if err := orch.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	targetID := orch.features.lastCheckpoint
	wantConversation := append([]session.ConversationTurn(nil), orch.sess.Conversation...)
	wantPermissions := append([]session.Permission(nil), orch.sess.PermQueue...)

	orch.sess.Conversation = []session.ConversationTurn{{Role: "user", Content: "later state"}}
	orch.sess.ModelRole = "later-model"
	orch.sess.DraftPrompt = "later draft"
	orch.sess.PermQueue = nil
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}
	if err := orch.Rewind(ctx, targetID, false, true); err != nil {
		t.Fatalf("Rewind conversation: %v", err)
	}
	if !reflect.DeepEqual(orch.sess.Conversation, wantConversation) || !reflect.DeepEqual(orch.sess.PermQueue, wantPermissions) {
		t.Errorf("session not restored: %+v", orch.sess)
	}
	if orch.sess.ModelRole != "captured-model" || orch.sess.DraftPrompt != "captured draft" {
		t.Errorf("restored fields = model %q, draft %q", orch.sess.ModelRole, orch.sess.DraftPrompt)
	}
	persisted, err := orch.sessions.Load(ctx, orch.sess.Project, orch.sess.ID)
	if err != nil {
		t.Fatalf("Load persisted session: %v", err)
	}
	if !reflect.DeepEqual(persisted.Conversation, wantConversation) || persisted.ModelRole != "captured-model" {
		t.Errorf("persisted session not restored: %+v", persisted)
	}

	var recovery *gitpkg.Checkpoint
	for _, cp := range orch.CheckpointList(ctx) {
		if cp.RecoveryFor == targetID {
			copy := cp
			recovery = &copy
			break
		}
	}
	if recovery == nil {
		t.Fatal("rewind did not retain a recovery checkpoint")
	}
	var recoveredLater session.Session
	if err := json.Unmarshal([]byte(recovery.Conv), &recoveredLater); err != nil {
		t.Fatalf("recovery conversation JSON: %v", err)
	}
	if len(recoveredLater.Conversation) != 1 || recoveredLater.Conversation[0].Content != "later state" {
		t.Errorf("recovery conversation = %+v", recoveredLater.Conversation)
	}
}

func TestRewindCodeAndConversationRestoresMissingSpecBeforeLoading(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Restore a missing spec"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := orch.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	targetID := orch.features.lastCheckpoint
	specID := orch.sess.SpecID
	specPath := orch.store.PathFor(specID, spec.FileSpec)
	if err := os.Remove(specPath); err != nil {
		t.Fatalf("remove active spec: %v", err)
	}
	orch.sess.Conversation = []session.ConversationTurn{{Role: "user", Content: "later conversation"}}

	if err := orch.Rewind(ctx, targetID, true, true); err != nil {
		t.Fatalf("combined rewind: %v", err)
	}
	if _, err := os.Stat(specPath); err != nil {
		t.Fatalf("checkpoint spec was not restored: %v", err)
	}
	if orch.spec == nil || orch.spec.ID != specID {
		t.Fatalf("active spec = %+v, want %q", orch.spec, specID)
	}
	if len(orch.sess.Conversation) != 0 {
		t.Fatalf("conversation = %+v, want checkpoint state", orch.sess.Conversation)
	}
}

func TestRewindRejectsInvalidConversationBeforeTouchingCode(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	cp, err := orch.features.checkpoints.Create(ctx, orch.git, "{invalid", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orch.sess.Conversation = []session.ConversationTurn{{Role: "user", Content: "must survive"}}

	if err := orch.Rewind(ctx, cp.ID, true, true); err == nil || !strings.Contains(err.Error(), "invalid checkpoint JSON") {
		t.Fatalf("Rewind error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || string(got) != "must survive\n" {
		t.Errorf("invalid conversation rewind changed code: %q, %v", got, err)
	}
	if len(orch.sess.Conversation) != 1 || orch.sess.Conversation[0].Content != "must survive" {
		t.Errorf("invalid conversation rewind changed session: %+v", orch.sess.Conversation)
	}
	if list := orch.CheckpointList(ctx); len(list) != 1 {
		t.Errorf("invalid preflight created a recovery checkpoint: %d", len(list))
	}
}

func TestRewindRollsCodeBackWhenConversationPersistenceFails(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	orch.appendConversation("user", "checkpoint conversation")
	if err := orch.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	targetID := orch.features.lastCheckpoint
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("later code must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orch.sess.Conversation = []session.ConversationTurn{{Role: "user", Content: "later conversation"}}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	orch.sessions = session.NewStore(filepath.Join(blocker, "sessions"))

	err := orch.Rewind(ctx, targetID, true, true)
	if err == nil || !strings.Contains(err.Error(), "code restored from recovery checkpoint") {
		t.Fatalf("Rewind error = %v", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(dir, "README.md")); readErr != nil || string(got) != "later code must survive\n" {
		t.Errorf("failed conversation rewind did not roll code back: %q, %v", got, readErr)
	}
	if len(orch.sess.Conversation) != 1 || orch.sess.Conversation[0].Content != "later conversation" {
		t.Errorf("failed conversation rewind mutated memory: %+v", orch.sess.Conversation)
	}
}

func TestBuildStopsWhenPreBuildCheckpointFails(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add a feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	orch.features.checkpoints = gitpkg.NewCheckpointStore(filepath.Join(blocker, "checkpoints"))

	if err := orch.Build(ctx, BuildOptions{}); err == nil || !strings.Contains(err.Error(), "build checkpoint") {
		t.Fatalf("Build error = %v", err)
	}
	if len(runner.files) != 0 {
		t.Errorf("dev runner wrote files despite checkpoint failure: %v", runner.files)
	}
	if orch.Phase() != session.PhaseSpec {
		t.Errorf("phase = %q, want spec", orch.Phase())
	}
}

func TestRewindRequiresMode(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	if err := orch.Rewind(context.Background(), "cp-x", false, false); err == nil {
		t.Error("rewind without code/conv should fail")
	}
}

func TestRememberReflect(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if err := orch.Remember(ctx, "chose worktrees for isolation", []string{"git"}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if err := orch.Remember(ctx, "chose postgres", []string{"db"}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	recalled := orch.RecallMemory(ctx, "worktrees")
	if len(recalled) != 1 || !strings.Contains(recalled[0].Text, "worktrees") {
		t.Fatalf("recall = %+v", recalled)
	}
	// Reflect writes reflections.md.
	if err := orch.ReflectMemory(ctx); err != nil {
		t.Fatalf("Reflect: %v", err)
	}
}

func TestRulesImportExport(t *testing.T) {
	dir := newTestRepo(t)
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write("AGENTS.md", "# Rules\n\nRun tests before commit.\n")
	write(".clinerules", "Keep functions small.\n")
	orch := newTestOrch(t, dir, &fakeRunner{})
	canonicalDir := orch.WorkDirDisplay()
	rules, err := orch.RulesImport(context.Background())
	if err != nil {
		t.Fatalf("RulesImport: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %d", len(rules))
	}
	origins := map[string]bool{}
	for _, r := range rules {
		origins[r.Origin] = true
	}
	if !origins[filepath.Join(canonicalDir, "AGENTS.md")] || !origins[filepath.Join(canonicalDir, ".clinerules")] {
		t.Errorf("origins = %v", origins)
	}
	out, err := orch.RulesExport(context.Background(), "AGENTS.md")
	if err != nil || !strings.Contains(out, "Run tests before commit") {
		t.Errorf("export = %q, %v", out, err)
	}
}

func TestLearnWritesFormattedFile(t *testing.T) {
	dir := newTestRepo(t)
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	orch := newTestOrch(t, dir, &fakeRunner{})
	src = filepath.Join(orch.WorkDirDisplay(), "main.go")
	out, formatted, err := orch.Learn(context.Background(), src, false)
	if err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if !strings.HasSuffix(out, "maestro/learn/main-go.md") {
		t.Errorf("out = %q", out)
	}
	data, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(data), "# Learn: ") {
		t.Errorf("file = %q, %v", data, err)
	}
	if !strings.Contains(formatted, "# Learn: ") {
		t.Errorf("formatted = %q", formatted)
	}
}

func TestSecurityScanInReview(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add auth"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// A dev round, then seed a secret + wildcard CORS.
	if err := orch.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seeded.go"), []byte(
		"package main\n\nconst key = \"sk-abcdefghijklmnop1234567890\"\nfunc main() { cors.AllowOrigins(\"*\") }\n",
	), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	verdict, err := orch.Review(ctx)
	var reviewFailed *ReviewFailedError
	if !errors.As(err, &reviewFailed) {
		t.Fatalf("Review error = %v, want ReviewFailedError", err)
	}
	if verdict.VerdictLevel() != "fail" || orch.Phase() != session.PhaseBuild {
		t.Fatalf("verdict = %q, phase = %q; failed review must stay in build", verdict.VerdictLevel(), orch.Phase())
	}
	if orch.Session().Review == nil || orch.Session().Review.Level != "fail" {
		t.Fatalf("persisted review = %+v, want fail", orch.Session().Review)
	}
	if err := orch.Archive(ctx, ArchiveOptions{Yes: true}); err == nil {
		t.Fatal("archive must reject a failed review")
	}
	// The seeded secret must be flagged.
	findings := orch.securityScan(ctx)
	got := map[string]bool{}
	for _, f := range findings {
		got[f.ID] = true
	}
	if !got["bundled-secret"] || !got["wildcard-cors"] {
		t.Errorf("findings = %v", got)
	}
}

func TestComprehensionAndTDDGates(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	writeFile2(t, dir, "place.go", "package main\n\n// TODO: finish this\nfunc main() {}\n")
	items := orch.comprehensionChecks(ctx)
	found := false
	for _, it := range items {
		if strings.Contains(it.Message, "placeholder") {
			found = true
		}
	}
	if !found {
		t.Errorf("comprehension items = %+v", items)
	}

	tdd := orch.tddGate(ctx)
	if len(tdd) == 0 || !strings.Contains(tdd[0].Message, "TDD") {
		t.Errorf("tdd gate = %+v", tdd)
	}
}

func TestDispatchCommitPlan(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// Two changed files.
	writeFile2(t, dir, "internal/a/a.go", "package a\n")
	writeFile2(t, dir, "internal/b/b.go", "package b\n")
	// Give the spec a batch owning the files.
	orch.spec.Batches = []spec.Batch{
		{ID: "b1", Files: []string{"internal/a/"}},
		{ID: "b2", Files: []string{"internal/b/"}},
	}
	// The plan needs the files under specs/... — update spec first.
	if err := orch.store.Save(ctx, orch.spec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var out strings.Builder
	orch.out = &out
	if err := orch.Dispatch(ctx, Command{Cmd: "commit"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !strings.Contains(out.String(), "feat(add-a-health-endpoint/b1)") || !strings.Contains(out.String(), "b2") {
		t.Errorf("plan output = %q", out.String())
	}
	// Apply it.
	if err := orch.Dispatch(ctx, Command{Cmd: "commit", Flags: map[string]string{"yes": "true"}}); err != nil {
		t.Fatalf("commit --yes: %v", err)
	}
	log := gitRun(t, dir, "log", "--oneline", "-4")
	if !strings.Contains(log, "b1") || !strings.Contains(log, "b2") {
		t.Errorf("log:\n%s", log)
	}
}

func writeFile2(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
