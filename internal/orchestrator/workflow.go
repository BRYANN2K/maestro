package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/runtimebridge"
	"github.com/bryann2k/maestro/internal/workflow"
)

func (o *Orchestrator) dispatchWorkflow(ctx context.Context, cmd Command) error {
	args := cmd.Args
	if len(args) == 0 {
		args = []string{"panel"}
	}
	action := args[0]
	if action == "profiles" {
		input := map[string]any{"action": "snapshot"}
		if len(args) > 1 {
			if len(args) != 2 {
				return errors.New("workflow profiles [settings-request.json]")
			}
			f, err := os.Open(args[1])
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
			f.Close()
			if err != nil {
				return err
			}
			if len(data) > 1<<20 {
				return errors.New("settings request is too large")
			}
			var request struct {
				Scope    string
				Revision string
				Settings map[string]any
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			input = map[string]any{"action": "settings", "scope": request.Scope, "revision": request.Revision, "settings": request.Settings}
		}
		result, err := o.workflowRuntime(ctx, input)
		if err != nil {
			return err
		}
		fmt.Fprintln(o.out, string(result))
		return nil
	}
	if action == "panel" || action == "delegate" || action == "contribution" {
		input := map[string]any{"action": "snapshot"}
		switch action {
		case "panel":
			if len(args) > 1 {
				input["change"] = args[1]
			}
		case "delegate":
			if len(args) != 3 {
				return errors.New("workflow delegate <change> <task>")
			}
			input["action"] = "delegate"
			input["task"] = map[string]any{"change_id": args[1], "task_id": args[2]}
		case "contribution":
			if len(args) < 5 {
				return errors.New("workflow contribution <change> <task> accepted|rejected <reason>")
			}
			input["action"] = "contribution"
			input["change"] = args[1]
			input["taskID"] = args[2]
			input["decision"] = args[3]
			input["reason"] = strings.Join(args[4:], " ")
		}
		result, err := o.workflowRuntime(ctx, input)
		if err != nil {
			return err
		}
		fmt.Fprintln(o.out, string(result))
		return nil
	}
	result, err := workflow.Run(ctx, o.workDir(), args, o.in)
	if err != nil {
		return err
	}
	fmt.Fprintln(o.out, string(result))
	return nil
}

func (o *Orchestrator) workflowRuntime(ctx context.Context, input map[string]any) (json.RawMessage, error) {
	engine, cleanup, err := workflow.Materialize()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	route := o.effectiveRoleRoute(string(agentcore.RoleOrchestrator))
	model := route.Model
	provider, _ := o.registry.ProviderOf(model)
	parentID := "ses_" + o.sess.ID
	input["version"] = 1
	input["operation"] = "workflow"
	input["root"] = o.workDir()
	input["engine"] = engine
	parentModel := map[string]any{"id": o.canonicalModel(model), "providerID": provider}
	if route.ReasoningEffort != "" {
		parentModel["variant"] = route.ReasoningEffort
	}
	input["parent"] = map[string]any{"id": parentID, "model": parentModel}
	models := make([]any, 0)
	for _, info := range o.ProviderList(ctx) {
		p, ok := o.registry.Provider(info.Name)
		if !ok {
			continue
		}
		for _, m := range p.Models() {
			var variants []any
			for _, effort := range o.registry.ReasoningEfforts(info.Name + "/" + m.ID) {
				if effort != "auto" {
					variants = append(variants, map[string]any{"id": effort, "settings": map[string]any{"reasoningEffort": effort}})
				}
			}
			models = append(models, map[string]any{"providerID": info.Name, "id": m.ID, "name": m.Name, "enabled": !info.RequiresKey || info.KeySet, "capabilities": map[string]any{"tools": true}, "variants": variants})
		}
	}
	input["models"] = models
	var result json.RawMessage
	err = runtimebridge.Run(ctx, input, func(ctx context.Context, message runtimebridge.Message) (any, error) {
		switch message.Method {
		case "workflow_result":
			result = append(json.RawMessage(nil), message.Params...)
			return nil, nil
		case "worker":
			var job struct {
				Prompt    string
				SessionID string
				Job       struct {
					RunID      string   `json:"run_id"`
					TaskID     string   `json:"task_id"`
					WritePaths []string `json:"write_paths"`
					Profile    struct {
						Model struct {
							ProviderID string `json:"providerID"`
							ID         string `json:"id"`
							Variant    string `json:"variant"`
						}
						Effort string
					}
				}
			}
			if err := json.Unmarshal(message.Params, &job); err != nil {
				return nil, err
			}
			selected := job.Job.Profile.Model.ProviderID + "/" + job.Job.Profile.Model.ID
			effort := job.Job.Profile.Model.Variant
			worker := &nativeRunner{o: o, model: selected, reasoningEffort: effort, toolOverride: o.workflowWorkerTools(job.Job.WritePaths)}
			workerID := "ses_" + strings.ReplaceAll(job.Job.RunID, "-", "")
			if job.SessionID != "" {
				workerID = job.SessionID
			}
			o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: job.Job.TaskID, Status: "running", Detail: selected}))
			answer, runErr := worker.Run(ctx, agentcore.RoleDev, job.Prompt)
			outcome, status := "succeeded", "returned"
			detail := answer.Summary
			if !answer.OK && runErr == nil {
				runErr = errors.New(answer.Summary)
			}
			if runErr != nil {
				outcome, status = "failed", "failed"
				detail = runErr.Error()
			}
			o.emit(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: job.Job.TaskID, Status: status, Detail: detail}))
			return map[string]any{"id": workerID, "parentID": parentID, "model": job.Job.Profile.Model, "outcome": outcome, "execution_observed": true, "time": map[string]any{"idle": time.Now().UnixMilli()}, "output": detail}, nil
		default:
			return nil, fmt.Errorf("workflow: unexpected runtime method %q", message.Method)
		}
	})
	return result, err
}

// Shared-checkout workers have constrained file tools. Shell, arbitrary MCP,
// RLM code and lifecycle transitions are coordinator-owned capabilities.
func (o *Orchestrator) workflowWorkerTools(paths []string) map[string]agentcore.Tool {
	source := o.scopedTools(agentcore.RoleDev)
	out := map[string]agentcore.Tool{}
	for _, name := range []string{"read", "grep"} {
		if t, ok := source[name]; ok {
			out[name] = t
		}
	}
	if len(paths) > 0 {
		if t, ok := source["write"]; ok {
			out["write"] = agentcore.NewToolFunc(t.Spec(), func(ctx context.Context, args map[string]any) (string, error) {
				requested, _ := args["path"].(string)
				resolved, err := resolveWorkspacePath(o.workDir(), requested)
				if err != nil {
					return "", err
				}
				if err := rejectWorkflowSymlinks(o.workDir(), resolved); err != nil {
					return "", err
				}
				rel, err := filepath.Rel(o.workDir(), resolved)
				if err != nil {
					return "", err
				}
				rel = filepath.ToSlash(rel)
				if rel == ".workflow" || strings.HasPrefix(rel, ".workflow/") || rel == ".git" || strings.HasPrefix(rel, ".git/") {
					return "", errors.New("workers cannot mutate workflow or Git authority")
				}
				allowed := false
				for _, owned := range paths {
					owned = strings.TrimSuffix(filepath.ToSlash(owned), "/")
					if rel == owned || strings.HasPrefix(rel, owned+"/") {
						allowed = true
						break
					}
				}
				if !allowed {
					return "", fmt.Errorf("path %q is outside this task's approved write ownership", rel)
				}
				return t.Run(ctx, args)
			})
		}
	}
	return out
}

// WorkflowSnapshot is loaded asynchronously by the frontend. It reads the
// contract and reconciles receipts; opening a phase never executes it.
func (o *Orchestrator) WorkflowSnapshot(ctx context.Context) (json.RawMessage, error) {
	if !workflow.Initialized(o.workDir()) {
		return json.RawMessage(`{"available":false,"changes":[],"jobs":[]}`), nil
	}
	return o.workflowRuntime(ctx, map[string]any{"action": "snapshot"})
}

func (o *Orchestrator) WorkflowSnapshotFor(ctx context.Context, id string) (json.RawMessage, error) {
	if id == "" {
		return o.WorkflowSnapshot(ctx)
	}
	if !workflowSlug.MatchString(id) {
		return nil, errors.New("invalid change identifier")
	}
	return o.workflowRuntime(ctx, map[string]any{"action": "snapshot", "change": id})
}

// WorkflowArtifact reads a phase's reviewable document; it cannot advance the
// lifecycle. Missing evidence stays visibly missing.
func (o *Orchestrator) WorkflowArtifact(ctx context.Context, id string, phase int) (string, error) {
	if !workflowSlug.MatchString(id) {
		return "", errors.New("select a change first")
	}
	files := []string{"project.md", "proposal.md", "spec.md", "execution-plan.json", "evidence.md", "state.json", "state.json"}
	if phase < 0 || phase >= len(files) {
		return "", errors.New("invalid phase")
	}
	dir := filepath.Join(".workflow", "changes", id)
	if _, err := os.Stat(filepath.Join(o.workDir(), dir)); errors.Is(err, os.ErrNotExist) {
		dir = filepath.Join(".workflow", "archive", id)
	}
	path := filepath.Join(dir, files[phase])
	if phase == 0 {
		path = filepath.Join(".workflow", "project.md")
	}
	resolved, err := resolveWorkspacePath(o.workDir(), path)
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	f, err := openReadOnly(resolved)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("artifact must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(data) > 1<<20 {
		return "", errors.New("artifact exceeds 1 MiB preview limit")
	}
	return string(data), err
}

// Existing sessions remain readable; a Stipulate project has one contract authority.
func (o *Orchestrator) requireLegacyWorkflow() error {
	if workflow.Initialized(o.workDir()) {
		return errors.New("this project uses Stipulate; open /workflow and use /workflow commands for its approved contract")
	}
	return nil
}

// Ownership is lexical. Disallow aliases even when they stay within the project,
// otherwise an owned src/link could redirect a write into an unowned directory.
func rejectWorkflowSymlinks(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for p := target; p != root; p = filepath.Dir(p) {
		if filepath.Dir(p) == p {
			return errors.New("managed path does not belong to project")
		}
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed paths cannot use symlinks")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
