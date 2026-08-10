package tui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/proposals"
	"github.com/bryann2k/maestro/internal/settings"
)

func TestCoachOverlayIsExplicitCompactAndSafe(t *testing.T) {
	o := newCoachOverlay(coachOffer{
		ID: "spec-intent", Title: "Explain the acceptance criteria\x1b[31m",
		Prompt: "Pick one scenario and predict how it can fail.",
		Why:    "The proposal is ready for validation.", DoneWhen: "One falsifiable failure is written.",
		Hint: "Start with Given / When / Then.", Duration: "2 min\x1b[2J", Mode: "guided",
	})
	rawView := o.View(NewStyles(ThemeForName("charmtone")), 40)
	if strings.Contains(rawView, "\x1b[2J") {
		t.Fatalf("coach view emitted injected terminal control: %q", rawView)
	}
	view := stripANSI(rawView)
	for _, want := range []string{"MAESTRO COACH", "Explain the acceptance criteria", "Next:", "2 min", "Why now:", "Done when:", "enter practice", "GUIDED"} {
		if !strings.Contains(view, want) {
			t.Fatalf("coach view missing %q:\n%s", want, view)
		}
	}
	for _, once := range []string{"Next:", "2 min", "Done when:"} {
		if strings.Count(view, once) != 1 {
			t.Fatalf("coach view duplicated %q:\n%s", once, view)
		}
	}
	plain := asciiProfile(t, rawView)
	for _, want := range []string{"Next:", "2 min", "Done when:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("ASCII coach view lost %q hierarchy: %q", want, plain)
		}
	}
	for _, r := range view {
		if r == '\x1b' || (unicode.IsControl(r) && r != '\n' && r != '\t') {
			t.Fatalf("coach rendered terminal control payload: %q", view)
		}
	}
	if got := o.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")}); got != coachNoop || !o.showHint {
		t.Fatalf("hint toggle = %v, show=%v", got, o.showHint)
	}
	if got := o.update(tea.KeyMsg{Type: tea.KeyEnter}); got != coachStart {
		t.Fatalf("enter action = %v", got)
	}
}

func TestCoachOverlayActions(t *testing.T) {
	for key, want := range map[string]coachAction{"d": coachComplete, "s": coachSnooze} {
		o := newCoachOverlay(coachOffer{})
		if got := o.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}); got != want {
			t.Fatalf("key %q = %v, want %v", key, got, want)
		}
	}
	o := newCoachOverlay(coachOffer{})
	if got := o.update(tea.KeyMsg{Type: tea.KeyEsc}); got != coachClose {
		t.Fatalf("esc = %v", got)
	}
}

func TestCoachOverlayMinimumTerminalKeepsDecisionAndControls(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m.overlay = overlayCoach
	m.overlayM = newCoachOverlay(coachOffer{
		Title:    "Read the trust boundary",
		Prompt:   "Trace untrusted input",
		Why:      "security review is due",
		DoneWhen: "one boundary is named",
		Hint:     "Start at the parser",
		Duration: "2 min",
		Mode:     "guided",
	})
	view := stripANSI(m.View())
	for _, want := range []string{
		"MAESTRO COACH", "2 min", "Next:", "Trace", "Why:", "Done:",
		"security", "one bound", "enter go", "h?", "d done", "s later", "esc",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("40x10 Coach clipped %q:\n%s", want, view)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 10 {
		t.Fatalf("40x10 Coach rendered %d rows", len(lines))
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got != 40 {
			t.Fatalf("40x10 Coach row %d width=%d: %q", row, got, line)
		}
	}
	coach := m.overlayM.(*coachOverlay)
	coach.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	hintView := stripANSI(m.View())
	for _, want := range []string{"Hint:", "Next:", "Why:", "Done:", "d done", "esc"} {
		if !strings.Contains(hintView, want) {
			t.Fatalf("40x10 hinted Coach clipped %q:\n%s", want, hintView)
		}
	}
}

func TestCoachOverlayAt80x24KeepsChromeAndFrameOnTheirRows(t *testing.T) {
	project := filepath.Join(t.TempDir(), "repo123")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.Models = []config.Model{{ID: "smoke-model", ContextWindow: 32_768}}
	cfg.ModelRoles["default"] = config.Slot{Model: "smoke-model"}
	store := proposals.NewProposalStore(filepath.Join(project, ".proposals"))
	perm := NewPermissionQueue(4)
	orch, err := orchestrator.New(t.Context(), orchestrator.Options{
		ProjectDir: project, SessionsDir: filepath.Join(project, ".sessions"),
		Config: cfg, Settings: settings.Defaults(), In: strings.NewReader(""), Out: io.Discard, Gate: perm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, total := orch.ContextUsage(); total != 32_768 {
		t.Fatalf("context-aware fixture total = %d", total)
	}
	m := New(orch, store, perm)
	if chrome := m.renderRuntimeChrome(33); lipgloss.Width(chrome) > 33 {
		t.Fatalf("runtime chrome width = %d, budget 33", lipgloss.Width(chrome))
	}
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.overlay = overlayCoach
	m.overlayM = newCoachOverlay(coachOffer{
		Title:    "Intent and non-goals",
		Prompt:   "Compare the worked example with the current task and point out one boundary.",
		Why:      "discovery is where a crisp boundary prevents prompt-only implementation",
		DoneWhen: "you have written one concrete response tied to the current task",
		Duration: "2 min",
		Mode:     "guided",
	})

	raw := m.View()
	assertTerminalRectangle(t, raw, 80, 24)
	lines := strings.Split(stripANSI(raw), "\n")
	if !strings.HasSuffix(strings.TrimRight(lines[0], " "), "M") {
		t.Fatalf("80x24 runtime mark escaped the tab row:\n%s", strings.Join(lines[:min(3, len(lines))], "\n"))
	}
	if strings.TrimSpace(lines[1]) == "M" {
		t.Fatalf("80x24 runtime mark wrapped below the tab row:\n%s", strings.Join(lines[:min(3, len(lines))], "\n"))
	}
	for row, line := range lines {
		if strings.TrimSpace(line) == "…" {
			t.Fatalf("80x24 frame fitter spliced an orphan ellipsis at row %d:\n%s", row, stripANSI(raw))
		}
	}
	plain := stripANSI(raw)
	for _, want := range []string{"MAESTRO COACH", "Next:", "Why now:", "Done when:", "enter practice", "╭", "╰"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("80x24 Coach lost %q:\n%s", want, plain)
		}
	}
}

func TestLearnCoachIsExplicitAndPreparesWithoutSending(t *testing.T) {
	t.Setenv("MAESTRO_LEARN_DIR", t.TempDir())
	m, _ := newTestModel(t)

	// /learn opens a mode picker. A fresh project must not silently opt in.
	m.input.Set("/learn")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := primaryBatchMessage(t, cmd)
	result, ok := msg.(coachResultMsg)
	if !ok {
		t.Fatalf("/learn returned %T", msg)
	}
	m.Update(result)
	picker, ok := m.overlayM.(*listOverlay)
	if !ok || m.overlay != overlayCoachMode {
		t.Fatalf("coach menu = %T/%v", m.overlayM, m.overlay)
	}
	if result.state.Mode != "off" {
		t.Fatalf("fresh Coach mode = %q, want off", result.state.Mode)
	}

	// Guided mode is an explicit choice. It may prepare an offer, but it may
	// not run an agent or overwrite the composer.
	picker.selected = 0
	cmd = m.selectOverlay(picker)
	msg = primaryBatchMessage(t, cmd)
	result, ok = msg.(coachResultMsg)
	if !ok || result.action != "guided" {
		t.Fatalf("guided action = %T/%+v", msg, result)
	}
	m.Update(result)
	if m.coachOffer == nil {
		t.Fatal("guided mode did not prepare a contextual offer")
	}
	offerView := stripANSI(newCoachOverlay(*m.coachOffer).View(m.styles, 54))
	for _, once := range []string{"Next:", "2 min", "Done when:"} {
		if strings.Count(offerView, once) != 1 {
			t.Fatalf("prepared Coach offer has %d %q labels:\n%s", strings.Count(offerView, once), once, offerView)
		}
	}
	if m.busy || m.streaming() {
		t.Fatal("enabling Coach started an agent")
	}

	m.input.Set("/learn next")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg = primaryBatchMessage(t, cmd)
	m.Update(msg)
	if m.overlay != overlayCoach {
		t.Fatalf("/learn next overlay = %v (%T)", m.overlay, m.overlayM)
	}
	messageCount := len(m.messages)
	_, cmd = m.updateOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Coach enter unexpectedly scheduled work")
	}
	if m.overlay != overlayNone || m.busy || m.streaming() {
		t.Fatal("Coach practice stole execution focus")
	}
	if !strings.Contains(m.input.Value(), "My reasoning:") {
		t.Fatalf("Coach composer = %q", m.input.Value())
	}
	if len(m.messages) != messageCount {
		t.Fatal("Coach enter sent the prepared exercise without explicit user submission")
	}
	if offer := m.visibleCoachOffer(); offer != nil {
		t.Fatal("Coach rail offer remained visible while the user was typing")
	}
}

func TestCoachModePickerUsesEnoughWidthForItsDecisionLabels(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.overlay = overlayCoachMode
	m.overlayM = newCoachModeOverlay(orchestrator.CoachState{Mode: orchestrator.CoachModeOff})

	view := stripANSI(m.View())
	for _, want := range []string{
		"Guided  · examples fade as evidence grows",
		"Challenge · reason first, ask for help on demand",
		"Next lesson · open the contextual activity",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("Coach mode picker wrapped or clipped %q:\n%s", want, view)
		}
	}
}

func TestLearnDonePersistsOnlyExplicitCompletion(t *testing.T) {
	t.Setenv("MAESTRO_LEARN_DIR", t.TempDir())
	m, _ := newTestModel(t)

	msg := primaryBatchMessage(t, m.runCoachAction("guided", "guided", ""))
	m.Update(msg)
	before, err := m.orch.CoachState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.PendingLessonID == "" {
		t.Fatal("guided mode did not create one pending lesson")
	}

	msg = primaryBatchMessage(t, m.runCoachAction("done", "", before.PendingLessonID))
	m.Update(msg)
	after, err := m.orch.CoachState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.PendingLessonID != "" || len(after.CompletedLessonIDs) != 1 {
		t.Fatalf("explicit completion was not persisted: %+v", after)
	}
}

func TestCoachRailOfferOpensByMouseWithoutRunningAgent(t *testing.T) {
	m, _ := newTestModel(t)
	m.coachOffer = &coachOffer{
		ID: "tests:observe:0", Title: "Tests as evidence", Prompt: "Name the bug this test catches.",
		Composer: "My reasoning: ", Duration: "2 min", Mode: "guided",
	}
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.View()
	var hit Region
	for _, region := range m.regions {
		if region.Action == ActionCoachOpen {
			hit = region
			break
		}
	}
	if hit.W == 0 || hit.H == 0 {
		t.Fatal("Coach offer did not register a clickable rail surface")
	}
	feed(m, tea.MouseMsg{X: hit.X, Y: hit.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.overlay != overlayCoach {
		t.Fatalf("Coach click overlay = %v", m.overlay)
	}
	if m.busy || m.streaming() {
		t.Fatal("opening Coach from the rail started an agent")
	}
}
