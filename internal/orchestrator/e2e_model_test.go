package orchestrator

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
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

// stubKeys satisfies agentcore.KeyStore with a fixed key.
type stubKeys struct{}

func (stubKeys) Key(name string) (string, bool) { return "test-key", true }

// TestChatSendsBareModelID is the end-to-end regression for the reported
// bug: picking "opencode/deepseek-v4-flash-free" from the remote catalog
// must POST model "deepseek-v4-flash-free" to the provider API — never the
// qualified selection handle (opencode sends api.id, the old opencode sent
// APIModel).
func TestChatSendsBareModelID(t *testing.T) {
	var srvURL string // assigned after the server is created
	var gotModel string
	var gotMessages []any
	var gotTools []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"opencode":{"id":"opencode","name":"OpenCode","env":["OPENCODE_API_KEY"],"api":%q,"models":{"deepseek-v4-flash-free":{"id":"deepseek-v4-flash-free","name":"DeepSeek V4 Flash Free"}}}}`, srvURL)
		default: // chat completions
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotModel, _ = body["model"].(string)
			gotMessages, _ = body["messages"].([]any)
			gotTools, _ = body["tools"].([]any)
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

	var out bytes.Buffer
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &out,
		Keys:        stubKeys{},
		Config:      &config.Config{}, // non-nil so the registry builds from the catalog
		ModelsDev:   agentcore.NewModelsDev(agentcore.ModelsDevOptions{URL: srv.URL + "/api.json", Disabled: false}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.SetModel("opencode/deepseek-v4-flash-free")
	if err := orch.ModelCheckError(); err != nil {
		t.Fatalf("preflight rejected the catalog model: %v", err)
	}
	if err := orch.Chat(context.Background(), "hello"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotModel != "deepseek-v4-flash-free" {
		t.Fatalf("API received model %q, want the bare id", gotModel)
	}
	encodedMessages, _ := json.Marshal(gotMessages)
	for _, required := range []string{"MAESTRO_OPERATION: CHAT", "PROPOSE_AUTHORIZED", "read-only discovery"} {
		if !strings.Contains(string(encodedMessages), required) {
			t.Errorf("API messages missing %q: %s", required, encodedMessages)
		}
	}
	encodedTools, _ := json.Marshal(gotTools)
	if strings.Contains(string(encodedTools), `"name":"write"`) || strings.Contains(string(encodedTools), `"name":"bash"`) {
		t.Fatalf("chat exposed mutating tools: %s", encodedTools)
	}
}

func TestChatSendsBareModelIDForQualifiedCustomConfig(t *testing.T) {
	testChatSendsConfiguredAPIModelID(t, "smoke-native/smoke-model", "smoke-model")
}

func TestChatPreservesNamespacedAPIModelIDAfterProviderPrefix(t *testing.T) {
	testChatSendsConfiguredAPIModelID(t,
		"smoke-native/accounts/acme/models/smoke-model",
		"accounts/acme/models/smoke-model",
	)
}

func testChatSendsConfiguredAPIModelID(t *testing.T, selectionHandle, wantAPIModel string) {
	t.Helper()
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: "smoke-native", Type: "openai-compat", BaseURL: srv.URL, APIKey: "test-key",
		}},
		Models: []config.Model{{
			ID: selectionHandle, Name: "Smoke Native", ContextWindow: 32768,
		}},
	}
	orch, err := New(context.Background(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.SetModel(selectionHandle)
	if err := orch.ModelCheckError(); err != nil {
		t.Fatalf("preflight rejected qualified custom model: %v", err)
	}
	if err := orch.Chat(context.Background(), "hello"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotModel != wantAPIModel {
		t.Fatalf("API received model %q, want %q", gotModel, wantAPIModel)
	}
}

func TestChatNeverFallsBackFromMissingQualifiedProvider(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "one", Type: "openai-compat", BaseURL: "https://one.invalid/v1", Disabled: true},
			{Name: "two", Type: "openai-compat", BaseURL: srv.URL, APIKey: "provider-two-key"},
		},
		Models: []config.Model{{ID: "one/shared"}},
	}
	orch, err := New(context.Background(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.SetModel("one/shared")
	if err := orch.Chat(context.Background(), "must not leave the process"); err == nil || !strings.Contains(err.Error(), `provider "one"`) {
		t.Fatalf("Chat missing-provider error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("qualified model was sent to unrelated provider: %d request(s)", requests)
	}
}
