package agentcore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	defaultDailyReservationLease  = 16 * time.Minute
	dailyReservationDeadlineSlack = 30 * time.Second
	maxDailyReservationLease      = time.Hour
)

// ErrBudgetReservationLimit lets a durable reservation backend distinguish a
// normal cap refusal from unavailable accounting. The former refuses only the
// request; the latter poisons the tracker so all later admission fails closed.
var ErrBudgetReservationLimit = errors.New("budget reservation reaches configured limit")

// Budget declares the guardrail limits (F2, §11.1). Zero values disable
// their limit.
type Budget struct {
	MaxUSD       float64       // per run
	MaxDailyUSD  float64       // per day
	MaxWallClock time.Duration // per run
	MaxToolCalls int           // per run
	MaxRepeated  int           // same tool+args before kill
}

// DailyBudgetLedgerSnapshot is the authoritative result of one durable
// reservation transaction.
type DailyBudgetLedgerSnapshot struct {
	Day            string
	SpentUSD       float64
	ReservedUSD    float64
	AlreadySettled bool
}

// DailyBudgetReservationStore persists native-provider liabilities. Every
// callback is invoked without a BudgetState mutex held.
type DailyBudgetReservationStore struct {
	Reserve func(context.Context, string, string, float64, float64, time.Time) (DailyBudgetLedgerSnapshot, error)
	Settle  func(context.Context, string, string, float64) (DailyBudgetLedgerSnapshot, error)
	Release func(context.Context, string, string) (DailyBudgetLedgerSnapshot, error)
}

type reservationState uint8

const (
	reservationPending reservationState = iota
	reservationSettling
	reservationSettled
	reservationReleased
	reservationAbandoned
)

// BudgetReservation is an opaque one-provider-turn admission token.
type BudgetReservation struct {
	id        string
	day       string
	estimate  float64
	expiresAt time.Time
	state     reservationState // guarded by the owning BudgetState.mu
}

type budgetLiability struct {
	day       string
	amount    float64
	expiresAt time.Time
}

type pendingDailyCost struct {
	day    string
	amount float64
}

// BudgetDecision is the detailed result of accounting one stream event.
// AccountDone is true exactly once for a valid incurred completion, including
// when durable settlement failed and the run must subsequently abort.
type BudgetDecision struct {
	Kill        bool
	Alert       bool
	AccountDone bool
}

// BudgetState tracks one run against the budget: live cost, tool counts,
// kill-switch, and provider-turn liabilities. One instance is used per run.
type BudgetState struct {
	budget Budget

	mu           sync.Mutex
	spentUSD     float64
	dailyUSD     float64 // last authoritative committed total for dailyDay
	dailyDay     string
	tools        int
	repeated     map[string]int
	started      time.Time
	alerted80    bool
	kill         context.CancelFunc
	now          func() time.Time
	recordDay    func(day string, deltaUSD float64) (float64, error)
	reservations DailyBudgetReservationStore
	liabilities  map[string]budgetLiability
	pendingDaily map[uint64]pendingDailyCost
	nextOp       uint64
	newID        func() (string, error)
	lastErr      error
}

// NewBudgetState starts a budget tracker. daily starts at the given amount
// (accumulated from earlier runs today).
func NewBudgetState(b Budget, dailySoFar float64) *BudgetState {
	now := time.Now
	started := now()
	return &BudgetState{
		budget:       b,
		dailyUSD:     dailySoFar,
		repeated:     map[string]int{},
		started:      started,
		dailyDay:     started.Format(time.DateOnly),
		now:          now,
		liabilities:  map[string]budgetLiability{},
		pendingDaily: map[uint64]pendingDailyCost{},
		newID:        newBudgetReservationID,
	}
}

// SetKill installs the kill-switch: cancelling the run's context.
func (b *BudgetState) SetKill(kill context.CancelFunc) {
	b.mu.Lock()
	b.kill = kill
	b.mu.Unlock()
}

// SetDailyRecorder installs legacy durable spend accounting. Native provider
// requests use SetDailyReservationStore so admission and settlement share the
// same filesystem transaction boundary.
func (b *BudgetState) SetDailyRecorder(record func(day string, deltaUSD float64) (float64, error)) {
	b.mu.Lock()
	b.recordDay = record
	b.mu.Unlock()
}

// SetDailyReservationStore installs durable per-turn reservation operations.
func (b *BudgetState) SetDailyReservationStore(store DailyBudgetReservationStore) {
	b.mu.Lock()
	b.reservations = store
	b.mu.Unlock()
}

// RestoreDaily replaces the in-memory daily total with a durable value when
// it belongs to the tracker's current local calendar day. A midnight crossed
// during loading safely discards the stale value.
func (b *BudgetState) RestoreDaily(day string, totalUSD float64) error {
	if invalidBudgetValue(totalUSD) {
		return fmt.Errorf("invalid daily budget total %v", totalUSD)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDailyLocked()
	if day == b.dailyDay {
		b.dailyUSD = totalUSD
	}
	return nil
}

// SetDailyError marks durable daily accounting unavailable. Hard-budget
// admission and the next tracked event then fail closed.
func (b *BudgetState) SetDailyError(err error) {
	b.mu.Lock()
	b.lastErr = err
	b.mu.Unlock()
}

// Err returns the latest budget-accounting failure, if any.
func (b *BudgetState) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

// DailyDay returns the local calendar day used by daily accounting.
func (b *BudgetState) DailyDay() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDailyLocked()
	return b.dailyDay
}

// MaxWallClock returns the configured per-run deadline.
func (b *BudgetState) MaxWallClock() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.budget.MaxWallClock
}

// Estimate returns the worst-case cost of a request (used pre-run).
func (b *BudgetState) Estimate(req Request) float64 {
	// Unknown pricing -> no estimate. Callers with provider pricing replace it.
	return 0
}

// CheckEstimate is a read-only preview. It intentionally creates no durable
// reservation; ReserveEstimate is the sole authoritative admission boundary
// immediately before a native provider request is dispatched.
func (b *BudgetState) CheckEstimate(estimatedUSD float64) error {
	if invalidBudgetValue(estimatedUSD) {
		return fmt.Errorf("invalid estimated cost %v", estimatedUSD)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDailyLocked()
	b.pruneLiabilitiesLocked()
	if b.lastErr != nil {
		return fmt.Errorf("budget accounting unavailable: %w", b.lastErr)
	}
	return b.checkEstimateLocked(estimatedUSD)
}

// ReserveEstimate atomically admits one native provider turn. Its durable
// callback runs without any BudgetState mutex held.
func (b *BudgetState) ReserveEstimate(ctx context.Context, estimatedUSD float64) (*BudgetReservation, error) {
	if invalidBudgetValue(estimatedUSD) {
		return nil, fmt.Errorf("invalid estimated cost %v", estimatedUSD)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	now := b.now()
	newID := b.newID
	b.mu.Unlock()
	expiresAt, err := budgetReservationExpiry(ctx, now)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		b.mu.Lock()
		b.lastErr = fmt.Errorf("create budget reservation ID: %w", err)
		b.mu.Unlock()
		return nil, fmt.Errorf("budget accounting unavailable: %w", err)
	}

	token := &BudgetReservation{id: id, estimate: estimatedUSD, expiresAt: expiresAt, state: reservationPending}
	b.mu.Lock()
	b.rollDailyLocked()
	b.pruneLiabilitiesLocked()
	if b.lastErr != nil {
		err := b.lastErr
		b.mu.Unlock()
		return nil, fmt.Errorf("budget accounting unavailable: %w", err)
	}
	if err := b.checkEstimateLocked(estimatedUSD); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	token.day = b.dailyDay
	b.liabilities[token.id] = budgetLiability{day: token.day, amount: estimatedUSD, expiresAt: expiresAt}
	store := b.reservations
	maxDailyUSD := b.budget.MaxDailyUSD
	b.mu.Unlock()

	if maxDailyUSD <= 0 || store.Reserve == nil {
		return token, nil
	}
	snapshot, reserveErr := store.Reserve(ctx, token.day, token.id, estimatedUSD, maxDailyUSD, expiresAt)
	if reserveErr != nil {
		b.mu.Lock()
		if errors.Is(reserveErr, ErrBudgetReservationLimit) {
			delete(b.liabilities, token.id)
			token.state = reservationReleased
		} else {
			// The callback may have committed before its result was lost. Keep the
			// liability until lease expiry and poison later admission.
			token.state = reservationAbandoned
			b.lastErr = reserveErr
		}
		b.mu.Unlock()
		if errors.Is(reserveErr, ErrBudgetReservationLimit) {
			return nil, reserveErr
		}
		return nil, fmt.Errorf("budget accounting unavailable: %w", reserveErr)
	}
	if err := validateDailyBudgetSnapshot(snapshot, token.day); err != nil {
		b.mu.Lock()
		token.state = reservationAbandoned
		b.lastErr = err
		b.mu.Unlock()
		return nil, fmt.Errorf("budget accounting unavailable: %w", err)
	}
	b.mu.Lock()
	b.rollDailyLocked()
	if b.dailyDay == token.day && snapshot.SpentUSD > b.dailyUSD {
		b.dailyUSD = snapshot.SpentUSD
	}
	b.mu.Unlock()
	return token, nil
}

// ReleaseReservation removes an estimate only before request dispatch is
// certain. After dispatch, callers must use AbandonReservation and let the
// bounded durable lease expire because the provider's actual cost is unknown.
func (b *BudgetState) ReleaseReservation(ctx context.Context, token *BudgetReservation) error {
	if token == nil {
		return nil
	}
	b.mu.Lock()
	if token.state != reservationPending {
		b.mu.Unlock()
		return nil
	}
	delete(b.liabilities, token.id)
	token.state = reservationReleased
	store := b.reservations
	b.mu.Unlock()
	if store.Release == nil {
		return nil
	}
	snapshot, err := store.Release(ctx, token.day, token.id)
	if err == nil {
		err = validateDailyBudgetSnapshot(snapshot, token.day)
	}
	if err != nil {
		b.mu.Lock()
		if token.expiresAt.After(b.now()) {
			b.liabilities[token.id] = budgetLiability{day: token.day, amount: token.estimate, expiresAt: token.expiresAt}
		}
		token.state = reservationAbandoned
		b.lastErr = err
		b.mu.Unlock()
		return fmt.Errorf("budget accounting unavailable: release reservation: %w", err)
	}
	b.mu.Lock()
	b.rollDailyLocked()
	if b.dailyDay == token.day && snapshot.SpentUSD > b.dailyUSD {
		b.dailyUSD = snapshot.SpentUSD
	}
	b.mu.Unlock()
	return nil
}

// AbandonReservation forgets the active token while retaining its local and
// durable liability until expiry. It is the only safe post-dispatch outcome
// when cancellation, a stream error, or a rule interruption prevents Done.
func (b *BudgetState) AbandonReservation(token *BudgetReservation) {
	if token == nil {
		return
	}
	b.mu.Lock()
	if token.state == reservationPending {
		token.state = reservationAbandoned
	}
	b.mu.Unlock()
}

// TrackReservation observes an event for an explicitly reserved native turn.
func (b *BudgetState) TrackReservation(ctx context.Context, ev StreamEvent, token *BudgetReservation) BudgetDecision {
	if ev.Type != EvDone {
		return b.TrackDecision(ev)
	}
	done, ok := ev.Content.(Done)
	if !ok {
		return b.failReservedCompletion(token, errors.New("provider completion omitted valid cost for reserved request"))
	}
	if done.Cost == nil {
		b.mu.Lock()
		zeroEstimate := token != nil && token.estimate == 0
		b.mu.Unlock()
		if !zeroEstimate {
			return b.failReservedCompletion(token, errors.New("provider completion omitted valid cost for reserved request"))
		}
		// Providers without pricing historically emit Done without Cost. A
		// zero estimate carries no monetary liability, so settle it as zero.
		done.Cost = &Cost{}
	}
	if err := validateCost(*done.Cost); err != nil {
		return b.failReservedCompletion(token, err)
	}
	actualUSD := done.Cost.Total()

	b.mu.Lock()
	b.rollDailyLocked()
	b.pruneLiabilitiesLocked()
	if token == nil || token.state != reservationPending {
		b.lastErr = errors.New("provider emitted a duplicate or unreserved completion")
		decision, cancel := b.decisionLocked(false, false)
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return decision
	}
	if actualUSD > math.MaxFloat64-b.spentUSD {
		b.lastErr = errors.New("provider cost overflowed budget accounting")
		token.state = reservationAbandoned
		decision, cancel := b.decisionLocked(false, false)
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return decision
	}
	token.state = reservationSettling
	delete(b.liabilities, token.id)
	b.spentUSD += actualUSD
	b.nextOp++
	opID := b.nextOp
	settlementFloor := actualUSD
	settlementFloorOverflow := false
	if token.day == b.dailyDay {
		b.pendingDaily[opID] = pendingDailyCost{day: token.day, amount: actualUSD}
		if actualUSD > math.MaxFloat64-b.dailyUSD {
			settlementFloorOverflow = true
		} else {
			settlementFloor = b.dailyUSD + actualUSD
		}
	}
	store := b.reservations
	b.mu.Unlock()

	var snapshot DailyBudgetLedgerSnapshot
	var settleErr error
	if store.Settle != nil {
		snapshot, settleErr = store.Settle(ctx, token.day, token.id, actualUSD)
		if settleErr == nil {
			settleErr = validateDailyBudgetSnapshot(snapshot, token.day)
		}
		if settleErr == nil && (settlementFloorOverflow || snapshot.SpentUSD < settlementFloor) {
			settleErr = fmt.Errorf("daily budget settlement total %.6f is below required %.6f", snapshot.SpentUSD, settlementFloor)
		}
	}

	b.mu.Lock()
	b.rollDailyLocked()
	delete(b.pendingDaily, opID)
	dailyCapReached := false
	if settleErr != nil {
		// Actual spend was incurred even though durable state is uncertain.
		// Retain both the local actual and the estimate liability, then fail
		// closed. The durable estimate expires automatically after the lease.
		if token.day == b.dailyDay && actualUSD <= math.MaxFloat64-b.dailyUSD {
			b.dailyUSD += actualUSD
		}
		if token.expiresAt.After(b.now()) {
			b.liabilities[token.id] = budgetLiability{day: token.day, amount: token.estimate, expiresAt: token.expiresAt}
		}
		token.state = reservationAbandoned
		b.lastErr = settleErr
	} else {
		token.state = reservationSettled
		if store.Settle == nil {
			if token.day == b.dailyDay {
				if actualUSD > math.MaxFloat64-b.dailyUSD {
					b.lastErr = errors.New("provider cost overflowed daily budget accounting")
				} else {
					b.dailyUSD += actualUSD
				}
			}
		} else {
			if token.day == b.dailyDay && snapshot.SpentUSD > b.dailyUSD {
				b.dailyUSD = snapshot.SpentUSD
			}
			dailyCapReached = b.budget.MaxDailyUSD > 0 && snapshot.SpentUSD >= b.budget.MaxDailyUSD
		}
	}
	decision, cancel := b.decisionLocked(true, dailyCapReached)
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return decision
}

func (b *BudgetState) failReservedCompletion(token *BudgetReservation, err error) BudgetDecision {
	b.mu.Lock()
	if token != nil && token.state == reservationPending {
		token.state = reservationAbandoned
	}
	b.lastErr = err
	decision, cancel := b.decisionLocked(false, false)
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return decision
}

// Track observes one stream event and reports (kill, alert). It is retained
// for legacy runners and callers that have no native reservation token.
func (b *BudgetState) Track(ev StreamEvent) (kill, alert bool) {
	decision := b.TrackDecision(ev)
	return decision.Kill, decision.Alert
}

// TrackDecision is Track with explicit valid-completion accounting metadata.
func (b *BudgetState) TrackDecision(ev StreamEvent) BudgetDecision {
	var record func(string, float64) (float64, error)
	var recordDay string
	var delta float64
	var opID uint64
	var recordFloor float64
	accountDone := false

	b.mu.Lock()
	b.rollDailyLocked()
	b.pruneLiabilitiesLocked()
	switch ev.Type {
	case EvDone:
		if done, ok := ev.Content.(Done); ok && done.Cost != nil {
			if err := validateCost(*done.Cost); err != nil {
				b.lastErr = err
				break
			}
			delta = done.Cost.Total()
			if delta > math.MaxFloat64-b.spentUSD {
				b.lastErr = errors.New("provider cost overflowed budget accounting")
				break
			}
			b.spentUSD += delta
			accountDone = true
			if delta > 0 && b.recordDay != nil {
				if delta > math.MaxFloat64-b.dailyUSD {
					b.lastErr = errors.New("provider cost overflowed daily budget accounting")
					break
				}
				b.nextOp++
				opID = b.nextOp
				recordDay = b.dailyDay
				recordFloor = b.dailyUSD + delta
				b.pendingDaily[opID] = pendingDailyCost{day: recordDay, amount: delta}
				record = b.recordDay
			} else if delta > 0 {
				if delta > math.MaxFloat64-b.dailyUSD {
					b.lastErr = errors.New("provider cost overflowed daily budget accounting")
				} else {
					b.dailyUSD += delta
				}
			}
		}
	case EvToolCall:
		if call, ok := ev.Content.(ToolCall); ok {
			b.tools++
			key := call.Name + "\x00" + sha256Sum(call.Args)
			b.repeated[key]++
			if b.budget.MaxRepeated > 0 && b.repeated[key] > b.budget.MaxRepeated {
				decision, cancel := b.decisionLocked(accountDone, true)
				b.mu.Unlock()
				if cancel != nil {
					cancel()
				}
				return decision
			}
		}
	}
	if record == nil {
		decision, cancel := b.decisionLocked(accountDone, false)
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return decision
	}
	b.mu.Unlock()

	total, recordErr := record(recordDay, delta)
	if recordErr == nil && invalidBudgetValue(total) {
		recordErr = fmt.Errorf("daily budget recorder returned invalid total %v", total)
	}
	b.mu.Lock()
	b.rollDailyLocked()
	delete(b.pendingDaily, opID)
	if recordErr != nil {
		if recordDay == b.dailyDay && delta <= math.MaxFloat64-b.dailyUSD {
			b.dailyUSD += delta
		}
		b.lastErr = recordErr
	} else if recordDay == b.dailyDay {
		if total < recordFloor {
			b.lastErr = fmt.Errorf("daily budget recorder total decreased below %.6f to %.6f", recordFloor, total)
		} else if total > b.dailyUSD {
			b.dailyUSD = total
		}
	}
	decision, cancel := b.decisionLocked(accountDone, false)
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return decision
}

// Spent returns the current run spend.
func (b *BudgetState) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spentUSD
}

// Daily returns today's accumulated actual spend (reservations are excluded
// from display but included in every admission and cap decision).
func (b *BudgetState) Daily() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDailyLocked()
	return b.dailyActualLocked()
}

// Tools returns the tool call count.
func (b *BudgetState) Tools() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tools
}

// Percent returns the spent fraction of the run cap (0-1, or 0 when off).
func (b *BudgetState) Percent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.budget.MaxUSD <= 0 {
		return 0
	}
	return b.spentUSD / b.budget.MaxUSD
}

// Kill aborts the run through the kill-switch without holding state locks.
func (b *BudgetState) Kill() {
	b.mu.Lock()
	kill := b.kill
	b.mu.Unlock()
	if kill != nil {
		kill()
	}
}

// String renders the budget state for the sidebar.
func (b *BudgetState) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDailyLocked()
	if b.budget.MaxUSD <= 0 && b.budget.MaxToolCalls <= 0 {
		return fmt.Sprintf("$%.4f · %d tools", b.spentUSD, b.tools)
	}
	return fmt.Sprintf("$%.2f/%.2f · %d/%d tools", b.spentUSD, b.budget.MaxUSD, b.tools, b.budget.MaxToolCalls)
}

func (b *BudgetState) checkEstimateLocked(estimatedUSD float64) error {
	runLiability, dailyLiability := b.liabilityTotalsLocked()
	if b.budget.MaxUSD > 0 {
		if runLiability > math.MaxFloat64-b.spentUSD || estimatedUSD > math.MaxFloat64-b.spentUSD-runLiability ||
			b.spentUSD+runLiability+estimatedUSD >= b.budget.MaxUSD {
			return fmt.Errorf("estimated cost $%.4f reaches run cap $%.4f", estimatedUSD, b.budget.MaxUSD)
		}
	}
	if b.budget.MaxDailyUSD > 0 {
		daily := b.dailyActualLocked()
		if dailyLiability > math.MaxFloat64-daily || estimatedUSD > math.MaxFloat64-daily-dailyLiability ||
			daily+dailyLiability+estimatedUSD >= b.budget.MaxDailyUSD {
			return fmt.Errorf("estimated cost $%.4f reaches daily cap $%.4f (already $%.4f)", estimatedUSD, b.budget.MaxDailyUSD, daily)
		}
	}
	return nil
}

func (b *BudgetState) decisionLocked(accountDone, forcedKill bool) (BudgetDecision, context.CancelFunc) {
	b.pruneLiabilitiesLocked()
	decision := BudgetDecision{AccountDone: accountDone}
	kill := forcedKill || b.lastErr != nil
	runLiability, dailyLiability := b.liabilityTotalsLocked()
	if b.budget.MaxUSD > 0 {
		projected := b.spentUSD + runLiability
		if projected >= b.budget.MaxUSD {
			kill = true
		} else if !b.alerted80 && b.spentUSD >= b.budget.MaxUSD*0.8 {
			b.alerted80 = true
			decision.Alert = true
		}
	}
	if b.budget.MaxDailyUSD > 0 && b.dailyActualLocked()+dailyLiability >= b.budget.MaxDailyUSD {
		kill = true
	}
	if b.budget.MaxWallClock > 0 && b.now().Sub(b.started) > b.budget.MaxWallClock {
		kill = true
	}
	if b.budget.MaxToolCalls > 0 && b.tools > b.budget.MaxToolCalls {
		kill = true
	}
	if kill {
		decision.Kill = true
		decision.Alert = false
		return decision, b.kill
	}
	return decision, nil
}

func (b *BudgetState) liabilityTotalsLocked() (runUSD, dailyUSD float64) {
	for _, liability := range b.liabilities {
		runUSD += liability.amount
		if liability.day == b.dailyDay {
			dailyUSD += liability.amount
		}
	}
	return runUSD, dailyUSD
}

func (b *BudgetState) dailyActualLocked() float64 {
	total := b.dailyUSD
	for _, pending := range b.pendingDaily {
		if pending.day == b.dailyDay {
			total += pending.amount
		}
	}
	return total
}

func (b *BudgetState) pruneLiabilitiesLocked() {
	now := b.now()
	for id, liability := range b.liabilities {
		if !liability.expiresAt.After(now) {
			delete(b.liabilities, id)
		}
	}
}

func (b *BudgetState) rollDailyLocked() {
	day := b.now().Format(time.DateOnly)
	if day == b.dailyDay {
		return
	}
	b.dailyDay = day
	b.dailyUSD = 0
}

func validateDailyBudgetSnapshot(snapshot DailyBudgetLedgerSnapshot, day string) error {
	if snapshot.Day != day {
		return fmt.Errorf("daily budget store returned day %q for %q", snapshot.Day, day)
	}
	if invalidBudgetValue(snapshot.SpentUSD) || invalidBudgetValue(snapshot.ReservedUSD) {
		return errors.New("daily budget store returned invalid totals")
	}
	return nil
}

func budgetReservationExpiry(ctx context.Context, now time.Time) (time.Time, error) {
	expiresAt := now.Add(defaultDailyReservationLease)
	if deadline, ok := ctx.Deadline(); ok {
		if !deadline.After(now) {
			return time.Time{}, context.DeadlineExceeded
		}
		expiresAt = deadline.Add(dailyReservationDeadlineSlack)
	}
	if expiresAt.After(now.Add(maxDailyReservationLease)) {
		return time.Time{}, fmt.Errorf("budget reservation deadline exceeds maximum lease %s", maxDailyReservationLease)
	}
	return expiresAt, nil
}

func newBudgetReservationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func invalidBudgetValue(value float64) bool {
	return value < 0 || math.IsNaN(value) || math.IsInf(value, 0)
}
