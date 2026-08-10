package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testMemory(t *testing.T) *Memory {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "memory"))
}

func TestRetainRecall(t *testing.T) {
	m := testMemory(t)
	ctx := context.Background()
	if _, err := m.Retain(ctx, "user prefers pgx over lib/pq", []string{"db"}); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if _, err := m.Retain(ctx, "spec API must stay backward compatible", []string{"api"}); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	recalled := m.Recall(ctx, "pgx driver", 5)
	if len(recalled) != 1 || !strings.Contains(recalled[0].Text, "pgx") {
		t.Fatalf("recall = %+v", recalled)
	}
	// Tags match too.
	byTag := m.Recall(ctx, "db", 5)
	if len(byTag) != 1 || !strings.Contains(byTag[0].Text, "pgx") {
		t.Fatalf("tag recall = %+v", byTag)
	}
	if len(m.All(ctx)) != 2 {
		t.Errorf("all = %d", len(m.All(ctx)))
	}
}

func TestRetainEmptyFails(t *testing.T) {
	m := testMemory(t)
	if _, err := m.Retain(context.Background(), "   ", nil); err == nil {
		t.Error("empty fact should fail")
	}
}

func TestReflectRoundTrip(t *testing.T) {
	m := testMemory(t)
	ctx := context.Background()
	m.Retain(ctx, "chose worktrees for isolation", []string{"git"})
	m.Retain(ctx, "chose postgres for storage", []string{"db"})
	out, err := m.Reflect(ctx)
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if !strings.Contains(out, "# Hindsight Reflection") || !strings.Contains(out, "worktrees") {
		t.Errorf("reflection = %q", out)
	}
	// reflections.md written.
	data, err := os.ReadFile(filepath.Join(m.dir, "reflections.md"))
	if err != nil || !strings.Contains(string(data), "postgres") {
		t.Errorf("reflections.md = %q, %v", data, err)
	}
}

func TestReflectEmpty(t *testing.T) {
	m := testMemory(t)
	if _, err := m.Reflect(context.Background()); err == nil {
		t.Error("reflect without facts should fail")
	}
}

func TestPersistenceAcrossInstances(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	m1 := New(dir)
	if _, err := m1.Retain(context.Background(), "persisted fact", []string{"x"}); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	m2 := New(dir)
	if got := m2.Recall(context.Background(), "persisted", 5); len(got) != 1 {
		t.Errorf("recall from new instance = %+v", got)
	}
}

func TestRecallEmptyQueryReturnsRecent(t *testing.T) {
	m := testMemory(t)
	m.Retain(context.Background(), "fact one", nil)
	m.Retain(context.Background(), "fact two", nil)
	got := m.Recall(context.Background(), "", 1)
	if len(got) != 1 || !strings.Contains(got[0].Text, "two") {
		t.Errorf("recall = %+v", got)
	}
}
