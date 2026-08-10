package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/settings"
)

type settingKind string

const (
	settingPermission settingKind = "permission"
	settingTheme      settingKind = "theme"
	settingEditorMode settingKind = "editor_mode"
	settingUpdates    settingKind = "updates"
	settingEngine     settingKind = "engine"
	settingAgent      settingKind = "agent"
	settingRoleModel  settingKind = "role_model"
	settingReasoning  settingKind = "reasoning"
	settingSlotModel  settingKind = "slot_model"
)

type settingsSectionID int

const (
	settingsGeneral settingsSectionID = iota
	settingsAppearance
	settingsAgents
	settingsProviders
	settingsIntegrations
	settingsSkills
)

type settingsFocus int

const (
	settingsFocusContent settingsFocus = iota
	settingsFocusSections
)

type settingRow struct {
	Label string
	Kind  settingKind
	Role  string
	Slot  string
}

type settingsSection struct {
	ID       settingsSectionID
	Label    string
	Subtitle string
}

var settingsSections = []settingsSection{
	{settingsGeneral, "General", "safety, editor, updates"},
	{settingsAppearance, "Appearance", "theme preview"},
	{settingsAgents, "Agents", "task routes"},
	{settingsProviders, "Providers", "native and plans"},
	{settingsIntegrations, "Integrations", "MCP · native only"},
	{settingsSkills, "Skills", "native + subscription by route"},
}

type settingsProvider struct {
	ID        string
	Label     string
	Kind      string
	Status    string
	Source    string
	Connected bool
	Models    int
}

type settingsIntegration struct {
	Name      string
	Transport string
	Status    string
	Error     string
	Tools     int
}

type settingsSkill struct {
	ID          string
	Name        string
	Description string
	Source      string
	Scope       string
	Valid       bool
	Enabled     bool
	Invokable   bool
	Warning     string
	Error       string
}

type settingsHit struct {
	x, y, w, h int
	kind       string
	index      int
}

type settingsOverlay struct {
	state           settings.Settings
	effectiveRoutes map[string]settings.RoleDefaults
	section         settingsSectionID
	selected        int
	focus           settingsFocus
	models          []string
	committedTheme  string

	providers        []settingsProvider
	providerLoading  bool
	integrations     []settingsIntegration
	mcpLoading       bool
	skills           []settingsSkill
	skillLoading     bool
	inspectedSkill   string
	inspectionPath   string
	inspectionBody   string
	actionGeneration uint64
	actionCancel     context.CancelFunc
	actionWorkspace  orchestrator.WorkspaceSnapshot
	actionSession    string

	noticeKind string
	notice     string
	hits       []settingsHit
	originX    int
	originY    int
}

type settingsProvidersLoadedMsg struct {
	target        *settingsOverlay
	generation    uint64
	workspace     orchestrator.WorkspaceSnapshot
	sessionID     string
	subscriptions []orchestrator.SubscriptionInfo
}

type settingsActionDoneMsg struct {
	target     *settingsOverlay
	generation uint64
	workspace  orchestrator.WorkspaceSnapshot
	sessionID  string
	action     string
	id         string
	path       string
	content    string
	err        error
}

func newSettingsOverlay(m *Model) *settingsOverlay {
	state := m.orch.SettingsSnapshot()
	models := make([]string, 0)
	seen := map[string]bool{}
	for _, info := range m.orch.ModelList(context.Background()) {
		id := qualifiedModelID(info.Provider, info.ID)
		if !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	o := &settingsOverlay{
		state: state, models: models, focus: settingsFocusContent,
		committedTheme: defaultString(state.Theme, "charmtone"),
	}
	o.refreshEffectiveRoutes(m.orch)
	o.refreshProviderSnapshot(m.orch)
	o.refreshEcosystemSnapshot(m.orch)
	return o
}

func (s *settingsOverlay) refreshEffectiveRoutes(orch *orchestrator.Orchestrator) {
	s.effectiveRoutes = map[string]settings.RoleDefaults{}
	for _, role := range []string{settings.RoleOrchestrator, settings.RoleDev, settings.RoleReviewer, settings.RoleDocs} {
		s.effectiveRoutes[role] = orch.TaskRoute(role)
	}
}

func (m *Model) openSettings(section settingsSectionID, loadProviders bool) tea.Cmd {
	o := newSettingsOverlay(m)
	o.section = section
	o.selected = 0
	o.focus = settingsFocusContent
	m.overlay = overlaySettings
	m.overlayM = o
	if loadProviders {
		return o.loadProviders(m)
	}
	return nil
}

// loadProviders checks vendor CLI sessions away from the Bubble Tea event
// loop. SubscriptionList is intentionally bounded by the orchestrator; no
// Settings timer or idle repaint is introduced.
func (s *settingsOverlay) loadProviders(m *Model) tea.Cmd {
	s.providerLoading = true
	guard, ctx := s.beginAction(m, "providers")
	return func() tea.Msg {
		return settingsProvidersLoadedMsg{
			target: s, generation: guard.generation, workspace: guard.workspace, sessionID: guard.sessionID,
			subscriptions: m.orch.SubscriptionList(ctx),
		}
	}
}

type settingsActionGuard struct {
	generation uint64
	workspace  orchestrator.WorkspaceSnapshot
	sessionID  string
}

func (s *settingsOverlay) beginAction(m *Model, kind string) (settingsActionGuard, context.Context) {
	if s.actionCancel != nil {
		s.actionCancel()
	}
	s.providerLoading = kind == "providers"
	s.mcpLoading = kind == "mcp"
	s.skillLoading = kind == "skills"
	s.actionGeneration++
	s.actionWorkspace = m.orch.SnapshotWorkspace()
	s.actionSession = m.orch.Session().ID
	ctx, cancel := context.WithCancel(m.ctx())
	s.actionCancel = cancel
	return settingsActionGuard{
		generation: s.actionGeneration,
		workspace:  s.actionWorkspace,
		sessionID:  s.actionSession,
	}, ctx
}

func (s *settingsOverlay) cancelAction() {
	if s.actionCancel != nil {
		s.actionCancel()
		s.actionCancel = nil
	}
	s.actionGeneration++
	s.providerLoading = false
	s.mcpLoading = false
	s.skillLoading = false
}

func (s *settingsOverlay) accepts(m *Model, generation uint64, workspace orchestrator.WorkspaceSnapshot, sessionID string) bool {
	return generation == s.actionGeneration && sessionID == m.orch.Session().ID && m.orch.WorkspaceIsCurrent(workspace)
}

func (s *settingsOverlay) finishAcceptedAction() {
	if s.actionCancel != nil {
		s.actionCancel()
		s.actionCancel = nil
	}
}

func (s *settingsOverlay) applyProviders(subscriptions []orchestrator.SubscriptionInfo) {
	s.providerLoading = false
	native := make([]settingsProvider, 0, len(s.providers))
	for _, provider := range s.providers {
		if provider.Kind == "provider · native" {
			native = append(native, provider)
		}
	}
	s.providers = s.providers[:0]
	for _, sub := range subscriptions {
		s.providers = append(s.providers, settingsProvider{
			ID: sub.ID, Label: safeIDEPlainText(sub.Label), Kind: "subscription", Status: safeIDEPlainText(sub.Status),
			Source: safeIDEPlainText(sub.CLI) + " CLI", Connected: sub.Authenticated, Models: len(sub.Models),
		})
	}
	s.providers = append(s.providers, native...)
	s.clampSelection()
}

func (s *settingsOverlay) refreshProviderSnapshot(orch *orchestrator.Orchestrator) {
	var providers []settingsProvider
	for _, info := range orch.ProviderList(context.Background()) {
		connected := info.KeySet || !info.RequiresKey
		status := "API key required"
		if connected {
			status = "ready"
		}
		providers = append(providers, settingsProvider{
			ID: info.Name, Label: safeIDEPlainText(info.Name), Kind: "provider · native", Status: safeIDEPlainText(status),
			Source: safeIDEPlainText(defaultString(info.Source, "catalog")), Connected: connected, Models: info.Models,
		})
	}
	s.providers = providers
}

// refreshEcosystemSnapshot is the narrow adapter seam used by the MCP and
// Skills backends. Keeping their view models local prevents Settings from
// taking a dependency on transport or persistence internals.
func (s *settingsOverlay) refreshEcosystemSnapshot(orch *orchestrator.Orchestrator) {
	integrations := orch.MCPServerSummaries(context.Background())
	s.integrations = make([]settingsIntegration, 0, len(integrations))
	for _, integration := range integrations {
		s.integrations = append(s.integrations, settingsIntegration{
			Name: integration.Name, Transport: integration.Type, Status: integration.Status,
			Error: integration.Error, Tools: integration.ToolCount,
		})
	}
	summaries := orch.SkillSummaries(context.Background())
	s.skills = make([]settingsSkill, 0, len(summaries))
	for _, skill := range summaries {
		s.skills = append(s.skills, settingsSkill{
			ID: skill.ID, Name: skill.Name, Description: skill.Description,
			Source: skill.Source, Scope: string(skill.Scope), Valid: skill.Valid,
			Enabled: skill.Enabled, Invokable: skill.UserInvokable,
			Warning: skill.Warning, Error: skill.Error,
		})
	}
	s.clampSelection()
}

func (s *settingsOverlay) reconnectMCP(m *Model, name string) tea.Cmd {
	if s.mcpLoading {
		return nil
	}
	s.mcpLoading = true
	s.noticeKind, s.notice = "loading", "Reconnecting MCP · "+name
	guard, ctx := s.beginAction(m, "mcp")
	return func() tea.Msg {
		return settingsActionDoneMsg{
			target: s, generation: guard.generation, workspace: guard.workspace, sessionID: guard.sessionID,
			action: "mcp-reconnect", id: name, err: m.orch.MCPReconnect(ctx, name),
		}
	}
}

func (s *settingsOverlay) toggleSkill(m *Model, skill settingsSkill) tea.Cmd {
	if s.skillLoading || !skill.Valid {
		if !skill.Valid {
			detail := defaultString(skill.Error, "skill metadata is invalid")
			s.noticeKind, s.notice = "error", "Invalid skill metadata · "+detail
		}
		return nil
	}
	s.skillLoading = true
	verb := "Enabling"
	if skill.Enabled {
		verb = "Disabling"
	}
	s.noticeKind, s.notice = "loading", verb+" skill · "+skill.Name
	guard, ctx := s.beginAction(m, "skills")
	return func() tea.Msg {
		return settingsActionDoneMsg{
			target: s, generation: guard.generation, workspace: guard.workspace, sessionID: guard.sessionID,
			action: "skill-toggle", id: skill.ID,
			err: m.orch.SetSkillEnabled(ctx, skill.ID, !skill.Enabled),
		}
	}
}

func (s *settingsOverlay) inspectSkill(m *Model, skill settingsSkill) tea.Cmd {
	if s.skillLoading || !skill.Valid {
		if !skill.Valid {
			detail := defaultString(skill.Error, "skill metadata is invalid")
			s.noticeKind, s.notice = "error", "Cannot inspect invalid skill · "+detail
		}
		return nil
	}
	s.skillLoading = true
	s.noticeKind, s.notice = "loading", "Inspecting skill · "+skill.Name
	guard, ctx := s.beginAction(m, "skills")
	return func() tea.Msg {
		inspection, err := m.orch.SkillInspect(ctx, skill.ID)
		return settingsActionDoneMsg{
			target: s, generation: guard.generation, workspace: guard.workspace, sessionID: guard.sessionID,
			action: "skill-inspect", id: skill.ID, path: inspection.Path, content: inspection.Content, err: err,
		}
	}
}

func (s *settingsOverlay) refreshSkills(m *Model) tea.Cmd {
	if s.skillLoading {
		return nil
	}
	s.skillLoading = true
	s.noticeKind, s.notice = "loading", "Refreshing skill metadata"
	guard, ctx := s.beginAction(m, "skills")
	return func() tea.Msg {
		return settingsActionDoneMsg{
			target: s, generation: guard.generation, workspace: guard.workspace, sessionID: guard.sessionID,
			action: "skill-refresh", err: m.orch.RefreshSkills(ctx),
		}
	}
}

func (s *settingsOverlay) finishAction(orch *orchestrator.Orchestrator, msg settingsActionDoneMsg) {
	s.finishAcceptedAction()
	s.mcpLoading = false
	s.skillLoading = false
	if msg.err != nil {
		s.noticeKind, s.notice = "error", truncateRunes(msg.err.Error(), 96)
		s.refreshEcosystemSnapshot(orch)
		return
	}
	s.refreshEcosystemSnapshot(orch)
	switch msg.action {
	case "mcp-reconnect":
		s.noticeKind, s.notice = "connected", "MCP reconnected · "+msg.id
	case "skill-toggle":
		s.noticeKind, s.notice = "saved", "Skill preference saved"
	case "skill-refresh":
		s.inspectedSkill, s.inspectionPath, s.inspectionBody = "", "", ""
		s.noticeKind, s.notice = "saved", "Skill catalog refreshed"
	case "skill-inspect":
		safePath := safeIDEPlainText(msg.path)
		s.inspectedSkill, s.inspectionPath = msg.id, safePath
		s.inspectionBody = boundedSkillPreview(msg.content)
		s.noticeKind, s.notice = "inspect", "Source · "+safePath
	}
}

const (
	settingsSkillPreviewLines = 12
	settingsSkillPreviewRunes = 2048
)

// boundedSkillPreview keeps explicitly inspected SKILL.md content useful in
// Settings without retaining a second unbounded document or allowing terminal
// controls into the renderer. Discovery remains metadata-only; this is called
// only after the user presses i and SkillInspect has completed successfully.
func boundedSkillPreview(content string) string {
	content = safeIDEPlainMultilineText(content)
	lines := strings.Split(content, "\n")
	const marker = "… preview truncated"
	contentLineLimit := settingsSkillPreviewLines - 1
	truncated := len(lines) > contentLineLimit
	if len(lines) > contentLineLimit {
		lines = lines[:contentLineLimit]
	}
	// Reserve enough space for the marker and structural newlines so the stored
	// preview remains below the advertised rune bound in every truncation path.
	remaining := settingsSkillPreviewRunes - len([]rune(marker)) - settingsSkillPreviewLines
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		runes := []rune(line)
		if len(runes) > remaining {
			if remaining > 0 {
				out = append(out, string(runes[:remaining]))
			}
			truncated = true
			remaining = 0
			break
		}
		out = append(out, line)
		remaining -= len(runes)
		if remaining == 0 {
			truncated = true
			break
		}
	}
	if truncated {
		out = append(out, marker)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func (s *settingsOverlay) rows() []settingRow {
	switch s.section {
	case settingsGeneral:
		return []settingRow{
			{Label: "Permission mode", Kind: settingPermission},
			{Label: "Editor mode", Kind: settingEditorMode},
			{Label: "Update checks", Kind: settingUpdates},
		}
	case settingsAppearance:
		return []settingRow{{Label: "Theme", Kind: settingTheme}}
	case settingsAgents:
		rows := make([]settingRow, 0, 18)
		for _, role := range []string{settings.RoleOrchestrator, settings.RoleDev, settings.RoleReviewer, settings.RoleDocs} {
			rows = append(rows,
				settingRow{Label: role + " engine", Kind: settingEngine, Role: role},
				settingRow{Label: role + " agent", Kind: settingAgent, Role: role},
				settingRow{Label: role + " model", Kind: settingRoleModel, Role: role},
				settingRow{Label: role + " reasoning", Kind: settingReasoning, Role: role},
			)
		}
		return append(rows,
			settingRow{Label: "model slot large", Kind: settingSlotModel, Slot: "large"},
			settingRow{Label: "model slot small", Kind: settingSlotModel, Slot: "small"},
		)
	default:
		return nil
	}
}

func (s *settingsOverlay) value(row settingRow) string {
	switch row.Kind {
	case settingPermission:
		return defaultString(s.state.PermissionMode, settings.PermAsk)
	case settingTheme:
		return defaultString(s.state.Theme, "charmtone")
	case settingEditorMode:
		return defaultString(s.state.EditorMode, "standard")
	case settingUpdates:
		if s.state.DisableUpdateChecks {
			return "off"
		}
		return "on · every 24h"
	case settingEngine:
		return engineDisplayName(defaultString(s.state.RoleDefaults[row.Role].Engine, "native"))
	case settingAgent:
		return defaultString(s.state.RoleDefaults[row.Role].Agent, "auto")
	case settingRoleModel:
		route := s.state.RoleDefaults[row.Role]
		if route.Model != "" {
			return route.Model
		}
		if effective := s.effectiveRoutes[row.Role].Model; effective != "" {
			return effective + " · inherited"
		}
		return "auto"
	case settingReasoning:
		route := s.state.RoleDefaults[row.Role]
		if route.ReasoningSet {
			return defaultString(route.ReasoningEffort, "auto")
		}
		return defaultString(s.effectiveRoutes[row.Role].ReasoningEffort, "auto") + " · inherited"
	case settingSlotModel:
		return defaultString(s.state.ModelSlots[row.Slot], "unset")
	}
	return ""
}

func engineDisplayName(engine string) string {
	if engine == "legacy" {
		return "subscription"
	}
	if engine == "native" || engine == "" {
		return "provider · native"
	}
	return engine
}

func (s *settingsOverlay) View(styles Styles, width int) string {
	return s.viewSized(styles, width, 28)
}

func (s *settingsOverlay) sectionIndex() int {
	for i, section := range settingsSections {
		if section.ID == s.section {
			return i
		}
	}
	return 0
}

func (s *settingsOverlay) itemCount() int {
	switch s.section {
	case settingsGeneral, settingsAppearance, settingsAgents:
		return len(s.rows())
	case settingsProviders:
		return len(s.providers)
	case settingsIntegrations:
		return len(s.integrations)
	case settingsSkills:
		return len(s.skills)
	default:
		return 0
	}
}

func (s *settingsOverlay) clampSelection() {
	s.selected = clamp(s.selected, 0, max(s.itemCount()-1, 0))
}

func (s *settingsOverlay) selectSection(index int) {
	index = clamp(index, 0, len(settingsSections)-1)
	s.section = settingsSections[index].ID
	s.selected = 0
	s.noticeKind, s.notice = "", ""
}

func (s *settingsOverlay) moveSection(delta int) {
	index := (s.sectionIndex() + delta) % len(settingsSections)
	if index < 0 {
		index += len(settingsSections)
	}
	s.selectSection(index)
}

func (s *settingsOverlay) moveItem(delta int) {
	count := s.itemCount()
	if count == 0 {
		s.selected = 0
		return
	}
	s.selected = (s.selected + delta) % count
	if s.selected < 0 {
		s.selected += count
	}
}

func (s *settingsOverlay) close(m *Model) {
	s.cancelAction()
	m.applyTheme(s.committedTheme)
	m.overlay = overlayNone
	m.overlayM = nil
}

func (s *settingsOverlay) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	s.clampSelection()
	switch msg.Type {
	case tea.KeyEsc:
		s.close(m)
		return nil
	case tea.KeyTab:
		if s.focus == settingsFocusContent {
			s.focus = settingsFocusSections
		} else {
			s.focus = settingsFocusContent
		}
		return nil
	case tea.KeyShiftTab:
		if s.focus == settingsFocusContent {
			s.focus = settingsFocusSections
		} else {
			s.focus = settingsFocusContent
		}
		return nil
	case tea.KeyUp:
		if s.focus == settingsFocusSections {
			s.moveSection(-1)
		} else {
			s.moveItem(-1)
		}
		return nil
	case tea.KeyDown:
		if s.focus == settingsFocusSections {
			s.moveSection(1)
		} else {
			s.moveItem(1)
		}
		return nil
	case tea.KeyLeft:
		if s.focus == settingsFocusSections {
			s.moveSection(-1)
			return nil
		}
		return s.activate(m, -1, false)
	case tea.KeyRight:
		if s.focus == settingsFocusSections {
			s.focus = settingsFocusContent
			return nil
		}
		return s.activate(m, 1, false)
	case tea.KeyEnter, tea.KeySpace:
		if s.focus == settingsFocusSections {
			s.focus = settingsFocusContent
			return nil
		}
		return s.activate(m, 1, true)
	case tea.KeyRunes:
		key := strings.ToLower(msg.String())
		switch key {
		case "j":
			if s.focus == settingsFocusSections {
				s.moveSection(1)
			} else {
				s.moveItem(1)
			}
			return nil
		case "k":
			if s.focus == settingsFocusSections {
				s.moveSection(-1)
			} else {
				s.moveItem(-1)
			}
			return nil
		case "[":
			s.moveSection(-1)
			return nil
		case "]":
			s.moveSection(1)
			return nil
		case "1", "2", "3", "4", "5", "6":
			s.selectSection(int(key[0] - '1'))
			return nil
		case "r":
			if s.section == settingsProviders {
				return s.loadProviders(m)
			}
			if s.section == settingsIntegrations {
				s.refreshEcosystemSnapshot(m.orch)
				s.noticeKind, s.notice = "info", "MCP status snapshot refreshed"
				return nil
			}
			if s.section == settingsSkills {
				return s.refreshSkills(m)
			}
		case "i":
			if s.section == settingsIntegrations && s.selected < len(s.integrations) {
				s.noticeKind, s.notice = "inspect", "Details shown · "+s.integrations[s.selected].Name
				return nil
			}
			if s.section == settingsSkills && s.selected < len(s.skills) {
				return s.inspectSkill(m, s.skills[s.selected])
			}
		}
	}
	return nil
}

func (s *settingsOverlay) activate(m *Model, delta int, explicit bool) tea.Cmd {
	switch s.section {
	case settingsGeneral, settingsAppearance, settingsAgents:
		rows := s.rows()
		if len(rows) == 0 {
			return nil
		}
		row := rows[s.selected]
		if explicit && row.Kind == settingTheme {
			s.commitTheme(m)
			return nil
		}
		s.change(m, row, delta)
	case settingsProviders:
		if !explicit {
			return nil
		}
		if s.selected >= len(s.providers) {
			return s.loadProviders(m)
		}
		provider := s.providers[s.selected]
		// A preview never leaks into another workspace: opening the dedicated
		// connection manager is equivalent to closing Settings without save.
		s.cancelAction()
		m.applyTheme(s.committedTheme)
		m.overlay = overlayProviders
		m.overlayM = newProvidersOverlay(m.orch, provider.ID)
	case settingsIntegrations:
		if explicit && s.selected < len(s.integrations) {
			return s.reconnectMCP(m, s.integrations[s.selected].Name)
		}
	case settingsSkills:
		if explicit {
			if s.selected < len(s.skills) {
				return s.toggleSkill(m, s.skills[s.selected])
			}
			return s.refreshSkills(m)
		}
	}
	return nil
}

func cloneSettingsState(in settings.Settings) settings.Settings {
	out := in
	out.RoleDefaults = make(map[string]settings.RoleDefaults, len(in.RoleDefaults))
	for role, route := range in.RoleDefaults {
		out.RoleDefaults[role] = route
	}
	out.ModelSlots = make(map[string]string, len(in.ModelSlots))
	for slot, model := range in.ModelSlots {
		out.ModelSlots[slot] = model
	}
	return out
}

func (s *settingsOverlay) change(m *Model, row settingRow, delta int) {
	next := cloneSettingsState(s.state)
	switch row.Kind {
	case settingPermission:
		next.PermissionMode = cycleValue(next.PermissionMode, []string{settings.PermAsk, settings.PermAllow, settings.PermDeny, settings.PermYolo}, delta)
	case settingTheme:
		next.Theme = cycleValue(next.Theme, themeNames(), delta)
		s.state = next
		s.section = settingsAppearance
		s.selected = 0
		m.applyTheme(next.Theme)
		s.noticeKind, s.notice = "preview", "Preview active · Enter saves · Esc restores"
		return
	case settingEditorMode:
		next.EditorMode = cycleValue(next.EditorMode, []string{"standard", "vim"}, delta)
	case settingUpdates:
		next.DisableUpdateChecks = !next.DisableUpdateChecks
	case settingEngine:
		route := next.RoleDefaults[row.Role]
		route.Engine = cycleValue(route.Engine, []string{"native", "legacy"}, delta)
		route = normalizeSettingsRouteReasoning(m.orch, route)
		next.RoleDefaults[row.Role] = route
	case settingAgent:
		route := next.RoleDefaults[row.Role]
		route.Agent = cycleValue(route.Agent, []string{"", "codex", "claude", "cursor", "opencode"}, delta)
		route = normalizeSettingsRouteReasoning(m.orch, route)
		next.RoleDefaults[row.Role] = route
	case settingRoleModel:
		route := next.RoleDefaults[row.Role]
		route.Model = cycleValue(route.Model, append([]string{""}, s.models...), delta)
		route = normalizeSettingsRouteReasoning(m.orch, route)
		next.RoleDefaults[row.Role] = route
	case settingReasoning:
		route := next.RoleDefaults[row.Role]
		capabilityRoute := route
		if capabilityRoute.Model == "" && capabilityRoute.Engine != "legacy" {
			capabilityRoute = m.orch.TaskRoute(row.Role)
		}
		values := m.orch.ReasoningEfforts(capabilityRoute.Engine, capabilityRoute.Agent, capabilityRoute.Model)
		current := route.ReasoningEffort
		if !route.ReasoningSet {
			current = capabilityRoute.ReasoningEffort
		}
		current = defaultString(current, "auto")
		route.ReasoningEffort = cycleValue(current, values, delta)
		if route.ReasoningEffort == "auto" {
			route.ReasoningEffort = ""
		}
		route.ReasoningSet = true
		next.RoleDefaults[row.Role] = route
	case settingSlotModel:
		next.ModelSlots[row.Slot] = cycleValue(next.ModelSlots[row.Slot], append([]string{""}, s.models...), delta)
	}
	persisted := cloneSettingsState(next)
	persisted.Theme = s.committedTheme
	if err := m.orch.UpdateSettings(context.Background(), persisted); err != nil {
		s.noticeKind, s.notice = "error", truncateRunes(err.Error(), 80)
		return
	}
	s.state = next
	if row.Kind == settingUpdates && next.DisableUpdateChecks {
		m.availableUpdate = ""
	}
	s.refreshEffectiveRoutes(m.orch)
	if m.perm != nil {
		m.perm.SetMode(next.PermissionMode)
	}
	// Rebuild the palette/editor with the current preview while reading the
	// newly persisted editor mode from the orchestrator.
	m.applyTheme(next.Theme)
	s.noticeKind, s.notice = "saved", "Saved to settings.json"
}

func normalizeSettingsRouteReasoning(orch *orchestrator.Orchestrator, route settings.RoleDefaults) settings.RoleDefaults {
	want := defaultString(route.ReasoningEffort, "auto")
	for _, allowed := range orch.ReasoningEfforts(route.Engine, route.Agent, route.Model) {
		if want == allowed {
			return route
		}
	}
	route.ReasoningEffort = ""
	route.ReasoningSet = false
	return route
}

func (s *settingsOverlay) commitTheme(m *Model) {
	next := cloneSettingsState(s.state)
	if err := m.orch.UpdateSettings(context.Background(), next); err != nil {
		m.applyTheme(s.committedTheme)
		s.state.Theme = s.committedTheme
		s.noticeKind, s.notice = "error", truncateRunes(err.Error(), 80)
		return
	}
	s.committedTheme = next.Theme
	m.applyTheme(next.Theme)
	s.noticeKind, s.notice = "saved", "Theme saved · "+next.Theme
}

func themeSwatch(name string) string {
	theme := ThemeForName(name)
	return lipgloss.NewStyle().Foreground(theme.Color(TokenCharple)).Render("●") +
		lipgloss.NewStyle().Foreground(theme.Color(TokenDolly)).Render("●") +
		lipgloss.NewStyle().Foreground(theme.Color(TokenJulep)).Render("●")
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func cycleValue(current string, values []string, delta int) string {
	if len(values) == 0 {
		return current
	}
	index := 0
	for i, value := range values {
		if value == current {
			index = i
			break
		}
	}
	index = (index + delta) % len(values)
	if index < 0 {
		index += len(values)
	}
	return values[index]
}

func themeNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, name := range append(ThemeNames(), editor.ThemeNames()...) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func (m *Model) applyTheme(name string) {
	theme := ThemeForName(name)
	m.styles = NewStyles(theme)
	if m.input != nil {
		m.input.styles = m.styles
	}
	if m.sidebar != nil {
		m.sidebar.T = theme
	}
	m.md, _ = newMarkdownRenderer(m.styles)
	m.invalidateMessageCaches()
	if m.ide != nil && m.ide.UI != nil {
		m.ide.UI.SetPaletteValue(theme.EditorPalette())
		m.ide.Ed.SetKeymap(m.orch.SettingsSnapshot().EditorMode)
	}
	// The viewport stores already-rendered ANSI. Invalidating message caches
	// alone leaves the old palette visible until the next resize or stream
	// event, so a live preview must rebuild the transcript immediately.
	m.renderMessages()
}

func (s *settingsOverlay) mouse(m *Model, msg tea.MouseMsg) tea.Cmd {
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		if s.focus == settingsFocusSections {
			s.moveSection(delta)
		} else {
			s.moveItem(delta)
		}
		return nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil
	}
	x, y := msg.X-s.originX, msg.Y-s.originY
	for _, hit := range s.hits {
		if x < hit.x || x >= hit.x+hit.w || y < hit.y || y >= hit.y+hit.h {
			continue
		}
		switch hit.kind {
		case "section":
			s.selectSection(hit.index)
			s.focus = settingsFocusSections
		case "item":
			wasSelected := s.focus == settingsFocusContent && s.selected == hit.index
			s.selected = hit.index
			s.focus = settingsFocusContent
			if wasSelected {
				return s.activate(m, 1, false)
			}
		case "action":
			s.focus = settingsFocusContent
			return s.activate(m, 1, true)
		case "previous-section":
			s.moveSection(-1)
		case "next-section":
			s.moveSection(1)
		}
		return nil
	}
	return nil
}

func (s *settingsOverlay) debugState() string {
	return fmt.Sprintf("section=%s focus=%d item=%d", settingsSections[s.sectionIndex()].Label, s.focus, s.selected)
}
