package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanGroupsAndOrders(t *testing.T) {
	sections := []Section{
		{ID: "b1", Files: []string{"internal/spec/"}},
		{ID: "b2", Files: []string{"internal/orchestrator/"}},
	}
	changed := []string{"internal/spec/spec.go", "internal/orchestrator/build.go"}
	deps := map[string][]string{
		"internal/orchestrator/build.go": {"internal/spec/spec.go"},
	}
	plan, err := Plan("api-go", changed, sections, deps)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Commits) != 2 {
		t.Fatalf("commits = %d", len(plan.Commits))
	}
	if plan.Commits[0].Section != "b1" || plan.Commits[1].Section != "b2" {
		t.Errorf("order = %s, %s", plan.Commits[0].Section, plan.Commits[1].Section)
	}
	if plan.Commits[1].Message != "feat(api-go/b2)" {
		t.Errorf("message = %q", plan.Commits[1].Message)
	}
	if plan.Commits[0].Files[0] != "internal/spec/spec.go" {
		t.Errorf("files = %v", plan.Commits[0].Files)
	}
}

func TestPlanRejectsCycle(t *testing.T) {
	sections := []Section{
		{ID: "b1", Files: []string{"a/"}},
		{ID: "b2", Files: []string{"b/"}},
	}
	changed := []string{"a/x.go", "b/y.go"}
	deps := map[string][]string{
		"a/x.go": {"b/y.go"},
		"b/y.go": {"a/x.go"},
	}
	if _, err := Plan("s", changed, sections, deps); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v, want cycle", err)
	}
}

func TestPlanMiscAndCategories(t *testing.T) {
	sections := []Section{{ID: "b1", Files: []string{"internal/"}}}
	plan, err := Plan("fix-api", []string{"internal/a.go", "README.md"}, sections, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Commits[0].Section != "b1" || plan.Commits[1].Section != "misc" {
		t.Errorf("sections = %s, %s", plan.Commits[0].Section, plan.Commits[1].Section)
	}
	if !strings.HasPrefix(plan.Commits[0].Message, "fix(fix-api/") {
		t.Errorf("message = %q", plan.Commits[0].Message)
	}
}

func TestImportDeps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	files := map[string]string{
		"main.go":               "package main\n\nimport \"example.com/app/internal/spec\"\n",
		"internal/spec/spec.go": "package spec\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	changed := []string{"main.go", "internal/spec/spec.go"}
	deps := ImportDeps(dir, changed)
	if len(deps["main.go"]) != 1 || deps["main.go"][0] != "internal/spec/spec.go" {
		t.Errorf("deps = %v", deps)
	}
}

func TestPlanApply(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	writeFile(t, dir, "a.go", "package a\n")
	writeFile(t, dir, "b.go", "package b\n")

	plan := &CommitPlan{Commits: []PlannedCommit{
		{Section: "b1", Message: "feat(s/b1)", Files: []string{"a.go"}},
		{Section: "b2", Message: "feat(s/b2)", Files: []string{"b.go"}},
	}}
	if err := plan.Apply(ctx, c); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	log := run(t, dir, "log", "--oneline", "-2")
	if !strings.Contains(log, "feat(s/b1)") || !strings.Contains(log, "feat(s/b2)") {
		t.Errorf("log:\n%s", log)
	}
}

func TestPlanApplyDoesNotCommitPreStagedFiles(t *testing.T) {
	dir := initRepo(t)
	c := New(dir)
	ctx := context.Background()
	writeFile(t, dir, "planned.go", "package planned\n")
	writeFile(t, dir, "unrelated.txt", "user work\n")
	if err := c.Add(ctx, "unrelated.txt"); err != nil {
		t.Fatalf("stage unrelated: %v", err)
	}

	plan := &CommitPlan{Commits: []PlannedCommit{{
		Section: "b1",
		Message: "feat(s/b1)",
		Files:   []string{"planned.go"},
	}}}
	if err := plan.Apply(ctx, c); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	committed := run(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if strings.TrimSpace(committed) != "planned.go" {
		t.Fatalf("committed files = %q, want planned.go", committed)
	}
	staged := run(t, dir, "diff", "--cached", "--name-only")
	if strings.TrimSpace(staged) != "unrelated.txt" {
		t.Fatalf("staged files = %q, want unrelated.txt preserved", staged)
	}
}
