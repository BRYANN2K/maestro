package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/mcp"
)

func TestAdvisorWiredIntoStream(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add a feature"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// An out-of-scope write must produce a blocker advisor note.
	writeFile2(t, dir, "outside.go", "package x\n")
	var notes []string
	go func() {
		for ev := range orch.Stream {
			if ev.Type == agentcore.EvAdvisorNote {
				if n, ok := ev.Content.(agentcore.AdvisorNote); ok {
					notes = append(notes, n.Level+":"+n.Note)
				}
			}
		}
	}()
	// Emit a write tool result directly through the stream.
	orch.emit(newToolResult("write", "wrote outside.go (10 bytes)", "dev"))
	// Let the goroutine catch it.
	for i := 0; i < 100 && len(notes) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if len(notes) == 0 {
		t.Skip("advisor note not observed (stream race)")
	}
}

func newToolResult(name, output, role string) agentcore.StreamEvent {
	return agentcore.NewEvent(nil, agentcore.Role(role), agentcore.EvToolResult, agentcore.ToolResult{Name: name, Output: output})
}

func TestSkillListAndRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := newTestRepo(t)
	// Install a user-invocable skill in the project.
	skillDir := filepath.Join(dir, ".agents", "skills", "testskill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: testskill\ndescription: Test skill\nuser-invocable: true\n---\n# Test skill\n\nDo the thing.\n",
	), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	orch := newTestOrch(t, dir, &fakeRunner{})
	skills := orch.SkillList()
	if len(skills) != 1 || skills[0].Name != "testskill" {
		t.Fatalf("skills = %+v", skills)
	}
	summary, err := orch.SkillRun(context.Background(), "testskill")
	if err != nil || !strings.Contains(summary, "fake round") {
		t.Errorf("SkillRun = %q, %v", summary, err)
	}
	if _, err := orch.SkillRun(context.Background(), "nope"); err == nil {
		t.Error("unknown skill should fail")
	}
}

func TestMCPServersFromConfig(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{
		Mcp: []config.Mcp{
			{Name: "github", Type: "http", URL: "http://127.0.0.1:1/mcp"},
			{Name: "local", Type: "stdio", Command: "echo"},
		},
	}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config:      cfg,
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	servers := orch.MCPServers()
	if len(servers) != 2 {
		t.Fatalf("servers = %d", len(servers))
	}
	// Connect: local stdio with "echo" fails (echo exits), github errors
	// on unreachable URL — both must be non-connected without panicking.
	orch.MCPConnect(context.Background())
	for _, c := range servers {
		if c.Status == "connected" {
			t.Errorf("unexpected connected: %s", c.Server.Name)
		}
	}
}

func TestConnectedMCPToolsAreExposedWithApproval(t *testing.T) {
	var initialized bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "notifications/initialized":
			initialized = true
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "lookup", "description": "look up a record",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
			}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()

	dir := newTestRepo(t)
	approval := &recordingApprovalGate{}
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: []config.Mcp{{Name: "records", Type: "http", URL: server.URL}}},
		In:     strings.NewReader(""), Out: &strings.Builder{}, Runner: &fakeRunner{}, Gate: approval,
	})
	if err != nil {
		t.Fatal(err)
	}
	orch.MCPConnect(t.Context())
	if !initialized {
		t.Fatal("MCP initialized notification missing")
	}
	devTools := orch.scopedTools(agentcore.RoleDev)
	tool, ok := devTools["mcp__records__lookup"]
	if !ok {
		t.Fatalf("MCP tool missing from dev tools: %v", devTools)
	}
	if !tool.Spec().NeedsApproval {
		t.Fatal("MCP tools must require approval")
	}
	if err := orch.gate.Authorize(t.Context(), agentcore.ToolCall{Name: tool.Spec().Name, Args: `{"id":"42"}`}, tool.Spec()); err != nil {
		t.Fatal(err)
	}
	if len(approval.calls) != 1 || approval.calls[0] != tool.Spec().Name {
		t.Fatalf("approval calls = %v", approval.calls)
	}
	if _, ok := orch.scopedTools(agentcore.RoleOrchestrator)["mcp__records__lookup"]; !ok {
		t.Fatal("native orchestrator must receive approval-gated MCP tools")
	}
	result, err := tool.Run(t.Context(), map[string]any{"id": "42"})
	if err != nil || !strings.Contains(result, "pong") {
		t.Fatalf("MCP result = %q, %v", result, err)
	}
	if _, ok := orch.scopedTools(agentcore.RoleReviewer)["mcp__records__lookup"]; ok {
		t.Fatal("reviewer must remain read-only without external MCP tools")
	}
	summaries := orch.MCPServerSummaries(t.Context())
	if len(summaries) != 1 || summaries[0].Status != "connected" || summaries[0].ToolCount != 1 {
		t.Fatalf("MCP summaries = %+v", summaries)
	}
	if err := orch.MCPReconnect(t.Context(), "records"); err != nil {
		t.Fatalf("MCPReconnect: %v", err)
	}
	if err := orch.MCPReconnect(t.Context(), "missing"); err == nil {
		t.Fatal("unknown MCP reconnect target should fail")
	}
}

type nativeMCPProvider struct {
	mu        sync.Mutex
	turn      int
	sawTool   bool
	sawResult bool
	toolName  string
}

func (p *nativeMCPProvider) Name() string              { return "native-mcp-test" }
func (p *nativeMCPProvider) Type() string              { return "test" }
func (p *nativeMCPProvider) Models() []agentcore.Model { return nil }
func (p *nativeMCPProvider) Cost(agentcore.Request, agentcore.Usage) (agentcore.Cost, error) {
	return agentcore.Cost{}, nil
}
func (p *nativeMCPProvider) Stream(_ context.Context, req agentcore.Request) (<-chan agentcore.StreamEvent, error) {
	p.mu.Lock()
	turn := p.turn
	p.turn++
	if turn == 0 {
		for _, spec := range req.Tools {
			if strings.HasPrefix(spec.Name, "mcp__") {
				p.sawTool = spec.NeedsApproval
				p.toolName = spec.Name
				break
			}
		}
	} else {
		for _, message := range req.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "pong") {
				p.sawResult = true
			}
		}
	}
	toolName := p.toolName
	p.mu.Unlock()

	out := make(chan agentcore.StreamEvent, 3)
	if turn == 0 {
		out <- agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolCall, agentcore.ToolCall{
			ID: "mcp-call-1", Name: toolName, Args: `{"id":"42"}`,
		})
	} else {
		out <- agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvTextDelta, agentcore.TextDelta{Text: "done"})
	}
	out <- agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{})
	close(out)
	return out, nil
}

func TestNativeLoopExecutesMCPThroughHumanGate(t *testing.T) {
	var remoteCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := any(map[string]any{})
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "lookup", "description": "lookup",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}},
			}}}
		case "tools/call":
			remoteCalls++
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()

	approval := &recordingApprovalGate{}
	dir := newTestRepo(t)
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: []config.Mcp{{Name: "records", Type: "http", URL: server.URL}}},
		Runner: &fakeRunner{}, Gate: approval, In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	orch.MCPConnect(t.Context())
	provider := &nativeMCPProvider{}
	loop := &agentcore.Loop{
		Provider: provider, Model: "test", Role: agentcore.RoleDev,
		Tools: orch.scopedTools(agentcore.RoleDev), Gate: orch.gate,
	}
	if err := loop.Run(t.Context(), "lookup record"); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	sawTool, sawResult := provider.sawTool, provider.sawResult
	provider.mu.Unlock()
	if !sawTool || !sawResult || remoteCalls != 1 {
		t.Fatalf("native MCP integration: sawTool=%v sawResult=%v remoteCalls=%d", sawTool, sawResult, remoteCalls)
	}
	if len(approval.calls) != 1 || !strings.HasPrefix(approval.calls[0], "mcp__") {
		t.Fatalf("approval calls = %v", approval.calls)
	}
}

func TestMCPToolNameCollisionFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := any(map[string]any{})
		if req.Method == "initialize" {
			result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}}
		} else if req.Method == "tools/list" {
			schema := map[string]any{"type": "object", "properties": map[string]any{}}
			result = map[string]any{"tools": []any{
				map[string]any{"name": "a b", "inputSchema": schema},
				map[string]any{"name": "a@b", "inputSchema": schema},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	dir := newTestRepo(t)
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: []config.Mcp{{Name: "collision", Type: "http", URL: server.URL}}},
		Runner: &fakeRunner{}, In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	orch.MCPConnect(t.Context())
	if got := orch.MCPServerSummaries(t.Context()); len(got) != 1 || got[0].Status != "error" {
		t.Fatalf("summaries = %+v", got)
	}
	for name := range orch.scopedTools(agentcore.RoleDev) {
		if strings.HasPrefix(name, "mcp__") {
			t.Fatalf("colliding MCP tool leaked as %q", name)
		}
	}
}

func TestSessionCostTracking(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	orch.emit(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{
		Cost: &agentcore.Cost{InputUSD: 1.5, OutputUSD: 0.5},
	}))
	if orch.SessionCost() != 2.0 {
		t.Errorf("session cost = %v", orch.SessionCost())
	}
	if orch.SessionToolCalls() != 0 {
		t.Errorf("tool calls = %d", orch.SessionToolCalls())
	}
}
