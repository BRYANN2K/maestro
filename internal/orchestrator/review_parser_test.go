package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestParseAgentReviewItemsAcceptsTaggedFindingAfterNarration(t *testing.T) {
	summary := "I checked the implementation and tests.[pass] No correctness, security, or error-handling issues found."
	want := []ReviewItem{{Level: "pass", Message: "No correctness, security, or error-handling issues found."}}
	if got := parseAgentReviewItems(summary); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAgentReviewItems() = %#v, want %#v", got, want)
	}
}

func TestParseAgentReviewItemsPreservesMultipleFindings(t *testing.T) {
	summary := "- [PASS] tests pass\n- [warn] tasks.md is incomplete\n[fail] missing authorization check"
	want := []ReviewItem{
		{Level: "pass", Message: "tests pass"},
		{Level: "warn", Message: "tasks.md is incomplete"},
		{Level: "fail", Message: "missing authorization check"},
		{Level: "fail", Message: "agent reviewer returned conflicting [pass] and [fail] findings"},
	}
	if got := parseAgentReviewItems(summary); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAgentReviewItems() = %#v, want %#v", got, want)
	}
}

func TestParseAgentReviewItemsRejectsUntaggedNarration(t *testing.T) {
	if got := parseAgentReviewItems("Everything looks fine."); len(got) != 0 {
		t.Fatalf("parseAgentReviewItems() = %#v, want no findings", got)
	}
}

func TestParseAgentReviewItemsRejectsTagEmbeddedInUnstructuredSentence(t *testing.T) {
	for _, summary := range []string{
		"The implementation uses a [pass] marker in a string.",
		"[pass]missing required separator",
	} {
		got := parseAgentReviewItems(summary)
		if level := (Verdict{Items: got}).VerdictLevel(); level != "fail" {
			t.Fatalf("parseAgentReviewItems(%q) = %#v, verdict %q; want fail", summary, got, level)
		}
	}
}

func TestParseAgentReviewItemsFailsClosedOnEmptyFailBeforePass(t *testing.T) {
	summary := "Review complete.[fail]\n[pass] tests pass"
	got := parseAgentReviewItems(summary)
	if len(got) < 2 || got[0].Level != "fail" || got[0].Message == "" || got[1].Level != "pass" {
		t.Fatalf("parseAgentReviewItems() = %#v, want retained fail before pass", got)
	}
	if level := (Verdict{Items: got}).VerdictLevel(); level != "fail" {
		t.Fatalf("verdict level = %q, want fail", level)
	}
}

func TestParseAgentReviewItemsFailsClosedOnTruncatedFailTag(t *testing.T) {
	got := parseAgentReviewItems("[pass] tests pass\n[fai")
	if len(got) < 2 || got[0].Level != "pass" || got[1].Level != "fail" || !strings.Contains(got[1].Message, "truncated") {
		t.Fatalf("parseAgentReviewItems() = %#v, want truncated fail finding", got)
	}
}

func TestParseAgentReviewItemsFailsClosedOnAnyPartialOrMalformedTag(t *testing.T) {
	for _, summary := range []string{
		"[f",
		"[fa]",
		"[fai",
		"[fail ",
		"[w",
		"[war]",
		"[pass",
		"[pass ] looks good",
	} {
		t.Run(summary, func(t *testing.T) {
			got := parseAgentReviewItems(summary)
			if level := (Verdict{Items: got}).VerdictLevel(); level != "fail" {
				t.Fatalf("parseAgentReviewItems(%q) = %#v, verdict %q; want fail", summary, got, level)
			}
		})
	}
}

func TestParseAgentReviewItemsFailsClosedOnPassFailConflict(t *testing.T) {
	got := parseAgentReviewItems("[pass] all checks pass\n[fail] authorization is missing")
	if level := (Verdict{Items: got}).VerdictLevel(); level != "fail" {
		t.Fatalf("verdict = %q for %#v, want fail", level, got)
	}
	foundConflict := false
	for _, item := range got {
		foundConflict = foundConflict || strings.Contains(item.Message, "conflicting")
	}
	if !foundConflict {
		t.Fatalf("conflicting reviewer output was not identified: %#v", got)
	}
}

func TestParseAgentReviewItemsFailsClosedOnUnknownFindingLevel(t *testing.T) {
	for _, summary := range []string{
		"[pass] tests pass\n[error] authorization could not be checked",
		"[pass] tests pass\n[FAIL?] truncated severity",
		"[pass] tests pass\n[unknown without a closing bracket",
	} {
		t.Run(summary, func(t *testing.T) {
			got := parseAgentReviewItems(summary)
			if level := (Verdict{Items: got}).VerdictLevel(); level != "fail" {
				t.Fatalf("verdict = %q for %#v, want fail", level, got)
			}
			foundMalformed := false
			for _, item := range got {
				foundMalformed = foundMalformed || strings.Contains(item.Message, "malformed finding tag")
			}
			if !foundMalformed {
				t.Fatalf("unknown reviewer tag was not identified: %#v", got)
			}
		})
	}
}

func TestAgentReviewFailsClosedOnInvalidRunnerResult(t *testing.T) {
	tests := []struct {
		name   string
		result agentcore.AgentResult
		err    error
	}{
		{name: "runner error", err: errors.New("transport closed")},
		{name: "ok false", result: agentcore.AgentResult{Role: "reviewer", Summary: "[pass] partial output must not be trusted", OK: false}},
		{name: "empty", result: agentcore.AgentResult{Role: "reviewer", OK: true}},
		{name: "wrong role", result: agentcore.AgentResult{Role: "dev", OK: true, Summary: "[pass] fine"}},
		{name: "unstructured", result: agentcore.AgentResult{Role: "reviewer", OK: true, Summary: "Everything looks fine."}},
		{name: "truncated", result: agentcore.AgentResult{Role: "reviewer", OK: true, Summary: "[pass] tests pass\n[fai"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Orchestrator{runner: runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
				return tt.result, tt.err
			})}
			items := o.agentReview(context.Background())
			if level := (Verdict{Items: items}).VerdictLevel(); level != "fail" {
				t.Fatalf("agentReview() = %#v, verdict %q; want fail", items, level)
			}
		})
	}
}

func TestAgentReviewAcceptsStructuredSuccessfulResult(t *testing.T) {
	o := &Orchestrator{runner: runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{Role: "reviewer", OK: true, Summary: "[pass] implementation matches the spec"}, nil
	})}
	items := o.agentReview(context.Background())
	if len(items) != 1 || items[0].Level != "pass" {
		t.Fatalf("agentReview() = %#v, want one pass", items)
	}
}
