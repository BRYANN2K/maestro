package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/learn"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/proposals"
)

func newNativeLearnTestModel(t *testing.T, source string) (*Model, string) {
	t.Helper()
	explanation, err := json.Marshal(learn.Explanation{
		HighLevel: "This file declares the executable entry point.",
		Blocks: []learn.Block{{
			Start: 1, End: 3, Code: strings.TrimSuffix(source, "\n"),
			What: "It defines an empty main function.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, marshalErr := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": string(explanation)}}},
		})
		if marshalErr != nil {
			t.Errorf("marshal native Learn response: %v", marshalErr)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.Providers = []config.Provider{{
		Name: "learn-native", Type: "openai-compat", BaseURL: server.URL, APIKey: "test-key",
	}}
	cfg.Models = []config.Model{{
		ID: "learn-native/learn-model", Name: "Learn Test", ContextWindow: 32_768,
	}}
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir: project, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: cfg, In: strings.NewReader(""), Out: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	orch.SetModel("learn-native/learn-model")
	store := proposals.NewProposalStore(filepath.Join(project, ".proposals"))
	return New(orch, store, nil), orch.WorkDirDisplay()
}

func TestNativeLearnRendersOnlyValidatedCardNotProtocolJSON(t *testing.T) {
	const source = "package main\n\nfunc main() {}\n"
	explanation, err := json.Marshal(learn.Explanation{
		HighLevel: "This file declares the executable entry point.",
		Blocks: []learn.Block{{
			Start: 1, End: 3, Code: strings.TrimSuffix(source, "\n"),
			What: "It defines an empty main function.",
		}},
		FollowUps: []string{"Read the main function body next."},
	})
	if err != nil {
		t.Fatal(err)
	}

	var mcpRequests atomic.Int32
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mcpRequests.Add(1)
		http.Error(w, "Learn must not contact MCP", http.StatusInternalServerError)
	}))
	defer mcpServer.Close()

	var requestMu sync.Mutex
	var requestTools string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if decodeErr := json.NewDecoder(request.Body).Decode(&body); decodeErr != nil {
			t.Errorf("decode native Learn request: %v", decodeErr)
			return
		}
		requestMu.Lock()
		if tools, exists := body["tools"]; exists {
			toolsJSON, marshalErr := json.Marshal(tools)
			if marshalErr != nil {
				requestMu.Unlock()
				t.Errorf("marshal native Learn tools: %v", marshalErr)
				return
			}
			requestTools = string(toolsJSON)
		} else {
			requestTools = "<absent>"
		}
		requestMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		middle := len(explanation) / 2
		for _, chunk := range [][]byte{explanation[:middle], explanation[middle:]} {
			payload, marshalErr := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": string(chunk)}}},
			})
			if marshalErr != nil {
				t.Errorf("marshal SSE payload: %v", marshalErr)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.Providers = []config.Provider{{
		Name: "learn-native", Type: "openai-compat", BaseURL: server.URL, APIKey: "test-key",
	}}
	cfg.Models = []config.Model{{
		ID: "learn-native/learn-model", Name: "Learn Test", ContextWindow: 32_768,
	}}
	cfg.Mcp = []config.Mcp{{Name: "learn-probe", Type: "http", URL: mcpServer.URL}}
	var commandOutput bytes.Buffer
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir: project, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: cfg, In: strings.NewReader(""), Out: &commandOutput,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	orch.SetModel("learn-native/learn-model")
	store := proposals.NewProposalStore(filepath.Join(project, ".proposals"))
	m := New(orch, store, nil)

	done := primaryBatchMessage(t, m.runLearn("main.go", false))
	// The progress shell is created synchronously by /learn; model protocol
	// events remain entirely private and cannot race the worker completion.
	if last := m.lastAssistant(); last == nil || last.think == nil ||
		!m.busy || !last.busy || last.think.Role != "coach" ||
		last.think.Status != "running" || last.think.Detail != "generating a focused lesson" {
		t.Fatalf("Learn progress is not visible in the transcript: %+v", last)
	}
	select {
	case event := <-orch.Stream:
		t.Fatalf("private Learn protocol leaked as %s: %#v", event.Type, event.Content)
	default:
	}
	requestMu.Lock()
	tools := requestTools
	requestMu.Unlock()
	if tools != "<absent>" {
		t.Fatalf("native Learn request exposed tools; want no tools field, got %s", tools)
	}
	if got := mcpRequests.Load(); got != 0 {
		t.Fatalf("native Learn contacted MCP %d time(s), want zero", got)
	}
	m.Update(done)

	for _, message := range m.messages {
		if strings.Contains(message.Text, `"high_level"`) || strings.Contains(message.Text, `"blocks"`) {
			t.Fatalf("private Learn JSON rendered in transcript: %q", message.Text)
		}
	}
	if len(m.pending) != 1 || m.pending[0].Proposal == nil {
		t.Fatalf("validated Learn card missing: %+v", m.pending)
	}
	if last := m.lastAssistant(); last == nil || last.busy || last.think == nil || last.think.Status != "done" {
		t.Fatalf("Learn progress did not resolve with the card: %+v", last)
	}
	var staged strings.Builder
	for _, hunk := range m.pending[0].Proposal.Hunks {
		staged.WriteString(strings.Join(hunk.NewLines, "\n"))
		staged.WriteByte('\n')
	}
	for _, want := range []string{"# Learn: main.go", "## Start here", "**Purpose:**", "## Next"} {
		if !strings.Contains(staged.String(), want) {
			t.Fatalf("Learn card lost accessible Markdown %q:\n%s", want, staged.String())
		}
	}
}
