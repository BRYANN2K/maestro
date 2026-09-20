package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDailyBudgetPersistsAndRollsCalendarDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	day := now.Format(time.DateOnly)
	first := NewStore(dir)
	first.now = func() time.Time { return now }
	total, err := first.UpdateDailyBudget(t.Context(), "project", day, 0.20, 0.10)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(total-0.30) > 1e-9 {
		t.Fatalf("first total = %.4f, want .30", total)
	}

	reopened := NewStore(dir)
	reopened.now = func() time.Time { return now }
	total, err = reopened.UpdateDailyBudget(t.Context(), "project", day, 0.20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(total-0.50) > 1e-9 {
		t.Fatalf("reopened total = %.4f, want .50", total)
	}

	now = now.AddDate(0, 0, 1)
	tomorrow := now.Format(time.DateOnly)
	total, err = reopened.UpdateDailyBudget(t.Context(), "project", tomorrow, 0.05, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(total-0.05) > 1e-9 {
		t.Fatalf("next-day total = %.4f, want .05", total)
	}
	if _, err := readBoundedRegularFile(filepath.Join(dir, "project", dailyBudgetFileName), maxDailyBudgetBytes); err != nil {
		t.Fatalf("durable ledger: %v", err)
	}
	ids, err := reopened.List(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("daily ledger leaked into session IDs: %v", ids)
	}
	summaries, err := reopened.ListSummaries(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("daily ledger leaked into session summaries: %+v", summaries)
	}
}

func TestDailyBudgetSerializesConcurrentStores(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().Format(time.DateOnly)
	stores := []*Store{NewStore(dir), NewStore(dir)}
	const updates = 20
	var wg sync.WaitGroup
	errCh := make(chan error, updates)
	for i := 0; i < updates; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := stores[i%len(stores)].UpdateDailyBudget(context.Background(), "project", day, 0.01, 0)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	total, err := stores[0].UpdateDailyBudget(t.Context(), "project", day, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(total-0.20) > 1e-9 {
		t.Fatalf("concurrent total = %.4f, want .20", total)
	}
}

func TestDailyBudgetReadRefreshDoesNotRewriteLedger(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().Format(time.DateOnly)
	store := NewStore(dir)
	if _, err := store.UpdateDailyBudget(t.Context(), "project", day, 0.25, 0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "project", dailyBudgetFileName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.UpdateDailyBudget(t.Context(), "project", day, 0, 0.1); err != nil || got != 0.25 {
		t.Fatalf("read refresh = %.2f, %v", got, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged read refresh replaced the ledger file")
	}
}

func TestDailyBudgetRejectsStaleDayUpdate(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	store.now = func() time.Time {
		return time.Date(2026, time.August, 24, 12, 0, 0, 0, time.Local)
	}
	if _, err := store.UpdateDailyBudget(t.Context(), "project", "2026-08-24", 0.25, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDailyBudget(t.Context(), "project", "2026-08-23", 0.25, 0); err == nil {
		t.Fatal("stale day update rolled the ledger backwards")
	}
	total, err := store.UpdateDailyBudget(t.Context(), "project", "2026-08-24", 0, 0)
	if err != nil || total != 0.25 {
		t.Fatalf("current ledger after stale update = %.2f, %v", total, err)
	}
}

func TestDailyBudgetReservationRefusesConcurrentOversubscription(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	stores := []*Store{NewStore(dir), NewStore(dir)}
	for _, store := range stores {
		store.now = func() time.Time { return now }
	}
	day := now.Format(time.DateOnly)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, store := range stores {
		go func(i int, store *Store) {
			<-start
			_, err := store.ReserveDailyBudget(
				context.Background(), "project", day, fmt.Sprintf("process-%d", i),
				0.60, 1, 0, now.Add(10*time.Minute),
			)
			results <- err
		}(i, store)
	}
	close(start)
	accepted, refused := 0, 0
	for range stores {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrDailyBudgetLimit):
			refused++
		default:
			t.Fatalf("reservation error = %v", err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("accepted=%d refused=%d, want one of each", accepted, refused)
	}
}

func TestDailyBudgetSettlementIsExactAndIdempotent(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return now }
	day := now.Format(time.DateOnly)
	reserved, err := store.ReserveDailyBudget(t.Context(), "project", day, "turn-1", 0.60, 1, 0, now.Add(10*time.Minute))
	if err != nil || reserved.ReservedUSD != 0.60 || reserved.SpentUSD != 0 {
		t.Fatalf("reserve = %+v, %v", reserved, err)
	}
	settled, err := store.SettleDailyBudget(t.Context(), "project", day, "turn-1", 0.20)
	if err != nil || settled.SpentUSD != 0.20 || settled.ReservedUSD != 0 || settled.AlreadySettled {
		t.Fatalf("settle = %+v, %v", settled, err)
	}
	retried, err := store.SettleDailyBudget(t.Context(), "project", day, "turn-1", 0.20)
	if err != nil || !retried.AlreadySettled || retried.SpentUSD != 0.20 {
		t.Fatalf("idempotent settle = %+v, %v", retried, err)
	}
	now = now.Add(30 * time.Minute)
	delayedRetry, err := store.SettleDailyBudget(t.Context(), "project", day, "turn-1", 0.20)
	if err != nil || !delayedRetry.AlreadySettled || delayedRetry.SpentUSD != 0.20 {
		t.Fatalf("delayed idempotent settle = %+v, %v", delayedRetry, err)
	}
	if _, err := store.SettleDailyBudget(t.Context(), "project", day, "turn-1", 0.21); err == nil {
		t.Fatal("settlement retry changed the actual cost")
	}

	second, err := store.ReserveDailyBudget(t.Context(), "project", day, "turn-2", 0.70, 1, 0, now.Add(10*time.Minute))
	if err != nil || math.Abs(second.ReservedUSD-0.70) > 1e-9 {
		t.Fatalf("reserve after exact settlement = %+v, %v", second, err)
	}
	released, err := store.ReleaseDailyBudget(t.Context(), "project", day, "turn-2")
	if err != nil || released.ReservedUSD != 0 || released.SpentUSD != 0.20 {
		t.Fatalf("release = %+v, %v", released, err)
	}
}

func TestDailyBudgetExpiredCrashReservationRestoresCapacity(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	crashed := NewStore(dir)
	crashed.now = func() time.Time { return now }
	day := now.Format(time.DateOnly)
	if _, err := crashed.ReserveDailyBudget(t.Context(), "project", day, "crashed-turn", 0.60, 1, 0, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(dir)
	reopened.now = func() time.Time { return now }
	if _, err := reopened.ReserveDailyBudget(t.Context(), "project", day, "blocked-turn", 0.40, 1, 0, now.Add(time.Minute)); !errors.Is(err, ErrDailyBudgetLimit) {
		t.Fatalf("live crash lease did not block capacity: %v", err)
	}
	now = now.Add(2 * time.Minute)
	admitted, err := reopened.ReserveDailyBudget(t.Context(), "project", day, "recovered-turn", 0.40, 1, 0, now.Add(time.Minute))
	if err != nil || math.Abs(admitted.ReservedUSD-0.40) > 1e-9 {
		t.Fatalf("capacity after lease expiry = %+v, %v", admitted, err)
	}
}

func TestDailyBudgetSettlementAcrossMidnightKeepsDaysIsolated(t *testing.T) {
	now := time.Date(2026, time.August, 23, 23, 59, 0, 0, time.Local)
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return now }
	oldDay := now.Format(time.DateOnly)
	if _, err := store.ReserveDailyBudget(t.Context(), "project", oldDay, "night-turn", 0.60, 1, 0, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	newDay := now.Format(time.DateOnly)
	if _, err := store.ReserveDailyBudget(t.Context(), "project", newDay, "morning-turn", 0.20, 1, 0, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := store.SettleDailyBudget(t.Context(), "project", oldDay, "night-turn", 0.30)
	if err != nil || oldSnapshot.Day != oldDay || oldSnapshot.SpentUSD != 0.30 {
		t.Fatalf("old-day settlement = %+v, %v", oldSnapshot, err)
	}
	current, err := store.UpdateDailyBudget(t.Context(), "project", newDay, 0, 0)
	if err != nil || current != 0 {
		t.Fatalf("new day was polluted by prior settlement: %.2f, %v", current, err)
	}
}

func TestDailyBudgetMigratesVersionOneOnReservationWrite(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	day := now.Format(time.DateOnly)
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(legacyDailyBudgetRecord{SchemaVersion: 1, Date: day, SpentUSD: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, dailyBudgetFileName)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	store.now = func() time.Time { return now }
	snapshot, err := store.ReserveDailyBudget(t.Context(), "project", day, "migrated-turn", 0.25, 1, 0, now.Add(time.Minute))
	if err != nil || snapshot.SpentUSD != 0.25 || snapshot.ReservedUSD != 0.25 {
		t.Fatalf("migration reservation = %+v, %v", snapshot, err)
	}
	data, err := readBoundedRegularFile(path, maxDailyBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	var migrated dailyBudgetRecord
	if err := json.Unmarshal(data, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != dailyBudgetSchemaVersion || len(migrated.Days) != 1 || migrated.Days[0].SpentUSD != 0.25 {
		t.Fatalf("migrated ledger = %+v", migrated)
	}
}

func TestDailyBudgetRejectsFutureRequestAndFutureLedger(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	store := NewStore(dir)
	store.now = func() time.Time { return now }
	if _, err := store.UpdateDailyBudget(t.Context(), "project", "2026-08-24", 0, 0); err == nil {
		t.Fatal("future request was accepted")
	}
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dailyBudgetRecord{SchemaVersion: dailyBudgetSchemaVersion, Days: []dailyBudgetDay{{Date: "2026-08-24"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, dailyBudgetFileName), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDailyBudget(t.Context(), "project", now.Format(time.DateOnly), 0, 0); err == nil {
		t.Fatal("future ledger was accepted")
	}
}
