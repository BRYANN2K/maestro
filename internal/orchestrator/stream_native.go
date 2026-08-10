package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/settings"
)

const legacyRunOutputLimit = 8 << 20

// EngineChoice is one option of the engine picker (§5.2).
type EngineChoice struct {
	Engine string // native | legacy (persisted compatibility for subscription)
	Agent  string // subscription CLI name, empty for native
	Model  string // model, empty = auto
}

// Label renders the picker row.
func (e EngineChoice) Label() string {
	if e.Engine == "native" {
		return "native · Maestro agent (API or local model)"
	}
	return fmt.Sprintf("subscription · %s", e.Agent)
}

// EngineChoices returns the picker options for a role: native plus every
// registered legacy agent.
func (o *Orchestrator) EngineChoices(role string) []EngineChoice {
	choices := []EngineChoice{{Engine: "native"}}
	for _, name := range agent.Names() {
		choices = append(choices, EngineChoice{Engine: "legacy", Agent: name})
	}
	return choices
}

// rememberEngine persists the engine+agent choice per role (§5.2).
func (o *Orchestrator) rememberEngine(role, engine, agentName string) {
	next := o.SettingsSnapshot()
	rd := next.RoleDefaults[role]
	rd.Engine = engine
	rd.Agent = agentName
	if !containsReasoningEffort(o.ReasoningEfforts(engine, agentName, rd.Model), rd.ReasoningEffort) {
		rd.ReasoningEffort = ""
		rd.ReasoningSet = false
	}
	if next.RoleDefaults == nil {
		next.RoleDefaults = map[string]settings.RoleDefaults{}
	}
	next.RoleDefaults[role] = rd
	_ = o.UpdateSettings(context.Background(), next)
}

// withRunContext installs the run context for CancelRun.
func (o *Orchestrator) withRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	o.runMu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	o.runID++
	id := o.runID
	o.runCancel = cancel
	o.runActive = true
	o.runMu.Unlock()
	var once sync.Once
	return runCtx, func() {
		once.Do(func() {
			cancel()
			o.runMu.Lock()
			if o.runID == id {
				o.runCancel = nil
				o.runActive = false
			}
			o.runMu.Unlock()
		})
	}
}

// CancelRun cancels the in-flight sub-agent run (Ctrl-C / sidebar click).
func (o *Orchestrator) CancelRun() {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.runCancel != nil {
		o.runCancel()
		o.runCancel = nil
	}
}

// nativeRunner runs the built-in agentcore loop via Spawn (§4).
type nativeRunner struct {
	o               *Orchestrator
	model           string
	reasoningEffort string
	sampling        agentcore.Sampling
	samplingSet     bool
	silent          bool // internal structured runs return their result without UI deltas
	readOnly        bool // skill runs expose only in-process read/grep tools and no MCP
	noTools         bool // private structured runs embed all input and expose no capabilities
}

// Run executes one spawned loop turn with role-scoped tools and the gate.
func (r *nativeRunner) Run(ctx context.Context, role agentcore.Role, taskPrompt string) (agentcore.AgentResult, error) {
	if r.o.registry == nil {
		return agentcore.AgentResult{}, errors.New("native engine: no provider configured (add one to maestrorc)")
	}
	model := r.model
	route := r.o.effectiveRoleRoute(string(role))
	sampling := r.o.effectiveNativeSampling(string(role), route)
	if r.samplingSet {
		sampling = r.sampling
	}
	if r.reasoningEffort != "" {
		sampling.ReasoningEffort = r.reasoningEffort
	}
	if model == "" {
		model = route.Model
	}
	if model == "" {
		return agentcore.AgentResult{}, errors.New("native engine: no model configured (modelRoles: default: ...)")
	}
	if err := r.o.registry.CheckModel(model); err != nil {
		msg := "native engine: " + err.Error()
		if avail := r.o.availableProviders(); avail != "" {
			msg += " — available: " + avail
		}
		return agentcore.AgentResult{}, errors.New(msg)
	}
	providerName, _ := r.o.registry.ProviderOf(model)
	if !containsReasoningEffort(r.o.ReasoningEfforts("native", "", model), sampling.ReasoningEffort) {
		return agentcore.AgentResult{}, fmt.Errorf(
			"native engine: reasoning effort %q is unsupported by model %q",
			defaultReasoningEffort(sampling.ReasoningEffort), model,
		)
	}
	provider, _ := r.o.registry.Provider(providerName)
	// Send the canonical API model ID, not the qualified selection handle:
	// picking "opencode/deepseek-v4-flash-free" must send
	// "deepseek-v4-flash-free" to the provider (models.dev ids are API ids,
	// exactly like opencode's api.id / the old APIModel field).
	model = r.o.canonicalModel(model)

	var specFiles []string
	if r.o.spec != nil && role != agentcore.RoleOrchestrator {
		specFiles = []string{
			r.o.store.PathFor(r.o.spec.ID, "spec.md"),
			r.o.store.PathFor(r.o.spec.ID, "design.md"),
			r.o.store.PathFor(r.o.spec.ID, "tasks.md"),
		}
	}
	var diff string
	if role == agentcore.RoleReviewer {
		d, err := r.o.workspaceRoute().git.WorktreeDiff(ctx, "HEAD")
		if err != nil {
			return agentcore.AgentResult{}, fmt.Errorf("native reviewer evidence: %w", err)
		}
		diff = d
	}
	if !r.readOnly && !r.noTools && (role == agentcore.RoleOrchestrator || role == agentcore.RoleDev || role == agentcore.RoleDocs) {
		// MCP is a native-loop capability only. Discovery is best effort: an
		// unavailable external server must not prevent the provider turn.
		_ = r.o.connectMCP(ctx)
	}
	var tools map[string]agentcore.Tool
	if !r.noTools {
		tools = r.o.scopedTools(role)
		if r.readOnly {
			tools = readOnlyNativeTools(tools)
		}
	}
	loop, err := agentcore.Spawn(ctx, agentcore.SpawnOptions{
		Role:      role,
		Provider:  provider,
		Model:     model,
		Sampling:  sampling,
		Tools:     tools,
		Gate:      r.o.gate,
		SpecFiles: specFiles,
		Diff:      diff,
		Stopper:   agentcore.NewStopper(),
		Rules:     r.o.guardrails.Rules,
		Budget:    r.o.guardrails.Budget,
		AntiLoop:  r.o.guardrails.AntiLoop,
		OnEvent: func(ev agentcore.StreamEvent) {
			if !r.silent {
				ev.Role = role
				r.o.emit(ev)
			}
		},
	})
	if err != nil {
		return agentcore.AgentResult{}, err
	}
	// F2 pre-run estimate: worst-case cost before execution.
	if est := r.o.estimateRunCost(provider, model, loop); est > 0 && !r.silent {
		r.o.emit(agentcore.NewEvent(nil, role, agentcore.EvHITL, agentcore.HITLItem{
			ID: "budget-estimate", Item: fmt.Sprintf("estimated cost $%.4f", est), Status: "done",
		}))
	}
	return agentcore.RunResult(ctx, loop, taskPrompt)
}

// estimateRunCost computes a rough worst-case cost from the history size
// and the model's output cap.
func (o *Orchestrator) estimateRunCost(p agentcore.Provider, modelID string, loop *agentcore.Loop) float64 {
	if o.guardrails.Budget == nil {
		return 0
	}
	m, ok := o.registry.Model(modelID)
	if !ok || m.PriceInput <= 0 && m.PriceOutput <= 0 {
		return 0
	}
	var chars int
	for _, msg := range append(loop.System, loop.History...) {
		chars += len(msg.Content)
	}
	output := m.DefaultMaxTokens
	if output <= 0 {
		output = 2048
	}
	usage := agentcore.Usage{InputTokens: chars / 4, OutputTokens: output}
	cost, err := p.Cost(agentcore.Request{Model: modelID}, usage)
	if err != nil {
		return 0
	}
	return cost.Total()
}

// defaultModel resolves the native fallback from maestrorc roles and then
// settings model_slots ("large"). Role-specific persisted routes are resolved
// by effectiveRoleRoute so legacy selections never inherit a native model.
func (o *Orchestrator) defaultModel() string {
	if model, _, ok := o.configRoleSelection(settings.RoleOrchestrator); ok {
		return model
	}
	if m := o.SettingsSnapshot().ModelSlots["large"]; m != "" {
		return m
	}
	return ""
}

// configRoleSelection resolves an exact Maestro role first, then maestrorc's
// canonical default role/large slot. This keeps custom modelRoles useful
// without silently mapping BUILD/REVIEW to unrelated smol/plan semantics.
func (o *Orchestrator) configRoleSelection(role string) (string, agentcore.Sampling, bool) {
	if o.cfg == nil {
		return "", agentcore.Sampling{}, false
	}
	slots, roles := agentcore.SlotsFromConfig(o.cfg)
	if selected, ok := roles[role]; ok {
		return selected.Model, selected.Sampling, true
	}
	return agentcore.ResolveRole(agentcore.RoleDefault, slots, roles)
}

func (o *Orchestrator) effectiveNativeSampling(role string, route settings.RoleDefaults) agentcore.Sampling {
	_, sampling, _ := o.configRoleSelection(role)
	// effectiveRoleRoute already applied Settings-explicit > config
	// precedence and provider capability validation.
	sampling.ReasoningEffort = route.ReasoningEffort
	return sampling
}

// canonicalModel maps a selection handle ("provider/model") to the API
// model ID served by the registry. Catalog model IDs are bare API ids, so
// "opencode/deepseek-v4-flash-free" resolves to "deepseek-v4-flash-free";
// genuinely-qualified ids (e.g. "accounts/fireworks/models/x") pass through
// unchanged.
func (o *Orchestrator) canonicalModel(model string) string {
	if o.registry == nil {
		return model
	}
	return o.registry.APIModelID(model)
}

// legacyRunner shells out to a third-party coding agent.
type legacyRunner struct {
	agent           agent.Agent
	model           string
	reasoningEffort string
	o               *Orchestrator
	silent          bool
	readOnly        bool
}

// Run streams the external agent's events and yields a summary result.
func (r *legacyRunner) Run(ctx context.Context, role agentcore.Role, taskPrompt string) (agentcore.AgentResult, error) {
	if r.readOnly && !agent.SupportsReadOnly(r.agent) {
		return agentcore.AgentResult{}, fmt.Errorf("subscription agent %q cannot enforce read-only execution", r.agent.Name())
	}
	if role == agentcore.RoleReviewer {
		evidence, err := r.o.legacyReviewEvidence(ctx)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		taskPrompt += evidence
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := r.agent.Execute(runCtx, taskPrompt, agent.Options{
		Model: r.model, ReasoningEffort: r.reasoningEffort,
		WorkDir: r.o.workDir(), ReadOnly: r.readOnly,
	})
	if err != nil {
		return agentcore.AgentResult{}, err
	}
	var summary strings.Builder
	var streamErr error
	outputExceeded := false
	sawDone := false
	for ev := range ch {
		ev.Role = role
		if !r.silent {
			r.o.emit(ev)
		}
		if ev.Type == agentcore.EvTextDelta {
			if td, ok := ev.Content.(agentcore.TextDelta); ok {
				if summary.Len()+len(td.Text) > legacyRunOutputLimit {
					outputExceeded = true
					cancel()
				} else if !outputExceeded {
					summary.WriteString(td.Text)
				}
			}
		}
		if ev.Type == agentcore.EvError && streamErr == nil {
			streamErr = legacyStreamError(ev.Content)
		}
		if ev.Type == agentcore.EvDone {
			if _, ok := ev.Content.(agentcore.Done); ok {
				sawDone = true
			} else if streamErr == nil {
				streamErr = agentcore.StreamError{Message: fmt.Sprintf("legacy agent returned malformed completion payload %T", ev.Content)}
			}
		}
	}
	if outputExceeded {
		return agentcore.AgentResult{Role: string(role), OK: false}, fmt.Errorf("legacy agent output exceeded %d bytes", legacyRunOutputLimit)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// A caller cancellation is a lifecycle outcome, not a vendor failure.
		// Drop the partial summary so Chat cannot persist an interrupted answer
		// into the next turn's conversation context.
		return agentcore.AgentResult{Role: string(role), OK: false}, ctxErr
	}
	if streamErr == nil && !sawDone {
		streamErr = agentcore.StreamError{Message: "legacy agent stream closed without a completion event"}
	}
	result := agentcore.AgentResult{Role: string(role), OK: streamErr == nil, Summary: summary.String()}
	if streamErr != nil {
		if strings.TrimSpace(result.Summary) == "" {
			result.Summary = streamErr.Error()
		}
		// The same typed error was already streamed to the UI. Returning it
		// unchanged lets the completion path de-duplicate the terminal message;
		// wrapping it with the agent name rendered a second, different error.
		return result, streamErr
	}
	return result, nil
}

// readOnlyNativeTools returns the minimal non-mutating native capability set.
// MCP, ask, shell, and write are deliberately absent regardless of metadata in
// the selected skill.
func readOnlyNativeTools(all map[string]agentcore.Tool) map[string]agentcore.Tool {
	tools := make(map[string]agentcore.Tool, 2)
	for _, name := range []string{"read", "grep"} {
		if tool := all[name]; tool != nil {
			tools[name] = tool
		}
	}
	return tools
}

func legacyStreamError(content any) error {
	var message string
	switch value := content.(type) {
	case agentcore.StreamError:
		message = value.Message
	case *agentcore.StreamError:
		if value != nil {
			message = value.Message
		}
	case error:
		message = value.Error()
	case string:
		message = value
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "legacy agent stream failed"
	}
	return agentcore.StreamError{Message: message}
}
