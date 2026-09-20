package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/rlm"
	"github.com/bryann2k/maestro/internal/runtimebridge"
	"github.com/bryann2k/maestro/internal/settings"
)

// EngineChoice is one option of the engine picker (§5.2).
type EngineChoice struct {
	Engine string // native | legacy (persisted compatibility for subscription)
	Agent  string // subscription CLI name, empty for native
	Model  string // model, empty = auto
}

// Label renders the picker row.
func (e EngineChoice) Label() string {
	if e.Engine == "native" {
		return "Maestro · built-in harness"
	}
	return fmt.Sprintf("subscription · %s", e.Agent)
}

// EngineChoices returns the picker options for a role: native plus every
// registered legacy agent.
func (o *Orchestrator) EngineChoices(role string) []EngineChoice {
	return []EngineChoice{{Engine: "native"}}
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
	toolOverride    map[string]agentcore.Tool
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
	modelMetadata, _ := r.o.registry.Model(model)
	selectedModel := model
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
	// Build and Docs prompts already embed the accepted trio because the same
	// prompt must work for subscription runners. Seeding those files again as
	// native system messages doubled their largest stable context. Review is
	// the only native route whose task prompt intentionally relies on Spawn to
	// supply the spec files (and diff) out of band.
	if r.o.spec != nil && role == agentcore.RoleReviewer {
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
	exposeTools := !r.noTools && role != agentcore.RoleDocs
	if !r.readOnly && exposeTools && (role == agentcore.RoleOrchestrator || role == agentcore.RoleDev) {
		// MCP is a native-loop capability only. Discovery is best effort: an
		// unavailable external server must not prevent the provider turn.
		_ = r.o.connectMCP(ctx)
	}
	var tools map[string]agentcore.Tool
	if exposeTools {
		tools = r.o.scopedTools(role)
		if r.readOnly {
			tools = readOnlyNativeTools(tools)
		}
	}
	if !r.readOnly && !r.noTools && r.toolOverride == nil && (role == agentcore.RoleDev || role == agentcore.RoleOrchestrator) {
		if role == agentcore.RoleOrchestrator {
			tools["stipulate"] = r.o.workflowTool()
		}
		kernel := rlm.New(r.o.workDir(), func(ctx context.Context, req map[string]any) (any, error) {
			if role != agentcore.RoleOrchestrator {
				return nil, errors.New("only the coordinator may delegate")
			}
			if req["type"] != "stip.delegate" {
				return nil, errors.New("use await rlm.host_request('stip.delegate', {'change_id': '...', 'task_id': '...'}) for approved tasks")
			}
			result, err := r.o.workflowRuntime(ctx, map[string]any{"action": "delegate", "task": map[string]any{"change_id": req["change_id"], "task_id": req["task_id"]}})
			if err != nil {
				return nil, err
			}
			return result, nil
		})
		defer kernel.Close()
		tools["rlm"] = agentcore.NewToolFunc(agentcore.ToolSpec{Name: "rlm", Description: "Execute a Python cell in Prime's persistent RLM kernel. Globals persist for this run; top-level await is supported. Requires approval: Python has shell-level access. Slice large context and return source paths and hashes. Coordinator delegation uses rlm.host_request('stip.delegate', {change_id, task_id}); only approved Stipulate tasks are admitted. 60s and 1MiB output per cell.", NeedsApproval: true, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"}}}, func(ctx context.Context, args map[string]any) (string, error) {
			code, _ := args["code"].(string)
			return kernel.Execute(ctx, code)
		})
	}
	if r.toolOverride != nil {
		tools = r.toolOverride
	}
	guardrails := r.o.guardrailSnapshot()
	loop, err := agentcore.Spawn(ctx, agentcore.SpawnOptions{
		Role:             role,
		Provider:         provider,
		Model:            model,
		ContextWindow:    modelMetadata.ContextWindow,
		DefaultMaxTokens: modelMetadata.DefaultMaxTokens,
		Sampling:         sampling,
		Tools:            tools,
		Gate:             r.o.gate,
		SpecFiles:        specFiles,
		Diff:             diff,
		Stopper:          agentcore.NewStopper(),
		Rules:            guardrails.Rules,
		Budget:           guardrails.Budget,
		AntiLoop:         guardrails.AntiLoop,
		OnEvent: func(ev agentcore.StreamEvent) {
			r.o.forwardRunnerEvent(ev, role, r.silent)
		},
	})
	if err != nil {
		return agentcore.AgentResult{}, err
	}
	// F2 pre-run estimate: worst-case cost before execution.
	if loop.Budget != nil {
		est := r.o.estimateRunCost(provider, selectedModel, loop, taskPrompt)
		if err := loop.Budget.CheckEstimate(est); err != nil {
			msg := "budget preflight: " + err.Error()
			if !r.silent {
				r.o.emit(agentcore.NewEvent(nil, role, agentcore.EvError, agentcore.StreamError{Message: msg}))
			}
			return agentcore.AgentResult{Role: string(role), OK: false}, errors.New(msg)
		}
		if est > 0 && !r.silent {
			r.o.emit(agentcore.NewEvent(nil, role, agentcore.EvHITL, agentcore.HITLItem{
				ID: "budget-estimate", Item: fmt.Sprintf("estimated cost $%.4f", est), Status: "done",
			}))
		}
	}
	loop.Harness = r.o.requireHarness || runtimebridge.Available()
	if role == agentcore.RoleOrchestrator && !r.noTools && !r.readOnly {
		loop.System = append(loop.System, agentcore.Message{Role: "system", Content: "Maestro owns the only agent harness. Use the built-in stipulate tool for spec-driven development: bootstrap, explore, contract and plan, validate, explicit user approval, start, delegate, check fresh evidence, docs, archive. Never fabricate approval or acceptance. Only the human can issue /workflow approve CHANGE --by NAME --ack-user-approval and archive. Worker completion is a contribution, not acceptance. Use RLM for bounded programmatic context work; Python requires approval. Preserve older specs and sessions; never silently translate an old approval to a new contract."})
	}
	return agentcore.RunResult(ctx, loop, taskPrompt)
}

func (o *Orchestrator) forwardRunnerEvent(ev agentcore.StreamEvent, role agentcore.Role, silent bool) {
	ev.Role = role
	if silent {
		o.accountSession(ev)
		return
	}
	o.emit(ev)
}

// estimateRunCost computes a conservative worst-case cost from the complete
// normalized request and the model's output cap.
func (o *Orchestrator) estimateRunCost(p agentcore.Provider, modelID string, loop *agentcore.Loop, taskPrompt string) float64 {
	if loop == nil || loop.Budget == nil {
		return 0
	}
	m, ok := o.registry.Model(modelID)
	if !ok || m.PriceInput <= 0 && m.PriceOutput <= 0 {
		return 0
	}
	specs := make([]agentcore.ToolSpec, 0, len(loop.Tools))
	for _, tool := range loop.Tools {
		specs = append(specs, tool.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	messages := append([]agentcore.Message(nil), loop.History...)
	messages = append(messages, agentcore.Message{Role: "user", Content: taskPrompt})
	req := agentcore.Request{
		Model: loop.Model, System: loop.System, Messages: messages,
		Sampling: loop.Sampling, Tools: specs,
	}
	plan, err := agentcore.PlanContext(req, loop.ContextWindow, m.DefaultMaxTokens)
	if err != nil {
		return 0
	}
	usage := agentcore.Usage{InputTokens: plan.EstimatedInputTokens, OutputTokens: plan.ReservedOutputTokens}
	cost, err := p.Cost(req, usage)
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
	return agentcore.AgentResult{}, errors.New("external harness execution has been removed")
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
