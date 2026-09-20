package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
)

// Guardrails wires F1/F2/F3 for the current session.
type Guardrails struct {
	Rules    *agentcore.RuleSet
	Budget   *agentcore.BudgetState
	AntiLoop *agentcore.AntiLoop
}

// compileRules derives the dormant stream rules from the active spec (F1).
// Recompiled on /accept and /edit so the rules track the spec revision.
func (o *Orchestrator) compileRules() error {
	if o.spec == nil {
		o.guardrailsMu.Lock()
		o.guardrails.Rules = nil
		o.guardrailsMu.Unlock()
		return nil
	}
	rs, err := agentcore.CompileRules(o.spec.Body)
	if err != nil {
		return err
	}
	o.guardrailsMu.Lock()
	o.guardrails.Rules = rs
	o.guardrailsMu.Unlock()
	return nil
}

// newBudget builds the budget state from maestrorc options (F2). All limits
// are disabled unless configured:
//
//	option budget-max-usd 1.5
//	option budget-max-daily-usd 5
//	option budget-max-wall-clock 10m
//	option budget-max-tool-calls 50
//	option budget-max-repeated 5
func (o *Orchestrator) newBudget() *agentcore.BudgetState {
	return o.newBudgetWithDaily(0, false)
}

func (o *Orchestrator) newBudgetWithDaily(dailySoFar float64, carryDaily bool) *agentcore.BudgetState {
	var b agentcore.Budget
	if o.cfg == nil {
		return nil
	}
	f := func(key string) float64 {
		v, ok := o.cfg.Options[key]
		if !ok {
			return 0
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0
		}
		return f
	}
	dur := func(key string) time.Duration {
		v, ok := o.cfg.Options[key]
		if !ok {
			return 0
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0
		}
		return d
	}
	i := func(key string) int {
		v, ok := o.cfg.Options[key]
		if !ok {
			return 0
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}
		return n
	}
	b.MaxUSD = f("budget-max-usd")
	b.MaxDailyUSD = f("budget-max-daily-usd")
	b.MaxWallClock = dur("budget-max-wall-clock")
	b.MaxToolCalls = i("budget-max-tool-calls")
	b.MaxRepeated = i("budget-max-repeated")
	if b == (agentcore.Budget{}) {
		return nil
	}
	daily := f("budget-daily-spent")
	if carryDaily && dailySoFar > daily {
		daily = dailySoFar
	}
	state := agentcore.NewBudgetState(b, daily)
	if b.MaxDailyUSD <= 0 || o.sessions == nil || o.sess.Project == "" {
		return state
	}
	project := o.sess.Project
	day := state.DailyDay()
	ledgerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	total, err := o.sessions.UpdateDailyBudget(ledgerCtx, project, day, 0, daily)
	cancel()
	if err != nil {
		state.SetDailyError(err)
	} else if err := state.RestoreDaily(day, total); err != nil {
		state.SetDailyError(err)
	}
	state.SetDailyRecorder(func(day string, deltaUSD float64) (float64, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return o.sessions.UpdateDailyBudget(ctx, project, day, deltaUSD, 0)
	})
	state.SetDailyReservationStore(agentcore.DailyBudgetReservationStore{
		Reserve: func(parent context.Context, day, reservationID string, estimateUSD, maxDailyUSD float64, expiresAt time.Time) (agentcore.DailyBudgetLedgerSnapshot, error) {
			ctx, cancel := context.WithTimeout(parent, 2*time.Second)
			defer cancel()
			snapshot, err := o.sessions.ReserveDailyBudget(ctx, project, day, reservationID, estimateUSD, maxDailyUSD, 0, expiresAt)
			if errors.Is(err, session.ErrDailyBudgetLimit) {
				err = fmt.Errorf("%w: %v", agentcore.ErrBudgetReservationLimit, err)
			}
			return agentcore.DailyBudgetLedgerSnapshot{
				Day: snapshot.Day, SpentUSD: snapshot.SpentUSD, ReservedUSD: snapshot.ReservedUSD,
				AlreadySettled: snapshot.AlreadySettled,
			}, err
		},
		Settle: func(_ context.Context, day, reservationID string, actualUSD float64) (agentcore.DailyBudgetLedgerSnapshot, error) {
			// Done is authoritative even when the provider turn is concurrently
			// canceled. Give its ledger transaction an independent bounded window.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			snapshot, err := o.sessions.SettleDailyBudget(ctx, project, day, reservationID, actualUSD)
			return agentcore.DailyBudgetLedgerSnapshot{
				Day: snapshot.Day, SpentUSD: snapshot.SpentUSD, ReservedUSD: snapshot.ReservedUSD,
				AlreadySettled: snapshot.AlreadySettled,
			}, err
		},
		Release: func(_ context.Context, day, reservationID string) (agentcore.DailyBudgetLedgerSnapshot, error) {
			// Release is used only after a proven pre-dispatch failure. Detaching
			// prevents a simultaneous caller cancellation from leaking that lease.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			snapshot, err := o.sessions.ReleaseDailyBudget(ctx, project, day, reservationID)
			return agentcore.DailyBudgetLedgerSnapshot{
				Day: snapshot.Day, SpentUSD: snapshot.SpentUSD, ReservedUSD: snapshot.ReservedUSD,
				AlreadySettled: snapshot.AlreadySettled,
			}, err
		},
	})
	return state
}

// refreshGuardrails (re)builds the F1-F3 state for the current spec.
func (o *Orchestrator) refreshGuardrails() error {
	if err := o.compileRules(); err != nil {
		return err
	}
	o.guardrailsMu.RLock()
	needsBudget := o.guardrails.Budget == nil
	o.guardrailsMu.RUnlock()
	var budget *agentcore.BudgetState
	if needsBudget {
		budget = o.newBudget()
	}
	o.guardrailsMu.Lock()
	defer o.guardrailsMu.Unlock()
	o.guardrails.AntiLoop = agentcore.NewAntiLoop(16, 4)
	if o.guardrails.Budget == nil && needsBudget {
		o.guardrails.Budget = budget
	}
	return nil
}

// BudgetState exposes the live budget for the sidebar.
func (o *Orchestrator) BudgetState() *agentcore.BudgetState {
	o.guardrailsMu.RLock()
	defer o.guardrailsMu.RUnlock()
	return o.guardrails.Budget
}

// RuleCount exposes how many spec rules have fired (diagnostics).
func (o *Orchestrator) RuleCount() int {
	rules := o.guardrailSnapshot().Rules
	if rules == nil {
		return 0
	}
	return rules.Fired()
}

func (o *Orchestrator) guardrailSnapshot() Guardrails {
	o.guardrailsMu.RLock()
	defer o.guardrailsMu.RUnlock()
	return o.guardrails
}

// bindBudgetKill wires the budget kill-switch to the current run.
func (o *Orchestrator) bindBudgetKill(ctx context.Context) (context.Context, context.CancelFunc) {
	// Budget and repetition counters are run-scoped. Reusing them makes a
	// later run inherit wall-clock age and tool calls from earlier work. Daily
	// spend is session-scoped and must carry across those fresh run trackers.
	o.guardrailsMu.RLock()
	dailySoFar := 0.0
	carryDaily := o.guardrails.Budget != nil
	if carryDaily {
		dailySoFar = o.guardrails.Budget.Daily()
	}
	o.guardrailsMu.RUnlock()
	budget := o.newBudgetWithDaily(dailySoFar, carryDaily)

	runCtx, baseCancel := o.withRunContext(ctx)
	cancel := baseCancel
	if budget != nil && budget.MaxWallClock() > 0 {
		var timeoutCancel context.CancelFunc
		runCtx, timeoutCancel = context.WithTimeout(runCtx, budget.MaxWallClock())
		cancel = func() {
			timeoutCancel()
			baseCancel()
		}
	}
	if budget != nil {
		budget.SetKill(cancel)
	}

	o.guardrailsMu.Lock()
	o.guardrails.Budget = budget
	o.guardrails.AntiLoop = agentcore.NewAntiLoop(16, 4)
	o.guardrailsMu.Unlock()
	return runCtx, cancel
}
