package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

func TestHITLOptionalEnvironmentVariableWithFallbackIsNotAnAction(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.spec = &spec.Spec{
		ID:     "greeting-name",
		Title:  "Personnaliser le message d’accueil via GREETING_NAME",
		Status: spec.StatusProposal,
		Body: `## Goal
Permettre de personnaliser le nom via la variable d’environnement GREETING_NAME.

## Scope
Lire GREETING_NAME, utiliser world si elle est absente ou vide et conserver les espaces.

## Non-goals
Aucune configuration persistante et aucun secret.`,
		Requirements: []spec.Requirement{{
			ID:        "REQ-GREETING-001",
			Statement: "The system shall use world when GREETING_NAME is absent or empty.",
		}},
	}
	orch.sess.SpecID = orch.spec.ID
	orch.sess.Phase = session.PhaseSpec

	items, err := orch.HITLItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "env:GREETING_NAME" || strings.Contains(item.Item, "GREETING_NAME") {
			t.Fatalf("optional fallback became a provisioning action: %+v", items)
		}
		if item.ID == "hitl" {
			t.Fatalf("synthetic summary leaked into actions: %+v", items)
		}
	}
}

func TestHITLExplicitRequiredEnvironmentVariableIsBlocking(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.spec = &spec.Spec{
		ID: "database", Title: "Database", Status: spec.StatusProposal,
		Body: "Before startup, configure the required environment variable DATABASE_URL in .env.",
	}
	orch.sess.SpecID = orch.spec.ID
	orch.sess.Phase = session.PhaseSpec

	items, err := orch.HITLItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "env:DATABASE_URL" || !items[0].Blocking || items[0].Status != "pending" {
		t.Fatalf("required environment action = %+v", items)
	}
}

func TestHITLRuntimeEnvironmentMentionIsNotProvisioning(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.spec = &spec.Spec{
		ID: "logging", Title: "Logging", Status: spec.StatusProposal,
		Body: "The program must read the LOG_LEVEL environment variable at startup.",
	}
	orch.sess.SpecID = orch.spec.ID
	orch.sess.Phase = session.PhaseSpec
	items, err := orch.HITLItems(context.Background())
	if err != nil || len(items) != 0 {
		t.Fatalf("runtime input mention became provisioning: %+v, %v", items, err)
	}
}

func TestBuildEnforcesAndPersistsBlockingHITLAcknowledgement(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	newOrch := func(runner Runner) *Orchestrator {
		t.Helper()
		t.Setenv("MAESTRO_MEMORY_DIR", filepath.Join(t.TempDir(), "mem"))
		t.Setenv("MAESTRO_CHECKPOINTS_DIR", filepath.Join(t.TempDir(), "cps"))
		o, err := New(context.Background(), Options{
			ProjectDir: dir, SessionsDir: sessionsDir, In: strings.NewReader("n\n"), Out: &bytes.Buffer{}, Runner: runner,
		})
		if err != nil {
			t.Fatal(err)
		}
		if fake, ok := runner.(*fakeRunner); ok {
			fake.Wd = func() string { return o.WorkDirDisplay() }
		}
		return o
	}

	orch := newOrch(&fakeRunner{})
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Configure the required environment variable DATABASE_URL in .env before startup"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(ctx, BuildOptions{}); err == nil || !strings.Contains(err.Error(), "blocked by human action") {
		t.Fatalf("build gate error = %v", err)
	}
	if err := orch.SetHITLStatus(ctx, "env:DATABASE_URL", true); err != nil {
		t.Fatal(err)
	}

	restored := newOrch(&fakeRunner{})
	items, err := restored.HITLItems(ctx)
	if err != nil || len(items) != 1 || items[0].Status != "done" {
		t.Fatalf("restored HITL = %+v, %v", items, err)
	}
	if err := restored.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("acknowledged build: %v", err)
	}
	if restored.Phase() != session.PhaseBuild {
		t.Fatalf("phase = %s, want build", restored.Phase())
	}
}

func TestFilledEnvDoesNotNeedAcknowledgement(t *testing.T) {
	dir := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DATABASE_URL=postgres://local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orch := newTestOrch(t, dir, &fakeRunner{})
	orch.spec = &spec.Spec{
		ID: "database", Title: "Database", Status: spec.StatusProposal,
		Body: "DATABASE_URL is required and must be configured in .env.",
	}
	orch.sess.SpecID = orch.spec.ID
	orch.sess.Phase = session.PhaseSpec
	items, err := orch.HITLItems(context.Background())
	if err != nil || len(items) != 0 {
		t.Fatalf("filled .env actions = %+v, %v", items, err)
	}
}
