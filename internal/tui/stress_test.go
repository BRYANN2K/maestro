package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	coretools "github.com/bryann2k/maestro/internal/agentcore/tools"
	"github.com/bryann2k/maestro/internal/proposals"
)

func assertTerminalRectangle(t *testing.T, view string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got != height {
		t.Fatalf("view height = %d, want %d at %dx%d", got, height, width, height)
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("row %d width = %d, want %d at %dx%d", row, got, width, width, height)
		}
	}
}

func TestStressResizeKeepsHarnessAndIDEInsideTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	for _, size := range []struct{ width, height int }{
		{10, 4},
		{40, 10},
		{80, 24},
		{240, 60},
	} {
		feed(m, tea.WindowSizeMsg{Width: size.width, Height: size.height})
		m.switchTab(TabHarness)
		assertTerminalRectangle(t, m.View(), size.width, size.height)

		m.overlay = overlayPalette
		m.overlayM = newPaletteOverlay(m.orch)
		assertTerminalRectangle(t, m.View(), size.width, size.height)
		m.overlay = overlayNone

		m.switchTab(TabIDE)
		editorW, treeW, railW := m.idePaneWidths()
		if editorW < 0 || treeW < 0 || railW < 0 {
			t.Fatalf("negative IDE pane at %dx%d: editor=%d tree=%d rail=%d", size.width, size.height, editorW, treeW, railW)
		}
		if editorW+treeW+railW != size.width {
			t.Fatalf("IDE panes total %d, want %d at %dx%d", editorW+treeW+railW, size.width, size.width, size.height)
		}
		assertTerminalRectangle(t, m.View(), size.width, size.height)
	}
}

func TestTinyTerminalUsesRecoverableFallback(t *testing.T) {
	m, _ := newTestModel(t)
	for _, size := range []struct{ width, height int }{{34, 6}, {10, 4}} {
		for _, tab := range []Tab{TabHarness, TabIDE} {
			feed(m, tea.WindowSizeMsg{Width: size.width, Height: size.height})
			m.switchTab(tab)
			view := m.View()
			assertTerminalRectangle(t, view, size.width, size.height)
			plain := stripANSI(view)
			if !strings.Contains(strings.ToLower(plain), "small") || !strings.Contains(plain, "40x10") {
				t.Fatalf("%v fallback missing recovery guidance at %dx%d:\n%s", tab, size.width, size.height, plain)
			}
			if size.width == 10 && size.height == 4 {
				lines := strings.Split(plain, "\n")
				if got := strings.TrimRight(lines[2], " "); got != "min 40x10" {
					t.Fatalf("10x4 fallback clipped the minimum size to %q:\n%s", got, plain)
				}
				if got := lipgloss.Width(strings.TrimRight(lines[2], " ")); got >= size.width {
					t.Fatalf("10x4 recovery label uses the terminal's unsafe final cell: width=%d", got)
				}
			}
		}
	}

	feed(m, tea.WindowSizeMsg{Width: 34, Height: 6})
	feed(m, tea.KeyMsg{Type: tea.KeySpace})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if view := stripANSI(m.View()); !strings.Contains(view, "HELP") || !strings.Contains(view, "ctrl+q") {
		t.Fatalf("tiny help is not reachable:\n%s", view)
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone {
		t.Fatalf("tiny help did not close: %v", m.overlay)
	}

	feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if view := stripANSI(m.View()); strings.Contains(view, "Terminal too small") {
		t.Fatalf("minimum supported canvas incorrectly uses fallback:\n%s", view)
	}
}

func TestTinyTerminalRejectsStaleMouseHitboxes(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 28})
	o := &taskModelOverlay{
		tasks: taskRoutes,
		sources: []modelSource{{
			id: "codex", label: "Codex", ready: true, installed: true,
			models: []routeModel{{id: "first"}, {id: "second"}},
		}},
		focus: 2,
	}
	m.overlay, m.overlayM = overlayModelPicker, o
	_ = m.View()
	var target workspaceHit
	for _, hit := range o.hits {
		if hit.kind == "model" && hit.index == 1 {
			target = hit
			break
		}
	}
	if target.w == 0 {
		t.Fatal("wide workspace did not register model hitbox")
	}
	click := tea.MouseMsg{
		X: o.originX + target.x, Y: o.originY + target.y,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	}

	feed(m, tea.WindowSizeMsg{Width: 10, Height: 4})
	_ = m.View()
	feed(m, click)
	feed(m, tea.MouseMsg{X: 9, Y: 2, Button: tea.MouseButtonWheelDown})
	if o.model != 0 || o.focus != 2 {
		t.Fatalf("hidden workspace reacted to mouse: model=%d focus=%d", o.model, o.focus)
	}

	m.busy = true
	if view := stripANSI(m.View()); !strings.Contains(view, "esc esc") {
		t.Fatalf("tiny busy fallback hides cancellation gesture:\n%s", view)
	}
}

func TestTinyTerminalCannotApproveHiddenPermission(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 28})
	respond := make(chan error, 1)
	dialog := newPermissionDialog(&permissionRequest{
		Call:    agentcore.ToolCall{Name: "bash", Args: `{"command":"go test ./..."}`},
		Spec:    agentcore.ToolSpec{Name: "bash", NeedsApproval: true},
		Respond: respond,
	}, m.perm)
	m.dialogs.push(dialog)
	_ = m.View()
	if len(dialog.buttons) == 0 {
		t.Fatal("wide permission dock did not register button hitboxes")
	}
	allow := dialog.buttons[0]

	// Width alone is enough to activate the fallback while the old button's
	// Y coordinate remains inside the terminal. The click must be inert.
	feed(m, tea.WindowSizeMsg{Width: 34, Height: 28})
	_ = m.View()
	feed(m, tea.MouseMsg{
		X: allow.x, Y: allow.y,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	select {
	case err := <-respond:
		t.Fatalf("hidden permission was resolved by stale click: %v", err)
	default:
	}
	if m.dialogs.empty() {
		t.Fatal("hidden permission dialog was dismissed by stale click")
	}
}

func TestTinyTerminalCancellationFailsClosedPermission(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 28})

	runCtx, cancelRunCtx := context.WithCancel(context.Background())
	respond := make(chan error, 1)
	req := &permissionRequest{
		ctx:     runCtx,
		Call:    agentcore.ToolCall{Name: "bash", Args: `{"command":"sleep 60"}`},
		Spec:    agentcore.ToolSpec{Name: "bash", NeedsApproval: true},
		Respond: respond,
	}
	m.busy = true
	cancelled := 0
	m.cancelRun = func() {
		cancelled++
		cancelRunCtx()
	}
	feed(m, permRequestMsg{req: req})
	dialog, ok := m.dialogs.top()
	if !ok {
		t.Fatal("active permission request did not open a dialog")
	}
	permission, ok := dialog.(*permissionDialog)
	if !ok {
		t.Fatalf("permission dialog type = %T", dialog)
	}

	_ = m.View()
	if len(permission.buttons) != 3 {
		t.Fatalf("wide permission dock buttons = %d, want 3", len(permission.buttons))
	}
	staleAlwaysHit := permission.buttons[1]
	feed(m, tea.KeyMsg{Type: tea.KeyRight}) // prime the dangerous "Always" choice
	if permission.buttonSel != 1 {
		t.Fatalf("permission selection = %d, want always", permission.buttonSel)
	}

	feed(m, tea.WindowSizeMsg{Width: 34, Height: 6})
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case err := <-respond:
		t.Fatalf("first escape resolved permission early: %v", err)
	default:
	}
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})

	if cancelled != 1 || !m.cancelling {
		t.Fatalf("double escape cancellation: calls=%d cancelling=%v", cancelled, m.cancelling)
	}
	select {
	case err := <-respond:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled permission error = %v, want context.Canceled", err)
		}
	default:
		t.Fatal("cancel did not resolve the permission fail-closed")
	}
	if !m.dialogs.empty() || !permission.resolved || len(permission.buttons) != 0 {
		t.Fatalf("cancel left actionable permission state: dialogs=%d resolved=%v buttons=%d", len(m.dialogs.items), permission.resolved, len(permission.buttons))
	}

	feed(m, tea.WindowSizeMsg{Width: 100, Height: 28})
	if view := stripANSI(m.View()); strings.Contains(view, "Permission required") {
		t.Fatalf("expired permission reappeared after resize:\n%s", view)
	}
	// Neither a click at the old hit target nor a stale direct resolution may
	// turn the cancelled request into an "always allow" grant.
	feed(m, tea.MouseMsg{
		X: staleAlwaysHit.x, Y: staleAlwaysHit.y,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	if err := permission.resolve(); err == nil {
		t.Fatal("stale permission dialog remained resolvable")
	}
	if m.perm.toolAllowed("bash") {
		t.Fatal("cancelled permission mutated allowedTools")
	}

	// A permission message already queued in Bubble Tea when cancellation won
	// the gate's context select must also be rejected instead of resurrected.
	lateRespond := make(chan error, 1)
	feed(m, permRequestMsg{req: &permissionRequest{
		ctx:     runCtx,
		Call:    agentcore.ToolCall{Name: "write", Args: `{"path":"late.txt"}`},
		Spec:    agentcore.ToolSpec{Name: "write", NeedsApproval: true},
		Respond: lateRespond,
	}})
	select {
	case err := <-lateRespond:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late permission error = %v, want context.Canceled", err)
		}
	default:
		t.Fatal("late cancelled permission was not resolved")
	}
	if !m.dialogs.empty() || m.perm.toolAllowed("write") {
		t.Fatal("late cancelled permission became actionable")
	}
}

func TestNarrowProviderAndModelOverlaysKeepControlsVisible(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})

	m.overlay = overlayModelPicker
	m.overlayM = newTaskModelOverlay(m.orch)
	modelView := stripANSI(m.View())
	if !strings.Contains(modelView, "MODELS") || !strings.Contains(modelView, "esc close") {
		t.Fatalf("narrow model workspace lost its controls:\n%s", modelView)
	}
	assertTerminalRectangle(t, m.View(), 40, 10)

	m.overlay = overlayProviders
	m.overlayM = newProvidersOverlay(m.orch, "")
	providerView := stripANSI(m.View())
	if !strings.Contains(providerView, "PROVIDERS") || !strings.Contains(providerView, "esc close") {
		t.Fatalf("narrow provider workspace lost its controls:\n%s", providerView)
	}
	assertTerminalRectangle(t, m.View(), 40, 10)
}

func TestStressNarrowPickersAndModalsRemainClosable(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})

	longItems := make([]string, 30)
	for i := range longItems {
		longItems[i] = "session item"
	}
	cases := []struct {
		name  string
		setup func()
	}{
		{"palette", func() { m.overlay, m.overlayM = overlayPalette, newPaletteOverlay(m.orch) }},
		{"session", func() {
			m.overlay, m.overlayM = overlaySessionPicker, &listOverlay{title: "Sessions", items: longItems}
		}},
		{"settings", func() { m.overlay, m.overlayM = overlaySettings, newSettingsOverlay(m) }},
		{"auth", func() { m.overlay, m.overlayM = overlayAuth, newAuthOverlay("openai", "openai/test") }},
		{"diff", func() {
			prop := &proposals.Proposal{Path: "résumé/界.go", BaseLines: []string{"old"}, Hunks: []proposals.Hunk{{Start: 1, OldLines: []string{"old"}, NewLines: []string{"new"}}}}
			m.overlay, m.overlayM = overlayDiff, newDiffOverlay(m.styles, prop, 34)
		}},
		{"ask", func() {
			m.overlay, m.overlayM = overlayNone, nil
			m.pendingAsk = &coretools.AskRequest{Question: "Choisir une option ?", Options: []string{"première", "deuxième", "troisième"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.pendingAsk = nil
			m.overlay, m.overlayM = overlayNone, nil
			tc.setup()
			view := m.View()
			assertTerminalRectangle(t, view, 40, 10)
			if !strings.Contains(strings.ToLower(stripANSI(view)), "esc") {
				t.Fatalf("%s modal lost its close control:\n%s", tc.name, stripANSI(view))
			}
		})
	}
}

func TestBracketedPasteKeepsUnicodeAndDropsTerminalControls(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	payload := "début 👩🏽‍💻 界\nligne\tfin\x00\x07\x1b[31mRED\x1b[0m\x7f\u009b"
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(payload), Paste: true})

	got := m.input.Value()
	for _, want := range []string{"début", "👩🏽‍💻", "界", "\n", "ligne", "fin", "RED"} {
		if !strings.Contains(got, want) {
			t.Fatalf("paste lost %q: %q", want, got)
		}
	}
	for _, r := range got {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			t.Fatalf("paste retained terminal control U+%04X in %q", r, got)
		}
	}
}

func TestBracketedPasteNormalizesTerminalLineEndingsInPrompt(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	feed(m, tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("α\r\nβ\r界\nfin"),
		Paste: true,
	})

	if got, want := m.input.Value(), "α\nβ\n界\nfin"; got != want {
		t.Fatalf("normalized prompt paste = %q, want %q", got, want)
	}
}

func TestBracketedPasteCannotInjectControlsIntoPickerFilter(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.overlay = overlayPalette
	m.overlayM = newPaletteOverlay(m.orch)
	feed(m, tea.KeyMsg{
		Type: tea.KeyRunes, Paste: true,
		Runes: []rune("résumé\x1b[31m\x07\x7f\u009b"),
	})
	list, ok := overlayList(m.overlayM)
	if !ok {
		t.Fatal("palette is not a list overlay")
	}
	if list.query != "résumé" {
		t.Fatalf("unsafe picker query = %q, want résumé", list.query)
	}
}

func TestBracketedPasteIntoPickerIsSanitizedWithoutActivatingIt(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.overlay = overlayPalette
	m.overlayM = newPaletteOverlay(m.orch)
	feed(m, tea.KeyMsg{
		Type: tea.KeyRunes, Paste: true,
		Runes: []rune("a\r\n界\x1b[31m\x07\x7f\u009b"),
	})

	list, ok := overlayList(m.overlayM)
	if !ok {
		t.Fatal("palette closed or changed after paste")
	}
	if got, want := list.query, "a 界"; got != want {
		t.Fatalf("sanitized picker query = %q, want %q", got, want)
	}
	if m.overlay != overlayPalette {
		t.Fatalf("paste activated picker action: overlay = %v", m.overlay)
	}
}

func TestFuzzyPickerMatchesUnicodeCaseInsensitively(t *testing.T) {
	items := []string{"Résumé été", "東京 session", "plain"}
	matches, _ := fuzzyMatch("rés", items)
	if len(matches) != 1 || matches[0] != items[0] {
		t.Fatalf("accented match = %v, want %q", matches, items[0])
	}
	matches, _ = fuzzyMatch("京", items)
	if len(matches) != 1 || matches[0] != items[1] {
		t.Fatalf("CJK match = %v, want %q", matches, items[1])
	}
}

func TestRapidNavigationAndRepeatedCancellationStayConsistent(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m.overlay = overlayPalette
	m.overlayM = &listOverlay{title: "Stress", items: []string{"Résumé", "東京", "界面"}}
	for i := 0; i < 500; i++ {
		feed(m, tea.KeyMsg{Type: tea.KeyDown})
		feed(m, tea.KeyMsg{Type: tea.KeyUp})
	}
	assertTerminalRectangle(t, m.View(), 40, 10)
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})

	cancelled := 0
	m.busy = true
	m.cancelRun = func() { cancelled++ }
	for i := 0; i < 100; i++ {
		feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	}
	if cancelled != 1 || !m.busy || !m.cancelling {
		t.Fatalf("repeated cancel state: calls=%d busy=%v cancelling=%v", cancelled, m.busy, m.cancelling)
	}
	feed(m, chatDoneMsg{})
	if m.busy || m.cancelling || m.cancelRun != nil {
		t.Fatalf("completion left stale run state: busy=%v cancelling=%v cancel=%v", m.busy, m.cancelling, m.cancelRun != nil)
	}
}

func TestNarrowIDEResizeCannotFocusHiddenCompanionRail(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.switchTab(TabIDE)
	if _, _, railW := m.idePaneWidths(); railW == 0 {
		t.Fatal("80-column IDE should expose its companion rail")
	}
	m.ide.Focus = ideHITL
	feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if _, _, railW := m.idePaneWidths(); railW != 0 {
		t.Fatalf("40-column IDE rail width = %d, want collapsed", railW)
	}
	if m.ide.Focus == ideHITL {
		t.Fatal("resize left focus trapped in the hidden companion rail")
	}
	for i := 0; i < 12; i++ {
		feed(m, tea.KeyMsg{Type: tea.KeyTab})
		if m.ide.Focus == ideHITL {
			t.Fatalf("tab %d focused hidden companion rail", i)
		}
	}
}

func FuzzSanitizeInputNeverLeavesTerminalControls(f *testing.F) {
	for _, seed := range []string{
		"plain text",
		"déjà 👩🏽‍💻 東京\nnext\tcell",
		"\x1b[31mred\x1b[0m\x00\x07\x7f\u009b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := sanitizeInput(input)
		for _, r := range got {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				t.Fatalf("sanitizeInput retained U+%04X in %q", r, got)
			}
		}
	})
}

func FuzzSanitizePastedInputPreservesSafeTextControls(f *testing.F) {
	for _, seed := range []string{
		"plain\ntext",
		"Windows\r\nUnix\nTmux\rfin",
		"déjà 👩🏽‍💻 東京\rnext\tcell",
		"\x1b[31mred\x1b[0m\x00\x07\x7f\u009b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := sanitizePastedInput(input)
		if strings.ContainsRune(got, '\r') {
			t.Fatalf("sanitizePastedInput retained CR in %q", got)
		}
		for _, r := range got {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				t.Fatalf("sanitizePastedInput retained U+%04X in %q", r, got)
			}
		}
	})
}

func FuzzFuzzySubsequenceUnicodeSafe(f *testing.F) {
	for _, seed := range [][2]string{{"rés", "Résumé"}, {"京", "東京"}, {"", "anything"}, {"xyz", "plain"}, {"0", strings.Repeat("1", 50) + "0"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, query, item string) {
		score := fuzzySubsequence(query, item)
		if query == "" && score != 0 {
			t.Fatalf("empty query score = %d", score)
		}
		if score < -1 {
			t.Fatalf("invalid fuzzy score %d for %q / %q", score, query, item)
		}
	})
}
