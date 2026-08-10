package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

// PromptGate is the headless HITL gate: every tool call asks on the REPL's
// stdin/stdout and waits for y/N. The TUI replaces it with the permission
// queue in B4.
type PromptGate struct {
	In  io.Reader
	Out io.Writer
}

// policyGate applies maestrorc permission rules before the interactive gate.
// The last matching rule wins, matching the config merge precedence. A deny
// can never be bypassed by a TUI "yolo" mode; an allow intentionally skips
// the prompt for that tool.
type policyGate struct {
	next  agentcore.Gate
	rules []config.PermissionRule
}

func (g *policyGate) Authorize(ctx context.Context, call agentcore.ToolCall, spec agentcore.ToolSpec) error {
	action := ""
	for _, rule := range g.rules {
		for _, token := range rule.Tools {
			if permissionMatches(token, spec.Name) {
				action = rule.Action
			}
		}
	}
	switch action {
	case "deny":
		return fmt.Errorf("permission denied by maestrorc for tool %q", spec.Name)
	case "allow":
		return nil
	}
	return g.next.Authorize(ctx, call, spec)
}

func permissionMatches(token, tool string) bool {
	if token == "*" || token == tool {
		return true
	}
	switch token {
	case "view":
		return tool == "read" || tool == "grep"
	case "edit":
		return tool == "write"
	case "admin":
		return tool == "bash"
	default:
		return false
	}
}

func (o *Orchestrator) permissionGate(next agentcore.Gate) agentcore.Gate {
	if next == nil {
		next = agentcore.GateFunc(agentcore.AllowAll)
	}
	if o.cfg == nil || len(o.cfg.Permissions) == 0 {
		return next
	}
	return &policyGate{next: next, rules: append([]config.PermissionRule(nil), o.cfg.Permissions...)}
}

// Authorize asks the human before a tool runs. NeedsApproval tools always
// ask; read-only tools pass through.
func (g *PromptGate) Authorize(ctx context.Context, call agentcore.ToolCall, spec agentcore.ToolSpec) error {
	if !spec.NeedsApproval {
		return nil
	}
	fmt.Fprintf(g.Out, "Allow %s(%s)? [y/N] ", call.Name, call.Args)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	reader := bufio.NewReader(g.In)
	line, err := reader.ReadString('\n')
	if err != nil && !strings.EqualFold(strings.TrimSpace(line), "y") {
		return fmt.Errorf("not approved")
	}
	if strings.EqualFold(strings.TrimSpace(line), "y") {
		return nil
	}
	return fmt.Errorf("not approved")
}
