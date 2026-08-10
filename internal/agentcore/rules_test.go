package agentcore

import (
	"strings"
	"testing"
)

const rulesSpec = `# Spec

## Goal

Something.

## Stream Rules

- forbid: "panic\("
  because: Never use panic for error handling.
- forbid: "_ = err"
  because: Never discard errors with _.

## Decisions

- keep it simple
`

func TestCompileRules(t *testing.T) {
	rs, err := CompileRules(rulesSpec)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	rules := rs.Rules()
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	if rules[0].Pattern != `panic\(` || rules[1].Pattern != `_ = err` {
		t.Errorf("patterns = %q, %q", rules[0].Pattern, rules[1].Pattern)
	}
	if !strings.Contains(rules[0].Reminder, "panic") {
		t.Errorf("reminder = %q", rules[0].Reminder)
	}
}

func TestCompileRulesNoBlock(t *testing.T) {
	rs, err := CompileRules("# Spec\n\n## Goal\n\nNothing.\n")
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	if len(rs.Rules()) != 0 {
		t.Errorf("rules = %+v", rs.Rules())
	}
}

func TestCompileRulesBadRegex(t *testing.T) {
	_, err := CompileRules("## Stream Rules\n- forbid: \"(unclosed\"\n")
	if err == nil || !strings.Contains(err.Error(), "stream rule") {
		t.Errorf("err = %v", err)
	}
}

func TestRulesFireOnce(t *testing.T) {
	rs, err := CompileRules(rulesSpec)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	if reminder, ok := rs.Check("we never use panic( here"); !ok {
		t.Fatal("violation not detected")
	} else if !strings.Contains(reminder, "panic") {
		t.Errorf("reminder = %q", reminder)
	}
	// Second violation of the same rule is silent (dormant → fired once).
	if _, ok := rs.Check("still panic( here"); ok {
		t.Error("rule should fire only once")
	}
	// Conforming text never triggers anything.
	if _, ok := rs.Check("clean code"); ok {
		t.Error("conforming text must not trigger rules")
	}
	// The other rule still fires.
	if _, ok := rs.Check("_ = err"); !ok {
		t.Error("second rule should still fire")
	}
	if rs.Fired() != 2 {
		t.Errorf("fired = %d", rs.Fired())
	}
}

func TestRulesSurviveCompaction(t *testing.T) {
	rs, err := CompileRules(rulesSpec)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	rs.Check("panic( somewhere")
	reminders := rs.Reminders()
	if len(reminders) != 1 || reminders[0].Role != "system" {
		t.Fatalf("reminders = %+v", reminders)
	}
	// The reminders are plain history messages — any compaction that keeps
	// history keeps the injected rules.
	if !strings.Contains(reminders[0].Content, "panic") {
		t.Errorf("reminder content = %q", reminders[0].Content)
	}
}
