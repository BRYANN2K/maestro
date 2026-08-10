package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHelperMCPProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MCP_HELPER_MODE")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == 0 {
			continue // initialized notification
		}
		result := any(map[string]any{})
		switch req.Method {
		case "initialize":
			if mode == "protocolsecret" {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32000, "message": os.Args[0] + " " + os.Getenv("MCP_CONFIG_SECRET")},
				})
				continue
			}
			result = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			}
		case "tools/list":
			if mode == "malformed" {
				_, _ = fmt.Fprintln(os.Stderr, "stderr-super-secret")
				_, _ = fmt.Fprintln(os.Stdout, "not-json")
				continue
			}
			if mode == "oversized" {
				_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", maxResponseBytes+1))
				continue
			}
			cursor, _ := req.Params["cursor"].(string)
			toolName := "echo"
			page := map[string]any{}
			if mode == "pagination" && cursor == "" {
				toolName = "alpha"
				page["nextCursor"] = "page-2"
			} else if mode == "pagination" {
				toolName = "omega"
			}
			page["tools"] = []any{testToolDefinition(toolName, false)}
			result = page
		case "tools/call":
			if mode == "treehang" {
				child := exec.Command("sleep", "600")
				if child.Start() == nil {
					_ = os.WriteFile(os.Getenv("MCP_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
				}
				time.Sleep(10 * time.Minute)
			}
			if mode == "escapedtreehang" {
				child := exec.Command(os.Args[0], "-test.run=^TestEscapedMCPProcessHelper$")
				child.Env = append(os.Environ(), "GO_WANT_ESCAPED_MCP_HELPER=1")
				if child.Start() != nil {
					os.Exit(2)
				}
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					if info, statErr := os.Stat(os.Getenv("MCP_CHILD_PID_FILE")); statErr == nil && info.Size() > 0 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				time.Sleep(10 * time.Minute)
			}
			if mode == "hang" {
				time.Sleep(10 * time.Minute)
			}
			args, _ := req.Params["arguments"].(map[string]any)
			message, _ := args["message"].(string)
			if failed, _ := args["fail"].(bool); failed {
				result = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "try another value"}},
					"isError": true,
				}
			} else if message == "bad-output" {
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": message}},
					"structuredContent": map[string]any{"reply": 42},
				}
			} else {
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": message}},
					"structuredContent": map[string]any{"reply": message},
				}
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func helperClient(t *testing.T, mode string) *Client {
	t.Helper()
	client := helperClientUnconnected(t, mode)
	if err := client.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return client
}

func helperClientUnconnected(t *testing.T, mode string) *Client {
	t.Helper()
	t.Setenv("GO_WANT_MCP_HELPER", "1")
	t.Setenv("MCP_HELPER_MODE", mode)
	command := fmt.Sprintf("%q -test.run=^TestHelperMCPProcess$", os.Args[0])
	client := New(Server{Name: "fake", Type: "stdio", Command: command, WorkDir: t.TempDir()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func testToolDefinition(name string, output bool) map[string]any {
	tool := map[string]any{
		"name": name, "description": "echo back",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "maxLength": 100},
				"fail":    map[string]any{"type": "boolean"},
			},
			"required": []any{"message"}, "additionalProperties": false,
		},
	}
	if output {
		tool["outputSchema"] = map[string]any{
			"type": "object", "properties": map[string]any{"reply": map[string]any{"type": "string"}},
			"required": []any{"reply"}, "additionalProperties": false,
		}
	}
	return tool
}

func TestStdioLifecycleSchemaAndSerialization(t *testing.T) {
	client := helperClient(t, "normal")
	client.mu.Lock()
	initialPID := client.cmd.Process.Pid
	client.mu.Unlock()
	if err := client.Connect(t.Context()); err != nil {
		t.Fatalf("idempotent Connect: %v", err)
	}
	client.mu.Lock()
	reconnectedPID := client.cmd.Process.Pid
	client.mu.Unlock()
	if reconnectedPID != initialPID {
		t.Fatalf("idempotent Connect replaced process %d with %d", initialPID, reconnectedPID)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
	if err := tools[0].inputValidator.validate(map[string]any{"message": 42}); err == nil {
		t.Fatal("schema must reject invalid arguments")
	}

	const calls = 12
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			message := fmt.Sprintf("value-%d", i)
			result, err := client.CallTool(t.Context(), "echo", map[string]any{"message": message})
			if err != nil {
				errs <- err
				return
			}
			if got := result.StructuredContent["reply"]; got != message {
				errs <- fmt.Errorf("reply = %v, want %q", got, message)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := client.Snapshot(); got.Status != "connected" || got.ToolCount != 1 || got.ProtocolVersion != ProtocolVersion {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestStdioPagination(t *testing.T) {
	client := helperClient(t, "pagination")
	tools, err := client.ListTools(t.Context())
	if err != nil || len(tools) != 2 || tools[0].Name != "alpha" || tools[1].Name != "omega" {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
}

func TestStdioCancellationKillsBlockedServer(t *testing.T) {
	client := helperClient(t, "hang")
	if _, err := client.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := client.CallTool(ctx, "echo", map[string]any{"message": "hello"})
	if !errorsIsDeadline(err) {
		t.Fatalf("CallTool error = %v, want deadline", err)
	}
	if got := client.Snapshot(); got.Status != "error" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestStdioCancellationKillsProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group assertion is Unix-specific")
	}
	pidFile := t.TempDir() + "/child.pid"
	t.Setenv("MCP_CHILD_PID_FILE", pidFile)
	client := helperClient(t, "treehang")
	if _, err := client.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _ = client.CallTool(ctx, "echo", map[string]any{"message": "hello"})
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper child pid: %v", err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 {
		t.Fatalf("child pid = %q, %v", raw, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("kill", "-0", strconv.Itoa(pid)).Run() != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("MCP grandchild process %d survived cancellation", pid)
}

func errorsIsDeadline(err error) bool {
	return err == context.DeadlineExceeded || err != nil && strings.Contains(err.Error(), "deadline exceeded")
}

func TestStdioRejectsMalformedAndOversizedFramesWithoutStderrLeak(t *testing.T) {
	for _, mode := range []string{"malformed", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			client := helperClient(t, mode)
			_, err := client.ListTools(t.Context())
			if err == nil {
				t.Fatal("ListTools unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), "stderr-super-secret") || strings.Contains(client.Snapshot().Error, "stderr-super-secret") {
				t.Fatalf("stderr leaked: %v / %+v", err, client.Snapshot())
			}
		})
	}
}

func TestSnapshotRedactsReflectedTransportConfiguration(t *testing.T) {
	t.Setenv("MCP_CONFIG_SECRET", "stdio-reflected-secret")
	stdio := helperClientUnconnected(t, "protocolsecret")
	stdio.Server.Token = "stdio-reflected-secret"
	if err := stdio.Connect(t.Context()); err == nil {
		t.Fatal("protocol error unexpectedly connected")
	}
	for _, forbidden := range []string{os.Args[0], "stdio-reflected-secret", stdio.Server.Command} {
		if forbidden != "" && strings.Contains(stdio.Snapshot().Error, forbidden) {
			t.Fatalf("stdio snapshot leaked %q: %+v", forbidden, stdio.Snapshot())
		}
	}

	const headerSecret = "header-reflected-secret"
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		message := endpoint + " " + r.Header.Get("Authorization") + " " + headerSecret
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32000, "message": message},
		})
	}))
	endpoint = server.URL + "/private?access_token=query-reflected-secret"
	defer server.Close()
	httpClient := New(Server{
		Name: "reflected", Type: "http", URL: endpoint,
		Headers: []string{"Authorization Bearer " + headerSecret},
	})
	if err := httpClient.Connect(t.Context()); err == nil {
		t.Fatal("HTTP protocol error unexpectedly connected")
	}
	for _, forbidden := range []string{endpoint, server.URL, headerSecret, "query-reflected-secret", "Bearer"} {
		if strings.Contains(httpClient.Snapshot().Error, forbidden) {
			t.Fatalf("HTTP snapshot leaked %q: %+v", forbidden, httpClient.Snapshot())
		}
	}
}

type httpMCPFixture struct {
	server      *httptest.Server
	initialized atomic.Bool
	deleted     atomic.Bool
	callCount   atomic.Int32
}

func newHTTPFixture(t *testing.T, outputSchema bool) *httpMCPFixture {
	t.Helper()
	fixture := &httpMCPFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			fixture.deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-123")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
				},
			})
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "session-123" || r.Header.Get("MCP-Protocol-Version") != ProtocolVersion {
			http.Error(w, "missing lifecycle headers", http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			fixture.initialized.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []any{testToolDefinition("lookup", outputSchema)}}
		case "tools/call":
			fixture.callCount.Add(1)
			args, _ := req.Params["arguments"].(map[string]any)
			message, _ := args["message"].(string)
			if failed, _ := args["fail"].(bool); failed {
				result = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "remote rejected value"}}, "isError": true,
				}
			} else if message == "bad-output" {
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": message}},
					"structuredContent": map[string]any{"reply": 42},
				}
			} else {
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": message}},
					"structuredContent": map[string]any{"reply": message},
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func TestHTTPLifecycleHeadersSchemasAndToolError(t *testing.T) {
	fixture := newHTTPFixture(t, true)
	client := New(Server{Name: "http", Type: "http", URL: fixture.server.URL})
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !fixture.initialized.Load() {
		t.Fatal("initialized notification was not received")
	}
	if _, err := client.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(t.Context(), "lookup", map[string]any{"message": 42}); err == nil {
		t.Fatal("invalid arguments must fail locally")
	}
	if fixture.callCount.Load() != 0 {
		t.Fatal("invalid arguments reached the remote server")
	}
	if _, err := client.CallTool(t.Context(), "lookup", map[string]any{"message": "bad-output"}); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("invalid structured output error = %v", err)
	}
	if _, err := client.CallTool(t.Context(), "lookup", map[string]any{"message": "x", "fail": true}); err == nil {
		t.Fatal("isError=true must surface as a failed tool call")
	} else if _, ok := err.(*ToolExecutionError); !ok || !strings.Contains(err.Error(), "remote rejected") {
		t.Fatalf("tool error = %T %v", err, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !fixture.deleted.Load() {
		t.Fatal("HTTP session DELETE was not sent")
	}
}

func TestHTTPFailedInitializationDeletesAllocatedSession(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			if r.Header.Get("Mcp-Session-Id") == "failed-session" {
				deleted.Store(true)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPost:
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Method == "initialize" {
				w.Header().Set("Mcp-Session-Id", "failed-session")
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"result": map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}},
				})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client := New(Server{Name: "failed-init", Type: "http", URL: server.URL})
	if err := client.Connect(t.Context()); err == nil {
		t.Fatal("initialized notification failure unexpectedly connected")
	}
	if !deleted.Load() {
		t.Fatal("failed initialization retained its allocated HTTP session")
	}
	if got := client.Snapshot(); got.Status != "error" || got.ToolCount != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestHTTPSSEReturnsBeforeStreamCloseAndTracksListChanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				"result": map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not flush")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
		result := map[string]any{"tools": []any{testToolDefinition("live", false)}}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		<-r.Context().Done() // proves the client closes after its matching event
	}))
	defer server.Close()

	client := New(Server{Name: "sse", Type: "http", URL: server.URL})
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	tools, err := client.ListTools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("SSE response waited for stream close: %s", elapsed)
	}
	if _, err := client.CallTool(t.Context(), "live", map[string]any{"message": "x"}); err == nil || !strings.Contains(err.Error(), "catalog changed") {
		t.Fatalf("CallTool after list_changed = %v", err)
	}
}

func TestHTTPDoesNotFollowRedirectWithAuthorization(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	client := New(Server{Name: "redirect", Type: "http", URL: redirect.URL, Token: "super-secret"})
	if err := client.Connect(t.Context()); err == nil {
		t.Fatal("redirecting endpoint unexpectedly connected")
	}
	if leaked.Load() {
		t.Fatal("redirect target was contacted")
	}
	if strings.Contains(client.Snapshot().Error, "super-secret") {
		t.Fatal("token leaked through status")
	}
}

func TestHTTPConnectHonorsShorterCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := New(Server{Name: "blocked", Type: "http", URL: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := client.Connect(ctx)
	if !errorsIsDeadline(err) {
		t.Fatalf("Connect error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("unavailable MCP blocked for %s", elapsed)
	}
}

func TestSchemaRejectsExternalReferenceAndBounds(t *testing.T) {
	_, err := compileToolSchema("input", map[string]any{"type": "object", "properties": map[string]any{
		"value": map[string]any{"$ref": "https://attacker.test/schema.json"},
	}})
	if err == nil || !strings.Contains(err.Error(), "external references") {
		t.Fatalf("external ref error = %v", err)
	}
	if _, err := splitCommand(`tool "unterminated`); err == nil {
		t.Fatal("unterminated command should fail")
	}
}

func TestRegistryDeterminismDuplicateAndUnknownTransport(t *testing.T) {
	registry := NewRegistry()
	registry.Add(Server{Name: "z", Type: "stdio", Command: "echo"})
	duplicate := registry.Add(Server{Name: "z", Type: "http", URL: "https://example.test"})
	registry.Add(Server{Name: "a", Type: "stdio", Command: "echo"})
	clients := registry.Clients()
	if len(clients) != 2 || clients[0].Server.Name != "a" || clients[1].Server.Name != "z" {
		t.Fatalf("clients = %+v", clients)
	}
	if duplicate.Snapshot().Status != "error" {
		t.Fatal("duplicate server name must fail closed")
	}
	unknown := New(Server{Name: "x", Type: "carrier-pigeon"})
	if err := unknown.Connect(t.Context()); err == nil {
		t.Fatal("unknown transport should fail")
	}
}

func TestOAuthContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := OAuthWithOpen(ctx, "client", "http://127.0.0.1:1/auth", "http://127.0.0.1:1/token", "", func(string) {})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled OAuth should error")
		}
	case <-time.After(time.Second):
		t.Fatal("OAuth did not abort on cancel")
	}
	if _, err := OAuth(t.Context(), "client", "a", "b", ""); err == nil || !strings.Contains(err.Error(), "callback") {
		t.Fatal("legacy OAuth entrypoint must fail closed without a UI callback")
	}
}
