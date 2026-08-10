package orchestrator

import (
	"context"
	"strconv"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
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
		o.guardrails.Rules = nil
		return nil
	}
	rs, err := agentcore.CompileRules(o.spec.Body)
	if err != nil {
		return err
	}
	o.guardrails.Rules = rs
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
	return agentcore.NewBudgetState(b, f("budget-daily-spent"))
}

// refreshGuardrails (re)builds the F1-F3 state for the current spec.
func (o *Orchestrator) refreshGuardrails() error {
	if err := o.compileRules(); err != nil {
		return err
	}
	o.guardrails.AntiLoop = agentcore.NewAntiLoop(16, 4)
	if o.guardrails.Budget == nil {
		o.guardrails.Budget = o.newBudget()
	}
	return nil
}

// BudgetState exposes the live budget for the sidebar.
func (o *Orchestrator) BudgetState() *agentcore.BudgetState {
	return o.guardrails.Budget
}

// RuleCount exposes how many spec rules have fired (diagnostics).
func (o *Orchestrator) RuleCount() int {
	if o.guardrails.Rules == nil {
		return 0
	}
	return o.guardrails.Rules.Fired()
}

// bindBudgetKill wires the budget kill-switch to the current run.
func (o *Orchestrator) bindBudgetKill(ctx context.Context) (context.Context, context.CancelFunc) {
	// Budget and repetition counters are run-scoped. Reusing them makes a
	// later run inherit cost, wall-clock age, and tool calls from earlier work.
	o.guardrails.Budget = o.newBudget()
	o.guardrails.AntiLoop = agentcore.NewAntiLoop(16, 4)
	runCtx, cancel := o.withRunContext(ctx)
	if o.guardrails.Budget != nil {
		o.guardrails.Budget.SetKill(cancel)
	}
	return runCtx, cancel
}
