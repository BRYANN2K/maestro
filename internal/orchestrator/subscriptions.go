package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/settings"
)

// SubscriptionInfo describes an official coding-agent subscription that
// Maestro can reuse through its CLI. Credentials remain owned by the vendor
// CLI/keychain; Maestro only invokes login/status/logout and never reads them.
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

type subscriptionSpec struct {
	id, label, cli, agent string
	login, status, logout []string
	models                []string
}

var subscriptionSpecs = []subscriptionSpec{
	{
		id: "codex", label: "Codex · ChatGPT plan", cli: "codex", agent: "codex",
		login: []string{"login"}, status: []string{"login", "status"}, logout: []string{"logout"},
		models: []string{"auto", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"},
	},
	{
		id: "claude", label: "Claude Code · Claude plan", cli: "claude", agent: "claude",
		login: []string{"auth", "login"}, status: []string{"auth", "status", "--json"}, logout: []string{"auth", "logout"},
		models: []string{"auto", "sonnet", "opus", "haiku"},
	},
}

// SubscriptionList returns bounded, secret-free CLI status. A slow or broken
// CLI is reported as unknown instead of blocking the TUI indefinitely.
func (o *Orchestrator) SubscriptionList(ctx context.Context) []SubscriptionInfo {
	out := make([]SubscriptionInfo, 0, len(subscriptionSpecs))
	for _, spec := range subscriptionSpecs {
		info := SubscriptionInfo{
			ID: spec.id, Label: spec.label, CLI: spec.cli, Agent: spec.agent,
			Status: "not installed", Models: append([]string(nil), spec.models...),
		}
		path, err := exec.LookPath(spec.cli)
		if err != nil {
			out = append(out, info)
			continue
		}
		info.Installed = true
		info.Status = "signed out"
		statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		cmd := exec.CommandContext(statusCtx, path, spec.status...)
		err = cmd.Run()
		if statusCtx.Err() != nil {
			info.Status = "status unavailable"
		} else if err == nil {
			info.Authenticated = true
			info.Status = "connected"
		}
		cancel()
		out = append(out, info)
	}
	return out
}

// SubscriptionCommand returns the official interactive CLI command. It is
// executed by Bubble Tea with terminal suspension, so browser/device prompts
// work exactly as they do in a normal shell.
func (o *Orchestrator) SubscriptionCommand(provider, action string) (*exec.Cmd, error) {
	for _, spec := range subscriptionSpecs {
		if spec.id != provider {
			continue
		}
		path, err := exec.LookPath(spec.cli)
		if err != nil {
			return nil, errors.New(spec.cli + " CLI is not installed")
		}
		var args []string
		switch action {
		case "login":
			args = spec.login
		case "logout":
			args = spec.logout
		default:
			return nil, errors.New("unknown subscription action " + action)
		}
		cmd := exec.Command(path, args...)
		cmd.Dir = o.baseDir
		return cmd, nil
	}
	return nil, errors.New("unknown subscription provider " + provider)
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
	if engine != "native" && engine != "legacy" {
		return errors.New("task engine must be native or subscription")
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

// normalizeEngineName keeps the historical on-disk value "legacy" readable
// while exposing the accurate product name "subscription" to users. External
// coding CLIs reuse vendor subscriptions; local models remain providers for
// the native Maestro loop rather than a third execution engine.
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
// default; legacy routes with no model deliberately remain empty so the
// vendor CLI can select its own "auto" model.
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

// runnerForRole resolves the persisted task route. Native providers and
// subscription-backed vendor CLIs implement the same Runner contract.
func (o *Orchestrator) runnerForRole(role string) (Runner, error) {
	if o.runner != nil {
		return o.runner, nil
	}
	route := o.effectiveRoleRoute(role)
	if route.Engine == "legacy" {
		name := route.Agent
		if name == "" {
			name = "codex"
		}
		a, err := agent.Create(name)
		if err != nil {
			return nil, err
		}
		return &legacyRunner{agent: a, model: route.Model, reasoningEffort: route.ReasoningEffort, o: o}, nil
	}
	if o.registry == nil {
		return nil, errors.New("native engine: no provider configured")
	}
	return &nativeRunner{
		o: o, model: route.Model, reasoningEffort: route.ReasoningEffort,
		sampling: o.effectiveNativeSampling(role, route), samplingSet: true,
	}, nil
}
