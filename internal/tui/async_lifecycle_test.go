package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/orchestrator"
)

func TestLateCoachResultCannotReplaceNewerOverlayOrComposer(t *testing.T) {
	t.Setenv("MAESTRO_LEARN_DIR", t.TempDir())
	m, _ := newTestModel(t)
	originalOffer := &coachOffer{ID: "existing", Title: "Existing lesson"}
	m.coachOffer = originalOffer

	cmd := m.runCoachAction("next", "", "")
	newerOverlay := &listOverlay{title: "Newer overlay", items: []string{"keep"}}
	m.overlay = overlaySettings
	m.overlayM = newerOverlay
	m.input.Set("newer draft")
	messageCount := len(m.messages)

	m.Update(primaryBatchMessage(t, cmd))
	if m.overlay != overlaySettings || m.overlayM != newerOverlay {
		t.Fatalf("late Coach result stole newer overlay: %v/%T", m.overlay, m.overlayM)
	}
	if got := m.input.Value(); got != "newer draft" {
		t.Fatalf("late Coach result replaced composer: %q", got)
	}
	if m.coachOffer != originalOffer {
		t.Fatal("late Coach result replaced the current rail offer")
	}
	if len(m.messages) != messageCount {
		t.Fatal("late Coach result appended transcript content after the UI moved on")
	}
}

func TestLateCoachResultCannotMutateAfterComposerEdit(t *testing.T) {
	t.Setenv("MAESTRO_LEARN_DIR", t.TempDir())
	m, _ := newTestModel(t)
	cmd := m.runCoachAction("status", "", "")
	m.input.Set("do not replace this draft")
	messageCount := len(m.messages)

	m.Update(primaryBatchMessage(t, cmd))
	if got := m.input.Value(); got != "do not replace this draft" {
		t.Fatalf("composer = %q", got)
	}
	if len(m.messages) != messageCount {
		t.Fatal("stale Coach status appended to the newer interaction")
	}
}

func TestLoadedSessionClearsAndInvalidatesCoachOffer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_LEARN_DIR", t.TempDir())
	m, dir := newTestModel(t)
	m.coachOffer = &coachOffer{ID: "old-session", Title: "Old session lesson"}
	cmd := m.runCoachAction("status", "", "")
	request := m.coachRequest

	m.applyLoadedSession(m.orch.Session().ID, dir)
	if m.coachOffer != nil {
		t.Fatal("session load retained a Coach offer from the previous route")
	}
	if m.coachRequest <= request || m.coachStop != nil {
		t.Fatalf("session load did not invalidate Coach request: request=%d old=%d stop=%v", m.coachRequest, request, m.coachStop != nil)
	}

	m.Update(primaryBatchMessage(t, cmd))
	if m.coachOffer != nil || m.overlay == overlayCoach {
		t.Fatal("cancelled Coach result resurfaced after a session load")
	}
}

func TestResumeActiveSessionIsNonDestructiveNoop(t *testing.T) {
	m, _ := newTestModel(t)
	active := m.orch.Session()
	card := &Card{ID: "keep-card", Status: "proposed"}
	message := &Message{Role: "assistant", Text: "keep runtime transcript"}
	m.pending = []*Card{card}
	m.messages = []*Message{message}
	m.coachOffer = &coachOffer{ID: "keep-coach"}
	m.overlay = overlaySessionPicker
	m.overlayM = &listOverlay{title: "Resume"}

	if cmd := m.loadSession(active.ID); cmd != nil {
		t.Fatal("resuming the active session scheduled a destructive reload")
	}
	if m.orch.Session().ID != active.ID || len(m.pending) != 1 || m.pending[0] != card {
		t.Fatal("active-session no-op changed session runtime state")
	}
	if len(m.messages) != 1 || m.messages[0] != message || m.coachOffer == nil {
		t.Fatal("active-session no-op cleared transcript or Coach state")
	}
	if m.overlay != overlayNone || m.overlayM != nil {
		t.Fatalf("active-session no-op left picker open: %v/%T", m.overlay, m.overlayM)
	}
	if len(m.status.toasts) == 0 || m.status.toasts[len(m.status.toasts)-1].Msg != "session already active" {
		t.Fatalf("active-session no-op did not explain itself: %#v", m.status.toasts)
	}
}

func TestLearnExplicitPathWinsOverCoachReservedWords(t *testing.T) {
	for _, name := range []string{"guided", "challenge", "off", "status", "next", "done", "later"} {
		t.Run(name, func(t *testing.T) {
			m, dir := newTestModel(t)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("package sample\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			m.input.Set("/learn --path " + name)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd == nil || !m.busy {
				t.Fatalf("explicit path %q routed to Coach instead of file learning", name)
			}
			if m.cancelRun == nil {
				t.Fatal("file learning did not create a cancellable run")
			}
			m.cancelRun()
		})
	}
}

func TestSessionPickerCloseCancelsAndInvalidatesRequest(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := m.openSessionPicker()
	request := m.sessionRequest
	if cmd == nil || m.sessionListStop == nil {
		t.Fatal("session list did not start a cancellable request")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone || m.sessionListStop != nil || m.sessionRequest <= request {
		t.Fatalf("dismissed session request remains live: overlay=%v request=%d old=%d stop=%v", m.overlay, m.sessionRequest, request, m.sessionListStop != nil)
	}
	m.Update(primaryBatchMessage(t, cmd))
	if m.overlay != overlayNone || m.overlayM != nil {
		t.Fatal("cancelled session listing reopened its picker")
	}
}

func TestReopenedSessionPickerIgnoresFirstRequest(t *testing.T) {
	m, _ := newTestModel(t)
	first := m.openSessionPicker()
	firstRequest := m.sessionRequest
	second := m.openSessionPicker()
	secondRequest := m.sessionRequest
	secondOverlay := m.overlayM
	if first == nil || second == nil || secondRequest <= firstRequest {
		t.Fatal("reopening the session picker did not coalesce requests")
	}

	m.Update(primaryBatchMessage(t, first))
	if m.overlay != overlaySessionPicker || m.overlayM != secondOverlay || m.sessionRequest != secondRequest {
		t.Fatal("first session request replaced the reopened picker")
	}
}

func TestWorkspacePickerCloseCancelsAndInvalidatesRequest(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := m.openWorkspacePicker()
	request := m.workspaceRequest
	if cmd == nil || m.workspaceListStop == nil {
		t.Fatal("workspace list did not start a cancellable request")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone || m.workspaceListStop != nil || m.workspaceRequest <= request {
		t.Fatalf("dismissed workspace request remains live: overlay=%v request=%d old=%d stop=%v", m.overlay, m.workspaceRequest, request, m.workspaceListStop != nil)
	}
	m.Update(workspaceListMsg{request: request})
	if m.overlay != overlayNone || m.overlayM != nil {
		t.Fatal("cancelled workspace listing reopened its picker")
	}
}

func TestReopenedWorkspacePickerIgnoresFirstRequest(t *testing.T) {
	m, _ := newTestModel(t)
	first := m.openWorkspacePicker()
	firstRequest := m.workspaceRequest
	second := m.openWorkspacePicker()
	secondRequest := m.workspaceRequest
	secondOverlay := m.overlayM
	if first == nil || second == nil || secondRequest <= firstRequest {
		t.Fatal("reopening the workspace picker did not coalesce requests")
	}

	m.Update(primaryBatchMessage(t, first))
	if m.overlay != overlayGit || m.overlayM != secondOverlay || m.workspaceRequest != secondRequest {
		t.Fatal("first workspace request replaced the reopened picker")
	}
}

func TestDiffOverlayResolvesItsExactProposal(t *testing.T) {
	for _, action := range []string{"accept", "discard"} {
		t.Run(action, func(t *testing.T) {
			m, dir := newTestModel(t)
			firstPath := filepath.Join(dir, "target.txt")
			secondPath := filepath.Join(dir, "second.txt")
			if err := os.WriteFile(secondPath, []byte("second base\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			first, err := m.proposals.Stage(firstPath, "first accepted\n")
			if err != nil {
				t.Fatal(err)
			}
			second, err := m.proposals.Stage(secondPath, "second newer\n")
			if err != nil {
				t.Fatal(err)
			}
			firstCard := &Card{ID: "first", Status: "proposed", Proposal: &first}
			secondCard := &Card{ID: "second", Status: "proposed", Proposal: &second}
			m.pending = []*Card{firstCard, secondCard}
			m.overlay = overlayDiff
			m.overlayM = newDiffOverlay(m.styles, &first, 90)

			key := 'a'
			if action == "discard" {
				key = 'd'
			}
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})

			if len(m.pending) != 1 || m.pending[0] != secondCard || secondCard.Status != "proposed" {
				t.Fatalf("%s resolved the newest proposal instead of the displayed one: %+v", action, m.pending)
			}
			secondData, err := os.ReadFile(secondPath)
			if err != nil || string(secondData) != "second base\n" {
				t.Fatalf("%s mutated the newer proposal target: %q, %v", action, secondData, err)
			}
			firstData, err := os.ReadFile(firstPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "first accepted\n"
			if action == "discard" {
				want = "one\ntwo\n"
			}
			if string(firstData) != want {
				t.Fatalf("%s displayed proposal target = %q, want %q", action, firstData, want)
			}
		})
	}
}

func TestCoachGuardTracksWorkspaceSnapshot(t *testing.T) {
	m, _ := newTestModel(t)
	_, result := m.beginCoachRequest("next")
	result.state.Mode = orchestrator.CoachModeGuided
	result.lesson = &orchestrator.CoachLesson{ID: "lesson", Title: "Do not surface"}
	result.workspace = orchestrator.WorkspaceSnapshot{}

	m.Update(result)
	if m.coachOffer != nil || m.overlay == overlayCoach {
		t.Fatal("Coach result with a stale workspace snapshot reached the UI")
	}
}
