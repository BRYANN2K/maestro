package settings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.PermissionMode != PermAsk {
		t.Errorf("default permission mode = %q, want ask", d.PermissionMode)
	}
	for _, role := range []string{RoleOrchestrator, RoleDev, RoleReviewer, RoleDocs} {
		if d.RoleDefaults[role].Engine != "native" {
			t.Errorf("role %s default engine = %q, want native", role, d.RoleDefaults[role].Engine)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maestro", "settings.json")
	ctx := context.Background()

	s := Defaults()
	s.RoleDefaults[RoleDev] = RoleDefaults{Engine: "legacy", Agent: "codex", Model: "gpt-4o", ReasoningEffort: "high", ReasoningSet: true}
	s.ModelSlots["large"] = "openai/gpt-4o"
	s.PermissionMode = PermAllow
	s.Theme = "dark"
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(ctx, path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RoleDefaults[RoleDev] != (RoleDefaults{Engine: "legacy", Agent: "codex", Model: "gpt-4o", ReasoningEffort: "high", ReasoningSet: true}) {
		t.Errorf("dev defaults = %+v", got.RoleDefaults[RoleDev])
	}
	if got.ModelSlots["large"] != "openai/gpt-4o" || got.PermissionMode != PermAllow || got.Theme != "dark" {
		t.Errorf("Load mismatch: %+v", got)
	}
}

func TestLoadMigratesReasoningAutoToOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	data := `{"role_defaults":{"dev":{"engine":"legacy","agent":"codex","reasoning_effort":"auto"}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if effort := got.RoleDefaults[RoleDev].ReasoningEffort; effort != "" {
		t.Fatalf("migrated reasoning effort = %q, want omitted auto", effort)
	}
	if !got.RoleDefaults[RoleDev].ReasoningSet {
		t.Fatal("explicit legacy auto was not preserved as an explicit override")
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	got, err := Load(context.Background(), filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PermissionMode != PermAsk {
		t.Errorf("Load missing = %+v, want defaults", got)
	}
}

func TestLoadEmptyPathReturnsDefaults(t *testing.T) {
	got, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.PermissionMode != PermAsk {
		t.Errorf("Load(\"\") = %+v, want defaults", got)
	}
}

func TestLoadCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Load(context.Background(), path); err == nil {
		t.Error("Load of corrupt settings should fail")
	}
}

func TestPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	ctx := context.Background()
	info, err := os.Stat(filepath.Dir(path))
	if err == nil && info.Mode().Perm() != 0o700 {
		// Parent may not exist; we only assert on the file below.
	}
	s := Defaults()
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("settings perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		s    Settings
		ok   bool
	}{
		{"defaults", Defaults(), true},
		{"bad mode", Settings{PermissionMode: "explosive"}, false},
		{"bad engine", Settings{PermissionMode: PermAsk, RoleDefaults: map[string]RoleDefaults{RoleDev: {Engine: "nope"}}}, false},
		{"legacy engine ok", Settings{PermissionMode: PermAsk, RoleDefaults: map[string]RoleDefaults{RoleDev: {Engine: "legacy"}}}, true},
		{"reasoning effort ok", Settings{PermissionMode: PermAsk, RoleDefaults: map[string]RoleDefaults{RoleDev: {ReasoningEffort: "xhigh"}}}, true},
		{"bad reasoning", Settings{PermissionMode: PermAsk, RoleDefaults: map[string]RoleDefaults{RoleDev: {ReasoningEffort: "ultra"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Valid()
			if tt.ok && err != nil {
				t.Errorf("Valid() = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Error("Valid() = nil, want error")
			}
			if err != nil && !strings.Contains(err.Error(), tt.name) && tt.name == "bad engine" {
				// role name appears in the error; sanity check below instead
				if !strings.Contains(err.Error(), "engine") {
					t.Errorf("error %q should mention engine", err)
				}
			}
		})
	}
}
