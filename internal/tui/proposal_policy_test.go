package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/projectprofile"
)

func TestMaestroManifestProposalIsReviewedAndAppliedAtomically(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 34})
	profile := projectprofile.ProjectProfile{
		SchemaVersion: projectprofile.SchemaVersion,
		Mode:          projectprofile.ModeBrownfield,
		Root:          dir,
		Name:          "atomic-contract",
	}
	baseAnswers := projectprofile.Answers{
		SchemaVersion: projectprofile.SchemaVersion,
		Mode:          projectprofile.ModeBrownfield,
		Name:          "atomic-contract",
		Purpose:       "Old purpose.",
		Safety:        []string{"Old safety boundary."},
		Verification:  []string{"go test ./..."},
	}
	base, err := projectprofile.Render(profile, baseAnswers)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, projectprofile.ManifestName)
	if err := os.WriteFile(target, base, 0o644); err != nil {
		t.Fatal(err)
	}
	nextAnswers := baseAnswers
	nextAnswers.Purpose = "New reviewed purpose."
	nextAnswers.Safety = []string{"Never expose secrets.", "Preserve unrelated user changes."}
	next, err := projectprofile.Render(profile, nextAnswers)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := m.proposals.Stage(target, string(next))
	if err != nil || len(proposal.Hunks) < 2 {
		t.Fatalf("stage atomic contract: hunks=%d err=%v", len(proposal.Hunks), err)
	}
	card := &Card{ID: "atomic-maestro", Kind: "write", Status: "proposed", Proposal: &proposal, ProposalPath: target}
	m.appendSystemCard(card)
	m.pending = append(m.pending, card)
	m.openProposalInIDE(&proposal)

	wantHunks := len(proposal.Hunks)
	m.decideProposalHunk(true)
	if len(proposal.Hunks) != wantHunks || card.Status != "proposed" || len(m.pending) != 1 {
		t.Fatalf("partial decision mutated atomic proposal: hunks=%d status=%q pending=%d", len(proposal.Hunks), card.Status, len(m.pending))
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != string(base) {
		t.Fatalf("partial decision changed manifest: %v", err)
	}
	if navigation := stripANSI(m.renderIDEReviewNavigation(60, card)); strings.Contains(navigation, "hunk") || !strings.Contains(navigation, "atomic contract") {
		t.Fatalf("atomic navigation = %q", navigation)
	}
	if buttons := stripANSI(m.renderIDEHunkButtons(60, card)); !strings.Contains(buttons, "whole contract only") {
		t.Fatalf("atomic controls = %q", buttons)
	}
	_ = m.View()
	for _, region := range m.regions {
		if region.Action == ActionAcceptHunk || region.Action == ActionDiscardHunk || region.Action == ActionPrevHunk || region.Action == ActionNextHunk {
			t.Fatalf("atomic proposal exposed hunk action: %+v", region)
		}
	}

	m.acceptLatestPending()
	if card.Status != "done" || len(m.pending) != 0 {
		t.Fatalf("whole decision status=%q pending=%d detail=%q", card.Status, len(m.pending), card.Detail)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != string(next) {
		t.Fatalf("whole decision manifest mismatch: %v", err)
	}
}
