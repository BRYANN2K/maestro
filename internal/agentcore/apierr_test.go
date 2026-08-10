package agentcore

import (
	"strings"
	"testing"
)

func TestParseAPIErrorOpenAI(t *testing.T) {
	body := []byte(`{"error":{"message":"model 'opencode/deepseek-v4-flash-free' is not supported","type":"invalid_request_error","code":"model_not_found"}}`)
	got := parseAPIError("400 Bad Request", body)
	if !strings.Contains(got, "400 Bad Request") {
		t.Errorf("missing status: %q", got)
	}
	if !strings.Contains(got, "is not supported") {
		t.Errorf("missing message: %q", got)
	}
	if !strings.Contains(got, "model_not_found") {
		t.Errorf("missing code: %q", got)
	}
	if strings.Contains(got, `{"error"`) || strings.Contains(got, "}}") {
		t.Errorf("raw JSON leaked into the message: %q", got)
	}
}

func TestParseAPIErrorAnthropic(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"model: not found"}}`)
	got := parseAPIError("404 Not Found", body)
	if !strings.Contains(got, "model: not found") {
		t.Errorf("anthropic shape not extracted: %q", got)
	}
}

func TestParseAPIErrorGarbageSuffix(t *testing.T) {
	// The exact shape behind the reported "is not supportedi}}" bug: a raw
	// body whose tail is an odd fragment. The parser must surface only the
	// clean message.
	body := []byte(`{"error":{"message":"model opencode/deepseek-v4-flash-free is not supported","reference":"i"}}`)
	got := parseAPIError("400 Bad Request", body)
	if got != "400 Bad Request: model opencode/deepseek-v4-flash-free is not supported" {
		t.Errorf("clean extraction failed: %q", got)
	}
}

func TestParseAPIErrorFallbacks(t *testing.T) {
	// Non-JSON body falls back to raw text.
	if got := parseAPIError("502 Bad Gateway", []byte("upstream timeout")); !strings.Contains(got, "upstream timeout") {
		t.Errorf("raw fallback failed: %q", got)
	}
	// Empty body gets a placeholder.
	if got := parseAPIError("429 Too Many Requests", nil); !strings.Contains(got, "empty error body") {
		t.Errorf("empty body placeholder missing: %q", got)
	}
	// Long bodies are capped.
	long := strings.Repeat("x", 5000)
	got := parseAPIError("400 Bad Request", []byte(long))
	if len([]rune(strings.TrimPrefix(got, "400 Bad Request: "))) > apiErrorLimit+1 {
		t.Errorf("error not truncated: len=%d", len(got))
	}
}
