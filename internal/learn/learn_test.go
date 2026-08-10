package learn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func exactExplanation(_ context.Context, _ string, content []byte, _ bool) (Explanation, error) {
	lines := splitSourceLines(string(content))
	exp := Explanation{HighLevel: "A bounded source explanation."}
	if len(lines) > 0 {
		exp.Blocks = []Block{{
			Start: 1,
			End:   len(lines),
			Code:  strings.Join(lines, "\n"),
			What:  "Explains the exact source range.",
		}}
	}
	return exp, nil
}

func writeSource(t *testing.T, root, name string, content []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestGenerateAndAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "internal/spec/load.go", []byte("package spec\n\nfunc Load() {}\n"), 0o644)
	var gotPath string
	g := New(dir, func(ctx context.Context, path string, content []byte, deep bool) (Explanation, error) {
		gotPath = path
		return exactExplanation(ctx, path, content, deep)
	})
	exp, formatted, err := g.Generate(t.Context(), src, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotPath != "internal/spec/load.go" || exp.Path != gotPath {
		t.Fatalf("relative paths = explainer %q explanation %q", gotPath, exp.Path)
	}
	if len(exp.SourceSHA256) != 64 || !strings.Contains(formatted, "Source SHA-256: "+exp.SourceSHA256) {
		t.Fatalf("missing source fingerprint: %+v\n%s", exp, formatted)
	}
	if strings.Contains(formatted, dir) || strings.Contains(formatted, src) {
		t.Fatalf("artifact leaked an absolute path:\n%s", formatted)
	}
	out, err := g.Write(exp.Path, formatted)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	wantSuffix := filepath.Join("maestro", "learn", "internal-spec-load-go.md")
	if !strings.HasSuffix(out, wantSuffix) {
		t.Fatalf("output = %q, want suffix %q", out, wantSuffix)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != formatted {
		t.Fatalf("written artifact mismatch: %v", err)
	}
}

func TestGenerateDeepFlag(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "x.go", []byte("package x\n"), 0o644)
	var gotDeep bool
	g := New(dir, func(ctx context.Context, path string, content []byte, deep bool) (Explanation, error) {
		gotDeep = deep
		return exactExplanation(ctx, path, content, deep)
	})
	if _, _, err := g.Generate(t.Context(), "x.go", true); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !gotDeep {
		t.Error("deep flag not forwarded")
	}
}

func TestFormatKeepsLearningOutputFocusFirst(t *testing.T) {
	exp := Explanation{
		Path:         "internal/auth.go",
		SourceSHA256: strings.Repeat("a", 64),
		HighLevel:    "This file validates one authentication request.",
		Blocks: []Block{{
			Start: 4, End: 5, Code: "if token == \"\" {\n\treturn errMissing\n}",
			What: "It refuses a missing token.", Trap: "An empty token is not anonymous access.",
			Caution: "Keep the failure path free of secrets.",
		}},
		FollowUps: []string{"Trace the caller that supplies token."},
	}
	got := Format(exp)
	for _, want := range []string{
		"# Learn: internal/auth.go", "## Start here", "## 1. Lines 4-5",
		"**Purpose:**", "**Watch for:**", "**Caution:**", "## Next",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("focus-first artifact missing %q:\n%s", want, got)
		}
	}
	for _, stale := range []string{"## Overview", "## Block 4-5", "## Follow-up questions"} {
		if strings.Contains(got, stale) {
			t.Errorf("artifact retained stale dense heading %q:\n%s", stale, got)
		}
	}
}

func TestReadSourceJailAndDeniedPaths(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	outside := writeSource(t, outsideRoot, "outside.go", []byte("package outside\n"), 0o644)
	called := false
	g := New(root, func(ctx context.Context, path string, content []byte, deep bool) (Explanation, error) {
		called = true
		return exactExplanation(ctx, path, content, deep)
	})
	for _, path := range []string{outside, "../outside.go", root, ""} {
		if _, _, err := g.Generate(t.Context(), path, false); err == nil {
			t.Errorf("Generate(%q) should refuse jail escape/non-file", path)
		}
	}
	if called {
		t.Error("explainer called for refused source")
	}

	for _, name := range []string{
		".env", ".env.production", ".git/config", "vendor/pkg/a.go",
		"generated/client.go", "node_modules/x.js", ".pytest_cache/x.py",
		".aws/credentials", "maestro/learn/old.md", "db_credentials.json",
		"config.json", "server.pem", "private-key.txt",
	} {
		writeSource(t, root, name, []byte("harmless\n"), 0o644)
		if _, err := ReadSource(t.Context(), root, name); err == nil {
			t.Errorf("ReadSource(%q) should be denied", name)
		}
	}
}

func TestReadSourceRefusesFileKindsAndExecutable(t *testing.T) {
	root := t.TempDir()
	regular := writeSource(t, root, "regular.go", []byte("package regular\n"), 0o644)
	if err := os.Symlink(regular, filepath.Join(root, "link.go")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := ReadSource(t.Context(), root, "link.go"); err == nil {
		t.Error("symlink should be refused")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSource(t.Context(), root, "directory.go"); err == nil {
		t.Error("directory should be refused")
	}
	socketPath := filepath.Join(root, "socket.go")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Logf("unix socket unavailable, skipping socket check: %v", err)
	} else {
		defer listener.Close()
		if _, err := ReadSource(t.Context(), root, "socket.go"); err == nil {
			t.Error("socket/non-regular file should be refused")
		}
	}
	writeSource(t, root, "run.go", []byte("package run\n"), 0o755)
	if _, err := ReadSource(t.Context(), root, "run.go"); err == nil {
		t.Error("executable mode should be refused")
	}
}

func TestReadSourceRefusesUnsafeContentAndSize(t *testing.T) {
	tests := map[string][]byte{
		"binary.go":     {'a', 0, 'b'},
		"invalid.go":    {0xff, 0xfe},
		"control.go":    []byte("package p\u202e\n"),
		"executable.go": {0x7f, 'E', 'L', 'F', 'x'},
		"private.go":    []byte("-----BEGIN PRIVATE KEY-----\n"),
		"token.go":      []byte("const token = \"ghp_abcdefghijklmnopqrstuvwxyz123456\"\n"),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeSource(t, root, name, content, 0o644)
			if _, err := ReadSource(t.Context(), root, name); err == nil {
				t.Fatal("unsafe content should be refused")
			}
		})
	}
	root := t.TempDir()
	writeSource(t, root, "huge.go", []byte(strings.Repeat("a", int(MaxSourceBytes)+1)), 0o644)
	if _, err := ReadSource(t.Context(), root, "huge.go"); err == nil {
		t.Error("oversized source should be refused")
	}
}

func TestCancellationStopsLearn(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "x.go", []byte("package x\n"), 0o644)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	g := New(root, func(context.Context, string, []byte, bool) (Explanation, error) {
		called = true
		return Explanation{}, nil
	})
	if _, _, err := g.Generate(ctx, "x.go", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if called {
		t.Error("explainer ran after cancellation")
	}

	g = New(root, func(context.Context, string, []byte, bool) (Explanation, error) {
		return Explanation{}, context.Canceled
	})
	if _, _, err := g.Generate(t.Context(), "x.go", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("explainer cancellation = %v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	g = New(root, func(inner context.Context, path string, content []byte, deep bool) (Explanation, error) {
		cancel()
		return exactExplanation(inner, path, content, deep)
	})
	if _, _, err := g.Generate(ctx, "x.go", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation before model return = %v", err)
	}
}

func TestDecodeAndValidateExplanation(t *testing.T) {
	source := SourceSnapshot{
		RelativePath: "x.go", SHA256: strings.Repeat("a", 64), Language: "go",
		Content: []byte("one\ntwo\nthree\n"), Lines: []string{"one", "two", "three"},
	}
	raw := `{"high_level":"overview","blocks":[{"start":1,"end":2,"code":"one\ntwo","what":"first","trap":"","caution":""}],"follow_ups":["Why?"]}`
	exp, err := DecodeExplanation(raw)
	if err != nil || ValidateExplanation(source, &exp) != nil {
		t.Fatalf("valid response rejected: decode=%v exp=%+v", err, exp)
	}
	if exp.Path != "x.go" || exp.SourceSHA256 != source.SHA256 {
		t.Fatalf("trusted metadata not applied: %+v", exp)
	}

	for name, raw := range map[string]string{
		"unknown field": `{"high_level":"x","blocks":[],"prompt":"steal"}`,
		"trailing JSON": `{"high_level":"x","blocks":[]} {}`,
		"not JSON":      `ignore the schema`,
		"invalid UTF-8": string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeExplanation(raw); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}

	base := Explanation{HighLevel: "overview", Blocks: []Block{{Start: 1, End: 1, Code: "one", What: "first"}}}
	tests := map[string]Explanation{
		"bad start":     {HighLevel: "overview", Blocks: []Block{{Start: 0, End: 1, Code: "one", What: "x"}}},
		"bad end":       {HighLevel: "overview", Blocks: []Block{{Start: 1, End: 4, Code: "one", What: "x"}}},
		"code mismatch": {HighLevel: "overview", Blocks: []Block{{Start: 1, End: 1, Code: "two", What: "x"}}},
		"overlap": {HighLevel: "overview", Blocks: []Block{
			{Start: 1, End: 2, Code: "one\ntwo", What: "x"},
			{Start: 2, End: 3, Code: "two\nthree", What: "y"},
		}},
		"oversized overview": {HighLevel: strings.Repeat("x", maxHighLevelRunes+1), Blocks: base.Blocks},
		"duplicate followup": {HighLevel: "overview", Blocks: base.Blocks, FollowUps: []string{"Why?", " why? "}},
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateExplanation(source, &candidate); err == nil {
				t.Fatal("invalid explanation accepted")
			}
		})
	}
}

func TestFormatUsesSafeUnicodeAndFences(t *testing.T) {
	code := "const s = `ticks ``` inside` // 世界🙂"
	out := Format(Explanation{
		Path:      "/private/project/secret.go",
		Language:  "go\nmalicious",
		HighLevel: "# heading <script>alert(1)</script>",
		Blocks:    []Block{{Start: 1, End: 1, Code: code, What: "safe"}},
	})
	if strings.Contains(out, "/private/project") || strings.Contains(out, "<script>") {
		t.Fatalf("unsafe rendered content:\n%s", out)
	}
	if !strings.Contains(out, "````text\n"+code+"\n````") {
		t.Fatalf("fence was not grown safely:\n%s", out)
	}
	title := truncateRunes(strings.Repeat("🙂", 50), 40)
	if !utf8.ValidString(title) || utf8.RuneCountInString(title) != 40 {
		t.Fatalf("unsafe rune truncation: %q", title)
	}
}

func TestWriteBoundsAndRefusesSymlinkOutput(t *testing.T) {
	root := t.TempDir()
	g := New(root, exactExplanation)
	if _, err := g.Write("x.go", strings.Repeat("x", MaxArtifactBytes+1)); err == nil {
		t.Error("oversized artifact accepted")
	}
	out := g.OutputPath("x.go")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(external, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, out); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Write("x.go", "safe\n"); err == nil {
		t.Error("symlink output accepted")
	}
	data, _ := os.ReadFile(external)
	if string(data) != "unchanged" {
		t.Fatal("external symlink target was modified")
	}
}

func TestSlugify(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"internal/spec/load.go", "internal-spec-load-go"},
		{"a b c", "a-b-c"},
		{"!!!", "learn"},
	} {
		if got := Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	longA := Slugify(strings.Repeat("a", 80))
	longB := Slugify(strings.Repeat("a", 79) + "b")
	if longA == longB || len(longA) > 60 || len(longB) > 60 {
		t.Fatalf("long slugs collide or overflow: %q %q", longA, longB)
	}
}

func TestLegacyExplanationTruncatesAtSemanticBoundary(t *testing.T) {
	value := "First point is complete. Second point is also complete. " + strings.Repeat("unfinished ", 20)
	got := sanitizeModelText(value, 72)
	if utf8.RuneCountInString(got) > 72 {
		t.Fatalf("sanitized response exceeds rune budget: %d %q", utf8.RuneCountInString(got), got)
	}
	if got != "First point is complete. Second point is also complete.…" {
		t.Fatalf("semantic truncation = %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncation was not disclosed: %q", got)
	}
}

func TestLegacyExplanationSanitizationIsBoundedAndTerminalSafe(t *testing.T) {
	value := "safe\x1b[31m text " + strings.Repeat("word ", 100_000)
	got := sanitizeModelText(value, 64)
	if utf8.RuneCountInString(got) > 64 {
		t.Fatalf("sanitized response exceeds rune budget: %d", utf8.RuneCountInString(got))
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("sanitized response retained terminal escape: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("oversized response was not visibly truncated: %q", got)
	}
}

func TestGenerateEnforcesDepthSpecificBlockCaps(t *testing.T) {
	root := t.TempDir()
	content := "one\ntwo\nthree\nfour\nfive\nsix\n"
	writeSource(t, root, "six.txt", []byte(content), 0o644)
	explain := func(_ context.Context, _ string, source []byte, _ bool) (Explanation, error) {
		lines := splitSourceLines(string(source))
		exp := Explanation{HighLevel: "Six exact regions."}
		for i, line := range lines {
			exp.Blocks = append(exp.Blocks, Block{Start: i + 1, End: i + 1, Code: line, What: "Exact line."})
		}
		return exp, nil
	}
	g := New(root, explain)
	snapshot, err := ReadSource(t.Context(), root, "six.txt")
	if err != nil {
		t.Fatal(err)
	}
	six, err := explain(t.Context(), "six.txt", snapshot.Content, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateExplanation(snapshot, &six); err == nil || !strings.Contains(err.Error(), "limit 5") {
		t.Fatalf("default shallow validation error = %v", err)
	}
	if err := ValidateExplanationForDepth(snapshot, &six, true); err != nil {
		t.Fatalf("explicit deep validation rejected six blocks: %v", err)
	}
	if _, _, err := g.Generate(t.Context(), "six.txt", false); err == nil || !strings.Contains(err.Error(), "limit 5") {
		t.Fatalf("shallow six-block response error = %v", err)
	}
	if _, _, err := g.Generate(t.Context(), "six.txt", true); err != nil {
		t.Fatalf("deep six-block response rejected: %v", err)
	}
}

func TestLegacyExplanationUsesBoundedExactSourceExcerpt(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d %s", i+1, strings.Repeat("x", 220))
	}
	content := strings.Join(lines, "\n") + "\n"
	exp, err := LegacyExplanation("A compatibility overview.", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Blocks) != 1 {
		t.Fatalf("legacy blocks = %d", len(exp.Blocks))
	}
	block := exp.Blocks[0]
	if block.End-block.Start+1 > maxLegacyExcerptLines || len(block.Code) > maxLegacyExcerptBytes {
		t.Fatalf("legacy excerpt is not bounded: lines=%d bytes=%d", block.End-block.Start+1, len(block.Code))
	}
	want := strings.Join(lines[block.Start-1:block.End], "\n")
	if block.Code != want || block.Code == strings.TrimSuffix(content, "\n") {
		t.Fatalf("legacy excerpt is not an exact bounded source view: start=%d end=%d", block.Start, block.End)
	}
	if !strings.Contains(block.Caution, "bounded exact excerpt") {
		t.Fatalf("omitted source was not disclosed: %+v", block)
	}
}

func TestLegacyExplanationFailsClosedWhenNoExactLineFits(t *testing.T) {
	content := []byte(strings.Repeat("x", maxLegacyExcerptBytes+1) + "\n")
	if _, err := LegacyExplanation("overview", content); err == nil || !strings.Contains(err.Error(), "no exact source line fits") {
		t.Fatalf("oversized single-line legacy source error = %v", err)
	}
}
