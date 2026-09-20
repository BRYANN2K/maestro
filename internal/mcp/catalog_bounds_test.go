package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeToolRejectsPathologicalSchemaAtPerSchemaBoundary(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"name": "pathological",
		"inputSchema": map[string]any{
			"type":        "object",
			"description": strings.Repeat("x", maxSchemaBytes),
			"properties":  map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTool(raw); err == nil || !strings.Contains(err.Error(), "schema exceeds") {
		t.Fatalf("decodeTool error = %v, want explicit per-schema byte limit", err)
	}
}

func TestListToolsRejectsAggregateSchemaOverflowAtomically(t *testing.T) {
	var pathological atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			if !pathological.Load() {
				result = map[string]any{"tools": []any{boundedSchemaTool("seed", "")}}
				break
			}
			padding := strings.Repeat("x", maxSchemaBytes/2)
			start, end := 0, 4
			page := map[string]any{"nextCursor": "overflow-page"}
			if req.Params["cursor"] == "overflow-page" {
				start, end = 4, 9
				page = map[string]any{}
			}
			tools := make([]any, 0, end-start)
			for i := start; i < end; i++ {
				tools = append(tools, boundedSchemaTool(string(rune('a'+i)), padding))
			}
			page["tools"] = tools
			result = page
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()

	client := New(Server{Name: "bounded", Type: "http", URL: server.URL})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if tools, err := client.ListTools(t.Context()); err != nil || len(tools) != 1 {
		t.Fatalf("initial ListTools = %d tools, %v", len(tools), err)
	}

	pathological.Store(true)
	client.markToolsDirty()
	if tools, err := client.ListTools(t.Context()); err == nil || !strings.Contains(err.Error(), "schemas exceed") || tools != nil {
		t.Fatalf("overflow ListTools = %v, %v", tools, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.tools) != 1 || client.tools["seed"].Name != "seed" {
		t.Fatalf("failed refresh published a partial catalog: %+v", client.tools)
	}
}

func TestConcurrentListToolsReusesSingleDiscovery(t *testing.T) {
	var listCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
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
			result = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			listCalls.Add(1)
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			result = map[string]any{"tools": []any{boundedSchemaTool("shared", "")}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()

	client := New(Server{Name: "concurrent", Type: "http", URL: server.URL})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tools, err := client.ListTools(context.Background())
			if err == nil && (len(tools) != 1 || tools[0].Name != "shared") {
				err = &unexpectedToolCatalogError{tools: tools}
			}
			errs <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent tools/list discovery did not start")
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	waitStart := time.Now()
	if _, err := client.ListTools(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled discovery follower = %v, want deadline", err)
	}
	if elapsed := time.Since(waitStart); elapsed > 500*time.Millisecond {
		t.Fatalf("cancelled discovery follower waited %s", elapsed)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("tools/list calls = %d, want one shared discovery", got)
	}
}

func TestCloseWakesListToolsFollowersAndPreventsLatePublication(t *testing.T) {
	var listCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
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
			result = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			listCalls.Add(1)
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			result = map[string]any{"tools": []any{boundedSchemaTool("late", "")}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()

	client := New(Server{Name: "closing", Type: "http", URL: server.URL})
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(context.Background())
		leaderDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("leader tools/list did not start")
	}
	followerDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(context.Background())
		followerDone <- err
	}()

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-followerDone:
		if err == nil {
			t.Fatal("ListTools follower succeeded after Close")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close left a ListTools follower blocked")
	}
	select {
	case err := <-leaderDone:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("late ListTools leader = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not cancel the active ListTools request")
	}
	close(release)
	if got := client.Snapshot(); got.ToolCount != 0 || got.Status != "disconnected" {
		t.Fatalf("late discovery republished after Close: %+v", got)
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("tools/list calls = %d, want one leader", got)
	}
}

func TestListToolsNeverPublishesCatalogInvalidatedByListChanged(t *testing.T) {
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
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				},
			})
			return
		}

		call := listCalls.Add(1)
		if call == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"tools\":[{\"name\":\"stale\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n", req.ID)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"tools": []any{boundedSchemaTool("fresh", "")}},
		})
	}))
	defer server.Close()

	client := New(Server{Name: "changing", Type: "http", URL: server.URL})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if tools, err := client.ListTools(t.Context()); err == nil || !strings.Contains(err.Error(), "catalog invalidated") || tools != nil {
		t.Fatalf("stale ListTools = %+v, %v", tools, err)
	}
	if snapshot := client.Snapshot(); snapshot.ToolCount != 0 {
		t.Fatalf("stale catalog was published: %+v", snapshot)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil || len(tools) != 1 || tools[0].Name != "fresh" {
		t.Fatalf("fresh ListTools = %+v, %v", tools, err)
	}
	if got := listCalls.Load(); got != 2 {
		t.Fatalf("tools/list calls = %d, want stale attempt plus refresh", got)
	}
}

type unexpectedToolCatalogError struct{ tools []Tool }

func (e *unexpectedToolCatalogError) Error() string { return "unexpected concurrent MCP tool catalog" }

func boundedSchemaTool(name, padding string) map[string]any {
	return map[string]any{
		"name": name,
		"inputSchema": map[string]any{
			"type": "object", "description": padding, "properties": map[string]any{},
		},
	}
}
