package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/settings"
)

func TestSettingsWorkspaceHasClearSectionsAndRuntimeLanguage(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.integrations = []settingsIntegration{{Name: "github", Status: "connected", Tools: 12}}
	o.skills = []settingsSkill{{ID: "project:review", Name: "review", Source: "project", Scope: "project", Valid: true, Enabled: true, Invokable: true}}

	view := stripANSI(o.viewSized(NewStyles(Charmtone()), 144, 40))
	for _, want := range []string{"General", "Appearance", "Agents", "Providers", "Integrations", "Skills", "provider · native + subscription", "[focus]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("wide Settings view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(strings.ToLower(view), "legacy") {
		t.Fatalf("Settings leaked the compatibility engine name:\n%s", view)
	}
}

func TestSettingsSlashCommandsOpenTheirSectionOnce(t *testing.T) {
	counts := map[string]int{}
	for _, suggestion := range slashCatalog {
		counts[suggestion.Command]++
	}
	if counts["/mcp"] != 1 || counts["/skills"] != 1 {
		t.Fatalf("ecosystem commands are not unique: %v", counts)
	}

	for _, tc := range []struct {
		command string
		section settingsSectionID
	}{
		{command: "/mcp", section: settingsIntegrations},
		{command: "/skills", section: settingsSkills},
	} {
		m, _ := newTestModel(t)
		m.input.Set(tc.command)
		feed(m, tea.KeyMsg{Type: tea.KeyEnter})
		o, ok := m.overlayM.(*settingsOverlay)
		if !ok {
			t.Fatalf("%s opened %v/%T", tc.command, m.overlay, m.overlayM)
		}
		if m.overlay != overlaySettings || o.section != tc.section {
			t.Fatalf("%s opened overlay=%v section=%v", tc.command, m.overlay, o.section)
		}
	}
}

func TestSettingsSlashSubcommandsStillReachBackend(t *testing.T) {
	for _, tc := range []struct {
		command string
		wantErr bool
	}{
		{command: "/skills list"},
		{command: "/skills unknown", wantErr: true},
		{command: "/mcp reconnect", wantErr: true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m, _ := newTestModel(t)
			feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			m.input.Set(tc.command)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(*Model)
			if cmd == nil {
				t.Fatalf("%s did not dispatch", tc.command)
			}
			if m.overlay == overlaySettings {
				t.Fatalf("%s was swallowed by Settings", tc.command)
			}
			if !tc.wantErr {
				return
			}
			msg := primaryBatchMessage(t, cmd)
			done, ok := msg.(chatDoneMsg)
			if !ok {
				t.Fatalf("%s returned %T, want chatDoneMsg", tc.command, msg)
			}
			if (done.err != nil) != tc.wantErr {
				t.Fatalf("%s error = %v, wantErr=%v", tc.command, done.err, tc.wantErr)
			}
			m.Update(done)
			if tc.wantErr && !strings.Contains(strings.ToLower(stripANSI(m.View())), "error") {
				t.Fatalf("%s error was not surfaced", tc.command)
			}
		})
	}
}

func TestSettingsAsyncActionsCancelAndIgnoreStaleResults(t *testing.T) {
	m, _ := newTestModel(t)
	old := newSettingsOverlay(m)
	m.overlay, m.overlayM = overlaySettings, old
	guard, ctx := old.beginAction(m, "mcp")
	old.mcpLoading = true

	old.close(m)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Escape did not cancel the in-flight Settings action")
	}

	newer := newSettingsOverlay(m)
	newer.notice = "new overlay"
	m.overlay, m.overlayM = overlaySettings, newer
	m.Update(settingsActionDoneMsg{
		target: old, generation: guard.generation, workspace: guard.workspace,
		sessionID: guard.sessionID, action: "mcp-reconnect", id: "stale",
	})
	if m.overlayM != newer || newer.notice != "new overlay" {
		t.Fatal("late Settings result stole the newer overlay")
	}
	if old.notice == "MCP reconnected · stale" {
		t.Fatal("late Settings result mutated its cancelled view model")
	}
}

func TestSettingsActionGuardRejectsWrongSession(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	m.overlay, m.overlayM = overlaySettings, o
	guard, _ := o.beginAction(m, "skills")
	o.notice = "keep"
	m.Update(settingsActionDoneMsg{
		target: o, generation: guard.generation, workspace: guard.workspace,
		sessionID: "another-session", action: "skill-refresh",
	})
	if o.notice != "keep" {
		t.Fatalf("cross-session result changed Settings: %q", o.notice)
	}
	o.cancelAction()
}

func TestSettingsEmptyIntegrationsDoNotAdvertiseDeadEnterAction(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsIntegrations
	o.integrations = nil
	view := stripANSI(o.viewSized(NewStyles(Charmtone()), 144, 40))
	if strings.Contains(view, "Enter  Reconnect") || strings.Contains(view, "Open /mcp") {
		t.Fatalf("empty MCP state advertises a dead action:\n%s", view)
	}
	if cmd := o.activate(m, 1, true); cmd != nil {
		t.Fatal("empty MCP state returned an action")
	}
}

func TestSettingsInvalidSkillCannotBeEnabled(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsSkills
	o.skills = []settingsSkill{{ID: "invalid:broken", Name: "broken", Source: "project", Valid: false, Error: "description is required"}}
	if cmd := o.activate(m, 1, true); cmd != nil {
		t.Fatal("invalid skill returned an enable command")
	}
	if o.noticeKind != "error" {
		t.Fatalf("invalid skill notice = %q/%q", o.noticeKind, o.notice)
	}
	view := stripANSI(o.viewSized(NewStyles(Charmtone()), 144, 40))
	if strings.Contains(view, "Enter  Toggle") || !strings.Contains(view, "fix SKILL.md") {
		t.Fatalf("invalid skill exposes a mutation:\n%s", view)
	}
}

func TestSettingsLayoutsFitRequestedTerminalClasses(t *testing.T) {
	m, _ := newTestModel(t)
	styles := NewStyles(Charmtone())
	for _, tc := range []struct {
		name          string
		width, height int
		want          []string
	}{
		{name: "40x10 compact", width: 36, height: 6, want: []string{"SETTINGS", "esc"}},
		{name: "80x24 split", width: 76, height: 20, want: []string{"SECTIONS", "DETAIL", "esc close"}},
		{name: "240x60 wide", width: 144, height: 40, want: []string{"SECTIONS", "DETAIL", "Tab sections/content"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newSettingsOverlay(m)
			view := o.viewSized(styles, tc.width, tc.height)
			if got := lipgloss.Width(view); got > tc.width {
				t.Fatalf("width = %d, want <= %d", got, tc.width)
			}
			if got := lipgloss.Height(view); got > tc.height {
				t.Fatalf("height = %d, want <= %d", got, tc.height)
			}
			plain := stripANSI(view)
			for _, want := range tc.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("view missing %q:\n%s", want, plain)
				}
			}
		})
	}
}

func TestSettingsNavigationKeepsDirectValueAccess(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	m.overlay, m.overlayM = overlaySettings, o

	if o.section != settingsGeneral || o.focus != settingsFocusContent {
		t.Fatalf("initial Settings state = %s", o.debugState())
	}
	if cmd := o.update(m, tea.KeyMsg{Type: tea.KeyRight}); cmd != nil {
		t.Fatal("local setting change unexpectedly scheduled a command")
	}
	if got := o.state.PermissionMode; got != "allow" {
		t.Fatalf("permission = %q, want allow", got)
	}

	o.update(m, tea.KeyMsg{Type: tea.KeyTab})
	o.update(m, tea.KeyMsg{Type: tea.KeyDown})
	if o.section != settingsAppearance || o.focus != settingsFocusSections {
		t.Fatalf("section navigation = %s", o.debugState())
	}
	o.update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if o.focus != settingsFocusContent {
		t.Fatal("Enter did not return focus to section content")
	}
	before := o.state.Theme
	o.update(m, tea.KeyMsg{Type: tea.KeyRight})
	if o.state.Theme == before || o.state.Theme == o.committedTheme {
		t.Fatal("Appearance did not enter a transactional preview")
	}
}

func TestSettingsNoColorFocusAndStatusRemainTextual(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsAppearance
	o.state.Theme = "tokyo-night"
	plain := stripANSI(o.viewSized(NewStyles(Charmtone()), 76, 20))
	for _, want := range []string{"[focus]", ">", "[preview]", "saved charmtone"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("text-only projection missing %q:\n%s", want, plain)
		}
	}
}

func TestSettingsMouseSelectsSectionsAndChangesValues(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	m.overlay, m.overlayM = overlaySettings, o
	_ = o.viewSized(NewStyles(Charmtone()), 144, 40)

	var appearance, permission settingsHit
	for _, hit := range o.hits {
		if hit.kind == "section" && hit.index == int(settingsAppearance) {
			appearance = hit
		}
		if hit.kind == "item" && hit.index == 0 {
			permission = hit
		}
	}
	if appearance.w == 0 || permission.w == 0 {
		t.Fatalf("missing Settings mouse regions: appearance=%+v permission=%+v", appearance, permission)
	}
	o.mouse(m, tea.MouseMsg{X: appearance.x, Y: appearance.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if o.section != settingsAppearance || o.focus != settingsFocusSections {
		t.Fatalf("mouse section selection = %s", o.debugState())
	}

	// Re-rendering replaces stale hitboxes with geometry for the new section.
	_ = o.viewSized(NewStyles(Charmtone()), 144, 40)
	var theme settingsHit
	for _, hit := range o.hits {
		if hit.kind == "item" && hit.index == 0 {
			theme = hit
			break
		}
	}
	before := o.state.Theme
	o.mouse(m, tea.MouseMsg{X: theme.x, Y: theme.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	o.mouse(m, tea.MouseMsg{X: theme.x, Y: theme.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if o.state.Theme == before {
		t.Fatal("second click on selected Theme did not preview the next value")
	}
}

func TestSettingsIntegrationsExplainMCPRouteBoundary(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsIntegrations
	o.integrations = []settingsIntegration{{Name: "github", Transport: "http", Status: "connected", Tools: 8}}
	view := stripANSI(o.viewSized(NewStyles(Charmtone()), 144, 40))
	for _, want := range []string{"[connected]", "8 tools", "only in Maestro's native engine", "do not automatically inherit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Integrations detail missing %q:\n%s", want, view)
		}
	}
}

func TestSettingsOverlayConsumesMouseOutsideItsSurface(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	o := newSettingsOverlay(m)
	m.overlay, m.overlayM = overlaySettings, o
	m.input.Set("unchanged")

	updated, cmd := m.updateMouse(tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if cmd != nil || updated.(*Model).overlay != overlaySettings {
		t.Fatal("click outside Settings escaped the modal")
	}
	if got := m.input.Value(); got != "unchanged" {
		t.Fatalf("click traversed Settings into composer: %q", got)
	}
}

func TestSettingsAgentsPersistsReasoningPerRole(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsAgents
	route := o.state.RoleDefaults[settings.RoleDev]
	route.Engine, route.Agent, route.Model = "legacy", "codex", "gpt-5.6-sol"
	o.state.RoleDefaults[settings.RoleDev] = route
	rows := o.rows()
	for i, row := range rows {
		if row.Role == settings.RoleDev && row.Kind == settingReasoning {
			o.selected = i
			o.change(m, row, 1)
			got := m.orch.SettingsSnapshot().RoleDefaults[settings.RoleDev]
			if got.ReasoningEffort != "minimal" {
				t.Fatalf("dev reasoning = %q, want minimal", got.ReasoningEffort)
			}
			if value := o.value(row); value != "minimal" {
				t.Fatalf("Settings value = %q", value)
			}
			return
		}
	}
	t.Fatal("dev reasoning row is missing")
}

func TestSettingsAgentsSelectionRemainsVisibleAfterResize(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsAgents
	o.selected = len(o.rows()) - 1

	for _, size := range [][2]int{{76, 20}, {144, 40}} {
		view := stripANSI(o.viewSized(NewStyles(Charmtone()), size[0], size[1]))
		if !strings.Contains(view, "model slot small") {
			t.Fatalf("%dx%d hid selected row:\n%s", size[0], size[1], view)
		}
	}
}

func TestSettingsWideActionHitboxMatchesWrappedDetailRow(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsIntegrations
	o.integrations = []settingsIntegration{{
		Name: "records", Transport: "streamable-http", Status: "error", Tools: 17,
		Error: "the integration returned a deliberately long terminal-safe diagnostic that wraps across several physical rows",
	}}
	m.overlay, m.overlayM = overlaySettings, o
	view := o.viewSized(NewStyles(Charmtone()), 144, 40)
	visibleActionY := -1
	for row, line := range strings.Split(stripANSI(view), "\n") {
		if strings.Contains(line, "Enter  Reconnect") {
			visibleActionY = row
			break
		}
	}
	if visibleActionY < 0 {
		t.Fatalf("wrapped detail hid its action:\n%s", stripANSI(view))
	}
	var action settingsHit
	for _, hit := range o.hits {
		if hit.kind == "action" {
			action = hit
			break
		}
	}
	if action.w == 0 || action.y != visibleActionY {
		t.Fatalf("action hitbox=%+v, visible row=%d", action, visibleActionY)
	}
	if cmd := o.mouse(m, tea.MouseMsg{X: action.x, Y: visibleActionY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}); cmd == nil {
		t.Fatal("clicking the visible wrapped-detail action did not dispatch reconnect")
	}
}

func TestSettingsTinyEscapeRollsBackThemePreview(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	o := newSettingsOverlay(m)
	o.section = settingsAppearance
	m.overlay, m.overlayM = overlaySettings, o
	original := o.committedTheme
	o.change(m, settingRow{Kind: settingTheme}, 1)
	if o.state.Theme == original {
		t.Fatal("theme preview did not change")
	}

	feed(m, tea.WindowSizeMsg{Width: 10, Height: 4})
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone || m.overlayM != nil {
		t.Fatal("tiny Escape did not close Settings")
	}
	if got := m.styles.T.Hex(TokenCharple); got != ThemeForName(original).Hex(TokenCharple) {
		t.Fatalf("tiny Escape retained preview palette %q; want saved %q", got, ThemeForName(original).Hex(TokenCharple))
	}
	if got := m.orch.SettingsSnapshot().Theme; got != original {
		t.Fatalf("preview mutated persisted theme: %q", got)
	}
}

func TestSettingsSkillInspectionIsOnDemandBoundedAndTerminalSafe(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsSkills
	skill := settingsSkill{
		ID: "project:safe", Name: "safe", Description: "Inspect safely", Source: "project/.agents",
		Scope: "project", Valid: true, Enabled: true, Invokable: true,
	}
	o.skills = []settingsSkill{skill}
	if detail := o.detail(); detail.preview != "" {
		t.Fatal("skill body was present before explicit inspection")
	}

	rawPath := "/tmp/\x1b]52;c;Y29weQ==\a/\u202eskill/SKILL.md"
	content := "# Real skill\n\nRead the selected code.\x1b]52;c;Y29weQ==\a\n" + strings.Repeat("bounded preview line\n", 30)
	o.finishAction(m.orch, settingsActionDoneMsg{action: "skill-inspect", id: skill.ID, path: rawPath, content: content})
	if strings.ContainsAny(o.inspectionPath+o.inspectionBody+o.notice, "\x1b\a") || strings.ContainsRune(o.inspectionPath, '\u202e') {
		t.Fatalf("inspection retained terminal controls: path=%q preview=%q", o.inspectionPath, o.inspectionBody)
	}
	if got := len([]rune(o.inspectionBody)); got > settingsSkillPreviewRunes {
		t.Fatalf("preview runes=%d, max=%d", got, settingsSkillPreviewRunes)
	}
	if got := len(strings.Split(o.inspectionBody, "\n")); got > settingsSkillPreviewLines {
		t.Fatalf("preview lines=%d, max=%d", got, settingsSkillPreviewLines)
	}
	if !strings.Contains(o.inspectionBody, "# Real skill") || !strings.Contains(o.inspectionBody, "preview truncated") {
		t.Fatalf("inspection is not a useful bounded preview: %q", o.inspectionBody)
	}

	// finishAction refreshes the live catalog; provide the selected row again to
	// exercise all responsive renderers without relying on a fixture skill.
	o.skills = []settingsSkill{skill}
	for _, size := range [][2]int{{36, 6}, {76, 20}, {144, 40}} {
		view := o.viewSized(NewStyles(Charmtone()), size[0], size[1])
		plain := stripANSI(view)
		if !strings.Contains(plain, "Preview") && !strings.Contains(plain, "PREVIEW") {
			t.Fatalf("%dx%d omitted inspected preview:\n%s", size[0], size[1], plain)
		}
		if strings.ContainsAny(view, "\a") || strings.Contains(view, "\x1b]52;") {
			t.Fatalf("%dx%d rendered an OSC payload: %q", size[0], size[1], view)
		}
	}
}

func TestSettingsThemeSwatchTruncationIsANSIWidthAwareAt40x10(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsAppearance
	view := o.viewSized(NewStyles(Charmtone()), 36, 6)
	plain := stripANSI(view)
	for _, want := range []string{"Theme", "charmtone", "[saved]"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("40x10 Settings lost %q while truncating ANSI swatches:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.HasSuffix(line, "\x1b") || strings.HasSuffix(line, "\x1b[") {
			t.Fatalf("broken ANSI sequence in compact Settings: %q", line)
		}
	}
}

func TestSettingsSkillCapabilitiesAndWarningsAreExplicit(t *testing.T) {
	m, _ := newTestModel(t)
	o := newSettingsOverlay(m)
	o.section = settingsSkills
	o.skills = []settingsSkill{{
		ID: "project:review", Name: "review", Description: "Review code", Source: "project/.agents",
		Scope: "project", Valid: true, Enabled: true, Invokable: false,
		Warning: "name collision; use the qualified skill ID",
	}}
	view := stripANSI(o.viewSized(NewStyles(Charmtone()), 144, 40))
	for _, want := range []string{"not invokable", "warning", "qualified skill ID", "does not make the skill runnable"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Settings omitted skill capability %q:\n%s", want, view)
		}
	}
	if cmd := o.activate(m, 1, true); cmd == nil {
		t.Fatal("valid warning-bearing skill was incorrectly treated as invalid")
	}
}
