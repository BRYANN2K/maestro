package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/proposals"
	"github.com/bryann2k/maestro/internal/settings"
)

// newTestModel builds a TUI model over a test orchestrator.
func newTestModel(t testing.TB) (*Model, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := proposals.NewProposalStore(filepath.Join(dir, ".proposals"))
	perm := NewPermissionQueue(4)
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(dir, ".sessions"),
		In:          strings.NewReader(""),
		Out:         os.Stdout,
		Gate:        perm,
		DevTools:    []agentcore.Tool{proposals.StagingWriteTool(store)},
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
	m := New(orch, store, perm)
	return m, orch.WorkDirDisplay()
}

func feed(m *Model, msg tea.Msg) tea.Model {
	updated, _ := m.Update(msg)
	return updated
}

func TestChatCompletionHasNoGenericSuccessNotification(t *testing.T) {
	m, _ := newTestModel(t)
	before := len(m.status.toasts)

	feed(m, chatDoneMsg{})
	if got := len(m.status.toasts); got != before {
		t.Fatalf("generic chat completion added a toast: before=%d after=%d toasts=%#v", before, got, m.status.toasts)
	}

	feed(m, chatDoneMsg{successToast: "build complete"})
	if len(m.status.toasts) != before+1 || m.status.toasts[len(m.status.toasts)-1].Msg != "build complete" {
		t.Fatalf("explicit completion toast missing: %#v", m.status.toasts)
	}
}

func TestTrailingBackslashInsertsNewline(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.input.Set("first line \\")
	n := len(m.messages)
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "first line \n" {
		t.Errorf("input = %q, want %q", got, "first line \n")
	}
	if len(m.messages) != n {
		t.Errorf("backslash enter must not send: messages %d → %d", n, len(m.messages))
	}
	// Without a trailing backslash, Enter sends.
	m.input.Set("send me")
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.messages) != n+1 {
		t.Errorf("enter should send: messages %d, want %d", len(m.messages), n+1)
	}
}

func TestTabBarKeepsProjectVisibleWithLongSessionAndBranch(t *testing.T) {
	m, _ := newTestModel(t)
	m.SetSize(120, 30)
	m.sessionTitle = strings.Repeat("long-session-title-", 4)
	project := m.orch.ProjectName()
	view := stripANSI(m.renderTabBar())
	if !strings.Contains(view, "◇ "+project) {
		t.Fatalf("tab bar hid project identity:\n%s", view)
	}
}

func TestEditorFinishedReinjectsBuffer(t *testing.T) {
	m, _ := newTestModel(t)
	path := filepath.Join(t.TempDir(), "msg.md")
	if err := os.WriteFile(path, []byte("  drafted in vi  "), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.editFile = path
	m.handleExecFinished(nil)
	if got := m.input.Value(); got != "drafted in vi" {
		t.Errorf("input = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("temp file must be removed after import")
	}
}

func TestEditorFinishedEmptyBuffer(t *testing.T) {
	m, _ := newTestModel(t)
	path := filepath.Join(t.TempDir(), "msg.md")
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.editFile = path
	m.input.Set("keep me")
	m.handleExecFinished(nil)
	if got := m.input.Value(); got != "keep me" {
		t.Errorf("input must be untouched on empty buffer, got %q", got)
	}
}

func TestEditorFinishedErrorKeepsPrompt(t *testing.T) {
	m, _ := newTestModel(t)
	m.editFile = "nonexistent"
	m.input.Set("keep me")
	m.handleExecFinished(fmt.Errorf("editor blew up"))
	if got := m.input.Value(); got != "keep me" {
		t.Errorf("input must be untouched on editor error, got %q", got)
	}
}

func TestOpenEditorBusyBlocked(t *testing.T) {
	m, _ := newTestModel(t)
	m.busy = true
	if cmd := m.openEditor(); cmd != nil {
		t.Error("openEditor must not run while busy")
	}
	if m.editFile != "" {
		t.Error("editFile must stay empty while busy")
	}
}

func TestOpenEditorWritesDraft(t *testing.T) {
	m, _ := newTestModel(t)
	m.input.Set("my draft")
	t.Setenv("EDITOR", "/bin/true")
	cmd := m.openEditor()
	if m.editFile == "" {
		t.Fatal("editFile must be set")
	}
	data, err := os.ReadFile(m.editFile)
	if err != nil {
		t.Fatalf("draft file: %v", err)
	}
	if string(data) != "my draft" {
		t.Errorf("draft = %q", data)
	}
	if cmd == nil {
		t.Error("openEditor must return a command")
	}
	_ = os.Remove(m.editFile)
	m.editFile = ""
}

func TestFinishFooterModelDuration(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.orch.SetModel("claude-sonnet-4")
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "done work"})})
	last := m.lastAssistant()
	if last == nil || last.Model == "" {
		t.Fatalf("assistant message must record the active model, got %+v", last)
	}
	if !last.FinishedAt.IsZero() {
		t.Fatal("unfinished message must not have a finish time")
	}
	view := stripANSI(m.renderRoleMessage(last, 80))
	if strings.Contains(view, "· "+last.Model) {
		t.Errorf("footer shown while running: %q", view)
	}
	// Finish with a sub-second turn: footer hidden.
	last.StartedAt = time.Now().Add(-500 * time.Millisecond)
	last.FinishedAt = time.Now()
	view = stripANSI(m.renderRoleMessage(last, 80))
	if strings.Contains(view, last.Model) {
		t.Errorf("footer shown for <1s turn: %q", view)
	}
	// Finish with a 3s turn: footer shows model · duration.
	last.busy = false
	last.StartedAt = time.Now().Add(-3 * time.Second)
	last.FinishedAt = time.Now()
	last.cachedValid = false
	view = stripANSI(m.renderRoleMessage(last, 80))
	if !strings.Contains(view, last.Model) || !strings.Contains(view, "3s") {
		t.Errorf("footer missing: %q", view)
	}
}

func TestActionLabel(t *testing.T) {
	cases := []struct{ name, want string }{
		{"bash", "Running command…"},
		{"read", "Reading file…"},
		{"view", "Reading file…"},
		{"grep", "Searching content…"},
		{"write", "Preparing write…"},
		{"glob", "Finding files…"},
		{"ask", "Asking…"},
		{"mystery", "Working…"},
	}
	for _, c := range cases {
		if got := actionLabel(c.name); got != c.want {
			t.Errorf("actionLabel(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRunningCardShowsActionLabel(t *testing.T) {
	styles := NewStyles(Charmtone())
	card := &Card{ID: "c", Name: "bash", Status: "running"}
	view := stripANSI(card.Render(styles, 60))
	if !strings.Contains(view, "Running command…") {
		t.Errorf("running card missing action label: %q", view)
	}
	card.Detail = "nproc"
	view = stripANSI(card.Render(styles, 60))
	if strings.Contains(view, "Running command…") {
		t.Errorf("card with detail must not show action label: %q", view)
	}
	if !strings.Contains(view, "nproc") {
		t.Errorf("card detail missing: %q", view)
	}
}

func TestActivityUsesRotatingOrchestralVerbs(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.runStart = time.Now()
	m.pulse = 0
	if got := m.activity(); got != "Composing." {
		t.Errorf("first orchestral activity = %q", got)
	}
	m.pulse = 2
	if got := m.activity(); got != "Composing..." {
		t.Errorf("animated ellipsis = %q", got)
	}
	m.runStart = time.Now().Add(-6500 * time.Millisecond)
	if got := m.activity(); got != "Orchestrating..." {
		t.Errorf("rotated orchestral activity = %q", got)
	}
}

func TestCompactParams(t *testing.T) {
	if got := compactParams("short", 60); got != "short" {
		t.Errorf("short = %q", got)
	}
	long := "a very long tool summary line that must be truncated to fit the card width budget"
	got := compactParams(long, 20)
	if len([]rune(got)) != 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated = %q (len %d)", got, len([]rune(got)))
	}
	if got := compactParams(long, 0); got != "" {
		t.Errorf("zero budget = %q", got)
	}
}

func TestCardDetailWidthBudget(t *testing.T) {
	styles := NewStyles(Charmtone())
	long := strings.Repeat("x", 200)
	card := &Card{ID: "c", Name: "read", Status: "done", Detail: long}
	narrow := stripANSI(card.Render(styles, 40))
	if strings.Contains(narrow, strings.Repeat("x", 200)) {
		t.Errorf("narrow card must truncate the detail: %q", narrow)
	}
	if !strings.Contains(narrow, "…") {
		t.Errorf("narrow card must show an ellipsis: %q", narrow)
	}
	card.Detail = "one line"
	wide := stripANSI(card.Render(styles, 120))
	if !strings.Contains(wide, "one line") {
		t.Errorf("short detail must pass through: %q", wide)
	}
}

func TestProposalCardShowsInlineDiffLikeAgentMockup(t *testing.T) {
	styles := NewStyles(Charmtone())
	card := &Card{
		ID: "proposal", Status: "proposed", ProposalPath: "internal/store.go",
		Proposal: &proposals.Proposal{Path: "/workspace/internal/store.go", Hunks: []proposals.Hunk{{
			Start: 84, OldLines: []string{"return nil"},
			NewLines: []string{"// ensure uniqueness", "return validateEmail(email)"},
		}}},
	}
	collapsed := stripANSI(card.Render(styles, 78))
	if strings.Contains(collapsed, "ensure uniqueness") || !strings.Contains(collapsed, "click to expand") {
		t.Fatalf("document proposal must be collapsed by default: %q", collapsed)
	}
	card.Expanded = true
	view := stripANSI(card.Render(styles, 78))
	for _, want := range []string{"internal/store.go", "+2", "-1", "@@ line 84", "-  return nil", "+  // ensure uniqueness", "a accept", "d discard"} {
		if !strings.Contains(view, want) {
			t.Errorf("proposal preview missing %q: %q", want, view)
		}
	}
}

func TestReasoningStreamsIntoCollapsedExpandableBlock(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	reasoning := "I should inspect the repository before choosing the implementation."
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvReasoningDelta, agentcore.ReasoningDelta{Text: reasoning})})
	collapsedFrame := m.lastContent
	moreReasoning := " Then I should verify the tests."
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvReasoningDelta, agentcore.ReasoningDelta{Text: moreReasoning})})
	reasoning += moreReasoning
	last := m.lastAssistant()
	if last == nil || last.think == nil || !last.think.Reasoning {
		t.Fatalf("reasoning block was not created: %+v", last)
	}
	if strings.Contains(last.Text, reasoning) {
		t.Fatal("reasoning leaked into the assistant response")
	}
	if m.lastContent != collapsedFrame {
		t.Fatal("collapsed reasoning rebuilt the transcript for a hidden delta")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Thinking") || strings.Contains(view, reasoning) {
		t.Fatalf("reasoning must be collapsed by default: %q", view)
	}

	m.focus = FocusViewport
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	view = stripANSI(m.View())
	if !strings.Contains(view, "I should inspect the repository") || !strings.Contains(view, "should verify the tests") {
		t.Fatalf("expanded reasoning body is missing: %q", view)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "Final answer"})})
	if last.think.Status != "done" || !strings.Contains(last.Text, "Final answer") {
		t.Fatalf("reasoning did not close before answer: think=%+v text=%q", last.think, last.Text)
	}
}

func TestWriteToolCallIsCollapsedDocumentBlock(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	body := "# Release notes\n\nA private draft that must stay folded."
	args := `{"path":"docs/release.md","content":"# Release notes\n\nA private draft that must stay folded."}`
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvToolCall, agentcore.ToolCall{
		ID: "write-doc", Name: "write", Args: args,
	})})
	card := m.lastAssistant().Cards[0]
	if card.Kind != "write" || card.Detail != "docs/release.md" || card.Full != body {
		t.Fatalf("write presentation = %+v", card)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "write  docs/release.md") || strings.Contains(view, "private draft") {
		t.Fatalf("write content must be folded by default: %q", view)
	}
	card.Expanded = true
	m.renderMessages()
	if view = stripANSI(m.View()); !strings.Contains(view, "private draft") {
		t.Fatalf("expanded write content is missing: %q", view)
	}
}

func TestAgentMessageUsesClockHeaderWithoutLegacyContainer(t *testing.T) {
	m, _ := newTestModel(t)
	msg := &Message{Role: "assistant", Text: "Ready to make the change.", State: "chat", ts: time.Date(2026, 8, 7, 10, 21, 0, 0, time.Local)}
	view := stripANSI(m.renderRoleMessage(msg, 80))
	if !strings.Contains(view, "Maestro  10:21") {
		t.Fatalf("agent clock header missing: %q", view)
	}
	if strings.Contains(view, "CHAT") || strings.ContainsAny(view, "┌└") {
		t.Fatalf("legacy message chrome leaked into transcript: %q", view)
	}
}

func TestStreamTextRenders(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "Hello "})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "world"})})
	last := m.lastAssistant()
	if last == nil || last.Text != "Hello world" {
		t.Fatalf("assistant text = %+v", last)
	}
	view := m.View()
	clean := stripANSI(view)
	if !strings.Contains(clean, "Hello world") {
		t.Errorf("view missing text: %q", clean)
	}
}

func TestSendAppendsUserMessage(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.input.Set("hello maestro")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send should return a cmd")
	}
	m2 := updated.(*Model)
	if len(m2.messages) == 0 || m2.messages[0].Role != "user" || m2.messages[0].Text != "hello maestro" {
		t.Fatalf("messages = %+v", m2.messages)
	}
	if m2.input.String() != "" {
		t.Error("input should be cleared after send")
	}
}

func TestWriteCardAcceptApplies(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	// Stage a write through the staging tool (as the loop would).
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path":    filepath.Join(dir, "target.txt"),
		"content": "one\nTWO\n",
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	if !strings.Contains(out, "staged") {
		t.Fatalf("tool output = %q", out)
	}
	// Feed the tool result as a stream event.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "t1", Name: "write", Output: out,
	})})
	if len(m.pending) != 1 {
		t.Fatalf("pending cards = %d, want 1", len(m.pending))
	}
	card := m.pending[0]
	if card.Status != "proposed" {
		t.Fatalf("card status = %q", card.Status)
	}

	// Keyboard accept ('a') also works from the normal empty composer. The
	// proposal hint is visible while this focus is active, so swallowing the
	// key as text would leave the review/HITL flow blocked.
	m.focus = FocusInput
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	data, err := os.ReadFile(filepath.Join(dir, "target.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "one\nTWO\n" {
		t.Errorf("file after accept = %q", data)
	}
	if len(m.pending) != 0 {
		t.Error("pending should be cleared after accept")
	}
}

func TestProposalShortcutDoesNotStealDraftText(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path": filepath.Join(dir, "target.txt"), "content": "one\nTWO\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "draft-proposal", Name: "write", Output: out,
	})})
	m.focus = FocusInput
	m.input.Set("draft")
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := m.input.Value(); got != "drafta" {
		t.Fatalf("proposal shortcut stole normal input: %q", got)
	}
	if len(m.pending) != 1 {
		t.Fatal("typing into a non-empty composer must not accept the proposal")
	}
}

func TestDiffOverlayAcceptShortcutClosesAndResolvesHITL(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 34})
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path": filepath.Join(dir, "target.txt"), "content": "one\nTWO\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "overlay-proposal", Name: "write", Output: out,
	})})
	m.sidebar.setItem(agentcore.HITLItem{ID: "hitl", Item: "Complete the HITL items below", Status: "pending"})
	m.sidebar.setItem(agentcore.HITLItem{ID: "diff", Item: "Review the git diff", Status: "pending"})
	m.overlay = overlayDiff
	m.overlayM = newDiffOverlay(m.styles, m.pending[0].Proposal, 90)
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.overlay != overlayNone || len(m.pending) != 0 {
		t.Fatalf("diff accept left overlay=%v pending=%d", m.overlay, len(m.pending))
	}
	if !m.sidebar.checked["diff"] {
		t.Fatalf("review decision did not resolve HITL: %+v", m.sidebar.checked)
	}
	data, err := os.ReadFile(filepath.Join(dir, "target.txt"))
	if err != nil || string(data) != "one\nTWO\n" {
		t.Fatalf("accepted diff was not applied: %q, %v", data, err)
	}
}

func TestIDEHITLRailAcceptsPendingProposal(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 150, Height: 36})
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path": filepath.Join(dir, "target.txt"), "content": "one\nTWO\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "ide-proposal", Name: "write", Output: out,
	})})
	m.ToggleIDE()
	m.ide.Focus = ideHITL
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if len(m.pending) != 0 {
		t.Fatal("IDE HITL rail swallowed the accept shortcut")
	}
}

func TestDiffOverlayDiscardShortcutClosesWithoutApplying(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 34})
	target := filepath.Join(dir, "target.txt")
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path": target, "content": "discard me\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "discard-overlay", Name: "write", Output: out,
	})})
	m.overlay = overlayDiff
	m.overlayM = newDiffOverlay(m.styles, m.pending[0].Proposal, 90)
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if m.overlay != overlayNone || len(m.pending) != 0 {
		t.Fatalf("diff discard left overlay=%v pending=%d", m.overlay, len(m.pending))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("discard applied the staged content: %q", data)
	}
}

func TestDiffMouseToIDEReviewButtonsResolveHITL(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 150, Height: 38})
	target := filepath.Join(dir, "target.txt")
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path": target, "content": strings.Repeat("proposed line\n", 80),
	})
	if err != nil {
		t.Fatal(err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "mouse-review", Name: "write", Output: out,
	})})
	m.sidebar.setItem(agentcore.HITLItem{ID: "hitl", Item: "Complete the HITL items below", Status: "pending"})
	m.sidebar.setItem(agentcore.HITLItem{ID: "diff", Item: "Review the git diff", Status: "pending"})
	m.overlay = overlayDiff
	diff := newDiffOverlay(m.styles, m.pending[0].Proposal, 100)
	m.overlayM = diff
	view := stripANSI(m.View())

	var openIDE Region
	for _, region := range m.regions {
		if region.Action == ActionOpenProposalIDE {
			openIDE = region
			break
		}
	}
	if openIDE.W == 0 {
		t.Fatal("diff overlay did not register the → IDE action")
	}
	footerRow := -1
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, diffOverlayFooter) {
			footerRow = i
			break
		}
	}
	if footerRow < 0 || openIDE.Y != footerRow {
		t.Fatalf("IDE action is not aligned with visible footer: region=%+v footer=%d", openIDE, footerRow)
	}
	feed(m, tea.MouseMsg{X: openIDE.X, Y: openIDE.Y - 1, Button: tea.MouseButtonWheelDown})
	if diff.scroll == 0 {
		t.Fatal("mouse wheel did not scroll the diff popup")
	}
	feed(m, tea.MouseMsg{X: openIDE.X + 1, Y: openIDE.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.ActiveTab() != TabIDE || m.ide == nil || m.ide.proposalPreview == nil {
		t.Fatal("→ IDE did not open the proposal preview")
	}

	view = stripANSI(m.View())
	for _, want := range []string{"PROPOSAL DIFF · READ ONLY", "Accept", "Decline"} {
		if !strings.Contains(view, want) {
			t.Fatalf("IDE proposal workspace missing %q", want)
		}
	}
	_, treeW, _ := m.idePaneWidths()
	feed(m, tea.MouseMsg{X: treeW + 2, Y: m.ideCodeTop() + 2, Button: tea.MouseButtonWheelDown})
	if m.ide.proposalScroll == 0 {
		t.Fatal("IDE proposal preview did not scroll")
	}

	m.View() // refresh IDE button regions
	var accept, decline Region
	for _, region := range m.regions {
		if region.Action == ActionAccept && region.CardID == m.pending[0].ID {
			accept = region
		}
		if region.Action == ActionDiscard && region.CardID == m.pending[0].ID {
			decline = region
		}
	}
	if accept.W == 0 || decline.W == 0 {
		t.Fatalf("IDE review buttons are incomplete: accept=%+v decline=%+v", accept, decline)
	}
	feed(m, tea.MouseMsg{X: accept.X + 1, Y: accept.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if len(m.pending) != 0 || !m.sidebar.checked["diff"] {
		t.Fatalf("IDE Accept did not settle proposal/HITL: pending=%d checked=%+v", len(m.pending), m.sidebar.checked)
	}
	if m.ide.proposalPreview != nil {
		t.Fatal("resolved proposal preview remained open")
	}
}

func TestWriteCardDiscardKeepsFile(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path":    filepath.Join(dir, "target.txt"),
		"content": "completely different\n",
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "t1", Name: "write", Output: out,
	})})
	m.focus = FocusViewport
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	data, err := os.ReadFile(filepath.Join(dir, "target.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "one\ntwo\n" {
		t.Errorf("file after discard = %q, want untouched", data)
	}
}

func TestKeymapActions(t *testing.T) {
	tests := []struct {
		key    tea.KeyMsg
		action ActionID
		ok     bool
	}{
		{tea.KeyMsg{Type: tea.KeyEnter}, ActionSend, true},
		{tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, ActionNewline, true},
		{tea.KeyMsg{Type: tea.KeyCtrlP}, ActionPalette, true},
		{tea.KeyMsg{Type: tea.KeyCtrlL}, ActionModelPicker, true},
		{tea.KeyMsg{Type: tea.KeyCtrlR}, ActionSessionPicker, true},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, ActionCancelTour, true},
		{tea.KeyMsg{Type: tea.KeyPgUp}, ActionScrollUp, true},
		{tea.KeyMsg{Type: tea.KeyPgDown}, ActionScrollDown, true},
		{tea.KeyMsg{Type: tea.KeyTab}, ActionFocusNext, true},
		{tea.KeyMsg{Type: tea.KeyEsc}, ActionEscape, true},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}, "", false},
	}
	for _, tt := range tests {
		action, ok := ActionFor(tt.key)
		if ok != tt.ok || (tt.ok && action != tt.action) {
			t.Errorf("ActionFor(%v) = %s, %v; want %s, %v", tt.key, action, ok, tt.action, tt.ok)
		}
	}
}

func TestKeymapViewer(t *testing.T) {
	view := KeymapView(NewStyles(Charmtone()), 60)
	for _, want := range []string{"ctrl+p", "ctrl+l", "enter", "esc", "alt+1", "alt+2"} {
		if !strings.Contains(view, want) {
			t.Errorf("keymap viewer missing %q", want)
		}
	}
}

func TestKeymapMultiColumn(t *testing.T) {
	styles := NewStyles(Charmtone())
	rows := keymapRows()
	// 120 columns fit two blocks of 10 → the joined layout has 2 columns.
	wide := renderHelpColumns(styles, 120, rows)
	wideLines := strings.Split(wide, "\n")
	if len(wideLines) < 10 {
		t.Fatalf("wide columns too short: %d lines", len(wideLines))
	}
	if !strings.Contains(wideLines[0], "Send message") {
		t.Errorf("first column missing first binding: %q", stripANSI(wideLines[0]))
	}
	// 60 columns fall back to a single column with every row.
	narrow := renderHelpColumns(styles, 60, rows)
	if got := strings.Count(narrow, "\n") + 1; got != len(rows) {
		t.Errorf("narrow single column has %d rows, want %d", got, len(rows))
	}
	// Dedup: a duplicated key keeps its first binding.
	dup := append(append([]keyRow{}, rows...), keyRow{key: "ctrl+p", desc: "shadow"})
	out := renderHelpColumns(styles, 200, dup)
	if strings.Contains(out, "shadow") {
		t.Errorf("duplicated key must keep the first binding: %q", stripANSI(out))
	}
	if !strings.Contains(out, "Command palette") {
		t.Errorf("first binding description lost: %q", stripANSI(out))
	}
}

func TestPermissionPromptFlow(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	req := &permissionRequest{
		Call:    agentcore.ToolCall{ID: "c1", Name: "bash", Args: `{"command":"rm -rf /"}`},
		Spec:    agentcore.ToolSpec{Name: "bash", NeedsApproval: true},
		Respond: make(chan error, 1),
	}
	feed(m, permRequestMsg{req: req})
	if m.dialogs.empty() {
		t.Fatal("permission dialog not pushed")
	}
	view := m.View()
	if !strings.Contains(stripANSI(view), "Permission required") {
		t.Errorf("dialog view missing title: %q", stripANSI(view))
	}

	// [s] Always allow approves and changes the queue policy for this session.
	reqSession := &permissionRequest{
		Call:    agentcore.ToolCall{ID: "c-session", Name: "bash", Args: `{"command":"go test ./..."}`},
		Spec:    agentcore.ToolSpec{Name: "bash", NeedsApproval: true},
		Respond: make(chan error, 1),
	}
	feed(m, permRequestMsg{req: reqSession})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if err := <-reqSession.Respond; err != nil {
		t.Fatalf("always allow returned %v", err)
	}
	if !m.perm.toolAllowed("bash") {
		t.Fatal("always allow did not grant bash for the session")
	}
	if got := m.perm.currentMode(); got != "ask" {
		t.Fatalf("always allow changed global permission mode to %q", got)
	}
	// [a] Allow approves; esc denies.
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !m.dialogs.empty() {
		t.Fatal("dialog should close after decision")
	}
	select {
	case err := <-req.Respond:
		if err != nil {
			t.Errorf("approve should return nil, got %v", err)
		}
	default:
		t.Error("respond channel not answered")
	}

	// Deny path.
	req2 := &permissionRequest{
		Call:    agentcore.ToolCall{ID: "c2", Name: "bash", Args: `{}`},
		Spec:    agentcore.ToolSpec{Name: "bash", NeedsApproval: true},
		Respond: make(chan error, 1),
	}
	feed(m, permRequestMsg{req: req2})
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case err := <-req2.Respond:
		if err == nil {
			t.Error("esc should deny")
		}
	default:
		t.Error("respond channel not answered on deny")
	}
}

func TestPermissionPromptMouseActions(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	req := &permissionRequest{
		Call:    agentcore.ToolCall{ID: "mouse", Name: "write", Args: `{"path":"internal/store.go"}`},
		Spec:    agentcore.ToolSpec{Name: "write", NeedsApproval: true},
		Respond: make(chan error, 1),
	}
	feed(m, permRequestMsg{req: req})
	_ = m.View() // lays out the dock's mouse hit targets
	dialog, ok := m.dialogs.items[len(m.dialogs.items)-1].(*permissionDialog)
	if !ok || len(dialog.buttons) != 3 {
		t.Fatalf("permission buttons = %+v", dialog)
	}
	reject := dialog.buttons[2]
	feed(m, tea.MouseMsg{X: reject.x + 1, Y: reject.y, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	if dialog.buttonSel != 2 {
		t.Fatalf("hover selection = %d, want reject", dialog.buttonSel)
	}
	feed(m, tea.MouseMsg{X: reject.x + 1, Y: reject.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !m.dialogs.empty() {
		t.Fatal("mouse decision did not close permission dock")
	}
	if err := <-req.Respond; err == nil {
		t.Fatal("reject click approved the request")
	}
}

func TestPermissionQueueAuthorize(t *testing.T) {
	perm := NewPermissionQueue(2)
	done := make(chan error, 1)
	go func() {
		done <- perm.Authorize(context.Background(), agentcore.ToolCall{Name: "bash"}, agentcore.ToolSpec{Name: "bash", NeedsApproval: true})
	}()
	select {
	case req := <-perm.req:
		req.Respond <- nil
	case <-done:
		t.Fatal("authorize should block until answered")
	}
	if err := <-done; err != nil {
		t.Errorf("Authorize = %v, want nil", err)
	}
	// Read-only tools never ask.
	err := perm.Authorize(context.Background(), agentcore.ToolCall{Name: "read"}, agentcore.ToolSpec{Name: "read"})
	if err != nil {
		t.Errorf("read-only authorize = %v", err)
	}
}

func TestModelConsumesItsPermissionQueue(t *testing.T) {
	m, _ := newTestModel(t)
	if m.perm == nil {
		t.Fatal("permission queue is nil")
	}
	if m.permReq != m.perm.req {
		t.Fatal("model permission pump is disconnected from the gate queue")
	}
}

func TestWriteCardMouseClickAccepts(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(context.Background(), map[string]any{
		"path":    filepath.Join(dir, "target.txt"),
		"content": "one\nTWO\n",
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "t1", Name: "write", Output: out,
	})})

	// Render fills the clickable regions, then click the accept button.
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.View()
	found := false
	for _, r := range m.regions {
		if r.Action == ActionAccept && r.CardID == m.pending[0].ID {
			feed(m, tea.MouseMsg{X: r.X + 1, Y: r.Y, Button: tea.MouseButtonLeft})
			found = true
			break
		}
	}
	if !found {
		t.Fatal("accept region not registered")
	}
	data, err := os.ReadFile(filepath.Join(dir, "target.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "one\nTWO\n" {
		t.Errorf("file after mouse accept = %q", data)
	}
}

func TestToolCardsHaveStableUniqueIdentity(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.appendAssistant("")
	m.lastAssistant().busy = true

	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolCall, agentcore.ToolCall{ID: "call-1", Name: "read", Args: `{"path":"a.go"}`})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolCall, agentcore.ToolCall{ID: "call-2", Name: "grep", Args: `{"pattern":"x"}`})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{ID: "call-1", Name: "read", Output: "done"})})

	cards := m.lastAssistant().Cards
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2: %+v", len(cards), cards)
	}
	if cards[0].ID == cards[1].ID {
		t.Fatalf("duplicate card id %q", cards[0].ID)
	}
	if cards[0].Status != "done" || cards[1].Status != "running" {
		t.Fatalf("card states = %s, %s", cards[0].Status, cards[1].Status)
	}
}

func TestInputBehavior(t *testing.T) {
	in := newInputBox(NewStyles(Charmtone()))
	in.Set("hello")
	if in.String() != "hello" {
		t.Errorf("input = %q", in.String())
	}
	// typing appends
	in.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	if in.String() != "hello!" {
		t.Errorf("after type = %q", in.String())
	}
	// shift+enter newline
	in.insertNewline()
	if in.String() != "hello!\n" {
		t.Errorf("after newline = %q", in.String())
	}
	// history: send pushes, up recalls, down returns
	in.Set("first prompt")
	in.pushHistory("first prompt")
	in.Set("second prompt")
	in.pushHistory("second prompt")
	in.Set("")
	in.update(tea.KeyMsg{Type: tea.KeyUp})
	if in.String() != "second prompt" {
		t.Errorf("history up = %q", in.String())
	}
	in.update(tea.KeyMsg{Type: tea.KeyUp})
	if in.String() != "first prompt" {
		t.Errorf("history up2 = %q", in.String())
	}
	in.update(tea.KeyMsg{Type: tea.KeyDown})
	if in.String() != "second prompt" {
		t.Errorf("history down = %q", in.String())
	}
	in.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[<35;218;76m")})
	if strings.Contains(in.String(), "35;218;76") {
		t.Error("terminal mouse escape sequence should not enter the input")
	}
}

func TestSidebarHITLToggle(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sidebar.setItem(agentcore.HITLItem{ID: "env:DATABASE_URL", Item: "Fill .env with DATABASE_URL", Status: "pending"})
	if m.sidebar.allChecked() {
		t.Fatal("should not be all-checked initially")
	}
	m.sidebar.toggleAt(0)
	if !m.sidebar.checked["env:DATABASE_URL"] || !m.sidebar.allChecked() {
		t.Error("toggle should check the item")
	}
}

func stripANSI(s string) string {
	re := ansiRegexp()
	return re.ReplaceAllString(s, "")
}

func ansiRegexp() *regexp.Regexp {
	return regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\[[0-9;?]*m`)
}

func TestSidebarReflectsAgents(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "dev", Status: "running", Detail: "spec-1"})})
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.View()
	if !strings.Contains(view, "dev") {
		t.Errorf("sidebar missing dev agent: %q", view)
	}
}

func TestCardRendersChrome(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	card := &Card{ID: "c1", Name: "read", Status: "done", Detail: "3 files", Full: "full output\nline2"}
	m.appendSystemCard(card)
	view := m.View()
	clean := stripANSI(view)
	if !strings.Contains(clean, "read") || !strings.Contains(clean, "click to expand") {
		t.Errorf("card chrome missing: %q", clean)
	}
	// The persistent Agent rail exposes approvals and run state.
	if !strings.Contains(clean, "HUMAN ACTIONS") || !strings.Contains(clean, "RUNS") {
		t.Errorf("sidebar sections missing: %q", clean)
	}
}

func TestToolCardClickActuallyExpandsOutput(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	card := &Card{ID: "clickable-tool", Name: "read", Status: "done", Detail: "README.md", Full: "expanded output"}
	m.appendSystemCard(card)
	m.View()
	var hit Region
	for _, region := range m.regions {
		if region.Action == ActionToggleCard && region.CardID == card.ID {
			hit = region
			break
		}
	}
	if hit.W == 0 || hit.H == 0 {
		t.Fatal("tool card did not register a clickable surface")
	}
	feed(m, tea.MouseMsg{X: hit.X + 1, Y: hit.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !card.Expanded {
		t.Fatal("tool card click did not expand its output")
	}
}

func TestErrorCardCanPrepareFixWithMaestro(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	card := &Card{ID: "failed-test", Name: "bash", Status: "error", Detail: "internal/store.go:12:4: undefined symbol", Full: "go test ./... failed"}
	m.appendSystemCard(card)
	m.View()
	var fix Region
	for _, region := range m.regions {
		if region.Action == ActionAskFix && region.CardID == card.ID {
			fix = region
			break
		}
	}
	if fix.W == 0 {
		t.Fatal("error card has no Fix with Maestro action")
	}
	feed(m, tea.MouseMsg{X: fix.X, Y: fix.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !strings.Contains(m.input.Value(), "internal/store.go:12:4") || !strings.Contains(m.input.Value(), "smallest safe change") {
		t.Fatalf("fix prompt = %q", m.input.Value())
	}
}

func TestStatuslineRenders(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	view := m.View()
	clean := stripANSI(view)
	if !strings.Contains(clean, "AGENT") || !strings.Contains(clean, "Maestro is ready") {
		t.Errorf("statusline missing: %q", clean)
	}
	if !strings.Contains(clean, "enter") || !strings.Contains(clean, "commands") {
		t.Errorf("composer actions missing: %q", clean)
	}
}

func TestSpaceQuestionOpensHelp(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, tea.KeyMsg{Type: tea.KeySpace})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.overlay != overlayKeymap {
		t.Fatalf("overlay = %v, want keymap", m.overlay)
	}
	view := m.View()
	if !strings.Contains(view, "Mouse") || !strings.Contains(view, "Ask about selection") {
		t.Fatalf("help view missing sections: %q", stripANSI(view))
	}
	if got := lipgloss.Height(view); got > 38 {
		t.Fatalf("help view height=%d > 38", got)
	}
}

func TestMouseDragCreatesChatSelectionMenu(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.appendAssistant("select this text")
	m.View() // registers the current viewport geometry and chat row map.
	row := -1
	for i, chatRow := range m.chatRows {
		if chatRow.Message != nil && chatRow.TextLine == 0 {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatal("assistant text was not mapped to a selectable chat row")
	}
	y := tabBarRows + row - m.viewport.YOffset
	feed(m, tea.MouseMsg{X: 1, Y: y, Button: tea.MouseButtonLeft})
	feed(m, tea.MouseMsg{X: 8, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	feed(m, tea.MouseMsg{X: 8, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.selectionMenu == nil || m.selectionMenu.Context == nil {
		t.Fatal("chat drag should open a selection menu")
	}
	if m.selectionMenu.Context.Text != "select " {
		t.Fatalf("selected chat text = %q, want %q", m.selectionMenu.Context.Text, "select ")
	}
	if m.selectionMenu.Actions[0] == "edit selection" {
		t.Fatal("chat selection must not offer IDE-only direct edit")
	}
}

func TestMouseWheelScrollsChatTranscript(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 32})
	for i := 0; i < 40; i++ {
		m.appendAssistant(fmt.Sprintf("message %02d with enough text to occupy a transcript row", i))
	}
	m.renderMessages()
	m.viewport.GotoBottom()
	before := m.viewport.YOffset
	if before == 0 {
		t.Fatal("fixture did not create a scrollable transcript")
	}
	feed(m, tea.MouseMsg{X: 4, Y: tabBarRows + 4, Button: tea.MouseButtonWheelUp})
	if m.viewport.YOffset >= before {
		t.Fatalf("mouse wheel did not scroll transcript up: before=%d after=%d", before, m.viewport.YOffset)
	}
}

func TestMouseClickTogglesHITLAction(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.sidebar.setItem(agentcore.HITLItem{ID: "approval", Item: "Approve migration", Status: "pending"})
	m.View()
	var hit Region
	for _, region := range m.regions {
		if region.Action == ActionToggleHITL && region.Index == 0 {
			hit = region
			break
		}
	}
	if hit.W == 0 {
		t.Fatal("HITL mouse region was not registered")
	}
	feed(m, tea.MouseMsg{X: hit.X + 1, Y: hit.Y, Button: tea.MouseButtonLeft})
	if !m.sidebar.checked["approval"] {
		t.Fatal("mouse click did not toggle HITL action")
	}
}

func TestChatStateLabelsMessages(t *testing.T) {
	m, _ := newTestModel(t)
	m.chatState = "spec"
	m.appendAssistant("draft")
	if m.messages[len(m.messages)-1].State != "spec" {
		t.Fatalf("message state = %q", m.messages[len(m.messages)-1].State)
	}
	view := stripANSI(m.renderRoleMessage(m.messages[len(m.messages)-1], 80))
	if !strings.Contains(view, "SPEC") {
		t.Fatalf("state label missing: %q", view)
	}
}

func TestViewPaintsFullScreen(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	for _, mode := range []Tab{TabHarness, TabIDE} {
		m.SwitchTab(mode)
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) != 38 {
			t.Fatalf("tab %s lines = %d, want 38 (full-screen paint)", mode, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != 140 {
				t.Fatalf("tab %s line %d width = %d, want 140 (no unpainted gaps)", mode, i, got)
			}
		}
	}
}

var brokenANSIRe = regexp.MustCompile(`\x1b\[[0-9;?]*$`)

func TestStripBrokenANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[38;2;107;80;255mok", "\x1b[38;2;107;80;255mok"},
		{"ok\x1b[38;2;107;80", "ok"},
		{"ok\x1b[38;2;107;80;255", "ok"},
		{"ok\x1b", "ok"},
		{"ok", "ok"},
		{"\x1b[?25l", "\x1b[?25l"},
	}
	for _, c := range cases {
		if got := stripBrokenANSI(c.in); got != c.want {
			t.Errorf("stripBrokenANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestViewHasNoBrokenANSI(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	for _, mode := range []Tab{TabHarness, TabIDE} {
		m.SwitchTab(mode)
		view := m.View()
		for i, line := range strings.Split(view, "\n") {
			if brokenANSIRe.MatchString(line) {
				t.Fatalf("tab %s line %d ends with a broken escape sequence: %q", mode, i, stripANSI(line))
			}
		}
	}
}

func TestStatuslineToastKeepsANSIIntact(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	for _, msg := range []string{
		"settings updated",
		"CHAT → gpt-5.6-luna",
		"press esc again to cancel task",
		strings.Repeat("x", 300),
	} {
		m.status.pushToast("info", msg, 10*time.Second)
		view := m.View()
		for i, line := range strings.Split(view, "\n") {
			if brokenANSIRe.MatchString(line) {
				t.Fatalf("line %d broken after toast truncation: %q", i, stripANSI(line))
			}
		}
		want := truncateRunes(msg, 48)
		if !strings.Contains(stripANSI(view), want) {
			t.Fatalf("toast was clipped: got %q, want full %q", stripANSI(view), want)
		}
	}
}

func TestTerminalNoiseKeysAreDropped(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})

	// Harness chat input stays clean.
	m.input.Set("hello")
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;10;10M"), Alt: true})
	if m.input.Value() != "hello" {
		t.Fatalf("noise entered chat input: %q", m.input.Value())
	}
	// SGR payload arriving without its ESC prefix (ESC consumed separately)
	// must also be dropped, with or without Alt.
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;10;10M")})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[0;10;10M")})
	if m.input.Value() != "hello" {
		t.Fatalf("CSI payload entered chat input: %q", m.input.Value())
	}

	// IDE insert mode never writes noise into the buffer.
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.ide.Focus = ideEditor
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;11;11M"), Alt: true})
	if buf := m.ide.Ed.Buffer(); buf.LineText(0) != "package main" {
		t.Fatalf("noise entered editor buffer: %q", buf.LineText(0))
	}

	// Overlay filters stay clean.
	m.overlay = overlayModelPicker
	m.overlayM = newModelPickerOverlay(m.orch)
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;12;12M"), Alt: true})
	if list, ok := m.overlayM.(*listOverlay); ok && list.query != "" {
		t.Fatalf("noise entered picker filter: %q", list.query)
	}
}

func TestSidebarHITLKeyboardNav(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.sidebar.setItem(agentcore.HITLItem{ID: "a", Item: "first", Status: "pending"})
	m.sidebar.setItem(agentcore.HITLItem{ID: "b", Item: "second", Status: "pending"})
	m.focus = FocusSidebar
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.sidebar.selected != 1 {
		t.Errorf("j should move selection to 1, got %d", m.sidebar.selected)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.sidebar.selected != 0 {
		t.Errorf("k should move selection back to 0, got %d", m.sidebar.selected)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if !m.sidebar.checked["a"] {
		t.Error("space should toggle the selected item")
	}
}

func TestViewFitsConfiguredWidth(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	for _, mode := range []Tab{TabHarness, TabIDE} {
		m.SwitchTab(mode)
		view := m.View()
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > 140 {
				t.Fatalf("tab %s line %d width=%d > 140: %q", mode, i, got, stripANSI(line))
			}
		}
		if got := lipgloss.Height(view); got > 38 {
			t.Fatalf("tab %s height=%d > 38", mode, got)
		}
	}
}

func TestCompactViewFitsConfiguredHeight(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := lipgloss.Height(m.View()); got > 30 {
		t.Fatalf("compact view height=%d > 30", got)
	}
}

func TestCompactWelcomeUsesAvailableWidth(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.viewport.Width < 70 {
		t.Fatalf("compact viewport width = %d", m.viewport.Width)
	}
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "Discuss the change with Maestro") {
		t.Fatalf("compact welcome is clipped:\n%s", plain)
	}
}

func TestActivityPanelIsVisibleByDefaultAndCollapsible(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	if !m.showActivityRail() {
		t.Fatal("activity panel should be visible at startup")
	}
	if chatW, sideW := m.harnessPaneWidths(); chatW >= 140 || sideW == 0 {
		t.Fatalf("agent rail widths = %d/%d", chatW, sideW)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyCtrlB})
	if m.showActivityRail() {
		t.Fatal("ctrl+b should collapse the activity panel")
	}
	if chatW, sideW := m.harnessPaneWidths(); chatW != 140 || sideW != 0 {
		t.Fatalf("collapsed activity widths = %d/%d", chatW, sideW)
	}
}

func TestChatRowMappingWithThinkAndFooter(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	msg := &Message{
		Role: "assistant", Text: "line one\nline two", State: "chat",
		Model: "claude-sonnet-4",
		think: &thinkingState{Role: "dev", Status: "done",
			Started: time.Now().Add(-3 * time.Second), Done: time.Now()},
		busy: false, ts: time.Now(),
		StartedAt: time.Now().Add(-3 * time.Second), FinishedAt: time.Now(),
	}
	m.messages = append(m.messages, msg)
	m.renderMessages()

	var seq []int
	for _, cr := range m.chatRows {
		if cr.Message == msg {
			seq = append(seq, cr.TextLine)
		}
	}
	want := []int{-1, -1, 0, 1, -1} // header, think, line1, line2, footer
	if len(seq) != len(want) {
		t.Fatalf("row mapping = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Errorf("row %d textLine = %d, want %d (all: %v)", i, seq[i], want[i], seq)
		}
	}
}

func TestChatRowMappingNoThinkNoFooter(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	msg := &Message{Role: "assistant", Text: "a\nb\nc", State: "chat", ts: time.Now()}
	m.messages = append(m.messages, msg)
	m.renderMessages()
	var seq []int
	for _, cr := range m.chatRows {
		if cr.Message == msg {
			seq = append(seq, cr.TextLine)
		}
	}
	want := []int{-1, 0, 1, 2}
	if len(seq) != len(want) {
		t.Fatalf("row mapping = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Errorf("row %d textLine = %d, want %d (all: %v)", i, seq[i], want[i], seq)
		}
	}
}

func TestErrorFinalizesAssistantMessage(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "**partial** work\n\n```go\nx := 1\n```"})})
	last := m.lastAssistant()
	if last == nil || !last.busy {
		t.Fatalf("streaming message must be busy, got %+v", last)
	}
	// A stream error must finalize the message so glamour renders the
	// accumulated text instead of leaving it raw forever.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvError, agentcore.StreamError{Message: "boom"})})
	if last.busy {
		t.Fatal("message must leave busy after EvError")
	}
	if last.FinishedAt.IsZero() {
		t.Fatal("message must be finalized after EvError")
	}
	view := stripANSI(m.renderRoleMessage(last, 60))
	if strings.Contains(view, "**partial**") || strings.Contains(view, "```") {
		t.Errorf("errored message must render markdown, got raw: %q", view)
	}
	if !strings.Contains(view, "partial") {
		t.Errorf("partial text lost: %q", view)
	}
}

func TestChatDoneFinalizesAssistantMessage(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "work in progress"})})
	last := m.lastAssistant()
	feed(m, chatDoneMsg{err: fmt.Errorf("send failed")})
	if last.busy {
		t.Fatal("chatDoneMsg must finalize the assistant message")
	}
	view := stripANSI(m.renderRoleMessage(last, 60))
	if !strings.Contains(view, "work in progress") {
		t.Errorf("text lost after finalize: %q", view)
	}
}

func TestChatDoneDoesNotDuplicateStreamError(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	streamError := agentcore.StreamError{Message: "provider failed"}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvError, streamError)})
	feed(m, chatDoneMsg{err: fmt.Errorf("provider failed")})

	count := 0
	for _, message := range m.messages {
		if message.Role == "system" && message.Text == "error: provider failed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("stream + completion rendered %d identical errors, want 1", count)
	}
}

func TestCancelWaitsForWorkerBeforeFinalizingAssistant(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "orchestrator", Status: "running", Detail: "composing",
	})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "half"})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvToolCall, agentcore.ToolCall{
		ID: "cancel-tool", Name: "bash", Args: `{"command":"sleep 60"}`,
	})})
	last := m.lastAssistant()
	m.busy = true
	m.cancelRun = func() {}
	feed(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !last.busy || !m.busy || !m.cancelling {
		t.Fatal("cancel must keep the run busy until its worker completion")
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "orchestrator", Status: "cancelled", Detail: "cancelled by user",
	})})
	feed(m, chatDoneMsg{err: context.Canceled})
	if last.busy || m.busy || m.cancelling {
		t.Fatal("worker completion must finalize the cancelled run")
	}
	if last.think == nil || last.think.Status != "cancelled" || last.think.Done.IsZero() {
		t.Fatalf("cancelled thinking state = %+v", last.think)
	}
	if len(last.Cards) != 1 || last.Cards[0].Status != "cancelled" {
		t.Fatalf("cancelled tool state = %+v", last.Cards)
	}
	view := stripANSI(m.renderRoleMessage(last, 70))
	if !strings.Contains(view, "cancelled after") || strings.Contains(view, "worked") {
		t.Fatalf("cancelled turn presentation = %q", view)
	}
	for _, message := range m.messages {
		if message.State == "error" || strings.HasPrefix(message.Text, "error:") {
			t.Fatalf("cancellation rendered as error: %+v", message)
		}
	}
}

func TestDoubleEscapeCancelsActiveTask(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "half"})})
	cancelled := 0
	m.busy = true
	m.runStart = time.Now()
	m.cancelRun = func() { cancelled++ }

	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 0 || !m.busy || !m.escapeArmed {
		t.Fatalf("first esc must only arm cancellation: cancelled=%d busy=%v armed=%v", cancelled, m.busy, m.escapeArmed)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 1 || !m.busy || !m.cancelling || m.cancelRun != nil {
		t.Fatalf("second esc did not request cancellation safely: cancelled=%d busy=%v cancelling=%v cancel=%v", cancelled, m.busy, m.cancelling, m.cancelRun != nil)
	}
	if last := m.lastAssistant(); last == nil || !last.busy {
		t.Fatalf("assistant finalized before worker stopped: %+v", last)
	}
}

func TestCancellationBlocksOverlappingRunUntilCompletion(t *testing.T) {
	m, _ := newTestModel(t)
	m.busy = true
	m.cancelRun = func() {}
	m.cancelActiveTask()
	if cmd := m.dispatch(orchestrator.Command{Cmd: "resume"}); cmd != nil {
		t.Fatal("a cancelling run must block a replacement run")
	}
	feed(m, chatDoneMsg{err: context.Canceled})
	if m.busy || m.cancelling {
		t.Fatal("completion did not release the run slot")
	}
}

func TestDoubleEscapeMustBeConsecutive(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	cancelled := 0
	m.busy = true
	m.cancelRun = func() { cancelled++ }
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 0 || !m.busy {
		t.Fatalf("non-consecutive escapes cancelled task: cancelled=%d busy=%v", cancelled, m.busy)
	}
}

func TestNoGhostMessageOnSubAgentDone(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	// Turn: sub-agent running → text → EvDone → trailing sub-agent done
	// (emitted by Chat after the loop's EvDone). The trailing done event
	// must NOT mint an empty duplicate message.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "orchestrator", Status: "running", Detail: "composing"})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "hello there"})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "orchestrator", Status: "done"})})
	feed(m, chatDoneMsg{err: nil})

	assistants := 0
	for _, msg := range m.messages {
		if msg.Role == "assistant" {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant messages = %d, want 1 (no ghost)", assistants)
	}
	last := m.lastAssistant()
	if last.Text != "hello there" {
		t.Errorf("reply text = %q", last.Text)
	}
	if last.think == nil || last.think.Status != "done" {
		t.Errorf("think = %+v, want done", last.think)
	}
}

func TestScrollbarNoTrackPipes(t *testing.T) {
	sb := scrollbar{}
	sb.set(10, 10, 40, 0)
	view := sb.View(NewStyles(Charmtone()))
	if strings.Contains(view, "│") {
		t.Errorf("track pipes must not render on chat rows: %q", view)
	}
	if !strings.Contains(stripANSI(view), "┃") {
		t.Errorf("thumb missing: %q", view)
	}
	sb.set(10, 10, 5, 0)
	if sb.View(NewStyles(Charmtone())) != "" {
		t.Error("scrollbar must hide when content fits")
	}
}

func TestActivityUsesOneSlowTimerAndRendersOnlyByMaestro(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.busy = true
	m.runStart = time.Now()
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "working"})})
	m.activityTickArmed = false
	if cmd := m.frameTicks(); cmd == nil || !m.activityTickArmed {
		t.Fatal("active task must arm exactly one slow activity timer")
	}
	if cmd := m.frameTicks(); cmd != nil {
		t.Fatal("a second activity timer was armed while one is in flight")
	}
	header := stripANSI(m.renderRoleMessage(m.lastAssistant(), 100))
	if !strings.Contains(header, "Maestro  ◌ Composing") {
		t.Fatalf("orchestral activity is not beside Maestro: %q", header)
	}
	m.renderStatusline()
	view := m.status.View(m.styles, 120, m)
	if strings.Contains(stripANSI(view), "Composing") {
		t.Errorf("orchestral activity must not be duplicated in footer: %q", stripANSI(view))
	}
}

func TestThinkingDurationRefreshesOnActivityTick(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "orchestrator", Status: "running",
	})})
	last := m.lastAssistant()
	if last == nil || last.think == nil {
		t.Fatal("running thinking summary was not created")
	}
	last.think.Started = time.Now().Add(-3 * time.Second)
	feed(m, activityTickMsg{at: time.Now()})
	view := stripANSI(m.View())
	if !strings.Contains(view, "thinking 3s") {
		t.Errorf("thinking timer did not refresh from its initial 0s: %q", view)
	}
}

func TestInputBoxSpansPaneWidth(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	leftW := m.harnessChatWidth()
	box := m.renderInputBox(leftW)
	lines := strings.Split(stripANSI(box), "\n")
	for _, l := range lines {
		if w := lipgloss.Width(l); w != leftW {
			t.Errorf("input box line width = %d, want %d (frame must span the pane)", w, leftW)
		}
	}
	// The composer is an inset, rounded surface with one clear focus boundary.
	if !strings.Contains(stripANSI(box), "╭") || !strings.Contains(stripANSI(box), "╯") || !strings.Contains(stripANSI(box), "✦") {
		t.Errorf("input composer chrome is not the inset dock design: %q", stripANSI(box))
	}
}

func TestQuietComposerActionsAreMouseComplete(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	restore := editorListFiles
	editorListFiles = func(string) []string { return []string{"target.txt"} }
	defer func() { editorListFiles = restore }()

	find := func(action ActionID) Region {
		m.View()
		for _, region := range m.regions {
			if region.Action == action {
				return region
			}
		}
		return Region{}
	}
	click := func(region Region) {
		feed(m, tea.MouseMsg{X: region.X, Y: region.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	}

	help := find(ActionKeymap)
	commands := find(ActionPalette)
	context := find(ActionAddContext)
	send := find(ActionSend)
	if help.W == 0 || commands.W == 0 || context.W == 0 || send.W == 0 {
		t.Fatalf("composer regions missing: help=%+v commands=%+v context=%+v send=%+v", help, commands, context, send)
	}
	click(help)
	if m.overlay != overlayKeymap {
		t.Fatalf("help click opened %v", m.overlay)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})

	click(find(ActionPalette))
	if m.overlay != overlayPalette {
		t.Fatalf("commands click opened %v", m.overlay)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})

	click(find(ActionAddContext))
	if m.overlay != overlayAtFile || m.input.Value() != "@" {
		t.Fatalf("context click: overlay=%v input=%q", m.overlay, m.input.Value())
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})

	m.input.Set("hello from mouse")
	before := len(m.messages)
	click(find(ActionSend))
	if m.input.Value() != "" || len(m.messages) != before+1 {
		t.Fatalf("send click: input=%q messages=%d want=%d", m.input.Value(), len(m.messages), before+1)
	}
}

func TestKeepLastLines(t *testing.T) {
	got := keepLastLines("a\nb\nc\nd", 2)
	if got != "c\nd" {
		t.Errorf("keepLastLines = %q, want c\\nd", got)
	}
	if k := keepLastLines("a\nb", 5); k != "a\nb" {
		t.Errorf("short content must pass through: %q", k)
	}
	if k := keepLastLines("a\nb", 0); k != "" {
		t.Errorf("zero height = %q", k)
	}
}

func TestClearScreenCmd(t *testing.T) {
	msg := clearScreenCmd()()
	if msg != tea.ClearScreen() {
		t.Errorf("clearScreenCmd must produce tea.ClearScreen(), got %#v", msg)
	}
}

func TestPipelineRendersFrenchEmojiClean(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	text := "Salut 👋 Je suis prêt à t'aider. Qu'est-ce que je peux faire pour toi aujourd'hui ?"
	msg := &Message{Role: "assistant", Text: text, State: "chat", ts: time.Now()}
	m.messages = append(m.messages, msg)
	m.renderMessages()
	for _, w := range []int{40, 60} {
		view := stripANSI(m.renderRoleMessage(msg, w))
		if !strings.Contains(view, "prêt à t'aider") {
			t.Errorf("w=%d: pipeline scrambled: %q", w, view)
		}
		if !strings.Contains(view, "aujourd'hui") {
			t.Errorf("w=%d: text lost: %q", w, view)
		}
	}
}

func TestStreamDeltaOrderIntegrity(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	// Feed a burst of small deltas exactly like a streaming run (the pump
	// race used to deliver them out of order, scrambling the text).
	var want strings.Builder
	for i := 0; i < 200; i++ {
		chunk := fmt.Sprintf(" mot%d ", i)
		want.WriteString(chunk)
		feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: chunk})})
	}
	last := m.lastAssistant()
	if last == nil || last.Text != want.String() {
		t.Fatalf("delta order corrupted:\n got %q\nwant %q", last.Text, want.String())
	}
}

func TestStreamingKeepsViewportPinnedToLatestOutput(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	for i := 0; i < 80; i++ {
		feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{
			Text: fmt.Sprintf("stream line %02d\n", i),
		})})
		if !m.viewport.AtBottom() {
			t.Fatalf("autoscroll detached at delta %d: offset=%d total=%d", i, m.viewport.YOffset, m.viewport.TotalLineCount())
		}
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "LATEST_OUTPUT"})})
	if !strings.Contains(stripANSI(m.View()), "LATEST_OUTPUT") {
		t.Fatal("latest streamed output is not visible")
	}
}

func TestManualScrollUpPausesAndBottomResumesAutofollow(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	for i := 0; i < 80; i++ {
		feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{
			Text: fmt.Sprintf("line %02d\n", i),
		})})
	}
	feed(m, tea.KeyMsg{Type: tea.KeyPgUp})
	before := m.viewport.YOffset
	if m.followOutput {
		t.Fatal("manual scroll up did not pause output following")
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "hidden tail\n"})})
	if m.viewport.YOffset != before {
		t.Fatalf("stream moved manually scrolled viewport: %d -> %d", before, m.viewport.YOffset)
	}
	m.viewport.GotoBottom()
	feed(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if !m.followOutput {
		t.Fatal("scrolling back to bottom did not resume output following")
	}
}

func TestStreamEmojiOrderIntegrity(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.orch.SetModel("claude-sonnet-4")
	// The exact failure pattern from the screenshots: emoji + accents.
	text := "Salut ! 👋 Je suis prêt à t'aider. Qu'est-ce que je peux faire pour vous aujourd'hui ?"
	chunks := []string{"Salut ! ", "👋 ", "Je suis ", "prêt à ", "t'aider. ", "Qu'est-ce que ", "je peux ", "faire ", "pour vous ", "aujourd'hui ?"}
	for _, c := range chunks {
		feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: c})})
	}
	last := m.lastAssistant()
	if last == nil || last.Text != text {
		t.Fatalf("emoji stream corrupted:\n got %q\nwant %q", last.Text, text)
	}
	// Finished render must contain the full sentence verbatim.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})})
	view := stripANSI(m.renderRoleMessage(last, 60))
	for _, want := range []string{"Salut !", "👋", "prêt à t'aider", "aujourd'hui"} {
		if !strings.Contains(view, want) {
			t.Errorf("rendered view missing %q: %q", want, view)
		}
	}
}
