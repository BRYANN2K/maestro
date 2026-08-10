package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestRegistryOrderAndSpecs(t *testing.T) {
	r := Default()
	names := r.Names()
	if len(names) != 5 {
		t.Fatalf("names = %v", names)
	}
	for _, n := range []string{"read", "grep", "write", "bash", "ask"} {
		if _, ok := r.Get(n); !ok {
			t.Errorf("tool %s missing", n)
		}
	}
	specs := r.Specs()
	if len(specs) != 5 || specs[0].Name != "read" {
		t.Errorf("specs = %+v", specs)
	}
	if !specs[2].NeedsApproval || specs[2].Name != "write" {
		t.Errorf("write spec = %+v", specs[2])
	}
}

func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tr := NewRead()
	out, err := tr.Run(context.Background(), map[string]any{"path": path})
	if err != nil || out != "hello" {
		t.Errorf("read = %q, %v", out, err)
	}
	if _, err := tr.Run(context.Background(), map[string]any{}); err == nil {
		t.Error("read without path should fail")
	}
	if _, err := tr.Run(context.Background(), map[string]any{"path": filepath.Join(dir, "missing")}); err == nil {
		t.Error("read of missing file should fail")
	}
	large := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(large, make([]byte, maxReadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Run(context.Background(), map[string]any{"path": large}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("large read error = %v", err)
	}
}

func TestGrepTool(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write("one.go", "package main\n// TODO: fix me\n")
	write("two.go", "package two\n// done\n")
	out, err := NewGrep().Run(context.Background(), map[string]any{"pattern": "TODO", "path": dir})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "one.go:2") || strings.Contains(out, "two.go") {
		t.Errorf("grep out = %q", out)
	}
	out, err = NewGrep().Run(context.Background(), map[string]any{"pattern": "nope", "path": dir})
	if err != nil || !strings.Contains(out, "no matches") {
		t.Errorf("grep no-match = %q, %v", out, err)
	}
	if _, err := NewGrep().Run(context.Background(), map[string]any{}); err == nil {
		t.Error("grep without pattern should fail")
	}
	regexOut, err := NewGrep().Run(context.Background(), map[string]any{"pattern": `T.DO`, "path": dir})
	if err != nil || !strings.Contains(regexOut, "one.go:2") {
		t.Fatalf("regex grep = %q, %v", regexOut, err)
	}
	if _, err := NewGrep().Run(context.Background(), map[string]any{"pattern": `[`, "path": dir}); err == nil {
		t.Fatal("invalid regular expression should fail")
	}
}

func TestWriteTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.go")
	out, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "package x\n"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "file.go") {
		t.Errorf("write out = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "package x\n" {
		t.Errorf("file = %q, %v", data, err)
	}
	if _, err := NewWrite().Run(context.Background(), map[string]any{}); err == nil {
		t.Error("write without path should fail")
	}
}

func TestBashTool(t *testing.T) {
	out, err := NewBash().Run(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil || !strings.Contains(out, "hello") {
		t.Errorf("bash = %q, %v", out, err)
	}
	if _, err := NewBash().Run(context.Background(), map[string]any{}); err == nil {
		t.Error("bash without command should fail")
	}
	if _, err := NewBash().Run(context.Background(), map[string]any{"command": "exit 3"}); err == nil {
		t.Error("bash failing command should return an error")
	}
}

func TestAskUnwired(t *testing.T) {
	_, err := NewAsk(nil).Run(context.Background(), map[string]any{"question": "which?", "options": []any{"a", "b"}})
	if err == nil || !strings.Contains(err.Error(), "no interactive picker") {
		t.Errorf("ask unwired error = %v", err)
	}
}

func TestAskWired(t *testing.T) {
	var got struct {
		question    string
		options     []string
		recommended int
	}
	tr := NewAsk(func(ctx context.Context, question string, options []string, recommended int) (int, error) {
		got.question, got.options, got.recommended = question, options, recommended
		return 1, nil
	})
	out, err := tr.Run(context.Background(), map[string]any{
		"question": "which?", "options": []any{"a", "b", "c"}, "recommended": 2,
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got.question != "which?" || len(got.options) != 3 || got.recommended != 2 {
		t.Errorf("ask args = %+v", got)
	}
	if !strings.Contains(out, "b") {
		t.Errorf("ask output = %q", out)
	}
}

func TestAskMissingFields(t *testing.T) {
	if _, err := NewAsk(nil).Run(context.Background(), map[string]any{}); err == nil {
		t.Error("ask without question should fail")
	}
	if _, err := NewAsk(nil).Run(context.Background(), map[string]any{"question": "q"}); err == nil {
		t.Error("ask without options should fail")
	}
}

func TestReadBeforeEditRequiresRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "v2"})
	if err == nil || !strings.Contains(err.Error(), "never read") {
		t.Fatalf("write without read: err = %v", err)
	}
	if _, err := NewRead().Run(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("read: %v", err)
	}
	out, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "v2"})
	if err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("write after read: %q, %v", out, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "v2" {
		t.Errorf("file = %q", data)
	}
}

func TestReadBeforeEditStaleness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewRead().Run(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("read: %v", err)
	}
	// External change between read and write → stale.
	if err := os.WriteFile(path, []byte("v1-changed"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}
	_, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "v2"})
	if err == nil || !strings.Contains(err.Error(), "modified since") {
		t.Fatalf("stale write: err = %v", err)
	}
}

func TestReadBeforeEditNewFileExempt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	out, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "hello"})
	if err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("write new file: %q, %v", out, err)
	}
}

func TestReadBeforeEditWriteRefreshesStamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "y.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewRead().Run(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "v2"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The tool's own write refreshed the stamp, so a second write passes.
	if _, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "v3"}); err != nil {
		t.Fatalf("second write after own write: %v", err)
	}
	// But an external modification is still caught.
	if err := os.WriteFile(path, []byte("v3-ext"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}
	if _, err := NewWrite().Run(context.Background(), map[string]any{"path": path, "content": "v4"}); err == nil {
		t.Fatal("write after external modification should fail")
	}
}

func TestToolFunc(t *testing.T) {
	called := false
	tf := agentcore.NewToolFunc(agentcore.ToolSpec{Name: "t"}, func(ctx context.Context, args map[string]any) (string, error) {
		called = true
		return "ran", nil
	})
	if tf.Spec().Name != "t" {
		t.Errorf("spec = %+v", tf.Spec())
	}
	out, err := tf.Run(context.Background(), nil)
	if !called || err != nil || out != "ran" {
		t.Errorf("run = %q, %v, called %v", out, err, called)
	}
}
