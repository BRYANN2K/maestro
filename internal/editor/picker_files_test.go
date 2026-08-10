package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestListFilesUsesGitTrackedAndUntrackedNULSafe(t *testing.T) {
	root := t.TempDir()
	runEditorGit(t, root, "init", "-q")
	writePickerFile(t, root, ".gitignore", []byte("*.test\n*.bin\n"))
	writePickerFile(t, root, "space name.txt", []byte("tracked space\n"))
	writePickerFile(t, root, "unicodé-界.txt", []byte("untracked unicode\n"))
	writePickerFile(t, root, "binary.bin", []byte{0x00, 0xff, 0x01, 0x00})
	writePickerFile(t, root, "root-only.md", []byte("untracked root\n"))
	writePickerFile(t, root, "hidden.test", []byte("ignored\n"))
	writePickerFile(t, root, filepath.Join("sub", "tracked.go"), []byte("package sub\n"))
	writePickerFile(t, root, filepath.Join("sub", "untracked.md"), []byte("untracked sub\n"))
	writePickerFile(t, root, filepath.Join("sub", "skip.test"), []byte("ignored\n"))
	gitlink := filepath.Join(root, "module-link")
	if err := os.Mkdir(gitlink, 0o755); err != nil {
		t.Fatal(err)
	}
	runEditorGit(t, gitlink, "init", "-q")
	runEditorGit(t, gitlink, "config", "user.email", "editor-test@maestro.local")
	runEditorGit(t, gitlink, "config", "user.name", "Maestro Editor Test")
	writePickerFile(t, gitlink, "README.md", []byte("nested repository\n"))
	runEditorGit(t, gitlink, "add", "README.md")
	runEditorGit(t, gitlink, "commit", "-q", "-m", "nested initial")

	tracked := []string{".gitignore", "space name.txt", filepath.Join("sub", "tracked.go")}
	newlineName := "line\nbreak.txt"
	if runtime.GOOS != "windows" {
		writePickerFile(t, root, newlineName, []byte("tracked newline\n"))
		tracked = append(tracked, newlineName)
	}
	gitArgs := append([]string{"add", "--"}, tracked...)
	runEditorGit(t, root, gitArgs...)
	runEditorGit(t, root, "add", "module-link")
	// A tracked file remains discoverable even if a later/current ignore rule
	// matches it; its binary contents must never affect filename discovery.
	runEditorGit(t, root, "add", "-f", "--", "binary.bin")

	want := []string{
		".gitignore",
		"binary.bin",
		"root-only.md",
		"space name.txt",
		filepath.Join("sub", "tracked.go"),
		filepath.Join("sub", "untracked.md"),
		"unicodé-界.txt",
	}
	if runtime.GOOS != "windows" {
		want = append(want, newlineName)
	}
	sort.Strings(want)
	if got := ListFiles(root, 100); !slices.Equal(got, want) {
		t.Fatalf("root files = %#v, want %#v", got, want)
	}

	wantSub := []string{"tracked.go", "untracked.md"}
	if got := ListFiles(filepath.Join(root, "sub"), 100); !slices.Equal(got, wantSub) {
		t.Fatalf("subdirectory files = %#v, want %#v", got, wantSub)
	}
	if got := ListFiles(root, 3); !slices.Equal(got, want[:3]) {
		t.Fatalf("limited files = %#v, want %#v", got, want[:3])
	}
}

func TestParseNULFileListDeduplicatesBeforeLimit(t *testing.T) {
	data := []byte("z.txt\x00a space.txt\x00z.txt\x00../escape.txt\x00/absolute.txt\x00line\nbreak.txt\x00a space.txt\x00")
	got := parseNULFileList(data, 3)
	want := []string{"a space.txt", "line\nbreak.txt", "z.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("parsed files = %#v, want %#v", got, want)
	}
	if got := parseNULFileList(data, 0); got != nil {
		t.Fatalf("zero-limit files = %#v, want nil", got)
	}
}

func TestReadGitFileListStreamsWithDeterministicCeiling(t *testing.T) {
	root := t.TempDir()
	writePickerFile(t, root, "a.txt", []byte("a\n"))
	writePickerFile(t, root, "z.txt", []byte("z\n"))

	got, stopped, err := readGitFileList(strings.NewReader("z.txt\x00a.txt\x00z.txt\x00"), root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stopped || !slices.Equal(got, []string{"a.txt"}) {
		t.Fatalf("streamed files = %#v, stopped=%v; want [a.txt] after complete sorted input", got, stopped)
	}

	boundedInput := strings.NewReader(strings.Repeat("a.txt\x00", listFilesGitMaxRecords+1))
	got, stopped, err = readGitFileList(boundedInput, root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || !slices.Equal(got, []string{"a.txt"}) {
		t.Fatalf("bounded files = %#v, stopped=%v; want deterministic ceiling", got, stopped)
	}
}

func TestListFilesFallsBackOutsideGit(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".standalone-project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writePickerFile(t, root, "alpha.txt", []byte("alpha\n"))
	writePickerFile(t, root, "space name.test", []byte("not Git-ignored\n"))
	writePickerFile(t, root, filepath.Join("src", "unicodé.go"), []byte("package src\n"))
	for _, skipped := range []string{
		filepath.Join(".hidden", "secret.txt"),
		filepath.Join("vendor", "dependency.go"),
		filepath.Join("node_modules", "package.js"),
		filepath.Join("specs", "internal.md"),
	} {
		writePickerFile(t, root, skipped, []byte("skip\n"))
	}

	want := []string{"alpha.txt", "space name.test", filepath.Join("src", "unicodé.go")}
	if got := ListFiles(root, 100); !slices.Equal(got, want) {
		t.Fatalf("fallback files = %#v, want %#v", got, want)
	}
	if got := ListFiles(root, 2); !slices.Equal(got, want[:2]) {
		t.Fatalf("limited fallback files = %#v, want %#v", got, want[:2])
	}
	if got := ListFiles(filepath.Join(root, "missing"), 10); got != nil {
		t.Fatalf("missing-root files = %#v, want nil", got)
	}
	fileRoot := filepath.Join(root, "alpha.txt")
	if got := ListFiles(fileRoot, 10); got != nil {
		t.Fatalf("regular-file root = %#v, want nil", got)
	}
}

func writePickerFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runEditorGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, output)
	}
}
