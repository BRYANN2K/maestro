package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

func TestContextUsage(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"opencode":{"id":"opencode","name":"OpenCode","env":["OPENCODE_API_KEY"],"api":%q,"models":{"deepseek-v4-flash-free":{"id":"deepseek-v4-flash-free","name":"DeepSeek V4 Flash Free","limit":{"context":64000,"output":16384}}}}}`, srvURL)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "test-key")

	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Keys:        stubKeys{},
		Config:      &config.Config{},
		ModelsDev:   agentcore.NewModelsDev(agentcore.ModelsDevOptions{URL: srvURL + "/api.json", Disabled: false}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.SetModel("opencode/deepseek-v4-flash-free")

	used, total := orch.ContextUsage()
	if total != 64000 {
		t.Errorf("total = %d, want 64000", total)
	}
	if used != 0 {
		t.Errorf("used = %d at start, want 0", used)
	}

	orch.trackSession(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{
		Usage: &agentcore.Usage{InputTokens: 100, OutputTokens: 50, CacheCreateTokens: 10, CacheHitTokens: 20},
	}))
	used, _ = orch.ContextUsage()
	if used != 180 {
		t.Errorf("used = %d, want 180", used)
	}

	orch.trackSession(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{
		Usage: &agentcore.Usage{InputTokens: 20, OutputTokens: 5, CacheHitTokens: 10},
	}))
	used, _ = orch.ContextUsage()
	if used != 35 {
		t.Errorf("used after next turn = %d, want latest context 35", used)
	}

	orch.SetModel("")
	_, total = orch.ContextUsage()
	if total != 0 {
		t.Errorf("total with unknown model = %d, want 0", total)
	}
}

func TestSilentRunnerEventsStillAccountWithoutPublishing(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.forwardRunnerEvent(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{
		Cost:  &agentcore.Cost{InputUSD: 0.5, OutputUSD: 0.25},
		Usage: &agentcore.Usage{InputTokens: 80, CacheHitTokens: 20, OutputTokens: 10},
	}), agentcore.RoleDocs, true)
	orch.forwardRunnerEvent(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvToolResult, agentcore.ToolResult{
		Name: "read", Output: "private result",
	}), agentcore.RoleDocs, true)

	if got := orch.SessionCost(); got != 0.75 {
		t.Fatalf("silent session cost = %v, want 0.75", got)
	}
	if got := orch.SessionToolCalls(); got != 1 {
		t.Fatalf("silent tool calls = %d, want 1", got)
	}
	if got, _ := orch.ContextUsage(); got != 0 {
		t.Fatalf("docs context leaked into orchestrator meter: %d", got)
	}
	select {
	case ev := <-orch.Stream:
		t.Fatalf("silent event leaked to public stream: %+v", ev)
	default:
	}
}
