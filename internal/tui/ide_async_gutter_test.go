package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/orchestrator"
)

func TestIdleIDEOpenDoesNotWaitForGitFileList(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 34})
	original := ideListFiles
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	ideListFiles = func(ctx context.Context, _ string, _ int) ([]string, error) {
		close(started)
		select {
		case <-release:
			return []string{"target.txt"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() {
		ideListFiles = original
		if !released {
			close(release)
		}
	}()

	startedAt := time.Now()
	cmd := m.ToggleIDE()
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("idle IDE open blocked for %s", elapsed)
	}
	if cmd == nil || m.ide == nil || !m.ide.filesLoading {
		t.Fatalf("idle IDE did not schedule its workspace snapshot: cmd=%v ide=%v loading=%v", cmd != nil, m.ide != nil, m.ide != nil && m.ide.filesLoading)
	}
	cmd = finishIDEHydrationForTest(t, m, cmd)
	if cmd == nil {
		t.Fatal("IDE hydration did not schedule its workspace file list")
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("IDE file-list worker did not start")
	}
	focus := m.ide.Focus
	startedAt = time.Now()
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 28})
	_ = m.View()
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("IDE resize/View waited for git listing for %s", elapsed)
	}
	if m.ide.Focus != focus || m.width != 110 || m.height != 28 {
		t.Fatalf("IDE async listing disturbed focus/resize: focus=%v want=%v size=%dx%d", m.ide.Focus, focus, m.width, m.height)
	}

	close(release)
	released = true
	m.Update(<-result)
	if m.ide.filesLoading || !containsString(m.ide.files(), "target.txt") {
		t.Fatalf("IDE snapshot not applied: loading=%v files=%v", m.ide.filesLoading, m.ide.files())
	}
}

func TestIDEGutterWorkerIgnoresStaleBufferResult(t *testing.T) {
	m, dir := newTestModel(t)
	for _, name := range []string{"first.go", "second.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package sample\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = m.ToggleIDE()

	original := ideGutterLoader
	ideGutterLoader = func(_ context.Context, _ orchestrator.WorkspaceSnapshot, path string) (*editor.Gutter, error) {
		return &editor.Gutter{Path: path, Signs: map[int]editor.Sign{1: editor.SignAdded}}, nil
	}
	defer func() { ideGutterLoader = original }()

	if !m.ide.OpenFileAt("first.go") {
		t.Fatal("open first buffer")
	}
	first := m.refreshIDEGutter()
	if !m.ide.OpenFileAt("second.go") {
		t.Fatal("open second buffer")
	}
	second := m.refreshIDEGutter()
	if first == nil || second == nil {
		t.Fatal("gutter refresh was not scheduled")
	}

	m.Update(first())
	secondPath := filepath.Join(dir, "second.go")
	if got := filepath.Clean(m.ide.UI.Gutter.Path); got != filepath.Clean(secondPath) || !m.ide.gutterDeferred {
		t.Fatalf("stale gutter replaced current buffer: path=%q loading=%v", got, m.ide.gutterDeferred)
	}
	m.Update(second())
	if got := filepath.Clean(m.ide.UI.Gutter.Path); got != filepath.Clean(secondPath) || m.ide.gutterDeferred {
		t.Fatalf("current gutter was not applied: path=%q loading=%v", got, m.ide.gutterDeferred)
	}
}

func TestIDERenderDoesNotRunGutterIO(t *testing.T) {
	m, dir := newTestModel(t)
	path := filepath.Join(dir, "slow.go")
	if err := os.WriteFile(path, []byte("package slow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = m.ToggleIDE()
	if !m.ide.OpenFileAt("slow.go") {
		t.Fatal("open slow buffer")
	}

	original := ideGutterLoader
	called := make(chan struct{}, 1)
	ideGutterLoader = func(context.Context, orchestrator.WorkspaceSnapshot, string) (*editor.Gutter, error) {
		called <- struct{}{}
		return nil, context.Canceled
	}
	defer func() { ideGutterLoader = original }()

	done := make(chan struct{})
	go func() {
		_ = m.View()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("IDE View blocked on gutter I/O")
	}
	select {
	case <-called:
		t.Fatal("IDE View invoked the gutter loader")
	default:
	}
	if !m.ide.gutterDeferred {
		t.Fatal("buffer navigation was not projected as a loading gutter")
	}
}
