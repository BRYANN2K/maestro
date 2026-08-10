package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/proposals"
	"github.com/bryann2k/maestro/internal/settings"
)

func TestStartupUnknownModelWarning(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
		Models: []config.Model{{ID: "opencode/deepseek-v4-flash"}},
	}
	store := proposals.NewProposalStore(filepath.Join(dir, ".proposals"))
	perm := NewPermissionQueue(4)
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(dir, ".s"),
		In:          strings.NewReader(""),
		Out:         os.Stdout,
		Gate:        perm,
		Config:      cfg,
		Settings: settings.Settings{
			RoleDefaults:   map[string]settings.RoleDefaults{},
			PermissionMode: settings.PermAsk,
			Theme:          "charmtone",
			EditorMode:     "vim",
		},
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	orch.SetModel("opencode/deepseek-v4-flash-free")
	m := New(orch, store, perm)
	if len(m.messages) == 0 {
		t.Fatal("no startup warning message")
	}
	msg := m.messages[0]
	if !strings.Contains(msg.Text, "not served by provider") {
		t.Errorf("warning text = %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "closest match") || !strings.Contains(msg.Text, "deepseek-v4-flash") {
		t.Errorf("warning missing catalog suggestion: %q", msg.Text)
	}
	if len(m.status.toasts) == 0 {
		t.Error("warning toast missing")
	}
}

func TestStartupKnownModelNoWarning(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
		Models: []config.Model{{ID: "opencode/deepseek-v4-flash"}},
	}
	store := proposals.NewProposalStore(filepath.Join(dir, ".proposals"))
	perm := NewPermissionQueue(4)
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(dir, ".s"),
		In:          strings.NewReader(""),
		Out:         os.Stdout,
		Gate:        perm,
		Config:      cfg,
		Settings: settings.Settings{
			RoleDefaults:   map[string]settings.RoleDefaults{},
			PermissionMode: settings.PermAsk,
			Theme:          "charmtone",
			EditorMode:     "vim",
		},
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	orch.SetModel("opencode/deepseek-v4-flash")
	m := New(orch, store, perm)
	if len(m.messages) != 0 {
		t.Errorf("unexpected startup message for a known model: %q", m.messages[0].Text)
	}
}
