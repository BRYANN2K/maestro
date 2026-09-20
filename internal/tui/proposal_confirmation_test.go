package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProposalConfirmationShowsExactTargetAndSafeFocusOrder(t *testing.T) {
	m, dir := newTestModel(t)
	target := filepath.Join(dir, "nested", "config.yaml")
	prop, err := m.proposals.Stage(target, "enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	card := &Card{ID: "confirm-target", Kind: "write", Status: "proposed", Proposal: &prop, ProposalPath: target}
	m.pending = append(m.pending, card)
	m.appendSystemCard(card)

	m.requestProposalConfirmation(card)
	confirm, ok := m.overlayM.(*proposalConfirmationOverlay)
	if !ok || confirm.selected != proposalConfirmationCancel {
		t.Fatalf("confirmation did not focus cancel first: %T %+v", m.overlayM, confirm)
	}
	view := stripANSI(confirm.View(m.styles, 200))
	if !strings.Contains(view, target) || !strings.Contains(view, "Writes 1 reviewed hunk(s)") {
		t.Fatalf("confirmation omitted its exact target/effect:\n%s", view)
	}

	feed(m, tea.KeyMsg{Type: tea.KeyTab})
	if confirm.selected != proposalConfirmationApply {
		t.Fatalf("first tab selected %d, want apply", confirm.selected)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyTab})
	if confirm.selected != proposalConfirmationDiff {
		t.Fatalf("second tab selected %d, want diff", confirm.selected)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != overlayDiff || len(m.pending) != 1 {
		t.Fatalf("review diff changed proposal state: overlay=%v pending=%d", m.overlay, len(m.pending))
	}
}

func TestWhichKeyAcceptOpensProposalConfirmation(t *testing.T) {
	m, dir := newTestModel(t)
	prop, err := m.proposals.Stage(filepath.Join(dir, "target.txt"), "updated\n")
	if err != nil {
		t.Fatal(err)
	}
	card := &Card{ID: "which-key-confirm", Kind: "write", Status: "proposed", Proposal: &prop, ProposalPath: prop.Path}
	m.pending = append(m.pending, card)
	m.appendSystemCard(card)
	m.overlay = overlayWhichKey
	m.spacePending = true

	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	confirm, ok := m.overlayM.(*proposalConfirmationOverlay)
	if m.overlay != overlayProposalConfirm || !ok || confirm.prop != &prop || confirm.selected != proposalConfirmationCancel {
		t.Fatalf("which-key accept bypassed safe confirmation: overlay=%v model=%T", m.overlay, m.overlayM)
	}
}

func TestCtrlCIsGlobalCancelOrQuit(t *testing.T) {
	t.Run("busy overlay cancels", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.overlay = overlayKeymap
		m.busy = true
		cancelled := 0
		m.cancelRun = func() { cancelled++ }

		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd != nil || cancelled != 1 || !m.cancelling || m.quitting {
			t.Fatalf("global ctrl+c = cmd=%v cancelled=%d cancelling=%v quitting=%v", cmd != nil, cancelled, m.cancelling, m.quitting)
		}
	})

	t.Run("idle overlay quits", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.overlay = overlayKeymap
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil || !m.quitting {
			t.Fatalf("idle global ctrl+c = cmd=%v quitting=%v", cmd != nil, m.quitting)
		}
	})
}

func TestQuitCancelsSettingsAction(t *testing.T) {
	m, _ := newTestModel(t)
	settings := newSettingsOverlay(m)
	cancelled := 0
	settings.actionCancel = func() { cancelled++ }
	settings.providerLoading = true
	settings.mcpLoading = true
	settings.skillLoading = true
	m.overlay = overlaySettings
	m.overlayM = settings

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd == nil || cancelled != 1 || settings.actionCancel != nil {
		t.Fatalf("settings quit cleanup = cmd=%v cancelled=%d cancel-set=%v", cmd != nil, cancelled, settings.actionCancel != nil)
	}
	if settings.providerLoading || settings.mcpLoading || settings.skillLoading {
		t.Fatalf("settings loading state survived quit: provider=%v mcp=%v skill=%v", settings.providerLoading, settings.mcpLoading, settings.skillLoading)
	}
}

func TestCompactResizeMovesFocusOffHiddenActivityRail(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.focus = FocusSidebar
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	if !m.compact || m.showActivityRail() || m.focus != FocusInput {
		t.Fatalf("compact resize left hidden focus: compact=%v rail=%v focus=%v", m.compact, m.showActivityRail(), m.focus)
	}
}
