package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bryann2k/maestro/internal/runtimebridge"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/settings"
)

// SubscriptionInfo describes a provider account used by the built-in harness.
type SubscriptionInfo struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	CLI           string   `json:"cli"`
	Agent         string   `json:"agent"`
	Installed     bool     `json:"installed"`
	Authenticated bool     `json:"authenticated"`
	Status        string   `json:"status"`
	Models        []string `json:"models"`
}

func (o *Orchestrator) SubscriptionList(ctx context.Context) []SubscriptionInfo {
	labels := map[string]string{"openai-codex": "OpenAI · ChatGPT", "anthropic": "Anthropic · Claude", "google-gemini-cli": "Google · Gemini", "github-copilot": "GitHub · Copilot"}
	var result []SubscriptionInfo
	for _, id := range agentcore.AccountProviders() {
		key := ""
		if o.vault != nil {
			key, _ = o.vault.Get("key:" + id)
		}
		connected := agentcore.IsRuntimeCredential(key)
		status := "connect account"
		if connected {
			status = "connected · Maestro"
		}
		var models []string
		if connected && o.registry != nil {
			if provider, ok := o.registry.Provider(id); ok {
				for _, model := range provider.Models() {
					models = append(models, id+"/"+model.ID)
				}
			}
		}
		result = append(result, SubscriptionInfo{ID: id, Label: labels[id], CLI: "Maestro", Installed: runtimebridge.Available(), Authenticated: connected, Status: status, Models: models})
	}
	return result
}

func (o *Orchestrator) SubscriptionCommand(provider, action string) (*exec.Cmd, error) {
	if !agentcore.OAuthRuntimeSupported(provider) {
		return nil, fmt.Errorf("unknown account provider %q", provider)
	}
	operation := "oauth"
	switch action {
	case "login":
	case "logout":
		operation = "logout"
	default:
		return nil, fmt.Errorf("unsupported account action %q", action)
	}
	if _, err := runtimebridge.Path(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(executable, "--dir", o.baseDir, "auth", operation, provider), nil
}

// SetTaskModel persists the execution route for a Maestro task/role.
func (o *Orchestrator) SetTaskModel(ctx context.Context, role, engine, agentName, model string) error {
	previous := o.SettingsSnapshot().RoleDefaults[strings.TrimSpace(role)]
	effort := previous.ReasoningEffort
	reasoningSet := previous.ReasoningSet
	engine = normalizeEngineName(engine)
	if model == "auto" {
		model = ""
	}
	if !containsReasoningEffort(o.ReasoningEfforts(engine, agentName, model), effort) {
		effort = ""
		reasoningSet = false
	}
	return o.setTaskModelWithReasoning(ctx, role, engine, agentName, model, effort, reasoningSet)
}

// SetTaskModelWithReasoning persists an execution route and an explicitly
// selected effort. "auto" is stored as the empty value for compatibility
// with settings written before reasoning selection existed.
func (o *Orchestrator) SetTaskModelWithReasoning(ctx context.Context, role, engine, agentName, model, effort string) error {
	engine = normalizeEngineName(engine)
	if model == "auto" {
		model = ""
	}
	effort = agentcore.NormalizeReasoningEffort(effort)
	if !containsReasoningEffort(o.ReasoningEfforts(engine, agentName, model), effort) {
		return fmt.Errorf("reasoning effort %q is unsupported for %s/%s", defaultReasoningEffort(effort), engine, defaultReasoningModel(model))
	}
	return o.setTaskModelWithReasoning(ctx, role, engine, agentName, model, effort, true)
}

func (o *Orchestrator) setTaskModelWithReasoning(ctx context.Context, role, engine, agentName, model, effort string, reasoningSet bool) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return errors.New("task role is required")
	}
	engine = normalizeEngineName(engine)
	if engine != "native" || agentName != "" {
		return errors.New("maestro is the only harness; select a provider/model")
	}
	next := o.SettingsSnapshot()
	if next.RoleDefaults == nil {
		next.RoleDefaults = map[string]settings.RoleDefaults{}
	}
	if engine == "native" {
		agentName = ""
	}
	next.RoleDefaults[role] = settings.RoleDefaults{
		Engine: engine, Agent: agentName, Model: model,
		ReasoningEffort: effort, ReasoningSet: reasoningSet,
	}
	return o.UpdateSettings(ctx, next)
}

var (
	autoReasoningEfforts  = []string{"auto"}
	codexReasoningEfforts = []string{"auto", "minimal", "low", "medium", "high", "xhigh"}
)

// ReasoningEfforts returns only the values honored by the selected route.
// It is shared by Settings, the model workspace, and route validation.
func (o *Orchestrator) ReasoningEfforts(engine, agentName, model string) []string {
	engine = normalizeEngineName(engine)
	if engine == "legacy" {
		if strings.EqualFold(strings.TrimSpace(agentName), "codex") {
			return append([]string(nil), codexReasoningEfforts...)
		}
		return append([]string(nil), autoReasoningEfforts...)
	}
	if o.registry == nil {
		return append([]string(nil), autoReasoningEfforts...)
	}
	return o.registry.ReasoningEfforts(model)
}

func containsReasoningEffort(values []string, effort string) bool {
	effort = defaultReasoningEffort(agentcore.NormalizeReasoningEffort(effort))
	for _, value := range values {
		if value == effort {
			return true
		}
	}
	return false
}

func defaultReasoningEffort(effort string) string {
	if effort == "" {
		return "auto"
	}
	return effort
}

func defaultReasoningModel(model string) string {
	if model == "" {
		return "auto"
	}
	return model
}

// initializeReasoningSettings performs the startup migration only after the
// provider registry exists, so compatibility is decided from the real wire
// protocol. Unsupported persisted selections are reset transactionally;
// incompatible effective maestrorc sampling fails closed before any run.
func (o *Orchestrator) initializeReasoningSettings(ctx context.Context) error {
	next := o.SettingsSnapshot()
	changed := false
	for role, route := range next.RoleDefaults {
		normalized := agentcore.NormalizeReasoningEffort(route.ReasoningEffort)
		if normalized != route.ReasoningEffort {
			route.ReasoningEffort = normalized
			changed = true
		}
		if route.ReasoningEffort != "" && !route.ReasoningSet {
			route.ReasoningSet = true
			changed = true
		}
		engine := normalizeEngineName(route.Engine)
		if engine == "" {
			engine = "native"
		}
		model := route.Model
		if model == "" && engine == "native" {
			o.settingsMu.RLock()
			model = o.model
			o.settingsMu.RUnlock()
			if model == "" {
				if configured, _, ok := o.configRoleSelection(role); ok {
					model = configured
				} else {
					model = next.ModelSlots["large"]
				}
			}
		}
		if route.ReasoningSet && !containsReasoningEffort(o.ReasoningEfforts(engine, route.Agent, model), route.ReasoningEffort) {
			route.ReasoningEffort = ""
			route.ReasoningSet = false
			changed = true
		}
		next.RoleDefaults[role] = route
	}

	for _, role := range []string{settings.RoleOrchestrator, settings.RoleDev, settings.RoleReviewer, settings.RoleDocs} {
		route := next.RoleDefaults[role]
		engine := normalizeEngineName(route.Engine)
		if engine == "legacy" || route.ReasoningSet {
			continue
		}
		configuredModel, sampling, ok := o.configRoleSelection(role)
		if !ok || agentcore.NormalizeReasoningEffort(sampling.ReasoningEffort) == "" {
			continue
		}
		model := route.Model
		if model == "" {
			model = configuredModel
		}
		effort := agentcore.NormalizeReasoningEffort(sampling.ReasoningEffort)
		if !containsReasoningEffort(o.ReasoningEfforts("native", "", model), effort) {
			return fmt.Errorf("maestrorc role %s: reasoning effort %q is unsupported by model %q", role, effort, model)
		}
	}

	if !changed {
		return next.Valid()
	}
	if err := o.UpdateSettings(ctx, next); err != nil {
		return fmt.Errorf("migrate reasoning settings: %w", err)
	}
	return nil
}

// normalizeEngineName recognizes retired settings so callers can reject them explicitly.
func normalizeEngineName(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "subscription":
		return "legacy"
	default:
		return strings.ToLower(strings.TrimSpace(engine))
	}
}

// effectiveRoleRoute is the single source of truth for execution and UI
// reporting. A model explicitly assigned to a task route always wins. Native
// routes may then inherit the process-level model override and maestrorc
// default. Retired routes remain identifiable and are rejected before execution.
func (o *Orchestrator) effectiveRoleRoute(role string) settings.RoleDefaults {
	snapshot := o.SettingsSnapshot()
	route := snapshot.RoleDefaults[role]
	if route.Engine == "" {
		route.Engine = "native"
	}
	if route.Model == "" && route.Engine != "legacy" {
		o.settingsMu.RLock()
		processModel := o.model
		o.settingsMu.RUnlock()
		if processModel != "" {
			route.Model = processModel
		} else if configured, _, ok := o.configRoleSelection(role); ok {
			route.Model = configured
		} else {
			route.Model = snapshot.ModelSlots["large"]
		}
	}
	if route.Engine == "native" && !route.ReasoningSet {
		if _, sampling, ok := o.configRoleSelection(role); ok {
			route.ReasoningEffort = agentcore.NormalizeReasoningEffort(sampling.ReasoningEffort)
		}
	}
	if !containsReasoningEffort(o.ReasoningEfforts(route.Engine, route.Agent, route.Model), route.ReasoningEffort) {
		route.ReasoningEffort = ""
		route.ReasoningSet = false
	}
	return route
}

// runnerForRole resolves a persisted route to the sole Maestro harness.
func (o *Orchestrator) runnerForRole(role string) (Runner, error) {
	if o.runner != nil {
		return o.runner, nil
	}
	route := o.effectiveRoleRoute(role)
	if route.Engine != "" && route.Engine != "native" || route.Agent != "" {
		return nil, errors.New("external harness route requires migration; select a provider/model in Maestro")
	}
	if o.registry == nil {
		return nil, errors.New("native engine: no provider configured")
	}
	return &nativeRunner{
		o: o, model: route.Model, reasoningEffort: route.ReasoningEffort,
		sampling: o.effectiveNativeSampling(role, route), samplingSet: true,
	}, nil
}
