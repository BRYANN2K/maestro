package agentcore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixedBudgetState(t *testing.T, budget Budget, now *time.Time) *BudgetState {
	t.Helper()
	b := NewBudgetState(budget, 0)
	var ids atomic.Uint64
	b.mu.Lock()
	b.now = func() time.Time { return *now }
	b.started = *now
	b.dailyDay = now.Format(time.DateOnly)
	b.newID = func() (string, error) { return fmt.Sprintf("turn-%d", ids.Add(1)), nil }
	b.mu.Unlock()
	return b
}

func TestBudgetReservationCallbacksAndCancelRunOutsideMutex(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 0.5, MaxDailyUSD: 2}, &now)
	b.SetDailyReservationStore(DailyBudgetReservationStore{
		Reserve: func(_ context.Context, day, _ string, _, _ float64, _ time.Time) (DailyBudgetLedgerSnapshot, error) {
			// This re-entry deadlocked when callbacks ran under trackMu.
			_ = b.CheckEstimate(0)
			_ = b.Daily()
			return DailyBudgetLedgerSnapshot{Day: day}, nil
		},
		Settle: func(_ context.Context, day, _ string, actual float64) (DailyBudgetLedgerSnapshot, error) {
			_ = b.CheckEstimate(0)
			_ = b.Spent()
			return DailyBudgetLedgerSnapshot{Day: day, SpentUSD: actual}, nil
		},
	})
	reentered := make(chan struct{})
	var once sync.Once
	b.SetKill(func() {
		// Cancellation also runs after all state locks are released.
		_ = b.CheckEstimate(0)
		once.Do(func() { close(reentered) })
	})
	token, err := b.ReserveEstimate(context.Background(), 0.2)
	if err != nil {
		t.Fatal(err)
	}
	decision := b.TrackReservation(t.Context(), NewEvent(nil, RoleDev, EvDone, Done{Cost: &Cost{InputUSD: 0.6}}), token)
	if !decision.Kill || !decision.AccountDone {
		t.Fatalf("decision = %+v", decision)
	}
	select {
	case <-reentered:
	case <-time.After(time.Second):
		t.Fatal("reentrant cancel callback deadlocked")
	}
}

func TestBudgetAbandonedReservationRemainsRunLiabilityUntilExpiry(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 1}, &now)
	token, err := b.ReserveEstimate(context.Background(), 0.6)
	if err != nil {
		t.Fatal(err)
	}
	b.AbandonReservation(token)
	if err := b.CheckEstimate(0.4); err == nil {
		t.Fatal("abandoned dispatched request no longer counted against run cap")
	}
	now = now.Add(defaultDailyReservationLease + time.Second)
	if err := b.CheckEstimate(0.4); err != nil {
		t.Fatalf("expired abandoned liability still blocked run: %v", err)
	}
}

func TestBudgetPredispatchReleaseRestoresCapacity(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 1}, &now)
	token, err := b.ReserveEstimate(context.Background(), 0.6)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ReleaseReservation(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckEstimate(0.6); err != nil {
		t.Fatalf("pre-dispatch release retained liability: %v", err)
	}
}

func TestBudgetSettlementFailureStillAccountsValidDone(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 2, MaxDailyUSD: 2}, &now)
	b.SetDailyReservationStore(DailyBudgetReservationStore{
		Reserve: func(_ context.Context, day, _ string, estimate, _ float64, _ time.Time) (DailyBudgetLedgerSnapshot, error) {
			return DailyBudgetLedgerSnapshot{Day: day, ReservedUSD: estimate}, nil
		},
		Settle: func(context.Context, string, string, float64) (DailyBudgetLedgerSnapshot, error) {
			return DailyBudgetLedgerSnapshot{}, errors.New("disk unavailable")
		},
	})
	token, err := b.ReserveEstimate(context.Background(), 0.5)
	if err != nil {
		t.Fatal(err)
	}
	decision := b.TrackReservation(t.Context(), NewEvent(nil, RoleDev, EvDone, Done{Cost: &Cost{InputUSD: 0.2}}), token)
	if !decision.Kill || !decision.AccountDone || b.Spent() != 0.2 || b.Err() == nil {
		t.Fatalf("failed settlement: decision=%+v spent=%.2f err=%v", decision, b.Spent(), b.Err())
	}
	b.mu.Lock()
	_, retained := b.liabilities[token.id]
	b.mu.Unlock()
	if !retained {
		t.Fatal("uncertain settlement released its durable liability early")
	}
}

func TestBudgetReservedDoneRequiresCostAndRejectsDuplicate(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 2}, &now)
	token, err := b.ReserveEstimate(context.Background(), 0.4)
	if err != nil {
		t.Fatal(err)
	}
	decision := b.TrackReservation(t.Context(), NewEvent(nil, RoleDev, EvDone, Done{}), token)
	if !decision.Kill || decision.AccountDone || b.Spent() != 0 || b.Err() == nil {
		t.Fatalf("costless reserved Done: decision=%+v spent=%.2f err=%v", decision, b.Spent(), b.Err())
	}

	b = fixedBudgetState(t, Budget{MaxUSD: 2}, &now)
	token, err = b.ReserveEstimate(context.Background(), 0.4)
	if err != nil {
		t.Fatal(err)
	}
	event := NewEvent(nil, RoleDev, EvDone, Done{Cost: &Cost{InputUSD: 0.2}})
	first := b.TrackReservation(t.Context(), event, token)
	second := b.TrackReservation(t.Context(), event, token)
	if !first.AccountDone || first.Kill || !second.Kill || second.AccountDone || b.Spent() != 0.2 {
		t.Fatalf("duplicate decisions first=%+v second=%+v spent=%.2f", first, second, b.Spent())
	}
}

func TestBudgetSettlementUsesAdmissionDayAcrossMidnight(t *testing.T) {
	now := time.Date(2026, time.August, 23, 23, 59, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 2, MaxDailyUSD: 1}, &now)
	oldDay := now.Format(time.DateOnly)
	var settledDay string
	b.SetDailyReservationStore(DailyBudgetReservationStore{
		Reserve: func(_ context.Context, day, _ string, estimate, _ float64, _ time.Time) (DailyBudgetLedgerSnapshot, error) {
			return DailyBudgetLedgerSnapshot{Day: day, ReservedUSD: estimate}, nil
		},
		Settle: func(_ context.Context, day, _ string, actual float64) (DailyBudgetLedgerSnapshot, error) {
			settledDay = day
			return DailyBudgetLedgerSnapshot{Day: day, SpentUSD: actual}, nil
		},
	})
	token, err := b.ReserveEstimate(context.Background(), 0.4)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	decision := b.TrackReservation(t.Context(), NewEvent(nil, RoleDev, EvDone, Done{Cost: &Cost{InputUSD: 0.2}}), token)
	if decision.Kill || !decision.AccountDone || settledDay != oldDay {
		t.Fatalf("midnight settlement decision=%+v day=%q", decision, settledDay)
	}
	if got := b.Daily(); got != 0 {
		t.Fatalf("old-day actual polluted new day: %.2f", got)
	}
}

func TestBudgetConcurrentReservationsAreRaceSafe(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxUSD: 10}, &now)
	const count = 40
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := b.ReserveEstimate(context.Background(), 0.1)
			if err == nil {
				b.AbandonReservation(token)
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	liabilities := len(b.liabilities)
	b.mu.Unlock()
	if liabilities != count {
		t.Fatalf("liabilities=%d, want %d", liabilities, count)
	}
}

type reservationBoundaryProvider struct {
	estimate Cost
	startErr error
	truncate bool
}

func (p *reservationBoundaryProvider) Name() string    { return "reservation-boundary" }
func (p *reservationBoundaryProvider) Type() string    { return "fake" }
func (p *reservationBoundaryProvider) Models() []Model { return nil }
func (p *reservationBoundaryProvider) Cost(Request, Usage) (Cost, error) {
	return p.estimate, nil
}
func (p *reservationBoundaryProvider) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	if p.startErr != nil {
		return nil, p.startErr
	}
	ch := make(chan StreamEvent, 1)
	if !p.truncate {
		ch <- NewEvent(nil, RoleDev, EvDone, Done{Cost: &p.estimate})
	}
	close(ch)
	return ch, nil
}

func TestLoopReleasesOnlyBeforeProviderDispatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provider    *reservationBoundaryProvider
		wantRelease int
	}{
		{name: "startup failure", provider: &reservationBoundaryProvider{estimate: Cost{InputUSD: 0.2}, startErr: errors.New("not dispatched")}, wantRelease: 1},
		{name: "truncated after dispatch", provider: &reservationBoundaryProvider{estimate: Cost{InputUSD: 0.2}, truncate: true}, wantRelease: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			b := fixedBudgetState(t, Budget{MaxUSD: 2, MaxDailyUSD: 2}, &now)
			var releases atomic.Int32
			b.SetDailyReservationStore(DailyBudgetReservationStore{
				Reserve: func(_ context.Context, day, _ string, estimate, _ float64, _ time.Time) (DailyBudgetLedgerSnapshot, error) {
					return DailyBudgetLedgerSnapshot{Day: day, ReservedUSD: estimate}, nil
				},
				Release: func(_ context.Context, day, _ string) (DailyBudgetLedgerSnapshot, error) {
					releases.Add(1)
					return DailyBudgetLedgerSnapshot{Day: day}, nil
				},
			})
			loop := &Loop{Provider: tc.provider, Model: "m", Tools: map[string]Tool{}, Budget: b}
			runErr := loop.Run(context.Background(), "work")
			if runErr == nil {
				t.Fatal("provider failure unexpectedly succeeded")
			}
			if got := int(releases.Load()); got != tc.wantRelease {
				t.Fatalf("releases=%d, want %d (run error: %v)", got, tc.wantRelease, runErr)
			}
		})
	}
}

func TestBudgetReadOnlyPreviewDoesNotReserve(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.Local)
	b := fixedBudgetState(t, Budget{MaxDailyUSD: 2}, &now)
	var reserves atomic.Int32
	b.SetDailyReservationStore(DailyBudgetReservationStore{
		Reserve: func(_ context.Context, day, _ string, estimate, _ float64, _ time.Time) (DailyBudgetLedgerSnapshot, error) {
			reserves.Add(1)
			return DailyBudgetLedgerSnapshot{Day: day, ReservedUSD: estimate}, nil
		},
	})
	if err := b.CheckEstimate(0.2); err != nil {
		t.Fatal(err)
	}
	if reserves.Load() != 0 {
		t.Fatal("read-only preview created a durable reservation")
	}
	if _, err := b.ReserveEstimate(context.Background(), 0.2); err != nil {
		t.Fatal(err)
	}
	if reserves.Load() != 1 {
		t.Fatalf("authoritative reservations=%d, want 1", reserves.Load())
	}
}
