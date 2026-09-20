package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

func TestGrepSearchesExplicitHiddenRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "visible.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := grepDir(t.Context(), root, regexp.MustCompile("needle"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out, "\n"); !strings.Contains(got, "visible.txt:1") {
		t.Fatalf("explicit hidden root was skipped: %q", got)
	}
}

func TestGrepBoundsFilesLinesAndOutput(t *testing.T) {
	dir := t.TempDir()
	tooLarge := filepath.Join(dir, "too-large.txt")
	if err := os.WriteFile(tooLarge, []byte("MATCH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(tooLarge, maxGrepFileBytes+1); err != nil {
		t.Fatal(err)
	}
	longLine := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(longLine, []byte("MATCH "+strings.Repeat("界", maxGrepLineBytes)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := NewGrep().Run(t.Context(), map[string]any{"pattern": "MATCH", "path": dir})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if strings.Contains(out, "too-large.txt") {
		t.Fatalf("oversized file was searched: %q", out)
	}
	if !strings.Contains(out, grepLineTruncated) || len(out) > maxGrepOutputBytes {
		t.Fatalf("bounded grep output = %d bytes, %q", len(out), out)
	}
}

func TestGrepBoundsAggregateScanWithoutMatches(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%d.txt", i))
		if err := os.WriteFile(path, []byte("nothing here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := grepDirLimited(t.Context(), dir, regexp.MustCompile("MATCH"), grepLimits{
		matches: 100,
		files:   2,
		bytes:   1 << 20,
		output:  1 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out, "\n"); got != grepScanLimit {
		t.Fatalf("bounded no-match scan = %q", got)
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
	t.Setenv("MAESTRO_SHELL", "")
	t.Setenv("COMSPEC", "cmd.exe")
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

func TestBashCancellationStopsDescendants(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "survived")
	started := filepath.Join(dir, "started")
	t.Setenv("MAESTRO_SHELL", "")
	t.Setenv("COMSPEC", "cmd.exe")
	t.Setenv("MAESTRO_BASH_TEST_MARKER", marker)
	t.Setenv("MAESTRO_BASH_TEST_STARTED", started)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewBash().Run(ctx, map[string]any{
			"command": bashCancellationCommand(os.Args[0]),
		})
		done <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	canceledAt := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled command returned no error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled command did not return")
	}
	if elapsed := time.Since(canceledAt); elapsed > 3*time.Second {
		t.Fatalf("canceled command waited on descendant for %s", elapsed)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived cancellation: stat error %v", err)
	}
}

func TestBashCancellationHelper(t *testing.T) {
	started := os.Getenv("MAESTRO_BASH_TEST_STARTED")
	marker := os.Getenv("MAESTRO_BASH_TEST_MARKER")
	if started == "" || marker == "" {
		return
	}
	if err := os.WriteFile(started, []byte("started"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := os.WriteFile(marker, []byte("survived"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCappedOutputRetainsPrefixAndDiscardsTheRest(t *testing.T) {
	var out cappedOutput
	out.limit = 5
	if n, err := out.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("first write = %d, %v", n, err)
	}
	if n, err := out.Write([]byte("defgh")); err != nil || n != 5 {
		t.Fatalf("second write = %d, %v", n, err)
	}
	if got := out.String(); got != "abcde"+bashOutputLimit {
		t.Fatalf("capped output = %q", got)
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

func TestDefaultRegistryScopesReadBeforeEditState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scoped.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := Default()
	read, _ := first.Get("read")
	if _, err := read.Run(t.Context(), map[string]any{"path": path}); err != nil {
		t.Fatal(err)
	}
	second := Default()
	write, _ := second.Get("write")
	if _, err := write.Run(t.Context(), map[string]any{"path": path, "content": "v2"}); err == nil || !strings.Contains(err.Error(), "never read") {
		t.Fatalf("fresh registry reused another run's read stamp: %v", err)
	}
}

func TestFileGuardEvictsOldEntries(t *testing.T) {
	guard := newFileGuard()
	for i := 0; i < maxFileGuardEntries+10; i++ {
		guard.recordRead(filepath.Join("root", fmt.Sprintf("file-%d", i)), time.Unix(int64(i), 0))
	}
	if len(guard.stamps) != maxFileGuardEntries || len(guard.order) != maxFileGuardEntries {
		t.Fatalf("guard retained stamps=%d order=%d, want %d", len(guard.stamps), len(guard.order), maxFileGuardEntries)
	}
}

func TestFileGuardCanonicalizesWriteRefresh(t *testing.T) {
	guard := newFileGuard()
	first, second := time.Unix(1, 0), time.Unix(2, 0)
	guard.recordRead(filepath.Join("relative", "file.txt"), first)
	guard.recordWrite(filepath.Join("relative", "file.txt"), second)
	if len(guard.stamps) != 1 || !guard.stamps[canonicalGuardPath(filepath.Join("relative", "file.txt"))].Equal(second) {
		t.Fatalf("canonical write refresh = %+v", guard.stamps)
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
