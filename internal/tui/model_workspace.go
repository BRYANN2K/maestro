package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/settings"
)

type taskRoute struct {
	role, label, hint string
}

var taskRoutes = []taskRoute{
	{settings.RoleOrchestrator, "CHAT", "conversation + planning"},
	{settings.RoleDev, "BUILD", "implementation agent"},
	{settings.RoleReviewer, "REVIEW", "quality + security"},
	{settings.RoleDocs, "DOCS", "ADR + documentation"},
}

type routeModel struct {
	id        string // canonical routing value
	displayID string // terminal-safe projection
	name      string
	context   int
	reasoning bool
	efforts   []string
	priceIn   float64
	priceOut  float64
}

func (m routeModel) display() string {
	if m.displayID != "" {
		return m.displayID
	}
	return safeIDEPlainText(m.id)
}

type modelSource struct {
	id, label, kind, agent, status string
	ready, installed               bool
	models                         []routeModel
}

type workspaceHit struct {
	x, y, w, h int
	kind       string
	index      int
}

type taskModelOverlay struct {
	tasks               []taskRoute
	sources             []modelSource
	task, source, model int
	reasoning           int
	focus               int // 0 task, 1 source, 2 model, 3 reasoning
	query               string
	hits                []workspaceHit
	originX, originY    int
	compact             bool
}

func newTaskModelOverlay(orch *orchestrator.Orchestrator) *taskModelOverlay {
	o := &taskModelOverlay{tasks: append([]taskRoute(nil), taskRoutes...), focus: 2}
	for _, sub := range orch.SubscriptionList(context.Background()) {
		models := make([]routeModel, 0, len(sub.Models))
		for _, id := range sub.Models {
			name := id
			if id == "auto" {
				name = "Automatic · vendor default"
			}
			models = append(models, routeModel{
				id: id, displayID: safeIDEPlainText(id), name: safeIDEPlainText(name), reasoning: sub.Agent == "codex",
				efforts: orch.ReasoningEfforts("legacy", sub.Agent, id),
			})
		}
		o.sources = append(o.sources, modelSource{
			id: sub.ID, label: safeIDEPlainText(sub.Label), kind: "subscription", agent: sub.Agent,
			status: safeIDEPlainText(sub.Status), ready: sub.Authenticated, installed: sub.Installed, models: models,
		})
	}
	providers := map[string]orchestrator.ProviderInfo{}
	for _, p := range orch.ProviderList(context.Background()) {
		providers[p.Name] = p
	}
	byProvider := map[string][]routeModel{}
	for _, model := range orch.ModelList(context.Background()) {
		handle := qualifiedModelID(model.Provider, model.ID)
		byProvider[model.Provider] = append(byProvider[model.Provider], routeModel{
			id: handle, displayID: safeIDEPlainText(handle), name: safeIDEPlainText(model.Name),
			context: model.Context, reasoning: model.Reasoning,
			efforts: orch.ReasoningEfforts("native", "", handle),
			priceIn: model.PriceIn, priceOut: model.PriceOut,
		})
	}
	var ids []string
	for id := range byProvider {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		pi, iok := providers[ids[i]]
		pj, jok := providers[ids[j]]
		iReady := iok && (!pi.RequiresKey || pi.KeySet)
		jReady := jok && (!pj.RequiresKey || pj.KeySet)
		if iReady != jReady {
			return iReady
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		info, ok := providers[id]
		if !ok {
			info, _ = orch.ProviderInfo(context.Background(), id)
		}
		status := "API key required"
		ready := !info.RequiresKey || info.KeySet
		if !info.RequiresKey {
			status = "local"
		} else if info.KeySet {
			status = "connected"
		}
		o.sources = append(o.sources, modelSource{
			id: id, label: safeIDEPlainText(id), kind: "native", status: safeIDEPlainText(status),
			ready: ready, installed: true, models: byProvider[id],
		})
	}
	o.selectCurrentRoute(orch)
	return o
}

func (o *taskModelOverlay) selectCurrentRoute(orch *orchestrator.Orchestrator) {
	if len(o.sources) == 0 {
		return
	}
	rd := orch.TaskRoute(o.tasks[o.task].role)
	for i, source := range o.sources {
		match := rd.Engine == "legacy" && source.kind == "subscription" && source.agent == rd.Agent
		if rd.Engine != "legacy" && source.kind == "native" && strings.HasPrefix(rd.Model, source.id+"/") {
			match = true
		}
		if !match {
			continue
		}
		o.source = i
		for j, model := range source.models {
			if model.id == rd.Model || (rd.Model == "" && model.id == "auto") {
				o.model = j
				break
			}
		}
		o.selectReasoning(rd.ReasoningEffort)
		return
	}
	for i, source := range o.sources {
		if source.ready {
			o.source = i
			o.selectReasoning(rd.ReasoningEffort)
			return
		}
	}
}

func (o *taskModelOverlay) View(styles Styles, width int) string {
	return o.viewSized(styles, width, 28)
}

func (o *taskModelOverlay) viewSized(styles Styles, width, height int) string {
	if width < 72 || height < 22 {
		return o.viewCompact(styles, width, height)
	}
	o.compact = false
	width = max(width, 72)
	height = max(height, 22)
	o.hits = nil
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	muted := styles.Hint
	selected := styles.SidebarActive
	plain := styles.SidebarItem
	focusNames := [...]string{"TASK", "PROVIDER", "MODEL", "REASONING"}
	focusName := focusNames[clamp(o.focus, 0, len(focusNames)-1)]

	var head strings.Builder
	head.WriteString(accent.Render("MODELS / TASK ROUTING"))
	head.WriteString(muted.Render("   Models · provider / model · one policy per workflow"))
	head.WriteString("\n")
	taskW := max((width-4)/len(o.tasks), 14)
	for i, task := range o.tasks {
		var label string
		if i == o.task {
			marker := "●"
			if o.focus == 0 {
				marker = ">"
			}
			label = selected.Render(marker + " " + task.label)
		} else {
			label = plain.Render("○ " + task.label)
		}
		head.WriteString(lipgloss.NewStyle().Width(taskW).Render(label))
		o.hits = append(o.hits, workspaceHit{x: 2 + i*taskW, y: 2, w: taskW, h: 1, kind: "task", index: i})
	}
	head.WriteString("\n" + muted.Render(o.tasks[o.task].hint))

	leftW := max(width*30/100, 23)
	rightW := max(width-leftW-5, 38)
	rows := max(height-11, 8)
	var left, right strings.Builder
	left.WriteString(accent.Render("PROVIDERS & PLANS") + "\n")
	right.WriteString(accent.Render("MODEL CATALOG") + "  " + muted.Render("filter: "+o.query) + "\n")

	sourceTop := centeredWindow(o.source, len(o.sources), rows)
	for row := 0; row < rows; row++ {
		i := sourceTop + row
		if i >= len(o.sources) {
			left.WriteString("\n")
			continue
		}
		s := o.sources[i]
		icon := "○"
		if s.ready {
			icon = "●"
		} else if !s.installed {
			icon = "×"
		}
		marker := " "
		if i == o.source {
			marker = "•"
			if o.focus == 1 {
				marker = ">"
			}
		}
		line := fmt.Sprintf("%s%s %-17s", marker, icon, truncateRunes(s.label, 17))
		style := plain
		if i == o.source {
			style = selected
		}
		left.WriteString(style.Width(leftW-2).Render(line) + "\n")
		o.hits = append(o.hits, workspaceHit{x: 2, y: 6 + row, w: leftW, h: 1, kind: "source", index: i})
	}

	models := o.filteredModels()
	modelTop := centeredWindow(o.model, len(models), rows)
	for row := 0; row < rows; row++ {
		i := modelTop + row
		if i >= len(models) {
			right.WriteString("\n")
			continue
		}
		model := models[i]
		meta := ""
		if model.context > 0 {
			meta += fmt.Sprintf(" %dk", model.context/1000)
		}
		if model.reasoning {
			meta += " reasoning"
		}
		marker := " "
		if i == o.model {
			marker = "•"
			if o.focus == 2 {
				marker = ">"
			}
		}
		line := fmt.Sprintf("%s%-29s%s", marker, truncateRunes(model.display(), 29), meta)
		style := plain
		if i == o.model {
			style = selected
		}
		right.WriteString(style.Width(rightW-2).Render(line) + "\n")
		o.hits = append(o.hits, workspaceHit{x: leftW + 4, y: 6 + row, w: rightW, h: 1, kind: "model", index: i})
	}

	columns := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(left.String()),
		lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render("│ "),
		lipgloss.NewStyle().Width(rightW).Render(right.String()),
	)
	source := o.currentSource()
	status := "No provider selected"
	if source != nil {
		status = source.status + " · " + source.label
	}
	efforts := o.currentReasoningEfforts()
	reasoningLine := accent.Render("REASONING") + "  "
	for i, effort := range efforts {
		displayEffort := safeIDEPlainText(effort)
		label := " " + displayEffort + " "
		if i == o.reasoning {
			marker := "•"
			if o.focus == 3 {
				marker = ">"
			}
			label = marker + displayEffort
		}
		x := 2 + lipgloss.Width(reasoningLine)
		reasoningLine += selected.Render(label) + "  "
		o.hits = append(o.hits, workspaceHit{x: x, y: height - 3, w: lipgloss.Width(label) + 2, h: 1, kind: "reasoning", index: i})
	}
	footer := muted.Render("FOCUS: " + focusName + " · tab focus · arrows navigate · enter apply · esc close")
	content := head.String() + "\n\n" + columns + "\n" + accent.Render("ROUTE STATUS") + "  " + status + "\n" + reasoningLine + "\n" + footer
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.T.Color(TokenIron)).Padding(0, 1).Width(width).Height(height).Render(content)
}

// viewCompact keeps model routing operable in small terminals. The full
// workspace needs two columns; below that threshold it becomes a focused
// single-column inspector instead of letting the provider/model column and
// the escape hint disappear beyond the viewport.
func (o *taskModelOverlay) viewCompact(styles Styles, width, height int) string {
	width = max(width, 1)
	height = max(height, 1)
	o.hits = nil
	o.compact = true
	// Rounded border plus one-cell horizontal padding consumes four cells.
	innerW := max(width-4, 1)
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	muted := styles.Hint
	active := styles.SidebarActive
	focusNames := [...]string{"TASK", "PROVIDER", "MODEL", "REASONING"}
	focus := clamp(o.focus, 0, len(focusNames)-1)
	marker := func(column int) string {
		if focus == column {
			return ">"
		}
		return " "
	}

	task := "route"
	if o.task >= 0 && o.task < len(o.tasks) {
		task = o.tasks[o.task].label
	}
	sourceLabel, sourceStatus := "no provider", ""
	if source := o.currentSource(); source != nil {
		sourceLabel, sourceStatus = source.label, source.status
	}
	modelLabel := "no model"
	models := o.filteredModels()
	if len(models) > 0 {
		o.model = clamp(o.model, 0, len(models)-1)
		modelLabel = models[o.model].display()
	}
	reasoningLabel := safeIDEPlainText(o.currentReasoningEfforts()[clamp(o.reasoning, 0, len(o.currentReasoningEfforts())-1)])

	lines := []string{accent.Render("MODELS · FOCUS: " + focusNames[focus])}
	values := [...]string{task, sourceLabel, modelLabel, reasoningLabel}
	labels := [...]string{"task", "provider", "model", "reasoning"}
	if height <= 7 {
		prefix := marker(focus) + " " + labels[focus] + "  "
		lines = append(lines, active.Render(prefix)+truncateRunes(values[focus], max(innerW-lipgloss.Width(prefix), 1)))
		indices := [...]int{o.task, o.source, o.model, o.reasoning}
		o.hits = append(o.hits, workspaceHit{x: 2, y: 2, w: innerW, h: 1, kind: labels[focus], index: indices[focus]})
	} else {
		lines = append(lines,
			active.Render(marker(0)+" task      ")+truncateRunes(task, max(innerW-12, 1)),
			active.Render(marker(1)+" provider  ")+truncateRunes(sourceLabel, max(innerW-12, 1)),
			active.Render(marker(2)+" model     ")+truncateRunes(modelLabel, max(innerW-12, 1)),
			active.Render(marker(3)+" reasoning ")+truncateRunes(reasoningLabel, max(innerW-12, 1)),
		)
		o.hits = append(o.hits,
			workspaceHit{x: 2, y: 2, w: innerW, h: 1, kind: "task", index: o.task},
			workspaceHit{x: 2, y: 3, w: innerW, h: 1, kind: "source", index: o.source},
			workspaceHit{x: 2, y: 4, w: innerW, h: 1, kind: "model", index: o.model},
			workspaceHit{x: 2, y: 5, w: innerW, h: 1, kind: "reasoning", index: o.reasoning},
		)
	}
	if sourceStatus != "" && height >= 8 {
		lines = append(lines, muted.Render(truncateRunes(sourceStatus, innerW)))
	}
	if o.query != "" && height >= 9 {
		lines = append(lines, muted.Render("filter  "+truncateRunes(o.query, max(innerW-8, 1))))
	}
	lines = append(lines,
		muted.Render("esc close · enter apply"),
		muted.Render("tab focus · arrows navigate"),
	)
	content := clampANSIHeight(clampANSIWidth(strings.Join(lines, "\n"), innerW), max(height-2, 1))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.T.Color(TokenIron)).
		Padding(0, 1).
		Width(width).MaxWidth(width).
		Height(height).MaxHeight(height).
		Render(content)
}

func centeredWindow(selected, total, rows int) int {
	if total <= rows {
		return 0
	}
	top := selected - rows/2
	return clamp(top, 0, total-rows)
}

func (o *taskModelOverlay) currentSource() *modelSource {
	if o.source < 0 || o.source >= len(o.sources) {
		return nil
	}
	return &o.sources[o.source]
}

func (o *taskModelOverlay) filteredModels() []routeModel {
	source := o.currentSource()
	if source == nil {
		return nil
	}
	if o.query == "" {
		return source.models
	}
	q := strings.ToLower(o.query)
	var out []routeModel
	for _, model := range source.models {
		if strings.Contains(strings.ToLower(model.display()+" "+model.name), q) {
			out = append(out, model)
		}
	}
	return out
}

func (o *taskModelOverlay) currentReasoningEfforts() []string {
	models := o.filteredModels()
	if len(models) == 0 {
		return []string{"auto"}
	}
	model := models[clamp(o.model, 0, len(models)-1)]
	if len(model.efforts) == 0 {
		return []string{"auto"}
	}
	return model.efforts
}

func (o *taskModelOverlay) selectReasoning(effort string) {
	if effort == "" {
		effort = "auto"
	}
	o.reasoning = 0
	for i, candidate := range o.currentReasoningEfforts() {
		if candidate == effort {
			o.reasoning = i
			return
		}
	}
}

func (o *taskModelOverlay) setTask(orch *orchestrator.Orchestrator, index int) {
	o.task = clamp(index, 0, len(o.tasks)-1)
	o.model, o.reasoning, o.query = 0, 0, ""
	o.selectCurrentRoute(orch)
}

func (o *taskModelOverlay) setSource(index int) {
	if len(o.sources) == 0 {
		return
	}
	o.source = clamp(index, 0, len(o.sources)-1)
	o.model, o.reasoning, o.query = 0, 0, ""
}

func (o *taskModelOverlay) apply(m *Model) tea.Cmd {
	source := o.currentSource()
	models := o.filteredModels()
	if source == nil || len(models) == 0 {
		return nil
	}
	o.model = clamp(o.model, 0, len(models)-1)
	model := models[o.model]
	efforts := o.currentReasoningEfforts()
	o.reasoning = clamp(o.reasoning, 0, len(efforts)-1)
	effort := efforts[o.reasoning]
	role := o.tasks[o.task].role
	if source.kind == "subscription" {
		if !source.installed || !source.ready {
			m.overlay = overlayProviders
			m.overlayM = newProvidersOverlay(m.orch, source.id)
			m.status.pushToast("info", "connect "+source.label+" first", 3*time.Second)
			return nil
		}
		if err := m.orch.SetTaskModelWithReasoning(m.ctx(), role, "legacy", source.agent, model.id, effort); err != nil {
			m.status.pushToast("error", safeIDEPlainText(err.Error()), 4*time.Second)
			return nil
		}
	} else {
		if !source.ready {
			m.overlay = overlayAuth
			m.overlayM = newTaskAuthOverlay(source.id, model.id, role)
			return nil
		}
		if err := m.orch.SetTaskModelWithReasoning(m.ctx(), role, "native", "", model.id, effort); err != nil {
			m.status.pushToast("error", safeIDEPlainText(err.Error()), 4*time.Second)
			return nil
		}
	}
	m.status.pushToast("success", o.tasks[o.task].label+" → "+model.display()+" · "+safeIDEPlainText(effort), 3*time.Second)
	return nil
}

func (o *taskModelOverlay) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		m.overlay = overlayNone
	case tea.KeyTab:
		o.focus = (o.focus + 1) % 4
	case tea.KeyShiftTab:
		o.focus = (o.focus + 3) % 4
	case tea.KeyLeft:
		if o.focus == 0 {
			o.setTask(m.orch, o.task-1)
		} else {
			o.focus = max(o.focus-1, 0)
		}
	case tea.KeyRight:
		if o.focus == 0 {
			o.setTask(m.orch, o.task+1)
		} else {
			o.focus = min(o.focus+1, 3)
		}
	case tea.KeyUp:
		if o.focus == 0 {
			o.setTask(m.orch, o.task-1)
		} else if o.focus == 1 {
			o.setSource(o.source - 1)
		} else if o.focus == 2 {
			o.model = max(o.model-1, 0)
			o.reasoning = 0
		} else {
			o.reasoning = max(o.reasoning-1, 0)
		}
	case tea.KeyDown:
		if o.focus == 0 {
			o.setTask(m.orch, o.task+1)
		} else if o.focus == 1 {
			o.setSource(o.source + 1)
		} else if o.focus == 2 {
			o.model = min(o.model+1, max(len(o.filteredModels())-1, 0))
			o.reasoning = 0
		} else {
			o.reasoning = min(o.reasoning+1, len(o.currentReasoningEfforts())-1)
		}
	case tea.KeyBackspace:
		if o.query != "" {
			r := []rune(o.query)
			o.query = string(r[:len(r)-1])
			o.model = 0
		}
	case tea.KeyEnter:
		if o.focus < 3 {
			o.focus++
		} else {
			return o.apply(m)
		}
	case tea.KeyCtrlP:
		m.overlay = overlayProviders
		m.overlayM = newProvidersOverlay(m.orch, "")
	case tea.KeyCtrlR:
		return func() tea.Msg { return modelsRefreshedMsg{err: m.orch.RefreshModels(context.Background())} }
	case tea.KeyRunes:
		o.query += sanitizeSingleLineInput(string(msg.Runes))
		o.model, o.reasoning, o.focus = 0, 0, 2
	}
	return nil
}

func (o *taskModelOverlay) mouse(m *Model, msg tea.MouseMsg) tea.Cmd {
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		x := msg.X - o.originX
		if o.focus == 3 {
			o.reasoning = clamp(o.reasoning+delta, 0, len(o.currentReasoningEfforts())-1)
		} else if o.compact && o.focus == 0 {
			o.setTask(m.orch, o.task+delta)
		} else if (o.compact && o.focus == 1) || (!o.compact && x < 35) {
			o.setSource(o.source + delta)
			o.focus = 1
		} else {
			o.model = clamp(o.model+delta, 0, max(len(o.filteredModels())-1, 0))
			o.focus = 2
		}
		return nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil
	}
	x, y := msg.X-o.originX, msg.Y-o.originY
	for _, hit := range o.hits {
		if x < hit.x || x >= hit.x+hit.w || y < hit.y || y >= hit.y+hit.h {
			continue
		}
		switch hit.kind {
		case "task":
			o.setTask(m.orch, hit.index)
			o.focus = 0
		case "source":
			o.setSource(hit.index)
			o.focus = 1
		case "model":
			if o.focus == 2 && o.model == hit.index {
				return o.apply(m)
			}
			o.model, o.focus = hit.index, 2
			o.reasoning = 0
		case "reasoning":
			if o.focus == 3 && o.reasoning == hit.index {
				return o.apply(m)
			}
			o.reasoning, o.focus = hit.index, 3
		}
		break
	}
	return nil
}
