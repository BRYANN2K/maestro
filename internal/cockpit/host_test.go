package cockpit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestCockpitPermissionRequiresExplicitAllow(t *testing.T) {
	for _, answer := range []string{"0", "", "1"} {
		t.Run("answer="+answer, func(t *testing.T) {
			h := New()
			h.send = func(v any) error {
				m := v.(map[string]any)
				if m["event"] == "prompt" {
					p := m["data"].(map[string]any)
					h.mu.Lock()
					ch := h.pending[p["id"].(string)]
					h.mu.Unlock()
					ch <- answer
				}
				return nil
			}
			err := h.Authorize(t.Context(), agentcore.ToolCall{Name: "write", Args: `{"path":"file"}`}, agentcore.ToolSpec{NeedsApproval: true})
			if (err == nil) != (answer == "1") {
				t.Fatalf("answer=%q error=%v", answer, err)
			}
			if len(h.pending) != 0 {
				t.Fatal("prompt leaked")
			}
		})
	}
}
func TestCockpitInputCancellationReleasesRead(t *testing.T) {
	h := New()
	h.ctx = t.Context()
	ctx, cancel := context.WithCancel(t.Context())
	h.operationCtx = ctx
	h.inputAllowed = true
	started := make(chan struct{})
	h.send = func(v any) error {
		if v.(map[string]any)["event"] == "prompt" {
			close(started)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := h.Read(make([]byte, 32)); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled input blocked")
	}
	if len(h.pending) != 0 {
		t.Fatal("prompt leaked")
	}
}
func TestCockpitPreviewContainsFilesAndPreservesSource(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# Heading\n`literal`\n"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip(err)
	}
	for _, path := range []string{"escape", "../secret", outside, "."} {
		if _, err := readFile(root, path); err == nil {
			t.Fatalf("preview accepted %q", path)
		}
	}
	got, err := readFile(root, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(got)
	if !strings.Contains(string(data), "# Heading") {
		t.Fatal(string(data))
	}
	os.WriteFile(filepath.Join(root, "large"), make([]byte, (256<<10)+1), 0600)
	if _, err := readFile(root, "large"); err == nil {
		t.Fatal("oversized preview")
	}
}

func TestCockpitNoninteractiveStdinDoesNotPrompt(t *testing.T) {
	h := New()
	h.ctx = t.Context()
	h.operationCtx = t.Context()
	h.send = func(any) error { t.Fatal("noninteractive command requested a value"); return nil }
	if _, err := h.Read(make([]byte, 8)); !errors.Is(err, io.EOF) {
		t.Fatalf("stdin=%v", err)
	}
}
