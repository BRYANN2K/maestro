package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/spec"
)

func TestNewSessionPhase(t *testing.T) {
	s := New("my-project")
	if !s.Phase.Valid() || s.Phase != PhaseChat {
		t.Errorf("New session phase = %q, want chat", s.Phase)
	}
	if s.ID == "" || s.Project != "my-project" {
		t.Errorf("New session = %+v", s)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	ctx := context.Background()

	s := New("proj")
	s.Phase = PhaseBuild
	s.SpecID = "api-go"
	s.PermQueue = []Permission{{ID: "p1", Type: "tool", Tool: "bash", Status: "pending"}}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, "proj", s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Phase != PhaseBuild || got.SpecID != "api-go" || len(got.PermQueue) != 1 {
		t.Errorf("Load mismatch: %+v", got)
	}
}

func TestStoreSaveInvalid(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Save(context.Background(), Session{}); err == nil {
		t.Error("Save of empty session should fail")
	}
	st2 := NewStore(t.TempDir())
	bad := New("p")
	bad.Phase = Phase("nope")
	if err := st2.Save(context.Background(), bad); err == nil {
		t.Error("Save of session with invalid phase should fail")
	}
}

func TestStoreRejectsTraversalAndIdentityMismatch(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"../escape", "a/b", "..", "."} {
		if _, err := store.Load(ctx, "project", id); err == nil {
			t.Fatalf("Load accepted unsafe id %q", id)
		}
	}
	if err := store.Save(ctx, Session{ID: "../escape", Project: "project", Phase: PhaseChat}); err == nil {
		t.Fatal("Save accepted unsafe id")
	}
	dir := filepath.Join(store.Dir(), "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"id":"different","project":"project","phase":"chat"}`)
	if err := os.WriteFile(filepath.Join(dir, "wanted.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "project", "wanted"); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("Load identity error = %v", err)
	}
}

func TestStoreListAndLatest(t *testing.T) {
	st := NewStore(t.TempDir())
	ctx := context.Background()

	if _, err := st.Latest(ctx, "proj"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Latest on empty store = %v, want os.ErrNotExist", err)
	}

	older := New("proj")
	older.ID = "111"
	newer := New("proj")
	newer.ID = "222"
	if err := st.Save(ctx, older); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := st.Save(ctx, newer); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ids, err := st.List(ctx, "proj")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 2 || ids[0] != "222" || ids[1] != "111" {
		t.Errorf("List = %v, want newest first", ids)
	}

	latest, err := st.Latest(ctx, "proj")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID != "222" {
		t.Errorf("Latest = %s, want 222", latest.ID)
	}
}

func TestStoreProjectsIsolated(t *testing.T) {
	st := NewStore(t.TempDir())
	ctx := context.Background()
	if err := st.Save(ctx, New("proj-a")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ids, err := st.List(ctx, "proj-b")
	if err != nil || len(ids) != 0 {
		t.Errorf("List(proj-b) = %v, %v; want empty", ids, err)
	}
}

// writeSpec saves a spec with the given status into storeDir and returns the store.
func writeSpec(t *testing.T, storeDir, id, status string) {
	t.Helper()
	st := spec.NewStore(storeDir)
	s := &spec.Spec{ID: id, Title: id, Status: status}
	if err := st.Save(context.Background(), s); err != nil {
		t.Fatalf("save spec: %v", err)
	}
}

func TestRestoreValidatesPhaseAgainstSpec(t *testing.T) {
	ctx := context.Background()
	specDir := filepath.Join(t.TempDir(), "specs")

	writeSpec(t, specDir, "live", spec.StatusImplemented)
	writeSpec(t, specDir, "dead", spec.StatusArchived)

	tests := []struct {
		name   string
		specID string
		phase  Phase
		want   Phase
		notice string
	}{
		{"chat stays", "live", PhaseChat, PhaseChat, ""},
		{"build on live spec stays", "live", PhaseBuild, PhaseBuild, ""},
		{"build on archived spec resets", "dead", PhaseBuild, PhaseChat, "archived"},
		{"review on archived spec resets", "dead", PhaseReview, PhaseChat, "archived"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := NewStore(t.TempDir())
			sess := New("proj")
			sess.Phase = tt.phase
			sess.SpecID = tt.specID
			if err := st.Save(ctx, sess); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, notice, err := st.Restore(ctx, "proj", specDir)
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if got.Phase != tt.want {
				t.Errorf("phase = %q, want %q", got.Phase, tt.want)
			}
			if tt.notice == "" && notice != "" {
				t.Errorf("unexpected notice %q", notice)
			}
			if tt.notice != "" && !strings.Contains(notice, tt.notice) {
				t.Errorf("notice = %q, want containing %q", notice, tt.notice)
			}
		})
	}
}

func TestRestoreNoSpecValidationWithoutStore(t *testing.T) {
	st := NewStore(t.TempDir())
	sess := New("proj")
	sess.Phase = PhaseBuild
	sess.SpecID = "ghost"
	if err := st.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, notice, err := st.Restore(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got.Phase != PhaseBuild || notice != "" {
		t.Errorf("Restore = phase %q notice %q; want build, no notice", got.Phase, notice)
	}
}

func TestRestoreDefersMovedArchiveUntilGitTransactionIsVerified(t *testing.T) {
	ctx := context.Background()
	specDir := filepath.Join(t.TempDir(), "specs")
	writeSpec(t, specDir, "moved", spec.StatusImplemented)
	if err := spec.NewStore(specDir).Archive(ctx, "moved"); err != nil {
		t.Fatal(err)
	}
	st := NewStore(t.TempDir())
	sess := New("proj")
	sess.Phase = PhaseArchive
	sess.SpecID = "moved"
	if err := st.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, notice, err := st.Restore(ctx, "proj", specDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != PhaseArchive || got.SpecID != "moved" || !strings.Contains(notice, "verifying Git transaction") {
		t.Fatalf("restore = phase %q spec %q notice %q", got.Phase, got.SpecID, notice)
	}
}

func TestRestoreUnknownPhase(t *testing.T) {
	st := NewStore(t.TempDir())
	sess := New("proj")
	sess.Phase = Phase("bogus")
	data := `{"id":"` + sess.ID + `","project":"proj","phase":"bogus"}`
	dir := filepath.Join(st.Dir(), "proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sess.ID+".json"), []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, notice, err := st.Restore(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got.Phase != PhaseChat || !strings.Contains(notice, "unknown phase") {
		t.Errorf("Restore = %q, %q", got.Phase, notice)
	}
}

func TestPermissionStatuses(t *testing.T) {
	if PhaseBuild.needsSpec() != true || PhaseChat.needsSpec() != false {
		t.Error("needsSpec misclassified phases")
	}
}
