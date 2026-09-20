package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/runtimebridge"
	"github.com/bryann2k/maestro/internal/settings"
	"github.com/bryann2k/maestro/internal/workflow"
)

func TestStipulateBundledDelegationRequiresApprovalAndAcceptance(t *testing.T) {
	if !runtimebridge.Available() {
		t.Skip("build bundled Maestro runtime")
	}
	root := newTestRepo(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"{\"role\":\"dev\",\"ok\":true,\"summary\":\"Verified scoped fixture\"}"},"finish_reason":null}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "data: [DONE]")
		fmt.Fprintln(w)
	}))
	defer server.Close()
	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "fixture", Type: "openai-compat", BaseURL: server.URL}}
	cfg.Models = []config.Model{{ID: "fixture/coder", ContextWindow: 32000, DefaultMaxTokens: 1000}}
	o, err := New(t.Context(), Options{ProjectDir: root, SessionsDir: t.TempDir(), Config: cfg, Keys: mapKeyStore{"fixture": "test"}, Settings: settings.Defaults(), Model: "fixture/coder", RequireHarness: true, Gate: agentcore.GateFunc(func(context.Context, agentcore.ToolCall, agentcore.ToolSpec) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	run := func(args ...string) {
		t.Helper()
		if out, err := workflow.Run(t.Context(), root, args, nil); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	run("bootstrap")
	run("explore", "sample")
	change := filepath.Join(root, ".workflow", "changes", "sample")
	if err := os.WriteFile(filepath.Join(change, "spec.md"), []byte("# Example\n\n- AC-1: Verify the fixture.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := `{"version":1,"tasks":[{"id":"verify","role":"verification","phase":"check","criteria":["AC-1"],"depends_on":[],"write_paths":[],"objective":"Read the fixture and report."}]}`
	file := filepath.Join(root, "plan.json")
	os.WriteFile(file, []byte(plan), 0600)
	run("plan", "sample", "--file", file)
	input := map[string]any{"action": "delegate", "task": map[string]any{"change_id": "sample", "task_id": "verify"}}
	if _, err := o.workflowRuntime(t.Context(), input); err == nil {
		t.Fatal("unapproved worker launched")
	}
	if calls != 0 {
		t.Fatal("provider contacted before contract approval")
	}
	run("validate", "sample")
	run("approve", "sample", "--by", "test-user", "--ack-user-approval")
	run("start", "sample")
	result, err := o.workflowRuntime(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Jobs []struct{ Status, Acceptance string }
	}
	if err = json.Unmarshal(result, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].Status != "returned" || snapshot.Jobs[0].Acceptance != "pending" {
		t.Fatalf("completion confused with acceptance: %s", result)
	}
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
	if _, err = o.workflowRuntime(t.Context(), map[string]any{"action": "contribution", "change": "sample", "taskID": "verify", "decision": "accepted", "reason": "Reviewed actual fixture output"}); err != nil {
		t.Fatal(err)
	}
	if tools := o.workflowWorkerTools([]string{"src"}); tools["bash"] != nil || tools["rlm"] != nil || tools["stipulate"] != nil {
		t.Fatal("worker can bypass ownership")
	}
	tools := o.workflowWorkerTools([]string{"src"})
	if _, err = tools["write"].Run(t.Context(), map[string]any{"path": "outside.txt", "content": "bad"}); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("ownership escape=%v", err)
	}
}

func TestStipulateSnapshotWithoutConnectedModels(t *testing.T) {
	if !runtimebridge.Available() {
		t.Skip("build bundled Maestro runtime")
	}
	root := newTestRepo(t)
	o := newTestOrch(t, root, &fakeRunner{})
	defer o.Close()
	o.registry = &agentcore.Registry{}
	if _, err := workflow.Run(t.Context(), root, []string{"bootstrap"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Run(t.Context(), root, []string{"explore", "sample"}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := o.WorkflowSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(snapshot) || !strings.Contains(string(snapshot), `"available":true`) {
		t.Fatalf("snapshot=%s", snapshot)
	}
}
