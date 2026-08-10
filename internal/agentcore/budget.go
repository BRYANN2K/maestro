package agentcore

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Budget declares the guardrail limits (F2, §11.1). Zero values disable
// their limit.
type Budget struct {
	MaxUSD       float64       // per run
	MaxDailyUSD  float64       // per day
	MaxWallClock time.Duration // per run
	MaxToolCalls int           // per run
	MaxRepeated  int           // same tool+args before kill
}

// BudgetState tracks one run against the budget: live cost, tool counts,
// kill-switch. One instance per run.
type BudgetState struct {
	budget Budget

	mu        sync.Mutex
	spentUSD  float64
	dailyUSD  float64
	tools     int
	repeated  map[string]int
	started   time.Time
	alerted80 bool
	kill      context.CancelFunc
}

// NewBudgetState starts a budget tracker. daily starts at the given amount
// (accumulated from earlier runs today).
func NewBudgetState(b Budget, dailySoFar float64) *BudgetState {
	return &BudgetState{
		budget:   b,
		dailyUSD: dailySoFar,
		repeated: map[string]int{},
		started:  time.Now(),
	}
}

// SetKill installs the kill-switch: cancelling the run's context.
func (b *BudgetState) SetKill(kill context.CancelFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.kill = kill
}

// Estimate returns the worst-case cost of a request (used pre-run).
func (b *BudgetState) Estimate(req Request) float64 {
	// Unknown pricing → no estimate.
	return 0
}

// Track observes one stream event and reports (kill, alert80).
func (b *BudgetState) Track(ev StreamEvent) (bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch ev.Type {
	case EvDone:
		if d, ok := ev.Content.(Done); ok && d.Cost != nil {
			b.spentUSD += d.Cost.Total()
			b.dailyUSD += d.Cost.Total()
		}
	case EvToolCall:
		if tc, ok := ev.Content.(ToolCall); ok {
			b.tools++
			key := tc.Name + "\x00" + tc.Args
			b.repeated[key]++
			if b.budget.MaxRepeated > 0 && b.repeated[key] > b.budget.MaxRepeated {
				b.killSwitch()
				return true, false
			}
		}
	}
	if b.budget.MaxUSD > 0 {
		if b.spentUSD >= b.budget.MaxUSD {
			b.killSwitch()
			return true, false
		}
		if !b.alerted80 && b.spentUSD >= b.budget.MaxUSD*0.8 {
			b.alerted80 = true
			return false, true
		}
	}
	if b.budget.MaxDailyUSD > 0 && b.dailyUSD >= b.budget.MaxDailyUSD {
		b.killSwitch()
		return true, false
	}
	if b.budget.MaxWallClock > 0 && time.Since(b.started) > b.budget.MaxWallClock {
		b.killSwitch()
		return true, false
	}
	if b.budget.MaxToolCalls > 0 && b.tools > b.budget.MaxToolCalls {
		b.killSwitch()
		return true, false
	}
	return false, false
}

// Spent returns the current run spend.
func (b *BudgetState) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spentUSD
}

// Daily returns today's accumulated spend.
func (b *BudgetState) Daily() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dailyUSD
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

// Kill aborts the run through the kill-switch.
func (b *BudgetState) Kill() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.killSwitch()
}

func (b *BudgetState) killSwitch() {
	if b.kill != nil {
		b.kill()
	}
}

// String renders the budget state for the sidebar.
func (b *BudgetState) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.budget.MaxUSD <= 0 && b.budget.MaxToolCalls <= 0 {
		return fmt.Sprintf("$%.4f · %d tools", b.spentUSD, b.tools)
	}
	return fmt.Sprintf("$%.2f/%.2f · %d/%d tools", b.spentUSD, b.budget.MaxUSD, b.tools, b.budget.MaxToolCalls)
}
