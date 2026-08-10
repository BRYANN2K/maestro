package orchestrator

import (
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

func TestMCPPermissionRuleCannotBypassConfiguredDeny(t *testing.T) {
	next := &recordingApprovalGate{}
	o := &Orchestrator{cfg: &config.Config{Permissions: []config.PermissionRule{{
		Action: "deny", Tools: []string{"mcp__records__delete"},
	}}}}
	spec := agentcore.ToolSpec{Name: "mcp__records__delete", NeedsApproval: true}
	err := o.permissionGate(next).Authorize(t.Context(), agentcore.ToolCall{Name: spec.Name, Args: `{}`}, spec)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Authorize error = %v", err)
	}
	if len(next.calls) != 0 {
		t.Fatalf("configured deny reached interactive gate: %v", next.calls)
	}
}
