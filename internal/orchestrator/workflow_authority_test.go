package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/workflow"
)

func TestStipulateOwnsLifecycleAndAcceptsPlanStdin(t *testing.T) {
	root := newTestRepo(t)
	o := newTestOrch(t, root, &fakeRunner{})
	defer o.Close()
	if _, err := workflow.Run(t.Context(), root, []string{"bootstrap"}, nil); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"propose": func() error { _, e := o.ProposeWithRecipe(t.Context(), "an idea", ""); return e },
		"accept":  func() error { _, e := o.Accept(t.Context(), BranchChoice{}); return e },
		"build":   func() error { return o.Build(t.Context(), BuildOptions{}) },
		"review":  func() error { _, e := o.Review(t.Context()); return e },
		"docs":    func() error { _, _, e := o.DocsDraft(t.Context()); return e },
		"archive": func() error { return o.Archive(t.Context(), ArchiveOptions{}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || !strings.Contains(err.Error(), "uses Stipulate") {
				t.Fatalf("alternate authority: %v", err)
			}
		})
	}
	tool := o.workflowTool()
	engine := func(argv ...string) (string, error) {
		args := make([]any, len(argv))
		for i, arg := range argv {
			args[i] = arg
		}
		return tool.Run(context.Background(), map[string]any{"action": "engine", "argv": args})
	}
	if _, err := engine("explore", "sample"); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(t.Context(), map[string]any{"action": "contract", "change": "sample", "file": "spec.md", "content": "# Sample\n\n- AC-1: Test the fixture.\n"}); err != nil {
		t.Fatal(err)
	}
	plan := `{"version":1,"tasks":[{"id":"verify","role":"verification","phase":"check","criteria":["AC-1"],"depends_on":[],"write_paths":[],"objective":"Check the fixture."}]}`
	if _, err := tool.Run(t.Context(), map[string]any{"action": "engine", "argv": []any{"plan", "sample", "--file", "-"}, "stdin": plan}); err != nil {
		t.Fatal(err)
	}
	if result, err := engine("plan", "sample"); err != nil || !strings.Contains(result, "verify") {
		t.Fatalf("plan: %s %v", result, err)
	}
	for _, action := range []string{"approve", "archive"} {
		if _, err := engine(action, "sample"); err == nil {
			t.Fatalf("tool exposed human %s", action)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".workflow", "changes", "sample", "spec.md")); err != nil {
		t.Fatal(err)
	}
}

func TestStipulateWorkerRejectsInProjectOwnershipAlias(t *testing.T) {
	root := newTestRepo(t)
	o := newTestOrch(t, root, &fakeRunner{})
	defer o.Close()
	for _, path := range []string{"owned", "unowned"} {
		if err := os.Mkdir(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "unowned"), filepath.Join(root, "owned", "alias")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tool := o.workflowWorkerTools([]string{"owned"})["write"]
	if _, err := tool.Run(t.Context(), map[string]any{"path": "owned/alias/escape.txt", "content": "bad"}); err == nil {
		t.Fatal("worker wrote outside its ownership through an in-project alias")
	}
	if _, err := os.Stat(filepath.Join(root, "unowned", "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("unowned file changed: %v", err)
	}
}
