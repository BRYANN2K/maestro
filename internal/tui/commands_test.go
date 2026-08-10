package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/settings"
)

func TestParseSlash(t *testing.T) {
	tests := []struct {
		in    string
		cmd   string
		flags map[string]string
		args  []string
		ok    bool
	}{
		{in: "/build", cmd: "build", flags: map[string]string{}, ok: true},
		{in: "/build --engine legacy --agent codex", cmd: "build", flags: map[string]string{"engine": "legacy", "agent": "codex"}, ok: true},
		{in: "/build --engine subscription --agent codex", cmd: "build", flags: map[string]string{"engine": "subscription", "agent": "codex"}, ok: true},
		{in: "/build --engine=native", cmd: "build", flags: map[string]string{"engine": "native"}, ok: true},
		{in: "/archive --yes --merge", cmd: "archive", flags: map[string]string{"yes": "true", "merge": "true"}, ok: true},
		{in: `/propose -m="add auth"`, cmd: "propose", flags: map[string]string{"m": "add auth"}, ok: true},
		{in: `/propose -m=add auth`, cmd: "propose", flags: map[string]string{"m": "add"}, args: []string{"auth"}, ok: true},
		{in: `/edit "keep --public API"`, cmd: "edit", args: []string{"keep --public API"}, ok: true},
		{in: `/answer Q-001 -m "yes, keep it"`, cmd: "answer", flags: map[string]string{"m": "yes, keep it"}, args: []string{"Q-001"}, ok: true},
		{in: `/remember -- "-leading fact"`, cmd: "remember", args: []string{"-leading fact"}, ok: true},
		{in: `/learn C:\Users\maestro\source.go`, cmd: "learn", args: []string{`C:\Users\maestro\source.go`}, ok: true},
		{in: `/learn "\\server\shared files\source.go"`, cmd: "learn", args: []string{`\\server\shared files\source.go`}, ok: true},
		{in: `/edit unfinished\`, cmd: "edit", args: []string{`unfinished\`}, ok: true},
		{in: "/", ok: false}, // "/" alone is an empty command
		{in: "build", ok: false},
		{in: `/edit "unfinished`, ok: false},
	}
	for _, tt := range tests {
		cmd, err := parseSlash(tt.in)
		if tt.ok && err != nil {
			t.Errorf("parseSlash(%q) = %v", tt.in, err)
			continue
		}
		if !tt.ok && err == nil {
			t.Errorf("parseSlash(%q) should fail", tt.in)
			continue
		}
		if !tt.ok {
			continue
		}
		if cmd.Cmd != tt.cmd {
			t.Errorf("cmd = %q, want %q", cmd.Cmd, tt.cmd)
		}
		for k, v := range tt.flags {
			if cmd.Flags[k] != v {
				t.Errorf("flag %s = %q, want %q (command %q)", k, cmd.Flags[k], v, tt.in)
			}
		}
		if strings.Join(cmd.Args, "|") != strings.Join(tt.args, "|") {
			t.Errorf("args = %q, want %q (command %q)", cmd.Args, tt.args, tt.in)
		}
	}
}

func TestSlashAliasesNormalizeWithoutCatalogDuplicates(t *testing.T) {
	tests := []struct {
		alias     string
		canonical string
	}{
		{alias: "/models", canonical: "/model"},
		{alias: "/provider", canonical: "/providers"},
		{alias: "/usages", canonical: "/usage"},
		{alias: "/exit", canonical: "/quit"},
		{alias: "/boostrap", canonical: "/bootstrap"},
		{alias: "/onboard", canonical: "/adopt"},
		{alias: "/MODELS", canonical: "/model"},
	}
	visible := make(map[string]bool, len(slashCatalog))
	spellings := make(map[string]string)
	for _, suggestion := range slashCatalog {
		command := strings.ToLower(suggestion.Command)
		if owner := spellings[command]; owner != "" {
			t.Fatalf("canonical command %q collides with %s", suggestion.Command, owner)
		}
		if visible[command] {
			t.Fatalf("duplicate canonical command %q", suggestion.Command)
		}
		visible[command] = true
		spellings[command] = suggestion.Command
		for _, alias := range suggestion.Aliases {
			spelling := strings.ToLower(alias)
			if prior := spellings[spelling]; prior != "" {
				t.Fatalf("alias %q for %s collides with %s", alias, suggestion.Command, prior)
			}
			spellings[spelling] = suggestion.Command
		}
	}
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			cmd, err := parseSlash(tt.alias)
			if err != nil {
				t.Fatal(err)
			}
			if got := "/" + cmd.Cmd; got != tt.canonical {
				t.Fatalf("canonical = %q, want %q", got, tt.canonical)
			}
			if visible[strings.ToLower(tt.alias)] {
				t.Fatalf("compatibility alias %q leaked into the visible catalog", tt.alias)
			}
			matches := matchingSlashSuggestions(strings.ToLower(tt.alias))
			if len(matches) != 1 || matches[0].Command != tt.canonical {
				t.Fatalf("alias completion = %+v, want one %s", matches, tt.canonical)
			}
		})
	}
}

func TestParseSlashKeepsCompleteProposeRequest(t *testing.T) {
	cmd, err := parseSlash("/propose add a billing portal")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd.Args, " "); got != "add a billing portal" {
		t.Fatalf("propose args = %q", got)
	}
}

func TestInlineSlashPreviewFiltersAndCompletes(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/mo")})
	clean := stripANSI(m.View())
	if !strings.Contains(clean, "/model") || !strings.Contains(clean, "choose the active model") {
		t.Fatalf("slash preview missing filtered command: %q", clean)
	}
	for _, suggestion := range m.slashMatches() {
		if suggestion.Command == "/build" {
			t.Fatalf("slash preview was not filtered: %+v", m.slashMatches())
		}
	}
	feed(m, tea.KeyMsg{Type: tea.KeyDown})
	feed(m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/model " {
		t.Fatalf("tab completion = %q, want /model", got)
	}
	if strings.Contains(stripANSI(m.View()), "choose the active model") {
		t.Fatal("preview should close after command completion")
	}
}

func TestInlineSlashPreviewCanBeClicked(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/id")})
	m.View()
	var command Region
	for _, region := range m.regions {
		if region.Action == ActionSlashComplete && region.Target == "/ide" {
			command = region
			break
		}
	}
	if command.W == 0 {
		t.Fatal("slash command mouse region not registered")
	}
	feed(m, tea.MouseMsg{X: command.X + 2, Y: command.Y, Button: tea.MouseButtonLeft})
	if got := m.input.Value(); got != "/ide " {
		t.Fatalf("mouse completion = %q, want /ide", got)
	}
}

func TestBareSlashNavigatesEntireCommandCatalog(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if got := len(m.slashMatches()); got != len(slashCatalog) {
		t.Fatalf("bare slash matches=%d, want complete catalog=%d", got, len(slashCatalog))
	}
	for i, want := range slashCatalog {
		if got := m.selectedSlashCommand(); got != want.Command {
			t.Fatalf("selection %d=%q, want %q", i, got, want.Command)
		}
		feed(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.slashSelected != 0 {
		t.Fatalf("selection did not wrap after full catalog: %d", m.slashSelected)
	}

	// Moving upward from the first item reaches the final command and scrolls
	// the six-row preview window so that selection remains visible.
	feed(m, tea.KeyMsg{Type: tea.KeyUp})
	view := stripANSI(m.View())
	last := slashCatalog[len(slashCatalog)-1].Command
	if m.selectedSlashCommand() != last || !strings.Contains(view, last) {
		t.Fatalf("last slash command is not reachable/visible: selected=%q view=%q", m.selectedSlashCommand(), view)
	}
}

func TestSlashCatalogDrivesCommandPalette(t *testing.T) {
	if len(paletteCommands) != len(slashCatalog) {
		t.Fatalf("palette/slash catalog drift: %d != %d", len(paletteCommands), len(slashCatalog))
	}
	for i, suggestion := range slashCatalog {
		if paletteCommands[i] != suggestion.Command {
			t.Fatalf("palette command %d = %q, want %q", i, paletteCommands[i], suggestion.Command)
		}
	}
}

func TestSlashHelpIsDerivedFromCanonicalCatalog(t *testing.T) {
	lines := strings.Split(slashHelpText(), "\n")
	if len(lines) != len(slashCatalog)+1 {
		t.Fatalf("help lines=%d, want header + %d commands", len(lines), len(slashCatalog))
	}
	for i, suggestion := range slashCatalog {
		if !strings.HasPrefix(strings.TrimSpace(lines[i+1]), suggestion.Command+" ") {
			t.Fatalf("help line %d = %q, want %s", i+1, lines[i+1], suggestion.Command)
		}
		for _, alias := range suggestion.Aliases {
			if strings.HasPrefix(strings.TrimSpace(lines[i+1]), alias+" ") {
				t.Fatalf("alias %q rendered as a duplicate help entry", alias)
			}
		}
	}
}

func TestBuildOpensEnginePicker(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.input.Set("/build")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if cmd != nil {
		t.Fatal("engine picker should return no cmd")
	}
	if m2.overlay != overlayEngine {
		t.Fatalf("overlay = %v, want engine picker", m2.overlay)
	}
	eng, ok := m2.overlayM.(*engineOverlay)
	if !ok {
		t.Fatalf("overlayM = %T", m2.overlayM)
	}
	if len(eng.choices) < 2 || eng.choices[0].Engine != "native" {
		t.Errorf("choices = %+v", eng.choices)
	}
}

func TestEnginePickerSelectionDispatches(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.input.Set("/build")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if m2.overlay != overlayEngine {
		t.Fatalf("overlay = %v", m2.overlay)
	}
	// Select the first choice (native) and confirm.
	updated, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := updated.(*Model)
	if cmd == nil {
		t.Fatal("dispatch should return a cmd")
	}
	if m3.overlay != overlayNone {
		t.Fatalf("overlay = %v after selection", m3.overlay)
	}
	if !m3.busy {
		t.Error("dispatch should mark the model busy")
	}
}

func TestPlainChatSkipsPicker(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.input.Set("hello there")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if cmd == nil {
		t.Fatal("plain message should return a chat cmd")
	}
	if m2.overlay != overlayNone {
		t.Fatalf("overlay = %v, want none", m2.overlay)
	}
}

func TestSlashUIAliases(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m.input.Set("/models")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || updated.(*Model).overlay != overlayModelPicker {
		t.Fatalf("/models = overlay %v, cmd=%v", updated.(*Model).overlay, cmd)
	}

	m.overlay = overlayNone
	m.input.Set("/settings")
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.messages) == 0 || strings.Contains(strings.ToLower(m.messages[len(m.messages)-1].Text), "unknown command") {
		t.Fatal("/settings should be handled by the TUI")
	}

	m.overlay = overlayNone
	m.input.Set("/help")
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Text, "/model") {
		t.Fatalf("/help output = %q", m.messages[len(m.messages)-1].Text)
	}
	if strings.Contains(m.messages[len(m.messages)-1].Text, "/models") {
		t.Fatalf("/help exposes duplicate model alias: %q", m.messages[len(m.messages)-1].Text)
	}
}

func TestUsageAliasHasOneCanonicalOutput(t *testing.T) {
	for _, command := range []string{"/usage", "/usages"} {
		t.Run(command, func(t *testing.T) {
			m, _ := newTestModel(t)
			if err := m.orch.SetTaskModel(m.ctx(), settings.RoleOrchestrator, "legacy", "codex", "gpt-5.6-luna"); err != nil {
				t.Fatalf("SetTaskModel: %v", err)
			}
			m.orch.EmitCost(0.125, 2)
			m.input.Set(command)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model := updated.(*Model)
			if cmd != nil {
				t.Fatalf("%s should be handled synchronously", command)
			}
			got := model.messages[len(model.messages)-1].Text
			for _, want := range []string{"usage:", "model gpt-5.6-luna", "context", "$0.1250", "2 tool calls"} {
				if !strings.Contains(got, want) {
					t.Fatalf("%s output %q missing %q", command, got, want)
				}
			}
		})
	}
}

func TestResumeSlashOpensSessionPicker(t *testing.T) {
	m, _ := newTestModel(t)
	m.input.Set("/resume")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(*Model)
	if cmd == nil || model.overlay != overlaySessionPicker {
		t.Fatalf("/resume = overlay %v, cmd=%v; want asynchronous session picker", model.overlay, cmd)
	}
	loading, ok := model.overlayM.(*listOverlay)
	if !ok {
		t.Fatalf("loading picker = %T", model.overlayM)
	}
	if got := loading.selectedValue(); got != "" {
		t.Fatalf("loading picker selected %q", got)
	}
	msg := primaryBatchMessage(t, cmd)
	if _, ok := msg.(sessionListMsg); !ok {
		t.Fatalf("/resume async result = %T", msg)
	}
	model.Update(msg)
	if _, ok := model.overlayM.(*listOverlay); !ok {
		t.Fatalf("loaded session picker = %T", model.overlayM)
	}
}

func TestSessionSwitchCannotOverlapActiveRun(t *testing.T) {
	m, _ := newTestModel(t)
	m.busy = true
	m.overlay = overlaySessionPicker
	if cmd := m.loadSession("another-session"); cmd != nil {
		t.Fatal("session switch started while another run was active")
	}
	if m.overlay != overlayNone || !m.busy {
		t.Fatalf("blocked switch left overlay=%v busy=%v", m.overlay, m.busy)
	}
}

func TestEngineOverlayView(t *testing.T) {
	m, _ := newTestModel(t)
	eng := newEngineOverlay(m.orch, "dev")
	view := eng.View(NewStyles(Charmtone()), 40)
	if !containsAll(view, "native · Maestro agent", "subscription · codex", "↑/↓") || strings.Contains(view, "legacy:") {
		t.Errorf("view = %q", view)
	}
}

func TestPickerQueryAndBackspace(t *testing.T) {
	list := &listOverlay{items: []string{"alpha", "beta", "agent"}}
	list.query = "ag"
	if got := list.Filter(); len(got) != 1 || got[0] != "agent" {
		t.Fatalf("filter = %v", got)
	}
	list.backspace()
	if list.query != "a" {
		t.Fatalf("query after backspace = %q", list.query)
	}
}

func TestOverlayWindowKeepsSelectionVisible(t *testing.T) {
	items := make([]string, 30)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}
	styles := NewStyles(Charmtone())

	list := &listOverlay{items: items, selected: 25}
	view := stripANSI(list.View(styles, 50))
	if !strings.Contains(view, "item-25") {
		t.Fatalf("selected item not visible after scrolling down: %q", view)
	}
	if !strings.Contains(view, "↑ …") {
		t.Fatal("top scroll indicator missing")
	}

	list.selected = 0
	view = stripANSI(list.View(styles, 50))
	if !strings.Contains(view, "item-00") || !strings.Contains(view, "↓ …") {
		t.Fatalf("top of the list broken: %q", view)
	}
}

func TestSettingsOverlayWindowKeepsSelectionVisible(t *testing.T) {
	m, _ := newTestModel(t)
	overlay := newSettingsOverlay(m)
	rows := overlay.rows()
	overlay.selected = len(rows) - 1
	view := stripANSI(overlay.View(NewStyles(Charmtone()), 60))
	last := rows[len(rows)-1]
	if !strings.Contains(view, last.Label) {
		t.Fatalf("last settings row %q not visible: %q", last.Label, view)
	}
}

func TestSettingsOverlayCyclesPermission(t *testing.T) {
	m, _ := newTestModel(t)
	overlay := newSettingsOverlay(m)
	if !strings.Contains(stripANSI(overlay.View(NewStyles(Charmtone()), 60)), "Permission mode") {
		t.Fatal("settings overlay should render its fields")
	}
	if got := overlay.value(overlay.rows()[0]); got != "ask" {
		t.Fatalf("default permission = %q", got)
	}
	overlay.change(m, overlay.rows()[0], 1)
	if got := overlay.value(overlay.rows()[0]); got != "allow" {
		t.Fatalf("permission after cycle = %q", got)
	}
}

func TestSettingsOverlayUsesSubscriptionProductName(t *testing.T) {
	m, _ := newTestModel(t)
	state := m.orch.SettingsSnapshot()
	route := state.RoleDefaults[settings.RoleDev]
	route.Engine = "legacy"
	route.Agent = "codex"
	state.RoleDefaults[settings.RoleDev] = route
	if err := m.orch.UpdateSettings(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	view := newSettingsOverlay(m).View(NewStyles(Charmtone()), 72)
	if !strings.Contains(view, "subscription") || strings.Contains(view, "legacy") {
		t.Fatalf("settings leaked compatibility engine name: %q", view)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
