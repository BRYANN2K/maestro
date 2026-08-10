package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/spec"
)

func TestProposeBuildsReadyStructuredFallback(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.Propose(context.Background(), "Fix the login crash"); err != nil {
		t.Fatal(err)
	}
	draft := orch.Session().Draft
	if draft.SchemaVersion != spec.CurrentSchemaVersion || draft.Recipe != spec.RecipeBug {
		t.Fatalf("draft version/recipe = %d/%q", draft.SchemaVersion, draft.Recipe)
	}
	if report := draft.ValidateReadiness(); !report.Ready() {
		t.Fatalf("fallback must be ready: %s", report.Error())
	}
}

func TestProposeRecipeOverride(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.ProposeWithRecipe(context.Background(), "Rewrite the datastore", spec.RecipeQuick); err != nil {
		t.Fatal(err)
	}
	if got := orch.Session().Draft.Recipe; got != spec.RecipeQuick {
		t.Fatalf("recipe override = %q", got)
	}
	if _, err := orch.ProposeWithRecipe(context.Background(), "Anything", "unknown"); err == nil {
		t.Fatal("invalid recipe should fail")
	}
}

func TestAcceptReadinessGateHasNoSideEffects(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add invitations"); err != nil {
		t.Fatal(err)
	}
	orch.sess.Draft.Questions = []spec.Question{{
		ID: "Q-001", Prompt: "Who may invite members?", Severity: "high", Status: "open", Blocking: true,
		RequirementIDs: []string{"REQ-CHANGE-001"},
	}}
	_, err := orch.Accept(ctx, BranchChoice{Kind: "branch", Name: "feat-invitations"})
	if err == nil || !strings.Contains(err.Error(), "QUESTION_UNRESOLVED") {
		t.Fatalf("Accept error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "specs", orch.sess.Draft.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("readiness failure created a spec folder: %v", statErr)
	}
	if branches := strings.TrimSpace(gitRun(t, dir, "branch", "--list", "feat-invitations")); branches != "" {
		t.Fatalf("readiness failure created a branch: %q", branches)
	}
}

func TestAnswerQuestionUnblocksDraftAndHITL(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add invitations"); err != nil {
		t.Fatal(err)
	}
	orch.sess.Draft.Questions = []spec.Question{{
		ID: "Q-001", Prompt: "Who may invite members?", Severity: "high", Status: "open", Blocking: true,
		RequirementIDs: []string{"REQ-CHANGE-001"},
	}}
	items, err := orch.HITLItems(ctx)
	if err != nil || len(items) != 1 || items[0].Status != "pending" {
		t.Fatalf("pending question HITL = %+v, %v", items, err)
	}
	if err := orch.AnswerQuestion(ctx, "q-001", "Workspace administrators"); err != nil {
		t.Fatal(err)
	}
	if report := orch.DraftReadiness(); !report.Ready() {
		t.Fatalf("answered draft not ready: %s", report.Error())
	}
	items, err = orch.HITLItems(ctx)
	if err != nil || len(items) != 1 || items[0].Status != "done" {
		t.Fatalf("resolved question HITL = %+v, %v", items, err)
	}
}
