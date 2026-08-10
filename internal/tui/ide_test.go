package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/proposals"
)

func TestToggleIDE(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.ToggleIDE()
	if m.ide == nil {
		t.Fatal("IDE not enabled")
	}
	view := m.View()
	clean := stripANSI(view)
	if !strings.Contains(clean, "NORMAL") || !strings.Contains(clean, "FILES") {
		t.Errorf("IDE view missing editor/tree: %q", view[:200])
	}
	m.ToggleIDE()
	if m.ide != nil {
		t.Error("IDE should be disabled")
	}
}

func TestWorkspaceTabsSwitchByKeyboardAndMouse(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	if m.ActiveTab() != TabHarness {
		t.Fatalf("initial tab = %v, want harness", m.ActiveTab())
	}

	m.View()
	var ideTab Region
	for _, r := range m.regions {
		if r.Action == ActionSwitchTab && r.Tab == TabIDE {
			ideTab = r
			break
		}
	}
	if ideTab.W == 0 {
		t.Fatal("IDE tab region not registered")
	}
	feed(m, tea.MouseMsg{X: ideTab.X + 1, Y: ideTab.Y, Button: tea.MouseButtonLeft})
	if m.ActiveTab() != TabIDE || m.ide == nil {
		t.Fatal("mouse click should select IDE tab")
	}

	m.View()
	var maestroTab Region
	for _, r := range m.regions {
		if r.Action == ActionSwitchTab && r.Tab == TabHarness {
			maestroTab = r
			break
		}
	}
	if maestroTab.W == 0 {
		t.Fatal("Maestro tab region not registered")
	}
	feed(m, tea.MouseMsg{X: maestroTab.X + 1, Y: maestroTab.Y, Button: tea.MouseButtonLeft})
	if m.ActiveTab() != TabHarness {
		t.Fatal("mouse click should select Maestro tab")
	}
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}, Alt: true})
	if m.ActiveTab() != TabIDE {
		t.Fatal("alt+2 should select IDE")
	}
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}, Alt: true})
	if m.ActiveTab() != TabHarness {
		t.Fatal("alt+1 should select Maestro")
	}
	m.input.Set("")
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if m.ActiveTab() != TabHarness || m.input.Value() != "2" {
		t.Fatalf("bare 2 must remain composer input: tab=%v input=%q", m.ActiveTab(), m.input.Value())
	}
	feed(m, tea.KeyMsg{Type: tea.KeyTab, Alt: true})
	if m.ActiveTab() != TabIDE {
		t.Fatal("alt+tab compatibility binding should cycle to IDE")
	}
}

func TestTopTabsExposeMaestroAndIDEHoverTargets(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	view := stripANSI(m.View())
	if !strings.Contains(view, "⌥1 AGENT") || !strings.Contains(view, "⌥2 IDE") {
		t.Fatalf("top tabs missing: %q", view[:min(len(view), 180)])
	}
	if !strings.Contains(view, "DISCOVERY · READ ONLY") {
		t.Fatalf("command bar must expose the read-only discovery contract: %q", view[:min(len(view), 220)])
	}
	var ide Region
	for _, region := range m.regions {
		if region.Action == ActionSwitchTab && region.Tab == TabIDE {
			ide = region
		}
	}
	feed(m, tea.MouseMsg{X: ide.X + 1, Y: ide.Y, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	if m.hoverMsg != "IDE — alt+2" {
		t.Fatalf("IDE hover hint = %q", m.hoverMsg)
	}
}

func TestIDEStatuslineUsesEditorDisplayMode(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.ToggleIDE()

	m.ide.Ed.SetKeymap("standard")
	view := stripANSI(m.View())
	if !strings.Contains(view, "EDIT") || strings.Contains(view, "NORMAL") {
		t.Fatalf("standard editor status must say EDIT: %q", view)
	}

	m.ide.Ed.SetKeymap("vim")
	if view = stripANSI(m.View()); !strings.Contains(view, "NORMAL") {
		t.Fatalf("vim editor status must expose modal state: %q", view)
	}
}

func TestIDEBufferTabsAndRailAreMouseComplete(t *testing.T) {
	m, dir := newTestModel(t)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	m.ToggleIDE()
	if err := m.ide.Ed.Open(second); err != nil {
		t.Fatal(err)
	}
	m.ide.Ed.CurBuf = 0
	m.View()

	var secondTab, railToggle Region
	for _, r := range m.regions {
		if r.Action == ActionSwitchBuffer && r.Index == 1 {
			secondTab = r
		}
		if r.Action == ActionToggleActivity && r.Y == 0 {
			railToggle = r
		}
	}
	if secondTab.W == 0 || railToggle.W == 0 {
		t.Fatalf("missing mouse regions: buffer=%+v rail=%+v", secondTab, railToggle)
	}
	feed(m, tea.MouseMsg{X: secondTab.X + 1, Y: secondTab.Y, Button: tea.MouseButtonLeft})
	if m.ide.Ed.CurBuf != 1 {
		t.Fatalf("buffer tab click selected %d, want 1", m.ide.Ed.CurBuf)
	}
	feed(m, tea.MouseMsg{X: railToggle.X, Y: railToggle.Y, Button: tea.MouseButtonLeft})
	_, _, railW := m.idePaneWidths()
	if m.activityOpen || railW != 0 {
		t.Fatalf("rail click did not collapse IDE companion: open=%v width=%d", m.activityOpen, railW)
	}
}

func TestStreamingDoesNotRebuildHiddenTranscriptInIDE(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 180, Height: 42})
	m.appendAssistant("before IDE")
	previous := m.lastContent
	m.ToggleIDE()
	last := m.lastAssistant()
	last.busy = true
	last.Text = strings.Repeat("streaming output ", 8000)
	m.renderMessages()
	if m.lastContent != previous {
		t.Fatal("hidden Agent transcript was rebuilt while IDE was active")
	}
	view := m.View()
	if !strings.Contains(stripANSI(view), "streaming output") {
		t.Fatal("bounded live agent summary missing from IDE companion")
	}
}

func TestClickingTranscriptFileOpensItInIDE(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 38})
	path := filepath.Join(dir, "internal", "clicked.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.appendAssistant("I changed internal/clicked.go and it is ready to inspect.")
	m.View()
	var file Region
	for _, region := range m.regions {
		if region.Action == ActionOpenPath && region.Target == "internal/clicked.go" {
			file = region
			break
		}
	}
	if file.W == 0 {
		t.Fatal("visible transcript file did not register an IDE link")
	}
	feed(m, tea.MouseMsg{X: file.X, Y: file.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.ActiveTab() != TabIDE || m.ide == nil || m.ide.Ed.Buffer() == nil {
		t.Fatal("file click did not switch to IDE")
	}
	if got := m.ide.Ed.Buffer().Path; got != path {
		t.Fatalf("opened buffer=%q, want %q", got, path)
	}
}

func TestClickingTranscriptLocationOpensExactCursor(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 38})
	path := filepath.Join(dir, "internal", "located.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.appendAssistant("Inspect internal/located.go:3:2 before continuing.")
	m.View()
	var link Region
	for _, region := range m.regions {
		if region.Action == ActionOpenPath && region.Target == "internal/located.go" {
			link = region
			break
		}
	}
	if link.W == 0 || link.Line != 3 || link.Column != 2 {
		t.Fatalf("exact location link missing: %+v", link)
	}
	feed(m, tea.MouseMsg{X: link.X, Y: link.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := m.ide.Ed.Buffer().Cur; got.Line != 2 || got.Col != 1 {
		t.Fatalf("cursor = %+v, want line=2 col=1", got)
	}
}

func TestFollowMaestroTracksToolLocationsUntilManualNavigation(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 150, Height: 36})
	path := filepath.Join(dir, "follow.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc follow() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.ToggleIDE()
	m.addToolCallCard(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolCall, agentcore.ToolCall{
		ID: "follow-read", Name: "read", Args: `{"path":"follow.go","line":3}`,
	}))
	if got := m.ide.Ed.Buffer(); got == nil || got.Path != path || got.Cur.Line != 2 {
		t.Fatalf("follow did not navigate to tool location: %+v", got)
	}
	if !m.followAgent {
		t.Fatal("automatic navigation disabled Follow mode")
	}
	m.openWorkspaceLocation("target.txt", 1, 1, true)
	if m.followAgent {
		t.Fatal("manual navigation must switch Follow to FREE")
	}
	m.addToolCard(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{ID: "follow-read", Name: "read", Output: "done"}))
	m.finalizeLastAssistant()
	m.input.Set("/follow on")
	m.send()
	if !m.followAgent {
		t.Fatal("/follow on did not re-enable Follow mode")
	}
}

func TestIDEResizeDragUsesPaneRatios(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	m.ToggleIDE()
	m.View()

	editorW, treeW, hitlW := m.idePaneWidths()
	if editorW+treeW+hitlW != m.width {
		t.Fatalf("pane widths = %d/%d/%d, total %d; want %d", editorW, treeW, hitlW, editorW+treeW+hitlW, m.width)
	}
	if editorW < m.width*65/100 || treeW >= editorW || hitlW >= editorW {
		t.Fatalf("default panes are not code-first: tree=%d editor=%d rail=%d", treeW, editorW, hitlW)
	}

	var divider Region
	for _, r := range m.regions {
		if r.Action == ActionResize && r.ResizeTarget == 1 {
			divider = r
			break
		}
	}
	if divider.W == 0 {
		t.Fatal("editor/tree resize region not registered")
	}
	feed(m, tea.MouseMsg{X: divider.X, Y: divider.Y, Button: tea.MouseButtonLeft})
	beforeTree := m.ideTreePct
	feed(m, tea.MouseMsg{X: divider.X + 16, Y: divider.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	feed(m, tea.MouseMsg{X: divider.X + 16, Y: divider.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.ideTreePct <= beforeTree || m.resizing {
		t.Fatalf("resize drag did not update tree split: tree=%d resizing=%v", m.ideTreePct, m.resizing)
	}
	m.View()

	var railDivider Region
	for _, r := range m.regions {
		if r.Action == ActionResize && r.ResizeTarget == 2 {
			railDivider = r
			break
		}
	}
	if railDivider.W == 0 {
		t.Fatal("editor/agent resize region not registered")
	}
	beforeRail := m.ideRailPct
	feed(m, tea.MouseMsg{X: railDivider.X, Y: railDivider.Y, Button: tea.MouseButtonLeft})
	feed(m, tea.MouseMsg{X: railDivider.X - 10, Y: railDivider.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	feed(m, tea.MouseMsg{X: railDivider.X - 10, Y: railDivider.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.ideRailPct == beforeRail {
		t.Fatal("editor/agent resize did not update rail split")
	}
}

func TestViewDoesNotExposeANSIControlText(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	m.ToggleIDE()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.input.Set("hello")
	clean := stripANSI(m.View())
	for _, fragment := range []string{"[38;2;", "[48;2;", "[<35;"} {
		if strings.Contains(clean, fragment) {
			t.Fatalf("view exposes ANSI sequence %q: %q", fragment, clean)
		}
	}
}

func TestVisualSelectionCanAskMaestro(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.ide.Focus = ideEditor
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	feed(m, tea.KeyMsg{Type: tea.KeyRight})
	feed(m, tea.KeyMsg{Type: tea.KeySpace})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.selectionMenu == nil {
		t.Fatal("selection menu should open for a visual selection")
	}
	// The menu defaults to a direct edit, while the old ask flow remains
	// available as an explicit action. Pick the Maestro modification action.
	feed(m, tea.KeyMsg{Type: tea.KeyDown})
	feed(m, tea.KeyMsg{Type: tea.KeyDown})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || updated.(*Model).ActiveTab() != TabHarness {
		t.Fatalf("ask selection did not return to Harness: cmd=%v tab=%v", cmd, updated.(*Model).ActiveTab())
	}
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Text, "Modify") {
		t.Fatalf("selection prompt missing: %+v", m.messages)
	}
}

func TestMouseDragCreatesEditorSelection(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.View()
	_, treeW, _ := m.idePaneWidths()
	feed(m, tea.MouseMsg{X: treeW + 8, Y: tabBarRows + 2, Button: tea.MouseButtonLeft})
	feed(m, tea.MouseMsg{X: treeW + 16, Y: tabBarRows + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	feed(m, tea.MouseMsg{X: treeW + 16, Y: tabBarRows + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if !m.ide.Ed.HasSelection() {
		t.Fatal("mouse drag should create a visual selection")
	}
	if m.selectionMenu == nil || m.selectionMenu.Context == nil || m.selectionMenu.Context.Source != "ide" {
		t.Fatal("mouse-selected code should open the Maestro interaction menu")
	}
}

func TestMouseWheelScrollsIDEWithoutMovingCursor(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "long.go")
	var source strings.Builder
	source.WriteString("package main\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&source, "// line %03d\n", i)
	}
	if err := os.WriteFile(path, []byte(source.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.View()
	editorW, treeW, _ := m.idePaneWidths()
	beforeCursor := m.ide.Ed.Buffer().Cur
	feed(m, tea.MouseMsg{X: treeW + editorW/2, Y: m.ideCodeTop() + 4, Button: tea.MouseButtonWheelDown})
	if got := m.ide.UI.ScrollOffset(); got == 0 {
		t.Fatal("mouse wheel did not scroll the IDE editor")
	}
	if got := m.ide.Ed.Buffer().Cur; got != beforeCursor {
		t.Fatalf("mouse scroll moved cursor: before=%v after=%v", beforeCursor, got)
	}
}

func TestStandardIDESelectionCanBeTypedFromFloatingMenu(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("abcdef\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	m.ide.Ed.SetKeymap("standard")
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	b := m.ide.Ed.Buffer()
	b.Cur = editor.Cursor{Line: 0, Col: 0}
	m.ide.Ed.BeginSelectionAt(b.Cur)
	m.ide.Ed.UpdateSelectionCursor(editor.Cursor{Line: 0, Col: 3})
	if !m.openIDESelectionMenu(8, m.ideCodeTop()) {
		t.Fatal("selection menu did not open")
	}
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Z'}})
	if m.selectionEdit == nil || m.selectionEdit.Value() != "Z" {
		t.Fatalf("direct typing did not start replacement: edit=%v value=%q", m.selectionEdit != nil, m.selectionEdit.Value())
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := b.LineText(0); got != "Zdef" {
		t.Fatalf("selection replacement = %q, want Zdef", got)
	}
}

func TestSelectionQuestionIsAskedInsideFloatingWindow(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	b := m.ide.Ed.Buffer()
	b.Cur = editor.Cursor{Line: 0, Col: 0}
	m.ide.Ed.BeginSelectionAt(b.Cur)
	m.ide.Ed.UpdateSelectionCursor(editor.Cursor{Line: 0, Col: 6})
	if !m.openIDESelectionMenu(8, m.ideCodeTop()) {
		t.Fatal("selection menu did not open")
	}
	m.selectionMenu.Selected = len(m.selectionMenu.Actions) - 1
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectionAsk == nil || m.selectionAskCtx == nil {
		t.Fatal("ask action should open the inline question editor")
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Ask about selection") || !strings.Contains(view, "What would you like to know?") {
		t.Fatalf("floating question editor missing: %q", view)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("why is this package declaration needed?")})
	if !strings.Contains(m.selectionAsk.Value(), "why is this package declaration needed?") {
		t.Fatalf("question input = %q", m.selectionAsk.Value())
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting a selection question should start a chat command")
	}
	if updated.(*Model).selectionAsk != nil || len(m.messages) == 0 {
		t.Fatal("selection question editor should close after submit")
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != "user" || !strings.Contains(last.Text, "Question: why is this package declaration needed?") {
		t.Fatalf("submitted selection question = %+v", last)
	}
}

func TestMarkdownPreviewScrollAndReturnToEdit(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "README.md")
	content := strings.Repeat("# Heading\n\nA paragraph with **Markdown** preview.\n\n", 20)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	m.ide.Ed.SetKeymap("standard")
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	feed(m, tea.KeyMsg{Type: tea.KeySpace})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !m.ide.preview {
		t.Fatal("Space p should open Markdown preview")
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "PREVIEW · MARKDOWN") || !strings.Contains(view, "Heading") {
		t.Fatalf("preview not rendered: %q", view[:min(len(view), 500)])
	}
	feed(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.ide.previewScroll == 0 {
		t.Fatal("PageDown should scroll a long Markdown preview")
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.ide.preview {
		t.Fatal("Escape should return to Markdown editing")
	}
}

func TestIDEUsesMockupGridInsteadOfNestedBoxes(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 180, Height: 42})
	m.ToggleIDE()

	body := m.renderIDE()
	plain := stripANSI(body)
	for _, want := range []string{"FILES", "TASKS  0/0", "MAESTRO", "REVIEW"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("IDE grid missing %q", want)
		}
	}
	if strings.ContainsAny(plain, "┌┐└┘") {
		t.Fatalf("IDE should use quiet separators, not boxed panels:\n%s", plain)
	}
	if strings.Contains(plain, "dir ") || strings.Contains(plain, "md README") || strings.Contains(plain, "go main") {
		t.Fatalf("file explorer leaked legacy textual icons:\n%s", plain)
	}
	if got, want := lipgloss.Height(body), m.bodyHeight(); got != want {
		t.Fatalf("IDE body height = %d, want %d", got, want)
	}
	for i, line := range strings.Split(body, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("IDE row %d width = %d, terminal width = %d", i, got, m.width)
		}
	}
}

func TestStandardIDEControlPOpensFilePicker(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 34})
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# notes\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	m.ide.Ed.SetKeymap("standard")
	m.ide.Focus = ideEditor
	feed(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.ide.Ed.Picker.Active {
		t.Fatal("Ctrl-P should open the IDE file picker in standard mode")
	}
}

func TestModelPickerRemainsReachableFromIDE(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 34})
	m.ToggleIDE()
	feed(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.overlay != overlayModelPicker {
		t.Fatalf("overlay = %v, want model picker from IDE", m.overlay)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Models · provider / model") {
		t.Fatalf("model picker was not rendered over IDE: %q", view[:min(len(view), 600)])
	}
}

func TestMouseClickPlacesCursorWithoutSelection(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.View()
	y := m.ideCodeTop()
	_, treeW, _ := m.idePaneWidths()
	feed(m, tea.MouseMsg{X: treeW + 10, Y: y, Button: tea.MouseButtonLeft})
	feed(m, tea.MouseMsg{X: treeW + 10, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.ide.Ed.HasSelection() {
		t.Fatal("plain click must not leave a visual selection")
	}
	if got := m.ide.Ed.Buffer().Cur.Col; got != 4 {
		t.Fatalf("cursor col = %d, want 4 (click at x=10, gutter=6)", got)
	}
}

func TestIDESelectionOpensContextualQuietComposer(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "selection.go")
	if err := os.WriteFile(path, []byte("package main\nfunc selected() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatal(err)
	}
	b := m.ide.Ed.Buffer()
	b.Cur = editor.Cursor{Line: 1, Col: 0}
	m.ide.Ed.BeginSelectionAt(b.Cur)
	m.ide.Ed.UpdateSelectionCursor(editor.Cursor{Line: 1, Col: 4})
	view := stripANSI(m.View())
	if !strings.Contains(view, "selection.go · line 2") || !strings.Contains(view, "Ask Maestro about this code") {
		t.Fatalf("contextual IDE composer missing:\n%s", view)
	}
	var actions Region
	for _, region := range m.regions {
		if region.Action == ActionIDESelection {
			actions = region
			break
		}
	}
	if actions.W == 0 {
		t.Fatal("selection action region missing")
	}
	feed(m, tea.MouseMsg{X: actions.X, Y: actions.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.selectionMenu == nil {
		t.Fatal("selection action click did not open the interaction menu")
	}
}

func TestClickOnEmptyIDEPaneFocusesIt(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	m.ToggleIDE()
	m.ide.Focus = ideEditor
	editorW, treeW, _ := m.idePaneWidths()
	m.View()
	feed(m, tea.MouseMsg{X: treeW + editorW + 4, Y: tabBarRows + 4, Button: tea.MouseButtonLeft})
	if m.ide.Focus != ideHITL {
		t.Fatalf("click on empty HITL pane focus = %v, want hitl", m.ide.Focus)
	}
}

func TestMouseWheelDoesNotStealFocus(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	if err := m.ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.ide.Focus = ideChat
	m.focus = FocusViewport
	editorW, treeW, _ := m.idePaneWidths()
	m.View()
	feed(m, tea.MouseMsg{X: treeW + editorW + 4, Y: tabBarRows + 2, Button: tea.MouseButtonWheelDown})
	if m.ide.Focus != ideChat {
		t.Fatalf("wheel must not steal focus: focus = %v, want chat", m.ide.Focus)
	}
	if m.focus != FocusViewport {
		t.Fatalf("IDE mouse input must not change harness focus: %v", m.focus)
	}
}

func TestIDESlashCommand(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.input.Set("/ide")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if cmd != nil {
		t.Fatal("ide toggle should not dispatch")
	}
	if m2.ide == nil {
		t.Fatal("/ide should enable the editor")
	}
}

func TestIDEEditorTypingAndSave(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m.ToggleIDE()
	ide := m.ide
	if err := ide.Ed.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	ide.Focus = ideEditor
	buf := ide.Ed.Buffer()
	buf.Cur = editor.Cursor{Line: 0, Col: 5}

	// i + type + esc
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if buf.LineText(0) != "helloX" {
		t.Errorf("buffer = %q", buf.LineText(0))
	}
	// :w saves to disk
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	for _, r := range "w" {
		feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "helloX\n" {
		t.Errorf("file after :w = %q, %v", data, err)
	}
	// :q leaves IDE mode
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	for _, r := range "q" {
		feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.ide != nil {
		t.Error(":q should leave IDE mode")
	}
}

func TestIDEBracketedPasteIsLiteralAtomicInEditingModes(t *testing.T) {
	tests := []struct {
		name      string
		keymap    string
		mode      editor.Mode
		selection bool
		wantLines []string
		wantMode  editor.Mode
	}{
		{name: "standard", keymap: "standard", mode: editor.ModeNormal, wantLines: []string{"dd\t", "界abc"}, wantMode: editor.ModeNormal},
		{name: "vim normal", keymap: "vim", mode: editor.ModeNormal, wantLines: []string{"dd\t", "界abc"}, wantMode: editor.ModeNormal},
		{name: "vim insert", keymap: "vim", mode: editor.ModeInsert, wantLines: []string{"dd\t", "界abc"}, wantMode: editor.ModeInsert},
		{name: "vim visual", keymap: "vim", mode: editor.ModeVisual, selection: true, wantLines: []string{"dd\t", "界bc"}, wantMode: editor.ModeNormal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
			m.ToggleIDE()
			m.ide.Focus = ideEditor
			m.ide.Ed.Buffers = []*editor.Buffer{editor.NewBuffer("paste.txt", []byte("abc\n"))}
			m.ide.Ed.CurBuf = 0
			m.ide.Ed.SetKeymap(tc.keymap)
			m.ide.Ed.Mode = tc.mode
			buf := m.ide.Ed.Buffer()
			if tc.selection {
				m.ide.Ed.BeginVisualAt(editor.Cursor{Line: 0, Col: 0})
				m.ide.Ed.UpdateVisualCursor(editor.Cursor{Line: 0, Col: 1})
			}

			feed(m, tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: []rune("dd\t\n界"),
				Paste: true,
			})

			if got := buf.Lines; strings.Join(got, "\x00") != strings.Join(tc.wantLines, "\x00") {
				t.Fatalf("pasted lines = %#v, want %#v", got, tc.wantLines)
			}
			if m.ide.Ed.Mode != tc.wantMode {
				t.Fatalf("mode = %v, want %v", m.ide.Ed.Mode, tc.wantMode)
			}
			if got := len(buf.UndoStack); got != 1 {
				t.Fatalf("undo entries = %d, want one", got)
			}
			if !buf.Undo() || buf.NumLines() != 1 || buf.LineText(0) != "abc" {
				t.Fatalf("one undo did not restore paste: %#v", buf.Lines)
			}
		})
	}
}

func TestIDEStandardSelectAllBracketedPastePreservesTmuxMultilineMarkdown(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.ToggleIDE()
	m.ide.Focus = ideEditor
	m.ide.Ed.SetKeymap("standard")
	m.ide.Ed.Buffers = []*editor.Buffer{
		editor.NewBuffer("README.md", []byte("old heading\nold body\n")),
	}
	m.ide.Ed.CurBuf = 0

	// tmux paste-buffer -p preserves bracketed-paste framing but, unless -r is
	// also supplied, represents every linefeed in its buffer as a carriage
	// return. The editor must still receive the original multiline document.
	feed(m, tea.KeyMsg{Type: tea.KeyCtrlA})
	feed(m, tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("\x1b[31m## Démo\x1b[0m\r\r- café\r- 界\x07\u009b"),
		Paste: true,
	})

	const want = "## Démo\n\n- café\n- 界\n"
	if got := m.ide.Ed.Buffer().String(); got != want {
		t.Fatalf("buffer after Ctrl+A + tmux paste = %q, want %q", got, want)
	}
	if m.ide.Ed.HasSelection() {
		t.Fatal("paste should clear the select-all range")
	}
	if got := len(m.ide.Ed.Buffer().UndoStack); got != 1 {
		t.Fatalf("undo entries = %d, want one atomic replacement", got)
	}
}

func TestIDEAgentReview(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Stage a proposal the way the agent tool would.
	store := proposals.NewProposalStore(filepath.Join(dir, ".proposals"))
	prop, err := store.Stage(target, "A\nb\nC\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(prop.Hunks) != 2 {
		t.Fatalf("hunks = %d", len(prop.Hunks))
	}

	m.ToggleIDE()
	ide := m.ide
	// :AgentReview
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	for _, r := range "AgentReview" {
		feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if ide.Ed.Review == nil || !ide.Ed.Review.Active {
		t.Fatalf("review not active: %+v", ide.Ed.Review)
	}
	if len(ide.Ed.Review.Items) != 1 {
		t.Fatalf("review items = %d", len(ide.Ed.Review.Items))
	}
	// Reject hunk 0, accept the remaining one.
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if len(ide.Ed.Review.Items) != 0 {
		t.Fatalf("items after review = %d", len(ide.Ed.Review.Items))
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "a\nb\nC\n" {
		t.Errorf("file after review = %q, %v", data, err)
	}
	// esc closes the review overlay
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if ide.Ed.Review.Active {
		t.Error("review should close on esc")
	}
}

func TestIDEProposalReviewAcceptsAndDeclinesPerHunk(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 34})
	target := filepath.Join(dir, "review.txt")
	if err := os.WriteFile(target, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prop, err := m.proposals.Stage(target, "A\nb\nc\nD\n")
	if err != nil || len(prop.Hunks) != 2 {
		t.Fatalf("Stage: hunks=%d err=%v", len(prop.Hunks), err)
	}
	card := &Card{ID: "review-hunks", Kind: "write", Status: "proposed", Proposal: &prop, ProposalPath: target}
	m.appendSystemCard(card)
	m.pending = append(m.pending, card)
	m.openProposalInIDE(&prop)
	m.decideProposalHunk(true)
	if len(prop.Hunks) != 1 || len(m.pending) != 1 {
		t.Fatalf("first hunk decision: hunks=%d pending=%d", len(prop.Hunks), len(m.pending))
	}
	m.decideProposalHunk(false)
	if len(m.pending) != 0 || m.ide.proposalPreview != nil {
		t.Fatalf("settled review remained pending: pending=%d preview=%v", len(m.pending), m.ide.proposalPreview != nil)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "A\nb\nc\nd\n" {
		t.Fatalf("reviewed file = %q, err=%v", data, err)
	}
}

func TestSelectionCanBePinnedAsComposerContext(t *testing.T) {
	m, _ := newTestModel(t)
	selection := &selectionContext{Source: "ide", Path: "internal/store.go", Start: editor.Cursor{Line: 4}, End: editor.Cursor{Line: 7}, Text: "selected code"}
	m.selectionMenu = newSelectionMenu(selection, 1, 1)
	index := -1
	for i, action := range m.selectionMenu.Actions {
		if action == "add to context" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("selection menu has no context action")
	}
	m.activateSelectionAction(index)
	if len(m.contextRefs) != 1 {
		t.Fatalf("context refs = %d", len(m.contextRefs))
	}
	prompt := promptWithContext("fix it", m.contextRefs)
	for _, want := range []string{"fix it", "internal/store.go lines 5-8", "selected code"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("context prompt missing %q: %q", want, prompt)
		}
	}
}

func TestIDEStagingWriteToolIntegration(t *testing.T) {
	// The TUI's staging write tool + IDE review share the same store: a
	// dev sub-agent write becomes a proposal the editor can review.
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	target := filepath.Join(dir, "staged.go")
	if err := os.WriteFile(target, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(m.ctx(), map[string]any{
		"path":    target,
		"content": "package main\n\nfunc main() { println(\"hi\") }\n",
	})
	if err != nil || !strings.Contains(out, "staged") {
		t.Fatalf("tool = %q, %v", out, err)
	}
	// The card flow (B4) and the editor flow (B7) both see it.
	m.ToggleIDE()
	ids, err := m.proposals.Pending()
	if err != nil || len(ids) != 1 {
		t.Fatalf("pending = %v, %v", ids, err)
	}
	if m.ide.Ed.ProposalSrc == nil {
		t.Fatal("editor proposal source not wired")
	}
	if got := m.ide.Ed.ProposalSrc(); len(got) != 1 {
		t.Fatalf("editor proposals = %d", len(got))
	}
}

func TestLearnStagesProposalCard(t *testing.T) {
	const source = "package main\n\nfunc main() {}\n"
	m, dir := newNativeLearnTestModel(t, source)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	src := filepath.Join(dir, "main.go")
	m.input.Set("/learn " + src)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if cmd == nil {
		t.Fatal("learn should return a cmd")
	}
	// Run the learn cmd synchronously (it stages the proposal).
	if msg := cmd(); msg != nil {
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, nested := range batch {
				if nestedMsg := nested(); nestedMsg != nil {
					m2.Update(nestedMsg)
				}
			}
		} else {
			m2.Update(msg)
		}
	}
	// The staged file is the learn output; verify via the store.
	ids, err := m.proposals.Pending()
	if err != nil || len(ids) != 1 {
		var messages []string
		for _, message := range m.messages {
			messages = append(messages, fmt.Sprintf("%s/%s: %s", message.Role, message.State, message.Text))
		}
		t.Fatalf("pending proposals = %v, %v; messages=%q", ids, err, messages)
	}
	prop, err := m.proposals.Load(ids[0])
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(prop.Path, "maestro/learn") {
		t.Errorf("learn proposal path = %q", prop.Path)
	}
	if len(prop.Hunks) == 0 {
		t.Fatal("learn proposal has no changes because the artifact was written before staging")
	}
	if _, err := os.Stat(prop.Path); !os.IsNotExist(err) {
		t.Fatalf("learn artifact exists before acceptance: %v", err)
	}
}
