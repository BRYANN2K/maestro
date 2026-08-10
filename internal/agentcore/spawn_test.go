package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type incompleteProvider struct {
	done any
}

func (*incompleteProvider) Name() string                      { return "incomplete" }
func (*incompleteProvider) Type() string                      { return "test" }
func (*incompleteProvider) Models() []Model                   { return nil }
func (*incompleteProvider) Cost(Request, Usage) (Cost, error) { return Cost{}, nil }
func (p *incompleteProvider) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 2)
	ch <- NewEvent(nil, RoleReviewer, EvTextDelta, TextDelta{Text: "[pass] partial output"})
	if p.done != nil {
		ch <- NewEvent(nil, RoleReviewer, EvDone, p.done)
	}
	close(ch)
	return ch, nil
}

func TestSpawnSeedsContext(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "spec.md")
	designFile := filepath.Join(dir, "design.md")
	if err := os.WriteFile(specFile, []byte("# Spec\nGoal: add auth\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(designFile, []byte("# Design\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loop, err := Spawn(context.Background(), SpawnOptions{
		Role:      RoleDev,
		Provider:  &fakeProvider{},
		Model:     "m",
		Sampling:  Sampling{ReasoningEffort: "high"},
		SpecFiles: []string{specFile, designFile},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var system strings.Builder
	for _, msg := range loop.System {
		system.WriteString(msg.Content)
	}
	if !strings.Contains(system.String(), "=== FILE: "+specFile+" ===") || !strings.Contains(system.String(), "Goal: add auth") {
		t.Errorf("system prompt missing spec content: %q", system.String())
	}
	if !strings.Contains(system.String(), "dev sub-agent") {
		t.Errorf("system prompt missing role: %q", system.String())
	}
	if loop.Role != RoleDev {
		t.Errorf("loop role = %q", loop.Role)
	}
	if loop.Sampling.ReasoningEffort != "high" {
		t.Errorf("loop reasoning effort = %q", loop.Sampling.ReasoningEffort)
	}
}

func TestDevRolePromptAllowsOnlyVerifiedTaskCheckboxUpdates(t *testing.T) {
	prompt := rolePrompt(RoleDev)
	for _, want := range []string{"Never edit spec.md or design.md", "only mark a checkbox [x]", "never rewrite the task text"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("dev role prompt missing %q: %s", want, prompt)
		}
	}
}

func TestSpawnReviewerDiff(t *testing.T) {
	loop, err := Spawn(context.Background(), SpawnOptions{
		Role:     RoleReviewer,
		Provider: &fakeProvider{},
		Model:    "m",
		Diff:     "diff --git a/x b/x",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var system strings.Builder
	for _, msg := range loop.System {
		system.WriteString(msg.Content)
	}
	if !strings.Contains(system.String(), "GIT DIFF") || !strings.Contains(system.String(), "reviewer sub-agent") {
		t.Errorf("system = %q", system.String())
	}
}

func TestSpawnOrchestratorCarriesOperationBoundary(t *testing.T) {
	loop, err := Spawn(context.Background(), SpawnOptions{Role: RoleOrchestrator, Provider: &fakeProvider{}, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var system strings.Builder
	for _, message := range loop.System {
		system.WriteString(message.Content)
	}
	for _, required := range []string{"MAESTRO_OPERATION: CHAT", "PROPOSE_AUTHORIZED", "Never modify files"} {
		if !strings.Contains(system.String(), required) {
			t.Errorf("orchestrator system prompt missing %q: %s", required, system.String())
		}
	}
}

func TestSpawnRequiresProviderAndModel(t *testing.T) {
	if _, err := Spawn(context.Background(), SpawnOptions{Role: RoleDev, Model: "m"}); err == nil {
		t.Error("spawn without provider should fail")
	}
	if _, err := Spawn(context.Background(), SpawnOptions{Role: RoleDev, Provider: &fakeProvider{}}); err == nil {
		t.Error("spawn without model should fail")
	}
}

func TestSpawnMissingSpecFile(t *testing.T) {
	_, err := Spawn(context.Background(), SpawnOptions{
		Role:      RoleDev,
		Provider:  &fakeProvider{},
		Model:     "m",
		SpecFiles: []string{filepath.Join(t.TempDir(), "nope.md")},
	})
	if err == nil {
		t.Error("spawn with missing spec file should fail")
	}
}

func TestValidateResult(t *testing.T) {
	good := `{"role":"dev","ok":true,"summary":"done","tasks_done":["t1"]}`
	r, err := ValidateResult(RoleDev, []byte(good))
	if err != nil {
		t.Fatalf("ValidateResult good: %v", err)
	}
	if !r.OK || r.Summary != "done" || len(r.TasksDone) != 1 {
		t.Errorf("result = %+v", r)
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"role missing", `{"ok":true,"summary":"x"}`, "role is required"},
		{"role mismatch", `{"role":"reviewer","ok":true,"summary":"x"}`, "mismatch"},
		{"ok without summary", `{"role":"dev","ok":true}`, "summary is required"},
		{"bad json", `{`, "unexpected end of JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateResult(RoleDev, []byte(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ValidateResult = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRunResultCapturesYield(t *testing.T) {
	p := &fakeProvider{turns: []fakeTurn{
		{deltas: []string{"Implemented the spec."}},
	}}
	loop, err := Spawn(context.Background(), SpawnOptions{
		Role:     RoleDev,
		Provider: p,
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	res, err := RunResult(context.Background(), loop, "do it")
	if err != nil {
		t.Fatalf("RunResult: %v", err)
	}
	if !res.OK || res.Summary != "Implemented the spec." {
		t.Errorf("result = %+v", res)
	}
	if res.Duration == "" {
		t.Error("duration missing")
	}
	if res.Role != "dev" {
		t.Errorf("role = %q", res.Role)
	}
}

func TestRunResultRejectsStreamClosedWithoutDone(t *testing.T) {
	loop, err := Spawn(context.Background(), SpawnOptions{
		Role:     RoleReviewer,
		Provider: &incompleteProvider{},
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	res, err := RunResult(context.Background(), loop, "review")
	if err == nil || !strings.Contains(err.Error(), "without a completion event") {
		t.Fatalf("RunResult error = %v, want truncated-stream refusal", err)
	}
	if res.OK {
		t.Fatalf("RunResult = %+v, want OK=false", res)
	}
}

func TestRunResultRejectsMalformedDoneEvent(t *testing.T) {
	loop, err := Spawn(context.Background(), SpawnOptions{
		Role:     RoleReviewer,
		Provider: &incompleteProvider{done: "not a Done payload"},
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	res, err := RunResult(context.Background(), loop, "review")
	if err == nil || !strings.Contains(err.Error(), "without a completion event") {
		t.Fatalf("RunResult error = %v, want malformed-stream refusal", err)
	}
	if res.OK {
		t.Fatalf("RunResult = %+v, want OK=false", res)
	}
}
