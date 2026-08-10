package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// Runner executes a delegated task for a role and returns its typed yield.
type Runner interface {
	Run(ctx context.Context, role agentcore.Role, taskPrompt string) (agentcore.AgentResult, error)
}

// BuildOptions controls a /build invocation.
type BuildOptions struct {
	Engine          string // native | subscription; legacy remains a compatibility alias
	Agent           string // subscription CLI name
	Model           string // model override
	ReasoningEffort string // empty/auto lets the selected route decide
	Isolated        bool   // run in a dedicated git worktree (§11.3.2)
}

// Build launches the dev sub-agent on the active spec's trio.
func (o *Orchestrator) Build(ctx context.Context, opts BuildOptions) error {
	if o.spec == nil {
		return errors.New("build: no active spec (propose + accept first)")
	}
	from := o.sess.Phase
	if from != session.PhaseSpec && from != session.PhaseReview {
		return fmt.Errorf("build: cannot start from phase %q", from)
	}
	if err := o.validateSessionWorkspaceIdentity(ctx, "build"); err != nil {
		return err
	}
	if err := o.ensureSpecContract(); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	pendingFloor := append([]bool(nil), o.sess.SpecContract.TaskStates...)
	blocked, err := o.pendingBlockingHITLItems(ctx)
	if err != nil {
		return fmt.Errorf("build: inspect human-action gates: %w", err)
	}
	if len(blocked) > 0 {
		labels := make([]string, 0, len(blocked))
		for _, item := range blocked {
			labels = append(labels, item.Item)
		}
		return fmt.Errorf("build: blocked by human action(s): %s", strings.Join(labels, "; "))
	}
	task, err := o.buildTaskPrompt()
	if err != nil {
		return err
	}
	runner, err := o.buildRunner(opts)
	if err != nil {
		return err
	}
	// F4: a build must never start without a recoverable snapshot of the
	// pre-build code and session.
	if err := o.Checkpoint(ctx); err != nil {
		return fmt.Errorf("build checkpoint: %w", err)
	}

	// Any new implementation round invalidates the previous release gate.
	o.sess.Review = nil
	if err := o.resetHITLStatus("diff"); err != nil {
		return fmt.Errorf("build: reset diff review: %w", err)
	}
	if err := o.setPhase(session.PhaseBuild); err != nil {
		return err
	}
	o.emitPhase(from, session.PhaseBuild)
	o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "running", Detail: o.spec.ID}))

	ctx, cancel := o.bindBudgetKill(ctx)
	defer cancel()
	res, err := runner.Run(ctx, agentcore.RoleDev, task)
	if err == nil && !res.OK {
		err = unsuccessfulAgentResult("build", res)
	}
	if err != nil {
		o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "error", Detail: err.Error()}))
		return o.rollbackAgentRun(from, err)
	}
	if _, err := o.validateDevSpecContractProgress(pendingFloor); err != nil {
		contractErr := fmt.Errorf("build: rejected dev changes: %w", err)
		o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "error", Detail: contractErr.Error()}))
		return o.rollbackAgentRun(from, contractErr)
	}
	detail := fmt.Sprintf("%s · $%.4f · %s", o.spec.ID, res.CostUSD, res.Duration)
	o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "done", Detail: detail}))
	o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{ID: "yield", Name: "dev", Output: res.Summary}))
	done, total := o.taskProgress(ctx)
	fmt.Fprintf(o.out, "\nDev complete. %d tasks done, %d total. %s\n", done, total, res.Summary)
	fmt.Fprintln(o.out, "  What's next?  /review | /accept (manual) | /chat")
	o.selfReview(ctx, res.Summary)
	return nil
}

// Fix replays the last review's findings back to the dev sub-agent.
func (o *Orchestrator) Fix(ctx context.Context) error {
	if o.sess.Phase != session.PhaseReview && !(o.sess.Phase == session.PhaseBuild && o.sess.Review != nil && o.sess.Review.Level == "fail") {
		return errors.New("fix: run /review first")
	}
	if err := o.validateSessionWorkspaceIdentity(ctx, "fix"); err != nil {
		return err
	}
	pendingState, err := o.validatePendingSpecContract()
	if err != nil {
		return fmt.Errorf("fix: %w", err)
	}
	findings := ""
	if o.sess.Review != nil {
		findings = o.sess.Review.Findings
	}
	// Legacy/restored review sessions may predate persisted verdicts. Refresh
	// the gate once so their findings are not silently lost.
	if findings == "" && o.sess.Phase == session.PhaseReview {
		verdict, err := o.Review(ctx)
		if err != nil {
			var failed *ReviewFailedError
			if !errors.As(err, &failed) {
				return err
			}
		}
		findings = verdict.Findings()
	}
	if len(findings) == 0 {
		fmt.Fprintln(o.out, "No findings to fix.")
		return nil
	}
	task, err := o.buildTaskPrompt()
	if err != nil {
		return err
	}
	task += "\n\n## Reviewer findings to fix\n\n" + findings
	runner, err := o.buildRunner(BuildOptions{})
	if err != nil {
		return err
	}
	from := o.sess.Phase
	if err := o.Checkpoint(ctx); err != nil {
		return fmt.Errorf("fix checkpoint: %w", err)
	}
	o.sess.Review = nil
	if err := o.resetHITLStatus("diff"); err != nil {
		return fmt.Errorf("fix: reset diff review: %w", err)
	}
	if err := o.setPhase(session.PhaseBuild); err != nil {
		return err
	}
	o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "running", Detail: "fix round"}))
	ctx, cancel := o.bindBudgetKill(ctx)
	defer cancel()
	res, err := runner.Run(ctx, agentcore.RoleDev, task)
	if err == nil && !res.OK {
		err = unsuccessfulAgentResult("fix", res)
	}
	if err != nil {
		o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "error", Detail: "fix round · " + err.Error()}))
		return o.rollbackAgentRun(from, err)
	}
	if _, err := o.validateDevSpecContractProgress(pendingState.taskStates); err != nil {
		contractErr := fmt.Errorf("fix: rejected dev changes: %w", err)
		o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "error", Detail: contractErr.Error()}))
		return o.rollbackAgentRun(from, contractErr)
	}
	o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "done", Detail: "fix round · " + res.Summary}))
	fmt.Fprintln(o.out, "\nFix round complete.")
	return nil
}

func unsuccessfulAgentResult(operation string, result agentcore.AgentResult) error {
	message := strings.TrimSpace(result.Summary)
	if message == "" {
		message = "sub-agent returned an unsuccessful result"
	}
	return fmt.Errorf("%s: %w", operation, agentcore.StreamError{Message: message})
}

// rollbackAgentRun restores the durable pre-run phase after a sub-agent
// failure. This is a rollback, not a user-driven state transition: build → spec
// is deliberately absent from the forward phase graph.
func (o *Orchestrator) rollbackAgentRun(from session.Phase, runErr error) error {
	current := o.sess.Phase
	o.sess.Phase = from
	if err := o.save(); err != nil {
		return errors.Join(runErr, fmt.Errorf("restore phase %q after failed agent run: %w", from, err))
	}
	if current != from {
		o.emitPhase(current, from)
	}
	return runErr
}

// buildTaskPrompt assembles spec + design + tasks into the dev prompt.
func (o *Orchestrator) buildTaskPrompt() (string, error) {
	specPath := o.store.PathFor(o.spec.ID, spec.FileSpec)
	designPath := o.store.PathFor(o.spec.ID, spec.FileDesign)
	tasksPath := o.store.PathFor(o.spec.ID, spec.FileTasks)
	specContent, err := os.ReadFile(specPath)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	design, err := os.ReadFile(designPath)
	if err != nil {
		design = []byte("(no design.md yet)")
	}
	tasks, err := os.ReadFile(tasksPath)
	if err != nil {
		tasks = []byte("(no tasks.md yet)")
	}
	return o.maestroTaskPrompt(fmt.Sprintf(`MAESTRO_OPERATION: BUILD_AUTHORIZED

You are the dev sub-agent for Maestro. Implement the spec below.
Follow the project's Go conventions. Write tests with your code. Never modify
spec.md or design.md. After a task is implemented and verified, update only its
checkbox in tasks.md from [ ] to [x]; never rewrite task text or mark an
unverified task complete.

=== SPEC (%s) ===
%s

=== DESIGN (%s) ===
%s

=== TASKS (%s) ===
%s
`, specPath, specContent, designPath, design, tasksPath, tasks)), nil
}

// taskProgress counts [x] vs total checkboxes in tasks.md.
func (o *Orchestrator) taskProgress(ctx context.Context) (done, total int) {
	data, err := os.ReadFile(o.store.PathFor(o.spec.ID, spec.FileTasks))
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "- [x]"):
			done++
			total++
		case strings.HasPrefix(line, "- [ ]"):
			total++
		}
	}
	return done, total
}

// buildRunner resolves the engine: an injected runner wins (tests), then
// explicit legacy/native, then settings. Isolation wraps whichever runner
// was chosen.
func (o *Orchestrator) buildRunner(opts BuildOptions) (Runner, error) {
	var r Runner
	snapshot := o.SettingsSnapshot()
	switch {
	case o.runner != nil:
		r = o.runner
	default:
		engine := opts.Engine
		if engine == "" {
			engine = snapshot.RoleDefaults["dev"].Engine
		}
		if engine == "" {
			engine = "native"
		}
		engine = normalizeEngineName(engine)
		switch engine {
		case "legacy":
			name := opts.Agent
			if name == "" {
				name = snapshot.RoleDefaults["dev"].Agent
			}
			if name == "" {
				name = "codex"
			}
			a, err := agent.Create(name)
			if err != nil {
				return nil, err
			}
			o.rememberEngine("dev", "legacy", name)
			model := opts.Model
			reasoningEffort := opts.ReasoningEffort
			if model == "" {
				model = snapshot.RoleDefaults["dev"].Model
			}
			if reasoningEffort == "" {
				reasoningEffort = snapshot.RoleDefaults["dev"].ReasoningEffort
			}
			if !containsReasoningEffort(o.ReasoningEfforts("legacy", name, model), reasoningEffort) {
				reasoningEffort = ""
			}
			r = &legacyRunner{agent: a, model: model, reasoningEffort: reasoningEffort, o: o}
		case "native":
			o.rememberEngine("dev", "native", "")
			route := o.effectiveRoleRoute("dev")
			sampling := o.effectiveNativeSampling("dev", route)
			reasoningEffort := agentcore.NormalizeReasoningEffort(opts.ReasoningEffort)
			if opts.ReasoningEffort != "" {
				sampling.ReasoningEffort = reasoningEffort
			} else {
				reasoningEffort = sampling.ReasoningEffort
			}
			model := opts.Model
			if model == "" {
				model = route.Model
			}
			if !containsReasoningEffort(o.ReasoningEfforts("native", "", model), reasoningEffort) {
				if opts.ReasoningEffort != "" {
					return nil, fmt.Errorf("build: reasoning effort %q is unsupported by model %q", opts.ReasoningEffort, model)
				}
				reasoningEffort, sampling.ReasoningEffort = "", ""
			}
			r = &nativeRunner{
				o: o, model: model, reasoningEffort: reasoningEffort,
				sampling: sampling, samplingSet: true,
			}
		default:
			return nil, fmt.Errorf("build: unknown engine %q", engine)
		}
	}
	if opts.Isolated {
		return o.isolatedRunner(r)
	}
	return r, nil
}
