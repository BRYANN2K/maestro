package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRepositoryContextIsBoundedAndSecretAware(t *testing.T) {
	dir := t.TempDir()
	write := func(relative, content string) {
		t.Helper()
		path := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("AGENTS.md", "Use table-driven tests.")
	write("go.mod", "module example.com/project")
	write("internal/api/handler.go", "package api")
	write(".env", "SUPER_SECRET=do-not-leak")
	write("credentials.json", "do-not-leak-either")

	got := buildRepositoryContext(dir)
	for _, expected := range []string{"Use table-driven tests.", "module example.com/project", "internal/api/handler.go"} {
		if !strings.Contains(got, expected) {
			t.Errorf("context missing %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "do-not-leak") || strings.Contains(got, ".env") || strings.Contains(got, "credentials.json") {
		t.Fatalf("context leaked a sensitive path or value:\n%s", got)
	}
	if len(got) > maxContextBytes+maxContextFiles*256 {
		t.Fatalf("context unexpectedly large: %d bytes", len(got))
	}
}
