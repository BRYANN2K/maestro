package agentcore

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Rule is one dormant stream rule compiled from the spec (F1, §11.1):
// the watcher interrupts the stream on the first violation and injects the
// reminder as a system message, then generation resumes. Conforming turns
// pay zero context tax — the rule only fires once.
type Rule struct {
	ID       string
	Pattern  string // compiled regexp
	Re       *regexp.Regexp
	Reminder string // injected system message
	Fired    bool
}

// RuleSet holds the compiled rules for one spec revision.
type RuleSet struct {
	mu    sync.Mutex
	rules []*Rule
}

// CompileRules parses the spec's "Stream Rules" block (F1):
//
//	## Stream Rules
//	- forbid: `panic\(`
//	  because: Never use panic for error handling.
//
// Every rule is dormant until the first violation.
func CompileRules(specText string) (*RuleSet, error) {
	rs := &RuleSet{}
	lines := strings.Split(specText, "\n")
	var wantPattern, wantBecause string
	flush := func() error {
		if wantPattern == "" {
			return nil
		}
		re, err := regexp.Compile(wantPattern)
		if err != nil {
			return fmt.Errorf("stream rule %q: %w", wantPattern, err)
		}
		reminder := wantBecause
		if reminder == "" {
			reminder = fmt.Sprintf("Stream rule violated: do not match %s", wantPattern)
		}
		rs.rules = append(rs.rules, &Rule{
			ID:       fmt.Sprintf("rule-%d", len(rs.rules)+1),
			Pattern:  wantPattern,
			Re:       re,
			Reminder: reminder,
		})
		wantPattern, wantBecause = "", ""
		return nil
	}
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.EqualFold(trimmed, "## Stream Rules") || strings.EqualFold(trimmed, "# Stream Rules"):
			inBlock = true
		case inBlock && (strings.HasPrefix(trimmed, "#") || trimmed == ""):
			// section ends at the next heading; keep parsing on blank lines
			if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "## Stream Rules") && !strings.HasPrefix(trimmed, "# Stream Rules") {
				inBlock = false
			}
		case inBlock && strings.HasPrefix(trimmed, "- forbid:"):
			if err := flush(); err != nil {
				return nil, err
			}
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "- forbid:"))
			wantPattern = unquote(raw)
		case inBlock && strings.HasPrefix(trimmed, "because:"):
			wantBecause = strings.TrimSpace(strings.TrimPrefix(trimmed, "because:"))
		case inBlock && trimmed != "":
			// continuation of a forbid pattern on the next line
			if wantPattern != "" && !strings.HasPrefix(trimmed, "-") {
				wantPattern = unquote(trimmed)
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return rs, nil
}

// unquote strips surrounding backticks or quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	for _, q := range []string{"`", `"`, "'"} {
		if strings.HasPrefix(s, q) && strings.HasSuffix(s, q) && len(s) >= 2 {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Check scans text against the dormant rules. On the first violation of a
// not-yet-fired rule it marks it fired and returns the reminder to inject.
func (rs *RuleSet) Check(text string) (string, bool) {
	if rs == nil {
		return "", false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.rules {
		if r.Fired {
			continue
		}
		if r.Re.MatchString(text) {
			r.Fired = true
			return r.Reminder, true
		}
	}
	return "", false
}

// Fired reports whether any rule has fired (for tests and diagnostics).
func (rs *RuleSet) Fired() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, r := range rs.rules {
		if r.Fired {
			n++
		}
	}
	return n
}

// Rules returns the compiled rules.
func (rs *RuleSet) Rules() []Rule {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]Rule, 0, len(rs.rules))
	for _, r := range rs.rules {
		out = append(out, *r)
	}
	return out
}

// Reminders returns the fired reminders as system messages — these are what
// survives compaction: they live in the message history.
func (rs *RuleSet) Reminders() []Message {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var out []Message
	for _, r := range rs.rules {
		if r.Fired {
			out = append(out, Message{Role: "system", Content: r.Reminder})
		}
	}
	return out
}
