package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/mcp"
)

func TestDispatchMCPStatusReconnectAndToolsAreTerminalSafe(t *testing.T) {
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
				"name": "lookup", "description": "safe\n\x1b[31mdescription",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()

	dir := newTestRepo(t)
	var out strings.Builder
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: []config.Mcp{
			{
				Name: "records", Type: "http", URL: server.URL,
				Headers: []string{"Authorization Bearer super-secret"},
			},
			{Name: "offline", Type: "http", URL: "http://127.0.0.1:1/mcp"},
		}},
		Runner: &fakeRunner{}, In: strings.NewReader(""), Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := orch.Dispatch(t.Context(), Command{Cmd: "mcp", Args: []string{"status"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "records · http · 0 tool(s) · disconnected") {
		t.Fatalf("status output = %q", out.String())
	}
	out.Reset()
	if err := orch.Dispatch(t.Context(), Command{Cmd: "mcp", Args: []string{"reconnect", "records"}}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Dispatch(t.Context(), Command{Cmd: "mcp", Args: []string{"tools", "records"}}); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"reconnected records", "mcp__records__lookup", "approval required", "description"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, "super-secret") || strings.Contains(output, "\x1b") || strings.Contains(output, "required · safe\n") {
		t.Fatalf("terminal/secret data leaked: %q", output)
	}
	if err := orch.Dispatch(t.Context(), Command{Cmd: "mcp", Args: []string{"wat"}}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("unknown subcommand error = %v", err)
	}
}
