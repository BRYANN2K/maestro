package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeTitleSafeUnicodeSingleLine(t *testing.T) {
	input := "  Hello\n\t世界\u202E spoof\u2066 " + strings.Repeat("é", 100)
	got := NormalizeTitle(input)
	if strings.ContainsAny(got, "\r\n\t") || strings.ContainsRune(got, '\u202e') || strings.ContainsRune(got, '\u2066') {
		t.Fatalf("NormalizeTitle retained unsafe controls: %q", got)
	}
	if utf8.RuneCountInString(got) > MaxTitleRunes {
		t.Fatalf("NormalizeTitle length = %d, want <= %d", utf8.RuneCountInString(got), MaxTitleRunes)
	}
	if !strings.HasPrefix(got, "Hello 世界 spoof") {
		t.Fatalf("NormalizeTitle = %q", got)
	}
}

func TestFallbackTitleDeterministicAndMeaningful(t *testing.T) {
	title1, seed1, ok1 := FallbackTitle("## Build café support. Include tests and docs")
	title2, seed2, ok2 := FallbackTitle("## Build café support. Include tests and docs")
	if !ok1 || !ok2 || title1 != "Build café support." || title1 != title2 || seed1 == "" || seed1 != seed2 {
		t.Fatalf("fallbacks = (%q,%q,%v), (%q,%q,%v)", title1, seed1, ok1, title2, seed2, ok2)
	}
	if _, _, ok := FallbackTitle(" # -- [] \u202e "); ok {
		t.Fatal("punctuation-only input was considered meaningful")
	}
}

func TestStoreTitleCASRejectsLateGenerationAfterRename(t *testing.T) {
	store := NewStore(t.TempDir())
	saved := New("project")
	saved.Title, saved.TitleSeedHash, _ = FallbackTitle("Initial user request")
	saved.TitleSource = TitleSourceFallback
	if err := store.Save(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	renamed, swapped, err := store.CompareAndSwapTitle(t.Context(), saved.Project, saved.ID, saved.TitleSeedHash, TitleSourceFallback, "Human title", TitleSourceUser)
	if err != nil || !swapped || renamed.TitleSource != TitleSourceUser {
		t.Fatalf("rename CAS = %+v swapped=%v err=%v", renamed, swapped, err)
	}
	late, swapped, err := store.CompareAndSwapTitle(t.Context(), saved.Project, saved.ID, saved.TitleSeedHash, TitleSourceFallback, "Late model title", TitleSourceLLM)
	if err != nil || swapped || late.Title != "Human title" || late.TitleSource != TitleSourceUser {
		t.Fatalf("late CAS = %+v swapped=%v err=%v", late, swapped, err)
	}
}

func TestIndependentStoreStaleSaveCannotOverwritePersistedUserTitle(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	second := NewStore(dir)
	saved := New("project")
	saved.Title, saved.TitleSeedHash, _ = FallbackTitle("Initial request")
	saved.TitleSource = TitleSourceFallback
	if err := first.Save(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	stale, err := second.Load(t.Context(), saved.Project, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.SetUserTitle(t.Context(), saved.Project, saved.ID, "Human title"); err != nil {
		t.Fatal(err)
	}
	stale.Phase = PhasePropose
	if err := second.Save(t.Context(), stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Save error = %v, want ErrConflict", err)
	}
	got, err := first.Load(t.Context(), saved.Project, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Human title" || got.TitleSource != TitleSourceUser || got.Phase != PhaseChat {
		t.Fatalf("durable session changed after conflict = %+v", got)
	}
}

func TestIndependentStoresRespectFilesystemRecordLockAndCancellation(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	second := NewStore(dir)
	saved := New("project")
	saved.Title, saved.TitleSeedHash, _ = FallbackTitle("Initial request")
	saved.TitleSource = TitleSourceFallback
	if err := first.Save(t.Context(), saved); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- first.withRecordLock(context.Background(), saved.Project, saved.ID, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	stale, err := second.Load(t.Context(), saved.Project, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale.Phase = PhasePropose
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err = second.Save(ctx, stale)
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		<-holderDone
		t.Fatalf("Save while another store holds the record lock = %v, want deadline exceeded", err)
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}

	before, err := first.Load(t.Context(), saved.Project, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Phase != PhaseChat {
		t.Fatalf("timed-out save changed phase to %q", before.Phase)
	}
	if err := second.Save(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	after, err := first.Load(t.Context(), saved.Project, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Phase != PhasePropose || after.Title != saved.Title || after.TitleSource != TitleSourceFallback {
		t.Fatalf("serialized save = %+v", after)
	}
}

func TestIndependentStoreTitleCASHasSingleWinner(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	second := NewStore(dir)
	saved := New("project")
	saved.Title, saved.TitleSeedHash, _ = FallbackTitle("Initial request")
	saved.TitleSource = TitleSourceFallback
	if err := first.Save(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	type result struct {
		swapped bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, store := range []*Store{first, second} {
		go func(i int, store *Store) {
			<-start
			_, swapped, err := store.CompareAndSwapTitle(context.Background(), saved.Project, saved.ID, saved.TitleSeedHash, TitleSourceFallback, fmt.Sprintf("Model %d", i), TitleSourceLLM)
			results <- result{swapped: swapped, err: err}
		}(i, store)
	}
	close(start)
	winners := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.swapped {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want 1", winners)
	}
}

func TestIndependentStoresRejectStaleLifecycleWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	second := NewStore(dir)
	initial := New("project")
	created, err := first.Commit(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.Load(t.Context(), created.Project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Load(t.Context(), created.Project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.Phase = PhasePropose
	a.Conversation = []ConversationTurn{{Role: "user", Content: "new durable context"}}
	committed, err := first.Commit(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, created.Project, created.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b.Phase = PhaseBuild
	b.Conversation = nil
	if _, err := second.Commit(t.Context(), b); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Commit error = %v, want ErrConflict", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("conflicting Commit changed the durable record")
	}
	loaded, err := second.Load(t.Context(), created.Project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != committed.Revision || loaded.Phase != PhasePropose || len(loaded.Conversation) != 1 {
		t.Fatalf("durable lifecycle = %+v", loaded)
	}
}

func TestTitleCASMergesOntoLatestLifecycleButStaleLifecycleCannotEraseRename(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	second := NewStore(dir)
	initial := New("project")
	initial.Title, initial.TitleSeedHash, _ = FallbackTitle("initial request")
	initial.TitleSource = TitleSourceFallback
	created, err := first.Commit(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}

	lifecycle := created
	lifecycle.Phase = PhasePropose
	lifecycle, err = first.Commit(t.Context(), lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	titled, swapped, err := second.CompareAndSwapTitle(t.Context(), created.Project, created.ID, created.TitleSeedHash, TitleSourceFallback, "Model title", TitleSourceLLM)
	if err != nil || !swapped {
		t.Fatalf("title CAS after lifecycle = %+v swapped=%v err=%v", titled, swapped, err)
	}
	if titled.Phase != PhasePropose || titled.Revision <= lifecycle.Revision {
		t.Fatalf("title CAS rolled back lifecycle: %+v", titled)
	}

	staleLifecycle := lifecycle
	staleLifecycle.Phase = PhaseReview
	if _, err := first.Commit(t.Context(), staleLifecycle); !errors.Is(err, ErrConflict) {
		t.Fatalf("lifecycle snapshot predating title error = %v, want ErrConflict", err)
	}
	renamed, err := second.SetUserTitle(t.Context(), created.Project, created.ID, "Human title")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Phase != PhasePropose || renamed.Title != "Human title" || renamed.TitleSource != TitleSourceUser {
		t.Fatalf("user rename did not preserve lifecycle: %+v", renamed)
	}
}

func TestListSummariesUsesUpdatedDisambiguatesAndDisablesCorrupt(t *testing.T) {
	store := NewStore(t.TempDir())
	dir := filepath.Join(store.Dir(), "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(id, updated string) {
		t.Helper()
		sess := New("project")
		sess.ID = id
		sess.Title = "Same title"
		sess.TitleSource = TitleSourceUser
		sess.Updated = updated
		data, err := json.Marshal(sess)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("old-111111", "2025-01-01T00:00:00Z")
	write("new-222222", "2026-01-01T00:00:00Z")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "broken.json"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	summaries, err := store.ListSummaries(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 || summaries[0].ID != "new-222222" || summaries[1].ID != "old-111111" {
		t.Fatalf("summary order = %+v", summaries)
	}
	if summaries[0].DisplayTitle == summaries[0].Title || !strings.Contains(summaries[0].DisplayTitle, "222222") || !strings.Contains(summaries[1].DisplayTitle, "111111") {
		t.Fatalf("duplicate labels not stable: %+v", summaries)
	}
	if !summaries[2].Disabled || summaries[2].DisabledReason == "" {
		t.Fatalf("corrupt summary = %+v", summaries[2])
	}
}

func TestBackwardCompatibleSessionJSONAndActivePointer(t *testing.T) {
	store := NewStore(t.TempDir())
	dir := filepath.Join(store.Dir(), "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"legacy","project":"project","phase":"chat","created":"2025-01-01T00:00:00Z","updated":"2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context(), "project", "legacy")
	if err != nil || loaded.SchemaVersion != 1 || loaded.Title != "" {
		t.Fatalf("legacy load = %+v err=%v", loaded, err)
	}
	if err := store.SetActive(t.Context(), "project", "legacy"); err != nil {
		t.Fatal(err)
	}
	active, err := store.Active(t.Context(), "project")
	if err != nil || active != "legacy" {
		t.Fatalf("Active = %q, %v", active, err)
	}
	restored, _, err := store.Restore(t.Context(), "project", "")
	if err != nil || restored.ID != "legacy" {
		t.Fatalf("Restore = %+v, %v", restored, err)
	}
}
