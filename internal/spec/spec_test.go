package spec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSpecBody = `# Spec — Test

## Goal

Round-trip test.
`

func testSpec() *Spec {
	return &Spec{
		ID:       "test-spec",
		Title:    "Test Spec",
		Status:   StatusProposal,
		Category: "feat",
		Tags:     []string{"maestro", "type/spec"},
		Batches: []Batch{{
			ID:    "b1",
			Name:  "Foundations",
			Files: []string{"internal/spec/"},
			Tasks: []string{"write code", "write tests"},
		}},
		Success:   []string{"round-trip works"},
		Decisions: []string{"keep it simple"},
		Created:   "2026-08-01",
		Body:      testSpecBody,
	}
}

func TestMarshalParseRoundTrip(t *testing.T) {
	s := testSpec()
	data, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ID != s.ID || got.Title != s.Title || got.Status != s.Status || got.Category != s.Category {
		t.Errorf("Parse mismatch: got %+v, want %+v", got, s)
	}
	if len(got.Batches) != 1 || got.Batches[0].ID != "b1" || got.Batches[0].Name != "Foundations" {
		t.Errorf("Parse batches: got %+v", got.Batches)
	}
	if got.Body != testSpecBody {
		t.Errorf("Parse body: got %q, want %q", got.Body, testSpecBody)
	}
	if !strings.Contains(string(data), "---") {
		t.Error("Marshal output has no frontmatter delimiters")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no frontmatter", "# No Frontmatter\n", "missing YAML frontmatter"},
		{"unclosed frontmatter", "---\nid: x\n", "never closed"},
		{"bad yaml", "---\nid: [unclosed\n---\n", "parse frontmatter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse(%q) error = %v, want containing %q", tt.name, err, tt.want)
			}
		})
	}
}

func TestSpecValid(t *testing.T) {
	tests := []struct {
		name string
		spec *Spec
		ok   bool
	}{
		{"valid", testSpec(), true},
		{"bad id", &Spec{ID: "Bad ID", Title: "x", Status: StatusProposal}, false},
		{"empty title", &Spec{ID: "ok-id", Status: StatusProposal}, false},
		{"bad status", &Spec{ID: "ok-id", Title: "x", Status: "nope"}, false},
		{"bad category", &Spec{ID: "ok-id", Title: "x", Status: StatusProposal, Category: "bogus"}, false},
		{"no category ok", &Spec{ID: "ok-id", Title: "x", Status: StatusProposal}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Valid()
			if tt.ok && err != nil {
				t.Errorf("Valid() = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Error("Valid() = nil, want error")
			}
		})
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()

	s := testSpec()
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != s.ID || got.Title != s.Title {
		t.Errorf("Load mismatch: %+v vs %+v", got, s)
	}

	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != s.ID || list[0].Status != StatusProposal {
		t.Errorf("List: %+v", list)
	}
}

func TestStoreWriteTrio(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()

	if err := st.WriteTrio(ctx, testSpec(), "# design", "# tasks"); err != nil {
		t.Fatalf("WriteTrio: %v", err)
	}
	for _, f := range []string{FileSpec, FileDesign, FileTasks} {
		if _, err := os.Stat(filepath.Join(st.Path(testSpec().ID), f)); err != nil {
			t.Errorf("WriteTrio missing %s: %v", f, err)
		}
	}
}

func TestStoreWriteTrioIsExclusive(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()
	if err := st.WriteTrio(ctx, testSpec(), "first design", "first tasks"); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteTrio(ctx, testSpec(), "overwritten", "overwritten"); err == nil {
		t.Fatal("second trio write should reject an existing spec")
	}
	data, err := os.ReadFile(filepath.Join(st.Path(testSpec().ID), FileDesign))
	if err != nil || string(data) != "first design" {
		t.Fatalf("existing trio changed after collision: %q, %v", data, err)
	}
}

func TestStoreArchive(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()

	if err := st.Save(ctx, testSpec()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := st.Archive(ctx, testSpec().ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := os.Stat(st.Path(testSpec().ID)); !os.IsNotExist(err) {
		t.Errorf("source still exists after archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.dir, ArchiveDir, testSpec().ID, FileSpec)); err != nil {
		t.Errorf("archived spec missing: %v", err)
	}
	if err := st.RestoreArchive(ctx, testSpec().ID); err != nil {
		t.Fatalf("RestoreArchive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Path(testSpec().ID), FileSpec)); err != nil {
		t.Fatalf("restored spec missing: %v", err)
	}
	if err := st.Archive(ctx, testSpec().ID); err != nil {
		t.Fatalf("Archive after restore: %v", err)
	}
	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List after archive = %+v, want empty", list)
	}

	if err := st.Archive(ctx, testSpec().ID); err == nil {
		t.Error("second Archive should fail (source gone)")
	}
	if err := st.Archive(ctx, "missing-spec"); err == nil {
		t.Error("Archive of unknown spec should fail")
	}
}

func TestStoreIdeas(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()

	if err := st.Save(ctx, testSpec()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	empty, err := st.Ideas(ctx, testSpec().ID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("Ideas on empty backlog = %v, %v", empty, err)
	}
	if err := st.AppendIdea(ctx, testSpec().ID, "parallel builds"); err != nil {
		t.Fatalf("AppendIdea: %v", err)
	}
	if err := st.AppendIdea(ctx, testSpec().ID, "arena mode"); err != nil {
		t.Fatalf("AppendIdea: %v", err)
	}
	ideas, err := st.Ideas(ctx, testSpec().ID)
	if err != nil {
		t.Fatalf("Ideas: %v", err)
	}
	if len(ideas) != 2 || !strings.Contains(ideas[0], "parallel builds") || !strings.Contains(ideas[1], "arena mode") {
		t.Errorf("Ideas = %v", ideas)
	}
}

func TestLoadRejectsIDMismatch(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()

	s := testSpec()
	s.ID = "other-id"
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := st.Load(ctx, testSpec().ID); err == nil {
		t.Error("Load with mismatched id should fail")
	}
}

func TestListSkipsNonSpecDirs(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "specs"))
	ctx := context.Background()

	if err := st.Save(ctx, testSpec()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	other := filepath.Join(st.dir, "notes.md")
	if err := os.WriteFile(other, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List = %+v, want only the spec", list)
	}
}

func TestListMissingRoot(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "no-such-dir"))
	list, err := st.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List = %+v, want empty", list)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "specs"))
	if err := st.Save(context.Background(), &Spec{ID: "Bad", Status: "x"}); err == nil {
		t.Error("Save of invalid spec should fail")
	}
}
