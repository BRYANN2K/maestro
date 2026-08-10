package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

func TestScanAllFivePatterns(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"main.go": `package main

const apiKey = "sk-abcdefghijklmnop1234567890"

func main() {
	cors.AllowOrigins("*")
	mux.Handle("/debug/pprof", nil)
}
`,
		"auth.go": `package main

func requireAuth(r *Request) bool { return true }
`,
		"migrations/001.sql": `CREATE TABLE users (id int);
`,
	})
	read := func(path string) ([]byte, error) { return os.ReadFile(path) }
	findings, err := Scan(context.Background(), []string{
		filepath.Join(dir, "main.go"),
		filepath.Join(dir, "auth.go"),
		filepath.Join(dir, "migrations/001.sql"),
	}, read)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := map[string]bool{}
	for _, f := range findings {
		got[f.ID] = true
	}
	for _, want := range []string{"bundled-secret", "wildcard-cors", "debug-route", "auth-without-authz", "missing-rls"} {
		if !got[want] {
			t.Errorf("missing finding %s (got %v)", want, got)
		}
	}
}

func TestScanCleanCode(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"ok.go": `package main

func main() {
	cors.AllowOrigins("https://app.example.com")
}
`,
	})
	findings, err := Scan(context.Background(), []string{filepath.Join(dir, "ok.go")}, func(p string) ([]byte, error) {
		return os.ReadFile(p)
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings on clean code: %+v", findings)
	}
}

func TestScanSkipsVendoredAndBinaries(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"vendor/lib.go": `const secret = "sk-abcdefghijklmnop1234567890"`,
		"logo.png":      "sk-abcdefghijklmnop1234567890",
	})
	findings, err := Scan(context.Background(), []string{
		filepath.Join(dir, "vendor/lib.go"),
		filepath.Join(dir, "logo.png"),
	}, func(p string) ([]byte, error) { return os.ReadFile(p) })
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings in skipped files: %+v", findings)
	}
}

func TestScanMissingFileSilent(t *testing.T) {
	findings, err := Scan(context.Background(), []string{filepath.Join(t.TempDir(), "gone.go")}, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v", findings)
	}
}

func TestSeverityRank(t *testing.T) {
	if SeverityRank(High) >= SeverityRank(Medium) || SeverityRank(Medium) >= SeverityRank(Low) {
		t.Error("rank order wrong")
	}
}

func TestFindingString(t *testing.T) {
	f := Finding{Severity: High, Message: "bundled-secret", File: "a.go", Line: 3}
	if !strings.Contains(f.String(), "high") || !strings.Contains(f.String(), "a.go:3") {
		t.Errorf("String = %q", f.String())
	}
}
