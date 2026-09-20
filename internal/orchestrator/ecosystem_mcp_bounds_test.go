package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/mcp"
)

func TestCanceledMCPDiscoveryDoesNotPublishAndRetrySucceeds(t *testing.T) {
	firstListStarted := make(chan struct{})
	var firstStart sync.Once
	var listCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": mcp.ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
				},
			})
		case "tools/list":
			if listCalls.Add(1) == 1 {
				firstStart.Do(func() { close(firstListStarted) })
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []any{map[string]any{
					"name": "retry", "description": "available after cancellation",
					"inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{}, "additionalProperties": false,
					},
				}}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	orch, err := New(t.Context(), Options{
		ProjectDir: newTestRepo(t), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: []config.Mcp{{Name: "retryable", Type: "http", URL: server.URL}}},
		Runner: &fakeRunner{}, In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orch.Close() })

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- orch.connectMCP(firstCtx) }()
	select {
	case <-firstListStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first MCP tools/list request did not start")
	}
	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled discovery error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled MCP discovery did not release its completion state")
	}
	orch.eco.mcpMu.Lock()
	ready, busy, done := orch.eco.mcpReady, orch.eco.mcpBusy, orch.eco.mcpDone
	toolCount := len(orch.eco.mcpTools)
	orch.eco.mcpMu.Unlock()
	if ready || busy || done != nil || toolCount != 0 {
		t.Fatalf("canceled discovery published stale state: ready=%v busy=%v done=%v tools=%d", ready, busy, done != nil, toolCount)
	}

	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRetry()
	if err := orch.connectMCP(retryCtx); err != nil {
		t.Fatalf("retry after canceled discovery: %v", err)
	}
	orch.eco.mcpMu.Lock()
	ready, busy, done = orch.eco.mcpReady, orch.eco.mcpBusy, orch.eco.mcpDone
	toolCount = len(orch.eco.mcpTools)
	orch.eco.mcpMu.Unlock()
	if !ready || busy || done != nil || toolCount != 1 || listCalls.Load() != 2 {
		t.Fatalf("retry state: ready=%v busy=%v done=%v tools=%d lists=%d", ready, busy, done != nil, toolCount, listCalls.Load())
	}
}

func TestMCPConnectPoolAndCloseCancellationAreBounded(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, mcp.MaxConfiguredServers)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "initialize" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()

	configured := make([]config.Mcp, 12)
	for i := range configured {
		configured[i] = config.Mcp{Name: fmt.Sprintf("blocked-%02d", i), Type: "http", URL: server.URL}
	}
	orch, err := New(t.Context(), Options{
		ProjectDir: newTestRepo(t), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: configured}, Runner: &fakeRunner{},
		In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}

	connectDone := make(chan error, 1)
	go func() { connectDone <- orch.connectMCP(context.Background()) }()
	for i := 0; i < mcpDiscoveryConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d bounded connect requests started", i)
		}
	}
	select {
	case <-started:
		t.Fatal("MCP Connect exceeded the four-worker pool")
	case <-time.After(100 * time.Millisecond):
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- orch.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel active MCP discovery")
	}
	select {
	case <-connectDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("MCP discovery survived orchestrator Close")
	}
	if got := peak.Load(); got > mcpDiscoveryConcurrency {
		t.Fatalf("peak MCP Connect concurrency = %d, want <= %d", got, mcpDiscoveryConcurrency)
	}
	if err := orch.connectMCP(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("MCP discovery restarted after Close: %v", err)
	}
}

func TestMCPNativeCatalogBudgetsAreDeterministicVisibleAndConcurrent(t *testing.T) {
	const toolsPerServer = 6
	tools := make([]any, 0, toolsPerServer)
	for i := 0; i < toolsPerServer; i++ {
		tools = append(tools, map[string]any{
			"name":        "tool-" + string(rune('a'+i)),
			"description": "bounded",
			"inputSchema": map[string]any{
				"type":        "object",
				"description": strings.Repeat("s", 12<<10),
				"properties":  map[string]any{},
			},
		})
	}

	bInitialized := make(chan struct{})
	aListStarted := make(chan struct{})
	aListRelease := make(chan struct{})
	aServer, aListCalls := newCatalogBudgetServer(t, tools, bInitialized, nil, aListStarted, aListRelease)
	bServer, bListCalls := newCatalogBudgetServer(t, tools, nil, bInitialized, nil, nil)

	dir := newTestRepo(t)
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		// Reverse configuration order and make b finish initialization first.
		// Admission must still start with server a.
		Config: &config.Config{Mcp: []config.Mcp{
			{Name: "b", Type: "http", URL: bServer.URL},
			{Name: "a", Type: "http", URL: aServer.URL},
		}},
		Runner: &fakeRunner{}, In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orch.Close() })

	firstDone := make(chan error, 1)
	go func() { firstDone <- orch.connectMCP(t.Context()) }()
	select {
	case <-aListStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("server a tools/list did not start")
	}
	listDeadline := time.Now().Add(500 * time.Millisecond)
	for bListCalls.Load() == 0 && time.Now().Before(listDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if bListCalls.Load() != 1 {
		t.Fatal("sorted discovery batch did not fetch server b concurrently")
	}
	if got := orch.connectedMCPTools(); len(got) != 0 {
		t.Fatalf("partial catalog became visible during discovery: %d tools", len(got))
	}

	const concurrentCallers = 24
	var wg sync.WaitGroup
	concurrentErrs := make(chan error, concurrentCallers)
	for range concurrentCallers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			concurrentErrs <- orch.connectMCP(t.Context())
			_ = orch.MCPServerSummaries(t.Context())
			_ = orch.MCPToolSummaries(t.Context(), "all")
		}()
	}
	wg.Wait()
	close(concurrentErrs)
	for err := range concurrentErrs {
		if err != nil {
			t.Fatalf("concurrent connectMCP = %v, want non-blocking shared discovery", err)
		}
	}
	close(aListRelease)
	select {
	case err := <-firstDone:
		if err == nil || !strings.Contains(err.Error(), "native catalog omitted") {
			t.Fatalf("connectMCP error = %v, want visible catalog omission", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded MCP discovery did not finish")
	}

	exposed := orch.connectedMCPTools()
	if len(exposed) == 0 || len(exposed) >= toolsPerServer*2 {
		t.Fatalf("bounded catalog size = %d", len(exposed))
	}
	specs := make([]agentcore.ToolSpec, 0, len(exposed))
	for _, tool := range exposed {
		spec := tool.Spec()
		if !strings.HasPrefix(spec.Name, "mcp__a__") {
			t.Fatalf("nondeterministic admission exposed %q before server a", spec.Name)
		}
		specs = append(specs, spec)
	}
	encodedCatalog, err := json.Marshal(specs)
	if err != nil || !json.Valid(encodedCatalog) {
		t.Fatalf("invalid admitted ToolSpec catalog JSON: %v", err)
	}
	if len(encodedCatalog) > maxMCPNativeCatalogBytes {
		t.Fatalf("native MCP catalog = %d bytes, limit %d", len(encodedCatalog), maxMCPNativeCatalogBytes)
	}

	summaries := orch.MCPServerSummaries(t.Context())
	if len(summaries) != 2 {
		t.Fatalf("MCP summaries = %+v", summaries)
	}
	omitted, retained := 0, 0
	for _, summary := range summaries {
		omitted += summary.OmittedTools
		retained += summary.ToolCount
		if summary.OmittedTools > 0 && (summary.OmissionReason == "" || !strings.Contains(summary.Error, "native catalog omitted")) {
			t.Fatalf("omission is not visible in status: %+v", summary)
		}
	}
	if retained != len(exposed) || retained+omitted != toolsPerServer*2 {
		t.Fatalf("catalog accounting retained=%d omitted=%d exposed=%d", retained, omitted, len(exposed))
	}
	if got := aListCalls.Load(); got != 1 {
		t.Fatalf("server a tools/list calls = %d, want 1", got)
	}
	if got := bListCalls.Load(); got != 1 {
		t.Fatalf("server b tools/list calls = %d, want 1", got)
	}
}

func newCatalogBudgetServer(
	t *testing.T,
	tools []any,
	initializeWait <-chan struct{},
	initializeSignal chan<- struct{},
	listStarted chan<- struct{},
	listRelease <-chan struct{},
) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	listCalls := &atomic.Int32{}
	var initializeOnce, listOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			if initializeWait != nil {
				select {
				case <-initializeWait:
				case <-r.Context().Done():
					return
				}
			}
			if initializeSignal != nil {
				initializeOnce.Do(func() { close(initializeSignal) })
			}
			result = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			listCalls.Add(1)
			if listStarted != nil {
				listOnce.Do(func() { close(listStarted) })
			}
			if listRelease != nil {
				select {
				case <-listRelease:
				case <-r.Context().Done():
					return
				}
			}
			result = map[string]any{"tools": tools}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server, listCalls
}
