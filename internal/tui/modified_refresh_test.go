package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
)

func TestModifiedFilesRefreshIgnoresOutOfOrderResult(t *testing.T) {
	m, _ := newTestModel(t)
	m.modFilesRequested = 2
	m.sidebar.setFiles([]git.NumStat{{Path: "current.go"}})

	feed(m, modFilesMsg{revision: 1, files: []git.NumStat{{Path: "stale.go"}}})
	if got := m.sidebar.modFiles[0].Path; got != "current.go" {
		t.Fatalf("stale refresh replaced current files with %q", got)
	}

	feed(m, modFilesMsg{revision: 2, files: []git.NumStat{{Path: "latest.go"}}})
	if got := m.sidebar.modFiles[0].Path; got != "latest.go" {
		t.Fatalf("latest refresh path = %q", got)
	}
	feed(m, modFilesMsg{revision: 1, files: []git.NumStat{{Path: "late-stale.go"}}})
	if got := m.sidebar.modFiles[0].Path; got != "latest.go" {
		t.Fatalf("late stale refresh replaced latest files with %q", got)
	}
}

func TestChatDoneSchedulesModifiedFilesAndBranchRefresh(t *testing.T) {
	m, _ := newTestModel(t)
	before := m.modFilesRequested
	_, cmd := m.Update(chatDoneMsg{})
	if cmd == nil {
		t.Fatal("chat completion returned no repaint/refresh command")
	}
	if got := m.modFilesRequested; got != before+1 {
		t.Fatalf("modified-files refresh generation = %d, want %d", got, before+1)
	}
}

func TestEvDoneAndChatDoneShareOneCompletionRefresh(t *testing.T) {
	orders := []struct {
		name string
		msgs []tea.Msg
	}{
		{
			name: "event then worker",
			msgs: []tea.Msg{
				streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})},
				chatDoneMsg{},
			},
		},
		{
			name: "worker then event",
			msgs: []tea.Msg{
				chatDoneMsg{},
				streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})},
			},
		},
	}
	for _, order := range orders {
		t.Run(order.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			before := m.modFilesRequested
			for _, msg := range order.msgs {
				_, _ = m.Update(msg)
			}
			if got := m.modFilesRequested; got != before+1 {
				t.Fatalf("completion pair requested %d scans, want 1", got-before)
			}
		})
	}
}

func TestKeyboardProposalAcceptSchedulesModifiedFilesRefresh(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 32})
	target := filepath.Join(dir, "target.txt")
	prop, err := m.proposals.Stage(target, "updated\n")
	if err != nil {
		t.Fatal(err)
	}
	card := &Card{ID: "refresh-proposal", Kind: "write", Status: "proposed", Proposal: &prop, ProposalPath: target}
	m.pending = append(m.pending, card)
	m.appendSystemCard(card)
	before := m.modFilesRequested
	_, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("proposal accept returned no modified-files refresh command")
	}
	if got := m.modFilesRequested; got != before+1 {
		t.Fatalf("modified-files refresh generation = %d, want %d", got, before+1)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "updated\n" {
		t.Fatalf("accepted file = %q, err=%v", data, err)
	}
}

func TestIDESaveSchedulesModifiedFilesRefresh(t *testing.T) {
	m, _ := newTestModel(t)
	m.ToggleIDE()
	if m.ide == nil || m.ide.Ed.Buffer() == nil {
		t.Fatal("IDE did not open an editor buffer")
	}
	before := m.modFilesRequested
	cmd := m.ide.handleAction(m, editor.ActSave)
	if cmd == nil {
		t.Fatal("IDE save returned no modified-files refresh command")
	}
	if got := m.modFilesRequested; got != before+1 {
		t.Fatalf("modified-files refresh generation = %d, want %d", got, before+1)
	}
}

func TestModifiedFilesBurstCoalescesToExactlyOneLatestRerun(t *testing.T) {
	m, _ := newTestModel(t)
	first := m.refreshModifiedFiles()
	if first == nil || !m.modFilesInFlight {
		t.Fatal("first refresh did not start a scan")
	}

	const burst = 100
	for i := 0; i < burst; i++ {
		if cmd := m.refreshModifiedFiles(); cmd != nil {
			t.Fatalf("burst request %d started a concurrent scan", i)
		}
	}
	wantRevision := uint64(burst + 1)
	if m.modFilesRequested != wantRevision {
		t.Fatalf("requested revision = %d, want %d", m.modFilesRequested, wantRevision)
	}

	firstRaw := first()
	firstResult, ok := firstRaw.(modFilesMsg)
	if !ok {
		t.Fatalf("first scan returned %T", firstRaw)
	}
	rerun := m.finishModifiedFilesRefresh(firstResult)
	if rerun == nil {
		t.Fatal("burst did not schedule its one latest rerun")
	}
	if !m.modFilesInFlight || m.modFilesRunning != wantRevision {
		t.Fatalf("rerun state: inFlight=%v running=%d, want running=%d", m.modFilesInFlight, m.modFilesRunning, wantRevision)
	}

	latestRaw := rerun()
	latest, ok := latestRaw.(modFilesMsg)
	if !ok {
		t.Fatalf("latest scan returned %T", latestRaw)
	}
	if extra := m.finishModifiedFilesRefresh(latest); extra != nil {
		t.Fatal("latest result scheduled an unnecessary third scan")
	}
	if m.modFilesInFlight || m.modFilesApplied != wantRevision {
		t.Fatalf("settled state: inFlight=%v applied=%d, want applied=%d", m.modFilesInFlight, m.modFilesApplied, wantRevision)
	}
}

func TestAcceptedNewFileAppearsInIDEFilesAfterLatestRefresh(t *testing.T) {
	m, dir := newTestModel(t)
	m.ToggleIDE()
	if m.ide == nil {
		t.Fatal("IDE did not open")
	}
	_ = m.ide.files() // Prime the cache before the new file exists.
	m.switchTab(TabHarness)

	target := filepath.Join(dir, "fresh.go")
	prop, err := m.proposals.Stage(target, "package fresh\n")
	if err != nil {
		t.Fatal(err)
	}
	card := &Card{ID: "new-file-proposal", Kind: "write", Status: "proposed", Proposal: &prop, ProposalPath: target}
	m.pending = append(m.pending, card)
	m.appendSystemCard(card)

	_, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("proposal accept returned no refresh command")
	}
	resultRaw := cmd()
	result, ok := resultRaw.(modFilesMsg)
	if !ok {
		t.Fatalf("refresh returned %T", resultRaw)
	}
	if extra := m.finishModifiedFilesRefresh(result); extra != nil {
		t.Fatal("latest accept refresh unexpectedly requested a rerun")
	}

	if !containsString(m.ide.files(), "fresh.go") {
		t.Fatalf("IDE file cache was not refreshed: %v", m.ide.files())
	}
	foundTree := false
	for _, entry := range m.ide.treeEntries() {
		if entry.Path == "fresh.go" {
			foundTree = true
			break
		}
	}
	if !foundTree {
		t.Fatalf("IDE tree cache was not invalidated: %+v", m.ide.treeEntries())
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
