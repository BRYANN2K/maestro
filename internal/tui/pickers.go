package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/session"
)

// overlayModel is the interface implemented by overlay states.
type overlayModel interface {
	View(styles Styles, width int) string
}

// visibleRows sizes the scrolling window of the list overlays.
const visibleRows = 12

// windowTop returns the first index of the visible window with the
// selection centered while it can be (opencode-style cursor centering),
// pinned to the top and bottom at the ends.
func windowTop(selected, total int) int {
	if total <= visibleRows {
		return 0
	}
	half := visibleRows / 2
	var top int
	switch {
	case selected < half:
		top = 0
	case selected >= total-half:
		top = total - visibleRows
	default:
		top = selected - half
	}
	if top < 0 {
		top = 0
	}
	if top+visibleRows > total {
		top = total - visibleRows
	}
	return top
}

// scrollUpHint reports whether the list can scroll further up (↑ indicator).
func scrollUpHint(top int) bool { return top > 0 }

// scrollDownHint reports whether the list can scroll further down (↓
// indicator).
func scrollDownHint(top, total int) bool {
	return top+visibleRows < total
}

// listOverlay is a fuzzy-filtered picker with match highlighting and
// shortcut hints (B11 §11.6). Optional group paging (model provider picker):
// when groups is set, h/l page between groups of items (opencode-style).
type listOverlay struct {
	title       string
	items       []string
	query       string
	selected    int
	disabled    map[string]bool
	valueOf     func(string) string
	activeValue string

	groups     []string            // ordered group names (paging)
	groupIdx   int                 // active group
	groupItems map[string][]string // group name → items (without headers)
	groupMeta  map[string]string   // group name → header line
}

// grouped reports whether provider paging is active.
func (l *listOverlay) grouped() bool { return l.groups != nil }

// pageItems returns the visible items: the active group's items when paging
// is on, the flat list otherwise — both filtered by the fuzzy query.
func (l *listOverlay) pageItems() []string {
	if !l.grouped() {
		return l.filterItems(l.items)
	}
	name := l.groups[l.groupIdx]
	return l.filterItems(l.groupItems[name])
}

// switchGroup moves the active group by delta with wrap-around and resets
// the query and selection.
func (l *listOverlay) switchGroup(delta int) {
	if !l.grouped() || len(l.groups) <= 1 {
		return
	}
	l.groupIdx = (l.groupIdx + delta + len(l.groups)) % len(l.groups)
	l.query = ""
	l.selected = 0
}

// groupPageable reports whether ←/→ provider paging is available.
func (l *listOverlay) groupPageable() bool {
	return l.grouped() && len(l.groups) > 1
}

func (l *listOverlay) selectable(item string) bool {
	return l.disabled == nil || !l.disabled[item]
}

func (l *listOverlay) ensureSelectable() {
	items := l.Filter()
	if len(items) == 0 {
		l.selected = 0
		return
	}
	if l.selected >= 0 && l.selected < len(items) && l.selectable(items[l.selected]) {
		return
	}
	for i := max(l.selected, 0); i < len(items); i++ {
		if l.selectable(items[i]) {
			l.selected = i
			return
		}
	}
	for i := min(l.selected, len(items)-1); i >= 0; i-- {
		if l.selectable(items[i]) {
			l.selected = i
			return
		}
	}
}

func (l *listOverlay) backspace() {
	if len(l.query) == 0 {
		return
	}
	runes := []rune(l.query)
	l.query = string(runes[:len(runes)-1])
	l.selected = 0
}

func (l *listOverlay) up() {
	l.ensureSelectable()
	items := l.Filter()
	for i := l.selected - 1; i >= 0; i-- {
		if l.selectable(items[i]) {
			l.selected = i
			return
		}
	}
}

func (l *listOverlay) down() {
	l.ensureSelectable()
	items := l.Filter()
	for i := l.selected + 1; i < len(items); i++ {
		if l.selectable(items[i]) {
			l.selected = i
			return
		}
	}
}

func (l *listOverlay) selectedValue() string {
	items := l.Filter()
	if l.selected < 0 || l.selected >= len(items) {
		return ""
	}
	if !l.selectable(items[l.selected]) {
		return ""
	}
	if l.valueOf != nil {
		return l.valueOf(items[l.selected])
	}
	return stripHint(items[l.selected])
}

// Filter applies the fuzzy query to the visible page.
func (l *listOverlay) Filter() []string {
	return l.pageItems()
}

// filterItems runs the fuzzy query over a raw item list.
func (l *listOverlay) filterItems(items []string) []string {
	if l.query == "" {
		return items
	}
	matches, scores := fuzzyMatch(l.query, items)
	// stable sort by score desc
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if scores[j] > scores[i] {
				matches[i], matches[j] = matches[j], matches[i]
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}
	return matches
}

// fuzzyMatch returns items containing the query as a subsequence, with
// their scores.
func fuzzyMatch(query string, items []string) ([]string, []int) {
	var out []string
	var scores []int
	for _, it := range items {
		if s := fuzzySubsequence(query, it); s >= 0 {
			out = append(out, it)
			scores = append(scores, s)
		}
	}
	return out, scores
}

// fuzzySubsequence scores a subsequence match (0-based chars of query in
// item); -1 when no match.
func fuzzySubsequence(query, item string) int {
	if query == "" {
		return 0
	}
	queryRunes := []rune(strings.ToLower(query))
	itemRunes := []rune(strings.ToLower(item))
	qi, ii := 0, 0
	first := -1
	for qi < len(queryRunes) && ii < len(itemRunes) {
		if queryRunes[qi] == itemRunes[ii] {
			if first < 0 {
				first = ii
			}
			qi++
		}
		ii++
	}
	if qi != len(queryRunes) {
		return -1
	}
	// -1 is the no-match sentinel. Long labels can otherwise push a valid
	// late match below zero and make it disappear from the picker entirely.
	return max(100*len(queryRunes)-ii-first, 0)
}

// highlight renders the item with matched query chars in accent.
func (l *listOverlay) highlight(styles Styles, item string) string {
	if l.query == "" {
		return item
	}
	q := []rune(strings.ToLower(l.query))
	var b strings.Builder
	qi := 0
	for _, r := range item {
		if qi < len(q) && unicode.ToLower(r) == q[qi] {
			b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render(string(r)))
			qi++
		} else {
			b.WriteString(string(r))
		}
	}
	return b.String()
}

// View renders the picker.
func (l *listOverlay) View(styles Styles, width int) string {
	var b strings.Builder
	b.WriteString(styles.DialogTitle(l.title) + "\n")
	if l.grouped() {
		b.WriteString(styles.PanelTitle(l.groupMeta[l.groups[l.groupIdx]]) + "\n")
	}
	b.WriteString(styles.InputHint.Render("filter: " + l.query + "\n\n"))
	items := l.Filter()
	if len(items) == 0 {
		b.WriteString(styles.SidebarItem.Render("no matches") + "\n")
	} else {
		top := windowTop(l.selected, len(items))
		if scrollUpHint(top) {
			b.WriteString(styles.SidebarItem.Render("↑ …") + "\n")
		}
		for i := top; i < len(items) && i < top+visibleRows; i++ {
			item := items[i]
			marker := "  "
			st := styles.SidebarItem
			if !l.selectable(item) {
				b.WriteString(styles.PanelTitle(clampANSIWidth(item, max(width-2, 1))) + "\n")
				continue
			}
			if i == l.selected {
				marker = "▸ "
				st = styles.SidebarActive
			} else if l.valueOf != nil && l.activeValue != "" && l.valueOf(item) == l.activeValue {
				marker = "● "
				st = styles.SidebarItem.Bold(true)
			}
			rendered := clampANSIWidth(marker+l.highlight(styles, item), max(width-2, 1))
			if h := hintOf(item); h != "" {
				rendered += " " + styles.Hint.Render(h)
			}
			b.WriteString(st.Width(width-2).MaxWidth(width-2).Render(rendered) + "\n")
		}
		if scrollDownHint(top, len(items)) {
			b.WriteString(styles.SidebarItem.Render("↓ …") + "\n")
		}
	}
	b.WriteString("\n" + styles.Hint.Render(l.hintLineForWidth(width)))
	return b.String()
}

func (l *listOverlay) hintLineForWidth(width int) string {
	if width < 48 {
		if l.groupPageable() {
			return "esc cancel · enter select · ↑/↓ · ←/→"
		}
		return "esc cancel · enter select · ↑/↓"
	}
	return l.hintLine()
}

// hintLine renders the footer shortcut hints, adding ←/→ provider paging
// when active.
func (l *listOverlay) hintLine() string {
	base := "type to filter · ↑/↓ · enter select · esc cancel"
	if l.groupPageable() {
		return "←/→ provider · " + base
	}
	return base
}

// stripHint removes a trailing "[key]" hint.
func stripHint(item string) string {
	if i := strings.LastIndex(item, "  "); i > 0 && strings.HasSuffix(item, "]") {
		return strings.TrimSpace(item[:i])
	}
	return item
}

func hintOf(item string) string {
	if i := strings.LastIndex(item, "  "); i > 0 && strings.HasSuffix(item, "]") {
		return strings.TrimSpace(item[i:])
	}
	return ""
}

// paletteCommands mirrors the inline slash catalog.
var paletteCommands = func() []string {
	commands := make([]string, 0, len(slashCatalog))
	for _, suggestion := range slashCatalog {
		commands = append(commands, suggestion.Command)
	}
	return commands
}()

// newPaletteOverlay lists commands plus user-invocable skills (§5.6, 9.2).
func newPaletteOverlay(orch *orchestrator.Orchestrator) *listOverlay {
	items := append([]string(nil), paletteCommands...)
	for _, s := range orch.SkillList() {
		items = append(items, "skill:"+s.Name)
	}
	return &listOverlay{title: "Commands + skills", items: items}
}

func newCheckpointOverlay(orch *orchestrator.Orchestrator) *listOverlay {
	checkpoints := orch.CheckpointList(context.Background())
	ids := make(map[string]string, len(checkpoints))
	items := make([]string, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		label := fmt.Sprintf("%s  ·  %s  ·  %d file(s)", checkpoint.ID, checkpoint.Created.Local().Format("02 Jan 15:04"), len(checkpoint.Changed))
		items = append(items, label)
		ids[label] = checkpoint.ID
	}
	if len(items) == 0 {
		items = []string{"No checkpoints yet · created before build runs"}
	}
	return &listOverlay{
		title:    "Checkpoints · select to prepare /rewind",
		items:    items,
		disabled: map[string]bool{"No checkpoints yet · created before build runs": true},
		valueOf: func(item string) string {
			return ids[item]
		},
	}
}

// newModelPickerOverlay lists the configured models, grouped by provider
// with badges (catalog pricing / discovered) (§10.7).
func newModelPickerOverlay(orch *orchestrator.Orchestrator) *listOverlay {
	infos := orch.ModelList(context.Background())
	byProvider := map[string][]string{}
	values := map[string]string{}
	providerInfos := map[string]orchestrator.ProviderInfo{}
	for _, provider := range orch.ProviderList(context.Background()) {
		providerInfos[provider.Name] = provider
	}
	for _, m := range infos {
		badges := ""
		if m.Discovered {
			badges += " discovered"
		}
		if info, ok := providerInfos[m.Provider]; ok && info.KeySet {
			badges += " authenticated"
		}
		price := ""
		if m.PriceIn > 0 || m.PriceOut > 0 {
			price = fmt.Sprintf("  $%.2f/$%.2f", m.PriceIn, m.PriceOut)
		}
		contextWindow := ""
		if m.Context > 0 {
			contextWindow = fmt.Sprintf("  %dk", m.Context/1000)
		}
		reasoning := ""
		if m.Reasoning {
			reasoning = "  reasoning"
		}
		qualified := qualifiedModelID(m.Provider, m.ID)
		baseLabel := fmt.Sprintf("%s  · %s  %s%s%s%s",
			safeIDEPlainText(qualified), safeIDEPlainText(m.Provider), safeIDEPlainText(m.Name),
			contextWindow, price, reasoning+badges)
		label := baseLabel
		for duplicate := 2; ; duplicate++ {
			if _, exists := values[label]; !exists {
				break
			}
			label = fmt.Sprintf("%s  · #%d", baseLabel, duplicate)
		}
		values[label] = qualified
		byProvider[m.Provider] = append(byProvider[m.Provider], label)
	}
	var order []string
	for name := range byProvider {
		order = append(order, name)
	}
	sort.Strings(order)
	groups := order
	groupItems := map[string][]string{}
	groupMeta := map[string]string{}
	disabled := map[string]bool{}
	for _, name := range order {
		info := providerInfos[name]
		if _, ok := providerInfos[name]; !ok {
			info.Name = name
			info.RequiresKey = !modelProviderIsLocal(name)
		}
		auth := ""
		switch {
		case info.RequiresKey && !info.KeySet:
			auth = " · API key missing"
		case info.KeySet:
			auth = " · authenticated"
		case !info.RequiresKey:
			auth = " · local"
		}
		groupMeta[name] = fmt.Sprintf("▾ %s  · %d models%s", safeIDEPlainText(name), len(byProvider[name]), auth)
		groupItems[name] = byProvider[name]
	}
	active := qualifiedModelIDFromActive(orch.ActiveModel())
	known := make(map[string]bool, len(values))
	for _, value := range values {
		known[value] = true
	}
	if active != "" && !known[active] {
		// Migrate a legacy raw stored model only when exactly one provider
		// advertises it. This also handles raw API IDs containing slashes;
		// ambiguous bare values deliberately stay unresolved.
		candidate := ""
		for _, info := range infos {
			if info.ID != active {
				continue
			}
			handle := qualifiedModelID(info.Provider, info.ID)
			if candidate != "" && candidate != handle {
				candidate = ""
				break
			}
			candidate = handle
		}
		if candidate != "" {
			active = candidate
		}
	}
	// Open on the group hosting the active model, unless that provider is
	// missing its API key (or is catalog-only, i.e. unregistered) — then
	// jump to the first authenticated provider so the user lands on
	// something usable instead of a dead group.
	groupIdx := 0
	if len(groups) > 0 {
		if active != "" {
			for i, name := range groups {
				if strings.HasPrefix(active, name+"/") {
					groupIdx = i
					break
				}
			}
		}
		if info, ok := providerInfos[groups[groupIdx]]; !ok || (info.RequiresKey && !info.KeySet) {
			usableGroup := -1
			for i, name := range groups {
				if pi, ok := providerInfos[name]; ok && (!pi.RequiresKey || pi.KeySet) {
					usableGroup = i
					break
				}
			}
			if usableGroup >= 0 {
				groupIdx = usableGroup
			} else {
				// Never strand the picker on an active provider that cannot run.
				// With no authenticated/local model group, return to the first
				// catalog group and let the user browse or start authentication.
				groupIdx = 0
			}
		}
	}
	return &listOverlay{
		title:    "Models · provider / model",
		query:    "",
		disabled: disabled,
		valueOf: func(item string) string {
			return values[item]
		},
		activeValue: active,
		groups:      groups,
		groupIdx:    groupIdx,
		groupItems:  groupItems,
		groupMeta:   groupMeta,
	}
}

func modelProviderIsLocal(name string) bool {
	switch strings.ToLower(name) {
	case "ollama", "llamacpp", "lmstudio", "litellm":
		return true
	default:
		return false
	}
}

func qualifiedModelID(provider, id string) string {
	id = strings.TrimSpace(id)
	provider = strings.TrimSpace(provider)
	// ModelInfo.ID is always the provider's raw API ID. Slashes inside it are
	// API namespaces (for example accounts/acme/models/coder), so every
	// selectable handle still needs the Maestro provider prefix.
	if provider == "" {
		return id
	}
	return provider + "/" + id
}

func qualifiedModelIDFromActive(id string) string {
	return strings.TrimSpace(id)
}

// newSessionPickerOverlay lists the project sessions.
func newSessionPickerOverlay(orch *orchestrator.Orchestrator) *listOverlay {
	summaries, err := orch.ListSessionSummaries(context.Background())
	if err != nil {
		const unavailable = "(sessions unavailable)"
		return &listOverlay{
			title: "Sessions", items: []string{unavailable},
			disabled: map[string]bool{unavailable: true},
		}
	}
	return newSessionSummaryPickerOverlay(summaries, orch.Session().ID)
}

func newSessionSummaryPickerOverlay(summaries []session.Summary, activeID string) *listOverlay {
	if len(summaries) == 0 {
		const empty = "(no sessions)"
		return &listOverlay{title: "Sessions", items: []string{empty}, disabled: map[string]bool{empty: true}}
	}
	items := make([]string, 0, len(summaries))
	values := make(map[string]string, len(summaries))
	disabled := map[string]bool{}
	for _, summary := range summaries {
		phase := strings.ToUpper(string(summary.Phase))
		if phase == "" {
			phase = "UNKNOWN"
		}
		label := summary.DisplayTitle + "  · " + phase
		if branch := strings.TrimPrefix(summary.WorkspaceRef, "refs/heads/"); branch != "" {
			label += " · " + branch
		}
		if summary.Disabled {
			reason := strings.TrimSpace(summary.DisabledReason)
			if reason == "" {
				reason = "unavailable"
			}
			label += " — " + reason
			disabled[label] = true
		}
		items = append(items, label)
		values[label] = summary.ID
	}
	return &listOverlay{
		title: "Resume session", items: items, disabled: disabled,
		activeValue: activeID,
		valueOf: func(item string) string {
			return values[item]
		},
	}
}

const createWorkspacePickerValue = "maestro:create-workspace"

// newWorkspacePickerOverlay presents exact Git worktree records while keeping
// paths as values rather than parsing them back out of display text.
func newWorkspacePickerOverlay(workspaces []git.Workspace, current string) *listOverlay {
	createLabel := "＋ Create linked worktree…"
	items := []string{createLabel}
	values := map[string]string{createLabel: createWorkspacePickerValue}
	disabled := map[string]bool{}
	seen := map[string]int{}
	for _, workspace := range workspaces {
		branch := workspace.Branch
		if branch == "" {
			branch = "detached"
		}
		state := "clean"
		if workspace.Dirty {
			state = "changed"
		}
		label := fmt.Sprintf("%s  · %s · %s", safeIDEPlainText(branch), state, safeIDEPlainText(filepath.Clean(workspace.Path)))
		seen[label]++
		if seen[label] > 1 {
			label = fmt.Sprintf("%s · %d", label, seen[label])
		}
		if !workspace.Healthy {
			reason := strings.TrimSpace(safeIDEPlainText(workspace.DisabledReason))
			if reason == "" {
				reason = "unavailable"
			}
			label += " — " + reason
			disabled[label] = true
		}
		items = append(items, label)
		values[label] = workspace.Path
	}
	return &listOverlay{
		title: "Git workspaces", items: items, disabled: disabled,
		activeValue: current,
		valueOf: func(item string) string {
			return values[item]
		},
	}
}

func newCoachModeOverlay(state orchestrator.CoachState) *listOverlay {
	items := []string{
		"Guided  · examples fade as evidence grows",
		"Challenge · reason first, ask for help on demand",
		"Next lesson · open the contextual activity",
		"Progress · private per-skill summary",
		"Off · no contextual offers",
	}
	values := map[string]string{
		items[0]: "guided",
		items[1]: "challenge",
		items[2]: "next",
		items[3]: "status",
		items[4]: "off",
	}
	disabled := map[string]bool{}
	if state.Mode == orchestrator.CoachModeOff {
		disabled[items[2]] = true
	}
	return &listOverlay{
		title: "Maestro Coach", items: items, disabled: disabled,
		activeValue: string(state.Mode),
		valueOf: func(item string) string {
			return values[item]
		},
	}
}

// overlayList extracts the listOverlay from any list-style overlay.
func overlayList(om overlayModel) (*listOverlay, bool) {
	switch o := om.(type) {
	case *listOverlay:
		return o, true
	case *engineOverlay:
		return o.listOverlay, true
	}
	return nil, false
}

// engineOverlay lists the engine choices for a role (§5.2).
type engineOverlay struct {
	*listOverlay
	choices []orchestrator.EngineChoice
}

func newEngineOverlay(orch *orchestrator.Orchestrator, role string) *engineOverlay {
	choices := orch.EngineChoices(role)
	items := make([]string, 0, len(choices))
	selected := 0
	defaults := orch.SettingsSnapshot().RoleDefaults[role]
	for _, c := range choices {
		items = append(items, c.Label())
	}
	for i, c := range choices {
		if c.Engine == defaults.Engine && (c.Engine != "legacy" || c.Agent == defaults.Agent) {
			selected = i
			break
		}
	}
	return &engineOverlay{
		listOverlay: &listOverlay{title: "Engine for " + role, items: items, selected: selected},
		choices:     choices,
	}
}

// selectedChoice returns the picked engine choice.
func (e *engineOverlay) selectedChoice() (orchestrator.EngineChoice, bool) {
	i := e.selected
	if i < 0 || i >= len(e.choices) {
		return orchestrator.EngineChoice{}, false
	}
	return e.choices[i], true
}
