package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedEngineLifecycleRejectsUnapprovedStart(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	for _, args := range [][]string{{"bootstrap"}, {"explore", "example", "--title", "Example"}} {
		if _, err := Run(context.Background(), root, args, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !Initialized(root) {
		t.Fatal("not initialized")
	}
	if _, err := Run(context.Background(), root, []string{"start", "example"}, nil); err == nil {
		t.Fatal("unapproved start accepted")
	}
	if _, err := Run(context.Background(), root, []string{"status", "example", "--root", t.TempDir()}, nil); err == nil {
		t.Fatal("root override accepted")
	}
	data, err := os.ReadFile(filepath.Join(root, ".workflow", "changes", "example", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "exploring") {
		t.Fatal(string(data))
	}
}
