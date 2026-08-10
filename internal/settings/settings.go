// Package settings stores per-user Maestro settings: engine/agent/model
// defaults per role, model slots, permission mode, and theme.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Roles that carry engine/agent/model defaults.
const (
	RoleOrchestrator = "orchestrator"
	RoleDev          = "dev"
	RoleReviewer     = "reviewer"
	RoleDocs         = "docs"
)

// Permission modes.
const (
	PermAsk   = "ask"   // default: every tool call asks
	PermAllow = "allow" // allow listed tools
	PermDeny  = "deny"  // deny listed tools
	PermYolo  = "yolo"  // auto-approve everything
)

// RoleDefaults remembers the engine + agent + model chosen for a role so the
// next /build pre-selects them.
type RoleDefaults struct {
	Engine          string `json:"engine,omitempty"` // native | legacy
	Agent           string `json:"agent,omitempty"`  // legacy agent name
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ReasoningSet    bool   `json:"reasoning_effort_explicit,omitempty"` // distinguishes explicit auto from inherited config
}

// Settings is the persisted user configuration.
type Settings struct {
	RoleDefaults   map[string]RoleDefaults `json:"role_defaults,omitempty"`
	ModelSlots     map[string]string       `json:"model_slots,omitempty"` // "large"|"small" → model ID
	PermissionMode string                  `json:"permission_mode,omitempty"`
	Theme          string                  `json:"theme,omitempty"`
	EditorMode     string                  `json:"editor_mode,omitempty"` // standard | vim
	// DisableUpdateChecks is opt-out so settings written before this feature
	// preserve the privacy-conscious default: one public npm metadata request
	// per 24 hours, with no telemetry and no automatic installation.
	DisableUpdateChecks bool `json:"disable_update_checks,omitempty"`
}

// Defaults returns a Settings with sane first-run values.
func Defaults() Settings {
	return Settings{
		RoleDefaults: map[string]RoleDefaults{
			RoleOrchestrator: {Engine: "native"},
			RoleDev:          {Engine: "native"},
			RoleReviewer:     {Engine: "native"},
			RoleDocs:         {Engine: "native"},
		},
		ModelSlots:     map[string]string{"large": "", "small": ""},
		PermissionMode: PermAsk,
		Theme:          "charmtone",
		EditorMode:     "standard",
	}
}

// DefaultPath returns the settings file path honoring XDG_CONFIG_HOME when
// set (all platforms), falling back to the platform user config dir, e.g.
// ~/.config/maestro/settings.json.
func DefaultPath() (string, error) {
	base, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(base, "maestro", "settings.json"), nil
}

// userConfigDir returns $XDG_CONFIG_HOME when set, os.UserConfigDir
// otherwise.
func userConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	return os.UserConfigDir()
}

// Load reads settings from path. A missing file yields defaults.
func Load(ctx context.Context, path string) (Settings, error) {
	if path == "" {
		return Defaults(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), nil
		}
		return Settings{}, fmt.Errorf("load settings %s: %w", path, err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("load settings %s: %w", path, err)
	}
	if s.RoleDefaults == nil {
		s.RoleDefaults = map[string]RoleDefaults{}
	}
	// Early development builds wrote the UI label "auto" verbatim. Keep old
	// files readable while preserving the stable on-disk contract: omitted
	// reasoning_effort means provider/vendor automatic selection.
	for role, defaults := range s.RoleDefaults {
		if defaults.ReasoningEffort == "auto" {
			defaults.ReasoningEffort = ""
			defaults.ReasoningSet = true
		} else if defaults.ReasoningEffort != "" {
			// Files written before ReasoningSet existed used a non-empty value
			// only for an explicit user selection.
			defaults.ReasoningSet = true
		}
		s.RoleDefaults[role] = defaults
	}
	if s.ModelSlots == nil {
		s.ModelSlots = map[string]string{}
	}
	if s.PermissionMode == "" {
		s.PermissionMode = PermAsk
	}
	if s.Theme == "" {
		s.Theme = "charmtone"
	}
	if s.EditorMode == "" {
		s.EditorMode = "standard"
	}
	return s, nil
}

// Save writes settings atomically with 0600 permissions, creating the parent
// directory (0700) as needed.
func (s Settings) Save(ctx context.Context, path string) error {
	if path == "" {
		return errors.New("settings path is required")
	}
	if err := s.Valid(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("save settings: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("save settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

// Valid reports whether the settings are structurally sound.
func (s Settings) Valid() error {
	switch s.PermissionMode {
	case "", PermAsk, PermAllow, PermDeny, PermYolo:
	default:
		return fmt.Errorf("permission mode %q invalid", s.PermissionMode)
	}
	for role, rd := range s.RoleDefaults {
		if rd.Engine != "" && rd.Engine != "native" && rd.Engine != "legacy" {
			return fmt.Errorf("role %s: engine %q invalid", role, rd.Engine)
		}
		switch rd.ReasoningEffort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return fmt.Errorf("role %s: reasoning effort %q invalid", role, rd.ReasoningEffort)
		}
	}
	if s.EditorMode != "" && s.EditorMode != "standard" && s.EditorMode != "vim" {
		return fmt.Errorf("editor mode %q invalid", s.EditorMode)
	}
	return nil
}
