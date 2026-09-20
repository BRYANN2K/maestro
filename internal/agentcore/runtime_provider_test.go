package agentcore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/runtimebridge"
)

func TestBundledProviderStreamsFromCompatibleEndpoint(t *testing.T) {
	if !runtimebridge.Available() {
		t.Skip("build runtime")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("request path=%s authPresent=%v", r.URL.Path, r.Header.Get("Authorization") != "")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	p := &RuntimeProvider{ProviderName: "fixture", BaseURL: server.URL, Key: "fixture-key"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events, err := p.Stream(ctx, Request{Model: "fixture", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	done := false
	for e := range events {
		switch v := e.Content.(type) {
		case TextDelta:
			text.WriteString(v.Text)
		case StreamError:
			t.Fatal(v.Message)
		case Done:
			done = true
			if len(v.ProviderState) == 0 {
				t.Fatal("lost provider state")
			}
		}
	}
	if text.String() != "hello" || !done {
		t.Fatalf("text=%s done=%v", text.String(), done)
	}
}
