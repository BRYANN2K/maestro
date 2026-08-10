package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/settings"
)

type structuredLearnLegacyAgent struct {
	called bool
}

func (*structuredLearnLegacyAgent) Name() string           { return "learn-structured-test" }
func (*structuredLearnLegacyAgent) Models() []string       { return []string{"learn-test"} }
func (*structuredLearnLegacyAgent) SupportsReadOnly() bool { return true }
func (a *structuredLearnLegacyAgent) Execute(_ context.Context, _ string, _ agent.Options) (<-chan agentcore.StreamEvent, error) {
	a.called = true
	return nil, errors.New("subscription Learn must reject before agent execution")
}

func TestLearnRejectsInjectedSubscriptionEvenWhenItAdvertisesReadOnly(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, nil)
	legacyAgent := &structuredLearnLegacyAgent{}
	orch.SetRunner(&legacyRunner{
		agent: legacyAgent,
		model: "learn-test",
		o:     orch,
	})
	_, _, err := orch.LearnDraft(t.Context(), "main.go", false)
	if err == nil || !strings.Contains(err.Error(), "cannot confine embedded source access") ||
		!strings.Contains(err.Error(), "native/API model") {
		t.Fatalf("subscription Learn refusal = %v", err)
	}
	if legacyAgent.called {
		t.Fatal("subscription agent executed before Learn confidentiality refusal")
	}
	select {
	case event := <-orch.Stream:
		t.Fatalf("failed secure Learn leaked event %s: %#v", event.Type, event.Content)
	default:
	}
}

func TestPrivateLearnRunnerClonesNativeWithNoCapabilities(t *testing.T) {
	original := &nativeRunner{model: "learn-model", reasoningEffort: "high"}
	got, err := privateLearnRunner(original)
	if err != nil {
		t.Fatal(err)
	}
	clone, ok := got.(*nativeRunner)
	if !ok {
		t.Fatalf("private Learn runner = %T, want *nativeRunner", got)
	}
	if !clone.silent || !clone.readOnly || !clone.noTools ||
		clone.model != original.model || clone.reasoningEffort != original.reasoningEffort {
		t.Fatalf("private Learn clone = %+v, original = %+v", clone, original)
	}
	if original.silent || original.readOnly || original.noTools {
		t.Fatalf("private Learn mutated shared runner: %+v", original)
	}
}

func TestLearnRejectsEveryPersistedSubscriptionRoute(t *testing.T) {
	for _, route := range []settings.RoleDefaults{
		{Engine: "legacy", Agent: "codex", Model: "gpt-5.6-luna", ReasoningEffort: "high", ReasoningSet: true},
		{Engine: "legacy", Agent: "claude", Model: "sonnet-4.5"},
	} {
		t.Run(route.Agent, func(t *testing.T) {
			state := settings.Defaults()
			state.RoleDefaults[settings.RoleOrchestrator] = route
			orch, err := New(t.Context(), Options{
				ProjectDir: newTestRepo(t), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
				In: strings.NewReader(""), Out: &strings.Builder{}, Settings: state,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := orch.LearnDraft(t.Context(), "main.go", false); err == nil ||
				!strings.Contains(err.Error(), "cannot confine embedded source access") ||
				!strings.Contains(err.Error(), "native/API model") {
				t.Fatalf("persisted %s Learn refusal = %v", route.Agent, err)
			}
			select {
			case event := <-orch.Stream:
				t.Fatalf("failed secure Learn leaked event %s: %#v", event.Type, event.Content)
			default:
			}
		})
	}
}
