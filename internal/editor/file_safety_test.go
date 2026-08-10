package editor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLoadRejectsNonTextBeforeConstructingBuffer(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		reason FileBlockReason
	}{
		{name: "ELF", data: []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}, reason: FileBlockExecutable},
		{name: "Mach-O", data: []byte{0xcf, 0xfa, 0xed, 0xfe, 7, 0, 0, 1}, reason: FileBlockExecutable},
		{name: "PE", data: []byte("MZthis header is otherwise printable"), reason: FileBlockExecutable},
		{name: "NUL", data: []byte("prefix\x00suffix"), reason: FileBlockBinary},
		{name: "invalid UTF-8", data: []byte{'x', 0xff, 0xfe, 'y'}, reason: FileBlockInvalidUTF8},
		{name: "terminal control", data: []byte("safe\x1b[2Junsafe"), reason: FileBlockControl},
		{name: "C1 control", data: []byte("safe\u009bunsafe"), reason: FileBlockControl},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tui.test")
			if err := os.WriteFile(path, tc.data, 0o755); err != nil {
				t.Fatal(err)
			}
			buffer, err := Load(path)
			if buffer != nil || err == nil {
				t.Fatalf("Load = (%v, %v), want nil blocked error", buffer, err)
			}
			var blocked *UnsupportedFileError
			if !errors.As(err, &blocked) {
				t.Fatalf("error type = %T, want *UnsupportedFileError", err)
			}
			if blocked.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", blocked.Reason, tc.reason)
			}
			if !utf8.ValidString(err.Error()) || strings.ContainsAny(err.Error(), "\x00\x1b") {
				t.Fatalf("blocked error is not terminal-safe: %q", err)
			}
		})
	}
}

func TestLoadPreservesValidUnicodeTextAndNormalizesLineEndings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unicode.txt")
	want := "Bonjour, café 世界 👋\nseconde ligne\n"
	if err := os.WriteFile(path, []byte("Bonjour, café 世界 👋\r\nseconde ligne\r"), 0o600); err != nil {
		t.Fatal(err)
	}
	buffer, err := Load(path)
	if err != nil {
		t.Fatalf("Load valid Unicode text: %v", err)
	}
	if got := buffer.String(); got != want {
		t.Fatalf("buffer = %q, want %q", got, want)
	}
}

func TestLoadRejectsOversizedSparseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxEditableFileSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Load(path)
	var blocked *UnsupportedFileError
	if !errors.As(err, &blocked) || blocked.Reason != FileBlockTooLarge {
		t.Fatalf("Load oversized error = %v", err)
	}
}

func TestLoadRejectsFIFOWithoutBlocking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes use different filesystem semantics on Windows")
	}
	path := filepath.Join(t.TempDir(), "source.pipe")
	if err := exec.Command("mkfifo", path).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Load(path)
		done <- err
	}()
	select {
	case err := <-done:
		var blocked *UnsupportedFileError
		if !errors.As(err, &blocked) || blocked.Reason != FileBlockNonRegular {
			t.Fatalf("Load FIFO error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Load blocked while opening a FIFO")
	}
}

func TestEditorOpenFailureKeepsActiveBufferAndBinaryUnchanged(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "safe.txt")
	binaryPath := filepath.Join(dir, "tui.test")
	binary := []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 1, 2, 3}
	if err := os.WriteFile(textPath, []byte("active text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewEditor(dir)
	if err := e.Open(textPath); err != nil {
		t.Fatal(err)
	}
	active := e.Buffer()
	if err := e.Open(binaryPath); err == nil {
		t.Fatal("opening a binary should fail")
	}
	if e.Buffer() != active || len(e.Buffers) != 1 || active.String() != "active text\n" {
		t.Fatalf("failed open changed active editor state: active=%p buffers=%d text=%q", e.Buffer(), len(e.Buffers), active.String())
	}
	got, err := os.ReadFile(binaryPath)
	if err != nil || string(got) != string(binary) {
		t.Fatalf("binary changed after rejected open: %x, %v", got, err)
	}
	if files := ListFiles(dir, 20); !containsPath(files, "tui.test") {
		t.Fatalf("tracked-style binary should remain discoverable and be refused on open: %v", files)
	}
}

func TestPickerViewProjectsUnsafeNamesWithoutChangingSelection(t *testing.T) {
	raw := "tui\x1b[2J\u009b\u202e" + string([]byte{0xff}) + ".test"
	selected := ""
	picker := NewPicker()
	picker.Start("Fi\x1b[2Jles", []string{raw, "café/世界.go"}, func(value string) {
		selected = value
	})
	view := picker.View(80)
	if !utf8.ValidString(view) || strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\u009b") || strings.Contains(view, "\u202e") {
		t.Fatalf("picker emitted unsafe content: %q", view)
	}
	picker.Update(Key{Kind: KeyEnter})
	if selected != raw {
		t.Fatalf("selected value = %q, want exact raw path %q", selected, raw)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if filepath.ToSlash(path) == want {
			return true
		}
	}
	return false
}
