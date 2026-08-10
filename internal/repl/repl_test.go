package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/mcp"
	"github.com/bryann2k/maestro/internal/spec"
)

func runRepl(t *testing.T, dir, input string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Dir:         dir,
		In:          strings.NewReader(input),
		Out:         &out,
		SessionsDir: filepath.Join(dir, ".maestro-test-sessions"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}

func TestRunSpecListEmpty(t *testing.T) {
	out := runRepl(t, t.TempDir(), "/spec list\n/quit\n")
	if !strings.Contains(out, "No specs yet") {
		t.Errorf("output = %q, want empty-state message", out)
	}
}

func TestRunQuitAndEOF(t *testing.T) {
	out := runRepl(t, t.TempDir(), "/quit\n")
	if !strings.Contains(out, "Maestro v1") {
		t.Errorf("banner missing: %q", out)
	}
	runRepl(t, t.TempDir(), "")
}

func TestRunUnknownCommand(t *testing.T) {
	out := runRepl(t, t.TempDir(), "/bogus\n/quit\n")
	if !strings.Contains(out, "unknown command") {
		t.Errorf("output = %q", out)
	}
}

func TestRunHelp(t *testing.T) {
	out := runRepl(t, t.TempDir(), "/help\n/quit\n")
	if !strings.Contains(out, "/propose") || !strings.Contains(out, "/archive") {
		t.Errorf("help output = %q", out)
	}
	for _, command := range []string{"/bootstrap", "/onboard", "/rename <title>", "/resume [id]", "/git list|create|select", "/learn guided|challenge|off", "/learn <path> [--deep]", "/skills list|show", "/skills run <id>", "/mcp list|status", "/mcp tools [server|all]", "/mcp reconnect <server|all>"} {
		if !strings.Contains(out, command) {
			t.Errorf("help missing %q: %q", command, out)
		}
	}
	if strings.Contains(out, "/boostrap") {
		t.Errorf("help exposes the compatibility typo alias: %q", out)
	}
}

func TestRunMCPStatusReconnectAndTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "lookup", "description": "lookup records",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": result,
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Dir: dir,
		In: strings.NewReader(strings.Join([]string{
			"/mcp status",
			"/mcp reconnect records",
			"/mcp tools records",
			"/quit",
		}, "\n") + "\n"),
		Out:         &out,
		SessionsDir: filepath.Join(dir, ".maestro-test-sessions"),
		Config: &config.Config{Mcp: []config.Mcp{{
			Name: "records", Type: "http", URL: server.URL,
			Headers: []string{"Authorization Bearer repl-secret"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"records · http · 0 tool(s) · disconnected",
		"mcp: reconnected records",
		"mcp__records__lookup",
		"approval required",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, server.URL) || strings.Contains(output, "repl-secret") || strings.Contains(output, "\x1b") {
		t.Fatalf("REPL leaked MCP configuration/control bytes: %q", output)
	}
}

func TestProjectQuestionnairesRedirectToTUIWithoutWriting(t *testing.T) {
	for _, command := range []string{"/bootstrap", "/boostrap", "/onboard"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			out := runRepl(t, dir, command+"\n/quit\n")
			if !strings.Contains(out, "maestro tui") {
				t.Fatalf("output = %q", out)
			}
			if _, err := os.Stat(filepath.Join(dir, "MAESTRO.md")); !os.IsNotExist(err) {
				t.Fatalf("%s unexpectedly wrote MAESTRO.md: %v", command, err)
			}
		})
	}
}

func TestSessionRenameAndCoachCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAESTRO_LEARN_DIR", filepath.Join(t.TempDir(), "learn"))
	out := runRepl(t, dir, strings.Join([]string{
		"/rename Guided learning session",
		"/resume",
		"/learn guided",
		"/learn next",
		"/learn done",
		"/learn status",
		"/quit",
	}, "\n")+"\n")
	for _, want := range []string{
		"session renamed: Guided learning session",
		`title "Guided learning session"`,
		"coach: mode guided",
		"coach lesson ",
		"coach: completed ",
		"coach: mode guided · 1 completed lesson(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestRunChatUnconfigured(t *testing.T) {
	out := runRepl(t, t.TempDir(), "hello there\n/quit\n")
	if !strings.Contains(out, "no provider configured") {
		t.Errorf("output = %q", out)
	}
}

func TestRunProposeWorksWithoutProvider(t *testing.T) {
	dir := t.TempDir()
	out := runRepl(t, dir, "/propose -m=hello-world\n/quit\n")
	if !strings.Contains(out, "Spec proposal ready") || !strings.Contains(out, "hello-world") {
		t.Errorf("output = %q", out)
	}
}

func TestRunSpecShowMissing(t *testing.T) {
	out := runRepl(t, t.TempDir(), "/spec show missing-spec\n/quit\n")
	if !strings.Contains(out, "no such file") {
		t.Errorf("output = %q", out)
	}
}

func TestRunOnceWithoutConfig(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Dir:         t.TempDir(),
		In:          strings.NewReader(""),
		Out:         &out,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Once:        "hi",
	})
	if err == nil {
		t.Fatal("one-shot without config should fail")
	}
	if !strings.Contains(err.Error(), "no provider configured") {
		t.Errorf("err = %v", err)
	}
}

func TestRunSpecShowRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := spec.NewStore(filepath.Join(dir, "specs"))
	s := &spec.Spec{ID: "demo-spec", Title: "Demo", Status: spec.StatusProposal, Body: "Goal: demo.\n"}
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out := runRepl(t, dir, "/spec show demo-spec\n/quit\n")
	if !strings.Contains(out, "Goal: demo") {
		t.Errorf("output = %q", out)
	}
}

func TestREPLRenderingNeutralizesTerminalControlsWithoutChangingEvents(t *testing.T) {
	raw := "Next: inspect\n```text\n\x1b]52;c;YXR0YWNr\a\u202E\n```"
	event := agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: raw})
	var out bytes.Buffer
	printEvent(&out, event)
	got := out.String()
	if strings.ContainsAny(got, "\x1b\a\u202e") {
		t.Fatalf("REPL emitted terminal control payload: %q", got)
	}
	for _, want := range []string{"Next: inspect", "```text", "<U+001B>", "<U+0007>", "<U+202E>"} {
		if !strings.Contains(got, want) {
			t.Errorf("safe REPL output missing %q: %q", want, got)
		}
	}
	if delta := event.Content.(agentcore.TextDelta); delta.Text != raw {
		t.Fatalf("terminal rendering mutated the machine event: %q", delta.Text)
	}
}

func TestREPLLearningErrorsAreActionableAndUnknownErrorsStayFactual(t *testing.T) {
	var out bytes.Buffer
	printREPLError(&out, errors.New("learn source: open missing.go\x1b[31m: no such file"))
	got := out.String()
	if !strings.HasPrefix(got, "Cause: learn source:") || !strings.Contains(got, "\nFix: Choose a readable") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("learn error output = %q", got)
	}
	out.Reset()
	printREPLError(&out, errors.New("unexpected provider failure"))
	if got := out.String(); got != "error: unexpected provider failure\n" {
		t.Fatalf("unknown error presentation = %q", got)
	}
}
