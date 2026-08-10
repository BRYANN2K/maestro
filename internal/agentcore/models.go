package agentcore

import "strings"

// Model metadata and sampling are shared by the registry, provider loop,
// role router, and TUI usage display.
type Model struct {
	ID               string
	Name             string
	ContextWindow    int
	DefaultMaxTokens int
	CanReason        bool
	SupportsImages   bool
	PriceInput       float64 // USD per 1M tokens
	PriceOutput      float64
	PriceCacheCreate float64
	PriceCacheHit    float64
	ReasoningEffort  string // low | medium | high
}

// Slot binds a model to a sampling profile. Slot names are "large" and
// "small".
type Slot struct {
	Model    string
	Sampling Sampling
}

// Sampling carries per-request sampling options. Pointer fields are unset
// (provider default) when nil.
type Sampling struct {
	Think            bool
	ReasoningEffort  string // low | medium | high
	MaxTokens        int
	Temperature      *float64
	TopP             *float64
	TopK             int
	FrequencyPenalty *float64
	PresencePenalty  *float64
	ProviderOptions  map[string]any
}

// ValidReasoningEffort reports whether effort is safe to send through a
// native provider. Empty and "auto" both mean omit the provider field.
func ValidReasoningEffort(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "auto", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// NormalizeReasoningEffort converts the user-facing automatic value to the
// zero value used by provider request structs and persisted settings.
func NormalizeReasoningEffort(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "auto" {
		return ""
	}
	return effort
}

// RoleName identifies a model role for traffic routing.
type RoleName string

// Model roles.
const (
	RoleDefault RoleName = "default"
	RoleSmol    RoleName = "smol"
	RoleSlow    RoleName = "slow"
	RolePlan    RoleName = "plan"
	RoleCommit  RoleName = "commit"
)

// ResolveRole picks the model + sampling for a role. It consults the
// explicit modelRoles map first, then falls back to the slot defaults:
// slow = large slot with thinking at high effort, smol = small slot without
// thinking, plan = large with medium effort, commit = small.
func ResolveRole(role RoleName, slots map[string]Slot, roles map[string]Slot) (string, Sampling, bool) {
	if r, ok := roles[string(role)]; ok {
		return r.Model, r.Sampling, true
	}
	switch role {
	case RoleDefault, RoleSlow, RolePlan:
		if s, ok := slots["large"]; ok {
			sm := s.Sampling
			switch role {
			case RoleSlow:
				sm.Think = true
				sm.ReasoningEffort = "high"
			case RolePlan:
				sm.ReasoningEffort = "medium"
			}
			return s.Model, sm, true
		}
	case RoleSmol, RoleCommit:
		if s, ok := slots["small"]; ok {
			sm := s.Sampling
			sm.Think = false
			return s.Model, sm, true
		}
	}
	return "", Sampling{}, false
}
