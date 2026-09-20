package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/workflow"
)

var workflowSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func (o *Orchestrator) workflowTool() agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name: "stipulate", NeedsApproval: true,
		Description: "Maestro's Stipulate contract authority. Inspect status/plan, bootstrap, explore, validate, start an approved change, record fresh check evidence, document it, or delegate an approved DAG task. action='engine' takes argv and optional stdin (for plan --file -); action='contract' writes proposal.md or spec.md in an existing change; action='delegate' takes change and task. Approval and archive are human commands only. Returned worker contributions are not accepted; action='contribution' records accepted/rejected with a review reason.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"engine", "contract", "delegate", "contribution"}},
			"stdin":  map[string]any{"type": "string", "description": "Plan JSON for engine plan --file -"},
			"argv":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"change": map[string]any{"type": "string"}, "task": map[string]any{"type": "string"},
			"file":    map[string]any{"type": "string", "enum": []string{"proposal.md", "spec.md"}},
			"content": map[string]any{"type": "string"}, "decision": map[string]any{"type": "string", "enum": []string{"accepted", "rejected"}}, "reason": map[string]any{"type": "string"},
		}, "required": []string{"action"}},
	}, func(ctx context.Context, args map[string]any) (string, error) {
		action, _ := args["action"].(string)
		change, _ := args["change"].(string)
		if action != "engine" && !workflowSlug.MatchString(change) {
			return "", errors.New("invalid change identifier")
		}
		switch action {
		case "engine":
			raw, ok := args["argv"].([]any)
			if !ok || len(raw) == 0 {
				return "", errors.New("argv is required")
			}
			argv := make([]string, len(raw))
			for i, v := range raw {
				var ok bool
				argv[i], ok = v.(string)
				if !ok {
					return "", errors.New("argv must contain strings")
				}
			}
			switch argv[0] {
			case "install-extensions", "bootstrap", "explore", "validate", "start", "status", "plan", "select", "check", "docs", "snapshot", "extensions":
			default:
				return "", errors.New("approval and archive require explicit human commands")
			}
			input, _ := args["stdin"].(string)
			if len(input) > 1<<20 {
				return "", errors.New("workflow input is too large")
			}
			data, err := workflow.Run(ctx, o.workDir(), argv, strings.NewReader(input))
			return string(data), err
		case "contract":
			name, _ := args["file"].(string)
			if name != "proposal.md" && name != "spec.md" {
				return "", errors.New("only proposal.md and spec.md are contract documents")
			}
			content, _ := args["content"].(string)
			if len(content) > 1<<20 {
				return "", errors.New("contract is too large")
			}
			path, err := resolveWorkspacePath(o.workDir(), filepath.Join(".workflow", "changes", change, name))
			if err != nil {
				return "", err
			}
			// The existing change must have been created by the workflow engine.
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "state.json")); err != nil {
				return "", err
			}
			// Managed contracts refuse symlinks, including aliases inside the root.
			if err := rejectWorkflowSymlinks(o.workDir(), path); err != nil {
				return "", err
			}

			f, err := os.CreateTemp(filepath.Dir(path), ".maestro-contract-")
			if err != nil {
				return "", err
			}
			tmp := f.Name()
			defer os.Remove(tmp)
			if _, err = f.WriteString(content); err != nil {
				f.Close()
				return "", err
			}
			if err = f.Close(); err != nil {
				return "", err
			}
			if err = os.Rename(tmp, path); err != nil {
				return "", err
			}
			return "Contract updated; validate it and obtain approval of this version before execution.", nil
		case "delegate", "contribution":
			task, _ := args["task"].(string)
			if !workflowSlug.MatchString(task) {
				return "", errors.New("invalid task identifier")
			}
			input := map[string]any{"action": action, "change": change, "taskID": task, "decision": args["decision"], "reason": args["reason"], "task": map[string]any{"change_id": change, "task_id": task}}
			result, err := o.workflowRuntime(ctx, input)
			return string(result), err
		}
		return "", fmt.Errorf("unknown Stipulate action %q", action)
	})
}
