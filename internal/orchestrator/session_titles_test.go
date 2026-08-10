package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
)

type titleStubRunner struct {
	summary   string
	err       error
	waitForCX bool
	calls     int
	prompt    string
}

func (runner *titleStubRunner) Run(ctx context.Context, _ agentcore.Role, prompt string) (agentcore.AgentResult, error) {
	runner.calls++
	runner.prompt = prompt
	if runner.waitForCX {
		<-ctx.Done()
		return agentcore.AgentResult{}, ctx.Err()
	}
	return agentcore.AgentResult{OK: runner.err == nil, Summary: runner.summary}, runner.err
}

func TestChatPersistsFallbackThenValidLLMTitle(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	titleRunner := &titleStubRunner{summary: `{"title":"Concise generated title"}`}
	orch.titleRunner = titleRunner
	if err := orch.Chat(t.Context(), "## Investigate Unicode title behavior and edge cases"); err != nil {
		t.Fatal(err)
	}
	got := orch.Session()
	if got.Title != "Concise generated title" || got.TitleSource != session.TitleSourceLLM || got.TitleSeedHash == "" || titleRunner.calls != 1 {
		t.Fatalf("session title = %+v title calls=%d", got, titleRunner.calls)
	}
	if strings.Contains(titleRunner.prompt, "MAESTRO_HUMAN_OUTPUT_V1") {
		t.Fatal("human prose contract leaked into structured title JSON prompt")
	}
	reloaded, err := orch.sessions.Load(t.Context(), got.Project, got.ID)
	if err != nil || reloaded.Title != got.Title || reloaded.TitleSource != session.TitleSourceLLM {
		t.Fatalf("reloaded = %+v, %v", reloaded, err)
	}
}

func TestChatKeepsFallbackForInvalidOrUnavailableTitleModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary string
		err     error
	}{
		{name: "fenced", summary: "```json\n{\"title\":\"Bad\"}\n```"},
		{name: "multiline", summary: `{"title":"bad\nline"}`},
		{name: "unknown field", summary: `{"title":"Valid words","extra":true}`},
		{name: "provider failure", err: errors.New("offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
			orch.titleRunner = &titleStubRunner{summary: tc.summary, err: tc.err}
			if err := orch.Chat(t.Context(), "Keep a deterministic fallback title"); err != nil {
				t.Fatal(err)
			}
			got := orch.Session()
			if got.Title != "Keep a deterministic fallback title" || got.TitleSource != session.TitleSourceFallback {
				t.Fatalf("fallback session = %+v", got)
			}
		})
	}
}

func TestTitleTimeoutAndRenamePreventReplacement(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.titleRunner = &titleStubRunner{waitForCX: true}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := orch.Chat(cancelled, "Fallback survives a title timeout"); err != nil {
		t.Fatal(err)
	}
	if got := orch.Session(); got.TitleSource != session.TitleSourceFallback {
		t.Fatalf("timeout title source = %q", got.TitleSource)
	}
	seed := orch.Session().TitleSeedHash
	if err := orch.RenameSession(t.Context(), "User controlled title"); err != nil {
		t.Fatal(err)
	}
	_, swapped, err := orch.sessions.CompareAndSwapTitle(t.Context(), orch.sess.Project, orch.sess.ID, seed, session.TitleSourceFallback, "Late generated title", session.TitleSourceLLM)
	if err != nil || swapped {
		t.Fatalf("late CAS swapped=%v err=%v", swapped, err)
	}
	if got := orch.Session(); got.Title != "User controlled title" || got.TitleSource != session.TitleSourceUser {
		t.Fatalf("renamed session = %+v", got)
	}
}

func TestProposalTitleRefinesOnlyFallback(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	orch.ensureFallbackTitle("Long raw proposal request")
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}
	orch.refineSessionTitleFromProposal("Focused proposal title")
	if orch.sess.Title != "Focused proposal title" || orch.sess.TitleSource != session.TitleSourceLLM {
		t.Fatalf("refined title = %+v", orch.sess)
	}
	if err := orch.RenameSession(t.Context(), "Pinned by user"); err != nil {
		t.Fatal(err)
	}
	orch.refineSessionTitleFromProposal("Should not replace")
	if orch.sess.Title != "Pinned by user" {
		t.Fatalf("proposal replaced user title: %+v", orch.sess)
	}
}

func TestRenameSessionDoesNotPartiallyApplyWhenActivePointerFails(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	projectDir := filepath.Join(orch.sessions.Dir(), orch.sess.Project)
	if err := os.Remove(filepath.Join(projectDir, "active")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "active"), 0o700); err != nil {
		t.Fatal(err)
	}

	err := orch.RenameSession(t.Context(), "Must not persist")
	if err == nil {
		t.Fatal("RenameSession succeeded with an unwritable active pointer")
	}
	if got := orch.Session(); got.Title != "" || got.TitleSource != "" || got.TitleSeedHash != "" {
		t.Fatalf("failed rename changed in-memory metadata: %+v", got)
	}
	persisted, loadErr := orch.sessions.Load(t.Context(), orch.sess.Project, orch.sess.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Title != "" || persisted.TitleSource != "" || persisted.TitleSeedHash != "" {
		t.Fatalf("failed rename changed persisted metadata: %+v", persisted)
	}
}

func TestDecodeSafeLLMTitleRejectsUnsafeOutput(t *testing.T) {
	for _, value := range []string{
		`{"title":"ok","extra":1}`,
		" `{\"title\":\"leading wrapper\"}`",
		`{"title":"ab"}`,
		`{"title":"direction \u202e override"}`,
		`{"title":"` + strings.Repeat("x", 73) + `"}`,
	} {
		if _, err := decodeSafeLLMTitle(value); err == nil {
			t.Errorf("decodeSafeLLMTitle(%q) succeeded", value)
		}
	}
}
