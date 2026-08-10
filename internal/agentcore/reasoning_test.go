package agentcore

import (
	"strings"
	"testing"
)

func TestReasoningEffortsAreProviderAndModelSpecific(t *testing.T) {
	tests := []struct {
		provider, model string
		canReason       bool
		want            string
	}{
		{"openai", "gpt-5.6-luna", true, "auto,none,low,medium,high,xhigh,max"},
		{"openai-compat", "custom-r1", true, "auto,low,medium,high"},
		{"openai", "gpt-4.1", false, "auto"},
		{"anthropic", "claude-sonnet-4-6", false, "auto,low,medium,high,max"},
		{"anthropic", "claude-opus-4-7", true, "auto,low,medium,high,xhigh,max"},
		{"anthropic", "claude-opus-4-5", true, "auto,low,medium,high"},
		{"anthropic", "claude-future-6", true, "auto"},
		{"ollama", "reasoner", true, "auto"},
		{"generic", "reasoner", true, "auto"},
	}
	for _, tt := range tests {
		if got := strings.Join(ReasoningEffortsForProvider(tt.provider, tt.model, tt.canReason), ","); got != tt.want {
			t.Errorf("%s/%s = %q, want %q", tt.provider, tt.model, got, tt.want)
		}
	}
}
