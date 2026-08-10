package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	legacyagent "github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/settings"
)

func installProjectSkill(t *testing.T, root, name, description, body string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\nuser-invocable: true\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type oversizedLegacyAgent struct {
	canceled chan struct{}
}

type untrustedCustomRunner struct{}

func (untrustedCustomRunner) Run(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
	return agentcore.AgentResult{OK: true}, nil
}

func (a *oversizedLegacyAgent) Name() string     { return "oversized" }
func (a *oversizedLegacyAgent) Models() []string { return nil }
func (a *oversizedLegacyAgent) Execute(ctx context.Context, _ string, _ legacyagent.Options) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent)
	go func() {
		defer close(ch)
		delta := strings.Repeat("x", 1<<20)
		for range 9 {
			select {
			case ch <- agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvTextDelta, agentcore.TextDelta{Text: delta}):
			case <-ctx.Done():
				close(a.canceled)
				return
			}
		}
		<-ctx.Done()
		close(a.canceled)
	}()
	return ch, nil
}

func TestSkillAPIsEnableInspectAndRunExplicitly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	installProjectSkill(t, dir, "secure-review", "Review security boundaries", "# Review\n\nInspect the code.\n")
	var prompt string
	runner := runnerFunc(func(_ context.Context, role agentcore.Role, task string) (agentcore.AgentResult, error) {
		if role != agentcore.RoleOrchestrator {
			t.Fatalf("role = %s", role)
		}
		prompt = task
		return agentcore.AgentResult{OK: true, Summary: "reviewed"}, nil
	})
	orch := newTestOrch(t, dir, runner)

	summaries := orch.SkillSummaries(t.Context())
	if len(summaries) != 1 || summaries[0].ID != "project:secure-review" || !summaries[0].Valid || !summaries[0].Enabled {
		t.Fatalf("summaries = %+v", summaries)
	}
	inspection, err := orch.SkillInspect(t.Context(), "secure-review")
	if err != nil || !strings.Contains(inspection.Content, "Inspect the code") {
		t.Fatalf("SkillInspect = %+v, %v", inspection, err)
	}
	if prompt != "" {
		t.Fatal("inspection executed a runner")
	}
	if err := orch.SetSkillEnabled(t.Context(), "secure-review", false); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.SkillRun(t.Context(), "secure-review"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled SkillRun = %v", err)
	}
	if err := orch.SetSessionSkillEnabled(t.Context(), "project:secure-review", true); err != nil {
		t.Fatal(err)
	}
	got, err := orch.SkillRun(t.Context(), "secure-review")
	if err != nil || got != "reviewed" {
		t.Fatalf("SkillRun = %q, %v", got, err)
	}
	for _, want := range []string{
		"MAESTRO_OPERATION: READ_ONLY_TASK", "MAESTRO_EXPLICIT_SKILL_JSON",
		"not authorization", `"id":"project:secure-review"`, "Inspect the code",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("skill prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "MAESTRO_HUMAN_OUTPUT_V1") {
		t.Fatal("human prose contract leaked into explicit Skill JSON envelope")
	}
	if strings.Contains(prompt, filepath.Join(dir, ".agents")) {
		t.Fatal("skill execution leaked a host skill path")
	}
}

func TestSkillBodyIsNeverInjectedIntoOrdinaryChat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	const marker = "SKILL_BODY_MUST_REQUIRE_EXPLICIT_RUN"
	installProjectSkill(t, dir, "audit", "Audit safely", marker+"\n")
	var prompts []string
	orch := newTestOrch(t, dir, runnerFunc(func(_ context.Context, _ agentcore.Role, task string) (agentcore.AgentResult, error) {
		prompts = append(prompts, task)
		return agentcore.AgentResult{OK: true, Summary: "hello"}, nil
	}))
	if err := orch.Chat(t.Context(), "explain the project"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prompts, "\n")
	if strings.Contains(joined, marker) || strings.Contains(joined, "MAESTRO_EXPLICIT_SKILL_JSON") {
		t.Fatalf("ordinary chat received skill instructions:\n%s", joined)
	}
}

func TestSkillRunUsesSubscriptionRoleRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	installProjectSkill(t, dir, "luna-review", "Review with subscription", "Read and explain.\n")
	state := settings.Defaults()
	state.RoleDefaults[settings.RoleOrchestrator] = settings.RoleDefaults{
		Engine: "legacy", Agent: "codex", Model: "gpt-5.6-luna",
	}
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Settings: state, In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := orch.runnerForRole(settings.RoleOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := runner.(*legacyRunner)
	if !ok || legacy.model != "gpt-5.6-luna" || legacy.agent.Name() != "Codex" {
		t.Fatalf("subscription route = %#v", runner)
	}
	// SkillRun calls this same resolver. A missing Codex executable proves it
	// reached the subscription wrapper instead of falling back to native.
	t.Setenv("PATH", t.TempDir())
	_, err = orch.SkillRun(t.Context(), "luna-review")
	if err == nil || strings.Contains(err.Error(), "native engine") {
		t.Fatalf("SkillRun subscription error = %v", err)
	}
}

func TestSkillRunRejectsUnsuccessfulOrEmptyRunnerResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		result agentcore.AgentResult
		want   string
	}{
		{name: "unsuccessful", result: agentcore.AgentResult{OK: false, Summary: "partial"}, want: "did not complete"},
		{name: "empty", result: agentcore.AgentResult{OK: true}, want: "without a response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
			dir := newTestRepo(t)
			installProjectSkill(t, dir, "audit", "Audit safely", "Review.\n")
			orch := newTestOrch(t, dir, runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
				return test.result, nil
			}))
			if _, err := orch.SkillRun(t.Context(), "audit"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SkillRun = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSkillRunNeutralizesTerminalControlSequences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	installProjectSkill(t, dir, "audit", "Audit safely", "Review.\n")
	orch := newTestOrch(t, dir, runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{OK: true, Summary: "line 1\n\titem\x1b]52;c;YXR0YWNr\a\u009b31m\u202E"}, nil
	}))
	got, err := orch.SkillRun(t.Context(), "audit")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got, "\x1b\a\u009b\u202e") {
		t.Fatalf("unsafe control survived: %q", got)
	}
	for _, visible := range []string{"line 1\n\titem", "<U+001B>", "<U+0007>", "<U+009B>", "<U+202E>"} {
		if !strings.Contains(got, visible) {
			t.Errorf("neutralized output missing %q: %q", visible, got)
		}
	}
}

func TestSkillRunNeutralizesAndBoundsRunnerErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	installProjectSkill(t, dir, "audit", "Audit safely", "Review.\n")
	raw := "vendor\x1b]52;c;YXR0YWNr\a" + strings.Repeat("x", skillRunErrorInputLimit*2)
	orch := newTestOrch(t, dir, runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{}, errors.New(raw)
	}))
	_, err := orch.SkillRun(t.Context(), "audit")
	if err == nil {
		t.Fatal("SkillRun unexpectedly succeeded")
	}
	if strings.ContainsAny(err.Error(), "\x1b\a") || !strings.Contains(err.Error(), "<U+001B>") {
		t.Fatalf("unsafe runner error = %q", err)
	}
	if len(err.Error()) > skillRunErrorSafeLimit+len(" [truncated]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("runner error was not bounded: len=%d error=%q", len(err.Error()), err)
	}
}

func TestSkillRunRejectsOversizedSummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	installProjectSkill(t, dir, "audit", "Audit safely", "Review.\n")
	orch := newTestOrch(t, dir, runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{OK: true, Summary: strings.Repeat("x", skillRunSummaryLimit+1)}, nil
	}))
	if _, err := orch.SkillRun(t.Context(), "audit"); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("SkillRun error = %v, want bounded output refusal", err)
	}
}

func TestReadOnlySkillRunnerConfinesShippedRunners(t *testing.T) {
	native := &nativeRunner{}
	gotNative, err := readOnlySkillRunner(native)
	if err != nil {
		t.Fatal(err)
	}
	nativeCopy := gotNative.(*nativeRunner)
	if !nativeCopy.silent || !nativeCopy.readOnly || native.silent || native.readOnly {
		t.Fatalf("native runner was not isolated: original=%+v copy=%+v", native, nativeCopy)
	}

	legacy := &legacyRunner{agent: legacyagent.NewCodexAgent(), reasoningEffort: "high"}
	gotLegacy, err := readOnlySkillRunner(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyCopy := gotLegacy.(*legacyRunner)
	if !legacyCopy.silent || !legacyCopy.readOnly || legacyCopy.reasoningEffort != "high" || legacy.readOnly {
		t.Fatalf("legacy runner was not isolated: original=%+v copy=%+v", legacy, legacyCopy)
	}
	if _, err := readOnlySkillRunner(&legacyRunner{agent: legacyagent.NewClaudeAgent()}); err == nil || !strings.Contains(err.Error(), "cannot enforce") {
		t.Fatalf("non-confinable subscription runner error = %v", err)
	}
	if _, err := readOnlySkillRunner(untrustedCustomRunner{}); err == nil || !strings.Contains(err.Error(), "cannot enforce") {
		t.Fatalf("unknown custom runner error = %v", err)
	}
}

func TestReadOnlyNativeToolsExcludeMutationAskAndMCP(t *testing.T) {
	all := map[string]agentcore.Tool{}
	for _, name := range []string{"read", "grep", "write", "bash", "ask", "mcp_server_tool"} {
		name := name
		all[name] = agentcore.NewToolFunc(agentcore.ToolSpec{Name: name}, func(context.Context, map[string]any) (string, error) {
			return "", nil
		})
	}
	got := readOnlyNativeTools(all)
	if len(got) != 2 || got["read"] == nil || got["grep"] == nil {
		t.Fatalf("read-only native tools = %v", got)
	}
	for _, forbidden := range []string{"write", "bash", "ask", "mcp_server_tool"} {
		if got[forbidden] != nil {
			t.Fatalf("read-only native tools exposed %q", forbidden)
		}
	}
}

func TestLegacyRunnerCumulativeOutputLimitCancelsStream(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	provider := &oversizedLegacyAgent{canceled: make(chan struct{})}
	runner := &legacyRunner{agent: provider, o: orch, silent: true}
	result, err := runner.Run(t.Context(), agentcore.RoleOrchestrator, "bounded")
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("Run result=%+v error=%v, want cumulative limit", result, err)
	}
	if result.Summary != "" {
		t.Fatalf("partial oversized result escaped: %d bytes", len(result.Summary))
	}
	select {
	case <-provider.canceled:
	case <-time.After(time.Second):
		t.Fatal("legacy agent context was not canceled")
	}
}

func TestDispatchSkillsLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	installProjectSkill(t, dir, "audit", "Audit code", "Review.\n")
	var out strings.Builder
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Out: &out, In: strings.NewReader(""),
		Runner: runnerFunc(func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error) {
			return agentcore.AgentResult{OK: true, Summary: "done"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	commands := []Command{
		{Cmd: "skills", Args: []string{"list"}},
		{Cmd: "skills", Args: []string{"show", "audit"}},
		{Cmd: "skills", Args: []string{"disable", "audit"}, Flags: map[string]string{"scope": "session"}},
		{Cmd: "skills", Args: []string{"enable", "audit"}, Flags: map[string]string{"scope": "session"}},
		{Cmd: "skills", Args: []string{"run", "audit"}},
	}
	for _, command := range commands {
		if err := orch.Dispatch(t.Context(), command); err != nil {
			t.Fatalf("Dispatch(%+v): %v", command, err)
		}
	}
	for _, want := range []string{"project:audit", "skill: project:audit", "disabled for session", "enabled for session", "done"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}
