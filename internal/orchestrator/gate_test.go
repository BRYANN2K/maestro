package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

type recordingApprovalGate struct{ calls []string }

func (g *recordingApprovalGate) Authorize(_ context.Context, call agentcore.ToolCall, _ agentcore.ToolSpec) error {
	g.calls = append(g.calls, call.Name)
	return nil
}

func TestPermissionGateEnforcesAllowDenyAndAliases(t *testing.T) {
	next := &recordingApprovalGate{}
	o := &Orchestrator{cfg: &config.Config{Permissions: []config.PermissionRule{
		{Action: "allow", Tools: []string{"view", "edit"}},
		{Action: "deny", Tools: []string{"bash"}},
	}}}
	g := o.permissionGate(next)

	for _, name := range []string{"read", "grep", "write"} {
		err := g.Authorize(t.Context(), agentcore.ToolCall{Name: name}, agentcore.ToolSpec{Name: name, NeedsApproval: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	err := g.Authorize(t.Context(), agentcore.ToolCall{Name: "bash"}, agentcore.ToolSpec{Name: "bash", NeedsApproval: true})
	if err == nil || !strings.Contains(err.Error(), "maestrorc") {
		t.Fatalf("bash error = %v, want policy denial", err)
	}
	if len(next.calls) != 0 {
		t.Fatalf("explicit rules delegated to prompt gate: %v", next.calls)
	}
}

func TestPermissionGateLastMatchingRuleWinsAndDelegatesUnknown(t *testing.T) {
	next := &recordingApprovalGate{}
	o := &Orchestrator{cfg: &config.Config{Permissions: []config.PermissionRule{
		{Action: "deny", Tools: []string{"write"}},
		{Action: "allow", Tools: []string{"write"}},
	}}}
	g := o.permissionGate(next)
	if err := g.Authorize(t.Context(), agentcore.ToolCall{Name: "write"}, agentcore.ToolSpec{Name: "write", NeedsApproval: true}); err != nil {
		t.Fatalf("last allow did not win: %v", err)
	}
	if err := g.Authorize(t.Context(), agentcore.ToolCall{Name: "custom"}, agentcore.ToolSpec{Name: "custom", NeedsApproval: true}); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if len(next.calls) != 1 || next.calls[0] != "custom" {
		t.Fatalf("delegated calls = %v", next.calls)
	}
}
