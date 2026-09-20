package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/bryann2k/maestro/internal/git"
)

func TestReviewFileCacheRejectsOversizeSourceBeforeRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.txt")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxReviewSourceFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	cache, err := newReviewFileCache(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	data, err := cache.Read("generated.txt")
	if err == nil || !strings.Contains(err.Error(), "per-file review limit") {
		t.Fatalf("Read error = %v, want explicit per-file review limit", err)
	}
	if data != nil || cache.retained != 0 || cache.Err() == nil {
		t.Fatalf("oversize read retained data=%d bytes, aggregate=%d, cache error=%v", len(data), cache.retained, cache.Err())
	}
}

func TestReviewFileCacheRejectsAggregateLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "next.txt"), []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := newReviewFileCache(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	cache.retained = maxReviewSourceBytes - 1

	if _, err := cache.Read("next.txt"); err == nil || !strings.Contains(err.Error(), "aggregate review limit") {
		t.Fatalf("Read error = %v, want explicit aggregate review limit", err)
	}
}

func TestReviewFileCacheReusesOneImmutableRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	if err := os.WriteFile(path, []byte("package initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := newReviewFileCache(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	first, err := cache.Read("source.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := cache.Read("source.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "package initial\n" || string(second) != string(first) {
		t.Fatalf("cached reads = %q then %q, want one immutable snapshot", first, second)
	}
}

func TestBoundReviewChangeInventoryRejectsExcessiveFiles(t *testing.T) {
	changes := make([]gitpkg.FileChange, maxReviewChangedFiles+1)
	if _, err := boundReviewChangeInventory(changes, nil); err == nil || !strings.Contains(err.Error(), "change inventory") {
		t.Fatalf("boundReviewChangeInventory error = %v, want explicit file-count limit", err)
	}
}
