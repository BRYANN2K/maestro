package agentcore

import "strings"

// reasoningMode is the provider-wire strategy used for an explicit effort.
// Unknown models deliberately have no strategy: advertising a value that the
// selected API ignores is worse than leaving the provider on automatic.
type reasoningMode uint8

const (
	reasoningAutomatic reasoningMode = iota
	reasoningOpenAI
	reasoningAnthropicAdaptive
	reasoningAnthropicManual
)

type reasoningCapability struct {
	mode         reasoningMode
	efforts      []string
	outputEffort bool // Anthropic manual mode: only Opus 4.5 supports it.
}

var (
	automaticEfforts = []string{"auto"}
	openAIGeneric    = []string{"auto", "low", "medium", "high"}
	openAIGPT56      = []string{"auto", "none", "low", "medium", "high", "xhigh", "max"}
	claudeStandard   = []string{"auto", "low", "medium", "high"}
	claudeWithMax    = []string{"auto", "low", "medium", "high", "max"}
	claudeAll        = []string{"auto", "low", "medium", "high", "xhigh", "max"}
)

// ReasoningEffortsForProvider returns only controls that Maestro actually
// serializes for this provider protocol and model family. The caller supplies
// CanReason metadata for OpenAI-compatible custom models. Anthropic uses an
// exact family table because its adaptive/manual request shapes differ.
func ReasoningEffortsForProvider(providerType, model string, canReason bool) []string {
	capability := reasoningCapabilityFor(providerType, model, canReason)
	return append([]string(nil), capability.efforts...)
}

func reasoningCapabilityFor(providerType, model string, canReason bool) reasoningCapability {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai", "openai-compat":
		if !canReason {
			return reasoningCapability{efforts: automaticEfforts}
		}
		id := bareReasoningModelID(model)
		if strings.HasPrefix(id, "gpt-5.6") {
			return reasoningCapability{mode: reasoningOpenAI, efforts: openAIGPT56}
		}
		return reasoningCapability{mode: reasoningOpenAI, efforts: openAIGeneric}
	case "anthropic":
		return anthropicReasoningCapability(model)
	default:
		return reasoningCapability{efforts: automaticEfforts}
	}
}

func anthropicReasoningCapability(model string) reasoningCapability {
	id := bareReasoningModelID(model)
	// Current adaptive-thinking families. Keep this table exact and fail
	// closed for new/unknown IDs until their wire contract is documented.
	switch {
	case claudeFamily(id, "opus", "4-7"), claudeFamily(id, "opus", "4-8"),
		claudeFamily(id, "opus", "5"), claudeFamily(id, "sonnet", "5"),
		claudeFamily(id, "fable", "5"), claudeFamily(id, "mythos", "5"):
		return reasoningCapability{mode: reasoningAnthropicAdaptive, efforts: claudeAll}
	case id == "claude-mythos-preview" || strings.HasPrefix(id, "claude-mythos-preview-"):
		return reasoningCapability{mode: reasoningAnthropicAdaptive, efforts: claudeWithMax}
	case claudeFamily(id, "opus", "4-6"), claudeFamily(id, "sonnet", "4-6"):
		return reasoningCapability{mode: reasoningAnthropicAdaptive, efforts: claudeWithMax}
	case claudeFamily(id, "opus", "4-5"):
		// Opus 4.5 is the only manual-thinking-only model that also accepts
		// output_config.effort; it still requires a budget_tokens budget.
		return reasoningCapability{mode: reasoningAnthropicManual, efforts: claudeStandard, outputEffort: true}
	case claudeFamily(id, "sonnet", "4-5"), claudeFamily(id, "haiku", "4-5"):
		// These 4.5 families support manual thinking but not Anthropic's
		// effort parameter. Maestro maps the three UI levels to safe budgets.
		return reasoningCapability{mode: reasoningAnthropicManual, efforts: claudeStandard}
	default:
		return reasoningCapability{efforts: automaticEfforts}
	}
}

func claudeFamily(id, family, version string) bool {
	prefix := "claude-" + family + "-" + version
	return id == prefix || strings.HasPrefix(id, prefix+"-")
}

func bareReasoningModelID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if first, rest, ok := strings.Cut(id, "/"); ok && rest != "" {
		// Selection handles are normally provider/model. Genuinely-qualified
		// API IDs stay intact unless the prefix is a known wire provider.
		if !strings.Contains(rest, "/") || first == "anthropic" || first == "openai" {
			id = rest
		}
	}
	return id
}

func effortAllowed(efforts []string, effort string) bool {
	effort = NormalizeReasoningEffort(effort)
	if effort == "" {
		effort = "auto"
	}
	for _, allowed := range efforts {
		if effort == allowed {
			return true
		}
	}
	return false
}
