package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// AntiLoop detects repeated tool calls with identical arguments (F3,
// §11.1): a ring buffer of (tool, argsHash, timestamp). Repeats beyond the
// threshold pause the agent with a guided reflection prompt — never a
// silent auto-abort: the pause is visible and the human always sees it.
type AntiLoop struct {
	mu        sync.Mutex
	ring      []string // circular buffer of call keys
	next      int
	threshold int
	counts    map[string]int
}

// NewAntiLoop returns an anti-loop watcher with a ring of size and a
// threshold of repeated identical calls before pausing.
func NewAntiLoop(size, threshold int) *AntiLoop {
	if size <= 0 {
		size = 12
	}
	if threshold <= 0 {
		threshold = 3
	}
	return &AntiLoop{
		ring:      make([]string, size),
		threshold: threshold,
		counts:    map[string]int{},
	}
}

// Observe records the call and reports whether the repetition threshold was
// crossed (pause + reflection).
func (a *AntiLoop) Observe(call ToolCall) bool {
	key := call.Name + "\x00" + sha256Sum(call.Args)
	a.mu.Lock()
	defer a.mu.Unlock()
	slot := a.next % len(a.ring)
	if a.ring[slot] != "" {
		old := a.ring[slot]
		if c := a.counts[old]; c > 1 {
			a.counts[old] = c - 1
		} else {
			delete(a.counts, old)
		}
	}
	a.ring[slot] = key
	a.next++
	a.counts[key]++
	return a.counts[key] >= a.threshold
}

// Count returns the repetition count of the most recent call.
func (a *AntiLoop) Count(call ToolCall) int {
	key := call.Name + "\x00" + sha256Sum(call.Args)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counts[key]
}

func sha256Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
