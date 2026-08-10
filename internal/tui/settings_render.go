package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type settingsDetail struct {
	title   string
	status  string
	body    string
	meta    []string
	preview string
	action  string
}

func (s *settingsOverlay) viewSized(styles Styles, width, height int) string {
	width = max(width, 4)
	height = max(height, 3)
	s.hits = nil
	s.clampSelection()
	switch {
	case width < 72 || height < 18:
		return s.viewCompact(styles, width, height)
	case width < 118 || height < 24:
		return s.viewSplit(styles, width, height)
	default:
		return s.viewWide(styles, width, height)
	}
}

func (s *settingsOverlay) viewWide(styles Styles, width, height int) string {
	innerW, innerH := max(width-2, 1), max(height-2, 1)
	headerH, footerH := 2, 1
	columnsH := max(innerH-headerH-footerH, 1)
	navW := clamp(innerW*18/100, 18, 24)
	listW := clamp(innerW*34/100, 30, 48)
	detailW := max(innerW-navW-listW-2, 18)
	if navW+listW+detailW+2 > innerW {
		listW = max(innerW-navW-detailW-2, 18)
	}

	header := s.renderHeader(styles, innerW, false)
	columnsY := 1 + headerH
	nav := s.renderSections(styles, navW, columnsH, 1, columnsY)
	list := s.renderItems(styles, listW, columnsH, 1+navW+1, columnsY, true)
	detail := s.renderDetail(styles, detailW, columnsH, 1+navW+1+listW+1, columnsY)
	divider := lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render("│")
	columns := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(navW).Height(columnsH).Render(nav),
		divider,
		lipgloss.NewStyle().Width(listW).Height(columnsH).Render(list),
		divider,
		lipgloss.NewStyle().Width(detailW).Height(columnsH).Render(detail),
	)
	footer := s.renderFooter(styles, innerW)
	content := header + "\n\n" + columns + "\n" + footer
	return settingsFrame(styles, width, height, content)
}

func (s *settingsOverlay) viewSplit(styles Styles, width, height int) string {
	innerW, innerH := max(width-2, 1), max(height-2, 1)
	headerH, footerH := 2, 1
	columnsH := max(innerH-headerH-footerH, 1)
	navW := clamp(innerW*25/100, 18, 22)
	contentW := max(innerW-navW-1, 20)

	header := s.renderHeader(styles, innerW, false)
	columnsY := 1 + headerH
	nav := s.renderSections(styles, navW, columnsH, 1, columnsY)
	body := s.renderSplitContent(styles, contentW, columnsH, 1+navW+1, columnsY)
	divider := lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render("│")
	columns := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(navW).Height(columnsH).Render(nav),
		divider,
		lipgloss.NewStyle().Width(contentW).Height(columnsH).Render(body),
	)
	content := header + "\n\n" + columns + "\n" + s.renderFooter(styles, innerW)
	return settingsFrame(styles, width, height, content)
}

func (s *settingsOverlay) viewCompact(styles Styles, width, height int) string {
	innerW, innerH := max(width-2, 1), max(height-2, 1)
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	muted := styles.Hint
	section := settingsSections[s.sectionIndex()]
	sectionLine := fmt.Sprintf("SETTINGS  %d/%d  %s", s.sectionIndex()+1, len(settingsSections), section.Label)
	sectionLine = truncateRunes(sectionLine, innerW)

	label, value, status := s.selectedSummary()
	itemLine := "> " + label
	if value != "" {
		itemLine += "  " + value
	}
	if status != "" {
		itemLine += "  " + status
	}
	detail := s.detail()
	detailLine := detail.body
	if detail.preview != "" {
		detailLine = "Preview · " + firstSettingsPreviewLine(detail.preview)
	}
	detailLine = safeSettingsTruncate(detailLine, innerW)
	action := detail.action
	if action == "" {
		if s.itemCount() == 0 {
			action = "No action · esc close"
		} else {
			action = "Enter change"
		}
	}

	lines := []string{accent.Render(sectionLine), accent.Render(safeSettingsTruncate(itemLine, innerW))}
	if innerH >= 4 {
		lines = append(lines, muted.Render(detailLine))
	}
	if innerH >= 5 {
		lines = append(lines, muted.Render(safeSettingsTruncate(action, innerW)))
	}
	for len(lines) < innerH-1 {
		lines = append(lines, "")
	}
	lines = append(lines, muted.Render(truncateRunes("[/] section · ↑/↓ item · esc", innerW)))
	content := strings.Join(lines, "\n")

	// At 40×10 the section header doubles as a large previous/next target,
	// while the selected value and action retain dedicated click rows.
	s.hits = append(s.hits,
		settingsHit{x: 1, y: 1, w: max(innerW/2, 1), h: 1, kind: "previous-section"},
		settingsHit{x: 1 + innerW/2, y: 1, w: max(innerW-innerW/2, 1), h: 1, kind: "next-section"},
		settingsHit{x: 1, y: 2, w: innerW, h: 1, kind: "item", index: s.selected},
	)
	if innerH >= 5 {
		s.hits = append(s.hits, settingsHit{x: 1, y: 4, w: innerW, h: 1, kind: "action", index: s.selected})
	}
	return settingsFrame(styles, width, height, content)
}

func settingsFrame(styles Styles, width, height int, content string) string {
	innerW, innerH := max(width-2, 1), max(height-2, 1)
	content = clampANSIWidth(content, innerW)
	content = clampANSIHeight(content, innerH)
	lines := strings.Split(content, "\n")
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	for i := range lines {
		if gap := innerW - lipgloss.Width(lines[i]); gap > 0 {
			lines[i] += strings.Repeat(" ", gap)
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.T.Color(TokenIron)).
		Background(styles.T.Color(TokenPanel)).
		Render(strings.Join(lines, "\n"))
}

func (s *settingsOverlay) renderHeader(styles Styles, width int, compact bool) string {
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	muted := styles.Hint
	title := "MAESTRO / SETTINGS"
	if compact {
		title = "SETTINGS"
	}
	status := s.globalStatus()
	gap := max(width-lipgloss.Width(title)-lipgloss.Width(status), 1)
	return clampANSIWidth(accent.Render(title)+strings.Repeat(" ", gap)+muted.Render(status), width)
}

func (s *settingsOverlay) globalStatus() string {
	if s.state.Theme != s.committedTheme {
		return "[preview] " + s.state.Theme
	}
	return "provider · native + subscription"
}

func (s *settingsOverlay) renderSections(styles Styles, width, height, x, y int) string {
	muted := styles.Hint
	active := styles.SidebarActive
	plain := styles.SidebarItem
	focus := ""
	if s.focus == settingsFocusSections {
		focus = " [focus]"
	}
	lines := []string{muted.Render(truncateRunes("SECTIONS"+focus, width)), ""}
	for i, section := range settingsSections {
		marker := "  "
		style := plain
		if section.ID == s.section {
			marker = "> "
			style = active
		}
		line := marker + fmt.Sprintf("%d  %s", i+1, section.Label)
		lines = append(lines, style.Width(width).Render(truncateRunes(line, width)))
		s.hits = append(s.hits, settingsHit{x: x, y: y + 2 + i, w: width, h: 1, kind: "section", index: i})
	}
	if height >= len(lines)+2 {
		lines = append(lines, "", muted.Render(truncateRunes(settingsSections[s.sectionIndex()].Subtitle, width)))
	}
	return clampANSIHeight(strings.Join(lines, "\n"), height)
}

func (s *settingsOverlay) renderItems(styles Styles, width, height, x, y int, withSummary bool) string {
	muted := styles.Hint
	section := settingsSections[s.sectionIndex()]
	focus := ""
	if s.focus == settingsFocusContent {
		focus = " [focus]"
	}
	lines := []string{muted.Render(truncateRunes(strings.ToUpper(section.Label)+focus, width))}
	itemStart := 1
	if withSummary {
		lines = append(lines, muted.Render(truncateRunes(s.sectionSummary(), width)), "")
		itemStart = 3
	}
	rows := max(height-itemStart, 1)
	count := s.itemCount()
	if count == 0 {
		lines = append(lines, s.emptyState(styles, width)...)
		return clampANSIHeight(strings.Join(lines, "\n"), height)
	}
	top := centeredWindow(s.selected, count, rows)
	for displayRow := 0; displayRow < rows; displayRow++ {
		index := top + displayRow
		if index >= count {
			break
		}
		line := s.renderItemLine(styles, width, index)
		lines = append(lines, line)
		s.hits = append(s.hits, settingsHit{x: x, y: y + itemStart + displayRow, w: width, h: 1, kind: "item", index: index})
	}
	return clampANSIHeight(strings.Join(lines, "\n"), height)
}

func (s *settingsOverlay) renderSplitContent(styles Styles, width, height, x, y int) string {
	muted := styles.Hint
	section := settingsSections[s.sectionIndex()]
	focus := ""
	if s.focus == settingsFocusContent {
		focus = " [focus]"
	}
	lines := []string{
		muted.Render(truncateRunes(strings.ToUpper(section.Label)+focus, width)),
		muted.Render(truncateRunes(s.sectionSummary(), width)),
		"",
	}
	detail := s.detail()
	count := s.itemCount()
	detailReserve := 4
	if detail.preview != "" {
		detailReserve = 6
	}
	itemRows := max(height-len(lines)-detailReserve, 1)
	if count == 0 {
		lines = append(lines, s.emptyState(styles, width)...)
	} else {
		top := centeredWindow(s.selected, count, itemRows)
		for row := 0; row < itemRows; row++ {
			index := top + row
			if index >= count {
				break
			}
			lines = append(lines, s.renderItemLine(styles, width, index))
			s.hits = append(s.hits, settingsHit{x: x, y: y + 3 + row, w: width, h: 1, kind: "item", index: index})
		}
	}
	for len(lines) < max(height-detailReserve, 0) {
		lines = append(lines, "")
	}
	lines = append(lines,
		muted.Render(truncateRunes("DETAIL · "+detail.title, width)),
		muted.Render(truncateRunes(detail.body, width)),
		muted.Render(truncateRunes(detail.status, width)),
	)
	if detail.preview != "" {
		lines = append(lines,
			muted.Render("PREVIEW"),
			muted.Render(safeSettingsTruncate(firstSettingsPreviewLine(detail.preview), width)),
		)
	}
	if detail.action != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render(truncateRunes(detail.action, width)))
		s.hits = append(s.hits, settingsHit{x: x, y: y + height - 1, w: width, h: 1, kind: "action", index: s.selected})
	}
	return clampANSIHeight(strings.Join(lines, "\n"), height)
}

func (s *settingsOverlay) renderDetail(styles Styles, width, height, x, y int) string {
	muted := styles.Hint
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	detail := s.detail()
	lines := []string{
		muted.Render("DETAIL"),
		"",
		accent.Render(truncateRunes(detail.title, width)),
	}
	if detail.status != "" {
		lines = append(lines, muted.Render(truncateRunes(detail.status, width)))
	}
	lines = append(lines, "")
	lines = appendSettingsWrapped(lines, detail.body, width, nil)
	for _, meta := range detail.meta {
		lines = append(lines, "")
		lines = appendSettingsWrapped(lines, meta, width, func(line string) string { return muted.Render(line) })
	}
	if detail.preview != "" {
		lines = append(lines, "", muted.Render("PREVIEW · inspected on demand"))
		for _, line := range strings.Split(detail.preview, "\n") {
			lines = append(lines, muted.Render(safeSettingsTruncate(line, width)))
		}
	}
	if detail.action != "" {
		// Keep the action on the final physical row. Wrapped body/meta strings
		// are expanded above, so the registered hitbox and the visible action
		// always describe the same terminal cell.
		actionY := max(height-1, 0)
		if len(lines) > actionY {
			lines = lines[:actionY]
		}
		for len(lines) < actionY {
			lines = append(lines, "")
		}
		lines = append(lines, accent.Render(truncateRunes(detail.action, width)))
		s.hits = append(s.hits, settingsHit{x: x, y: y + actionY, w: width, h: 1, kind: "action", index: s.selected})
	}
	return clampANSIHeight(strings.Join(lines, "\n"), height)
}

func (s *settingsOverlay) renderItemLine(styles Styles, width, index int) string {
	label, value, status := s.itemSummary(index)
	marker := "  "
	style := styles.SidebarItem
	if index == s.selected {
		marker = "> "
		style = styles.SidebarActive
	}
	available := max(width-lipgloss.Width(marker)-lipgloss.Width(status)-1, 4)
	if value != "" {
		labelWidth := max(available*48/100, 4)
		valueWidth := max(available-labelWidth-1, 1)
		label = safeSettingsTruncate(label, labelWidth)
		value = safeSettingsTruncate(value, valueWidth)
		line := marker + padSettingsRight(label, labelWidth) + " " + value
		if status != "" {
			line += " " + status
		}
		return style.Width(width).Render(safeSettingsTruncate(line, width))
	}
	line := marker + label
	if status != "" {
		gap := max(width-lipgloss.Width(line)-lipgloss.Width(status), 1)
		line += strings.Repeat(" ", gap) + status
	}
	return style.Width(width).Render(safeSettingsTruncate(line, width))
}

func safeSettingsTruncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return stripBrokenANSI(ansi.Truncate(value, width, "…"))
}

func padSettingsRight(value string, width int) string {
	value = safeSettingsTruncate(value, width)
	if gap := width - lipgloss.Width(value); gap > 0 {
		value += strings.Repeat(" ", gap)
	}
	return value
}

func appendSettingsWrapped(lines []string, value string, width int, render func(string) string) []string {
	wrapped := strings.Split(wrapPlain(value, width), "\n")
	for _, line := range wrapped {
		line = safeSettingsTruncate(line, width)
		if render != nil {
			line = render(line)
		}
		lines = append(lines, line)
	}
	return lines
}

func firstSettingsPreviewLine(preview string) string {
	for _, line := range strings.Split(preview, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return "(empty file)"
}

func (s *settingsOverlay) selectedSummary() (label, value, status string) {
	if s.itemCount() == 0 {
		return "Nothing configured", "", "[empty]"
	}
	return s.itemSummary(s.selected)
}

func (s *settingsOverlay) itemSummary(index int) (label, value, status string) {
	switch s.section {
	case settingsGeneral, settingsAppearance, settingsAgents:
		rows := s.rows()
		if index < 0 || index >= len(rows) {
			return "", "", ""
		}
		row := rows[index]
		value = s.value(row)
		status = "[saved]"
		if row.Kind == settingTheme {
			value = themeSwatch(value) + " " + value
			if s.state.Theme != s.committedTheme {
				status = "[preview]"
			}
		} else {
			// Settings keeps canonical model/agent values for persistence and
			// projects them only at the terminal boundary.
			value = safeIDEPlainText(value)
		}
		return row.Label, value, status
	case settingsProviders:
		if index < 0 || index >= len(s.providers) {
			return "", "", ""
		}
		provider := s.providers[index]
		status = "[attention]"
		if provider.Connected {
			status = "[connected]"
		}
		return provider.Label, provider.Kind, status
	case settingsIntegrations:
		if index < 0 || index >= len(s.integrations) {
			return "", "", ""
		}
		integration := s.integrations[index]
		status = "[" + defaultString(integration.Status, "disconnected") + "]"
		if s.mcpLoading && index == s.selected {
			status = "[loading]"
		}
		return integration.Name, fmt.Sprintf("%d tools", integration.Tools), status
	case settingsSkills:
		if index < 0 || index >= len(s.skills) {
			return "", "", ""
		}
		skill := s.skills[index]
		status = settingsSkillStatus(skill, false)
		if s.skillLoading && index == s.selected {
			status = "[loading]"
		}
		return skill.Name, skill.Source, status
	}
	return "", "", ""
}

func (s *settingsOverlay) sectionSummary() string {
	switch s.section {
	case settingsGeneral:
		return "2 saved preferences"
	case settingsAppearance:
		if s.state.Theme != s.committedTheme {
			return "previewing " + s.state.Theme + " · saved " + s.committedTheme
		}
		return "saved theme · " + s.committedTheme
	case settingsAgents:
		return "4 task routes · 2 model slots"
	case settingsProviders:
		connected := 0
		for _, provider := range s.providers {
			if provider.Connected {
				connected++
			}
		}
		summary := fmt.Sprintf("%d connected · %d known", connected, len(s.providers))
		if s.providerLoading {
			summary += " · checking plans"
		}
		return summary
	case settingsIntegrations:
		connected := 0
		for _, integration := range s.integrations {
			if integration.Status == "connected" {
				connected++
			}
		}
		loading := ""
		if s.mcpLoading {
			loading = " · reconnecting"
		}
		return fmt.Sprintf("MCP · native only · %d/%d connected%s", connected, len(s.integrations), loading)
	case settingsSkills:
		enabled := 0
		for _, skill := range s.skills {
			if skill.Enabled {
				enabled++
			}
		}
		loading := ""
		if s.skillLoading {
			loading = " · loading"
		}
		return fmt.Sprintf("Skills · native + subscription by route · %d/%d enabled%s", enabled, len(s.skills), loading)
	}
	return ""
}

func (s *settingsOverlay) emptyState(styles Styles, width int) []string {
	muted := styles.Hint
	switch s.section {
	case settingsProviders:
		if s.providerLoading {
			return []string{muted.Render(truncateRunes("Checking subscription CLIs…", width))}
		}
		return []string{muted.Render(truncateRunes("No model provider is configured.", width)), muted.Render(truncateRunes("Use /providers to connect one.", width))}
	case settingsIntegrations:
		return []string{muted.Render(truncateRunes("No MCP server configured.", width)), muted.Render(truncateRunes("Add one to maestrorc, then reopen.", width))}
	case settingsSkills:
		return []string{muted.Render(truncateRunes("No skill discovered.", width)), muted.Render(truncateRunes("Add SKILL.md under .agents/skills.", width))}
	default:
		return []string{muted.Render("No settings in this section.")}
	}
}

func (s *settingsOverlay) detail() settingsDetail {
	switch s.section {
	case settingsGeneral, settingsAppearance, settingsAgents:
		rows := s.rows()
		if len(rows) == 0 {
			return settingsDetail{title: "No setting", body: "This section has no configurable value."}
		}
		return s.settingDetail(rows[s.selected])
	case settingsProviders:
		if len(s.providers) == 0 {
			return settingsDetail{title: "Provider connections", status: "[not configured]", body: "Native providers run Maestro's model loop. Subscription routes reuse an official vendor CLI session.", action: "r  Check connections"}
		}
		provider := s.providers[s.selected]
		status := "[attention] " + provider.Status
		if provider.Connected {
			status = "[connected] " + provider.Status
		}
		body := "Maestro uses this configured provider in its native engine."
		if provider.Kind == "subscription" {
			body = "This route reuses the official vendor CLI. Credentials stay in the vendor keychain."
		}
		return settingsDetail{
			title: provider.Label, status: status, body: body,
			meta:   []string{fmt.Sprintf("%d models · %s", provider.Models, provider.Source)},
			action: "Enter  Open provider workspace",
		}
	case settingsIntegrations:
		boundary := "MCP tools run only in Maestro's native engine. Subscription routes reuse the vendor CLI and do not automatically inherit these tools."
		if len(s.integrations) == 0 {
			return settingsDetail{title: "MCP boundary", status: "[not configured]", body: boundary}
		}
		integration := s.integrations[s.selected]
		meta := []string{fmt.Sprintf("%s · %d tools", defaultString(integration.Transport, "MCP"), integration.Tools), boundary}
		if integration.Error != "" {
			meta = append([]string{"Error: " + integration.Error}, meta...)
		}
		status := "[" + integration.Status + "]"
		action := "Enter  Reconnect · i  Inspect"
		if s.mcpLoading {
			status, action = "[loading] reconnecting", "Reconnect in progress"
		}
		return settingsDetail{title: integration.Name, status: status, body: "External tools exposed through the native Maestro runtime.", meta: meta, action: action}
	case settingsSkills:
		if len(s.skills) == 0 {
			return settingsDetail{title: "Agent Skills", status: "[none discovered]", body: "Skills add focused instructions from project, user, or configured sources.", action: "r  Refresh discovery"}
		}
		skill := s.skills[s.selected]
		status := settingsSkillStatus(skill, true)
		meta := []string{skill.Scope + " · " + skill.Source}
		if skill.Warning != "" {
			meta = append(meta, "Warning: "+skill.Warning)
		}
		if skill.Valid && !skill.Invokable {
			meta = append(meta, "Not user-invokable. This preference does not make the skill runnable from the palette or /skills run.")
		}
		if s.inspectedSkill == skill.ID && s.inspectionPath != "" {
			meta = append(meta, "Source: "+s.inspectionPath)
		}
		action := "Enter  Toggle · i  Inspect · r  Refresh"
		if !skill.Valid {
			action = "Invalid metadata · fix SKILL.md · r Refresh"
		}
		if s.skillLoading {
			status, action = "[loading]", "Skill operation in progress"
		}
		preview := ""
		if s.inspectedSkill == skill.ID {
			preview = s.inspectionBody
		}
		return settingsDetail{title: skill.Name, status: status, body: defaultString(skill.Description, "No description provided."), meta: meta, preview: preview, action: action}
	}
	return settingsDetail{}
}

func settingsSkillStatus(skill settingsSkill, verbose bool) string {
	if !skill.Valid {
		if verbose && skill.Error != "" {
			return "[error] " + skill.Error
		}
		return "[error]"
	}
	state := "disabled"
	if skill.Enabled {
		state = "enabled"
	}
	if !skill.Invokable {
		state += " · not invokable"
	}
	if skill.Warning != "" {
		state += " · warning"
	}
	return "[" + state + "]"
}

func (s *settingsOverlay) settingDetail(row settingRow) settingsDetail {
	value := s.value(row)
	displayValue := safeIDEPlainText(value)
	detail := settingsDetail{title: row.Label, status: "[saved] " + displayValue, action: "←/→  Change"}
	switch row.Kind {
	case settingPermission:
		switch value {
		case "ask":
			detail.body = "Ask before every tool call. This is the recommended default for active development."
		case "allow":
			detail.body = "Apply the configured allow rules while retaining Maestro's permission gate."
		case "deny":
			detail.body = "Apply the configured deny rules before any tool reaches the workspace."
		case "yolo":
			detail.body = "Auto-approve every tool call. Use this only inside a trusted, disposable workspace."
		}
	case settingEditorMode:
		detail.body = "Standard keeps direct editing keys. Vim enables modal navigation in the embedded IDE."
	case settingTheme:
		detail.body = "Preview the full TUI and embedded editor without writing settings.json."
		if s.state.Theme != s.committedTheme {
			detail.status = "[preview] " + displayValue + " · saved " + safeIDEPlainText(s.committedTheme)
		}
		detail.action = "←/→  Preview · Enter  Save"
	case settingEngine:
		if value == "subscription" {
			detail.body = "Reuse the official vendor CLI session. This route does not automatically inherit Maestro's native MCP tools."
		} else {
			detail.body = "Run through a configured native provider. Maestro guardrails, approvals, and MCP tools remain available."
		}
	case settingAgent:
		detail.body = "Choose the vendor coding CLI used when this task route is set to subscription."
	case settingRoleModel:
		detail.body = "Pin a model for this task role, or leave auto to use the route default."
	case settingReasoning:
		detail.body = "Choose the reasoning effort honored by this role's engine and model. Auto leaves the provider or vendor default unchanged."
	case settingSlotModel:
		detail.body = "Bind a reusable model slot used by Maestro when a task has no explicit model."
	}
	return detail
}

func (s *settingsOverlay) renderFooter(styles Styles, width int) string {
	text := "Tab sections/content · ↑/↓ browse · ←/→ change · Enter action · esc close"
	if s.state.Theme != s.committedTheme {
		text = "[preview] " + s.state.Theme + " · Enter on Theme saves · esc restores " + s.committedTheme
	} else if s.notice != "" {
		text = "[" + safeIDEPlainText(defaultString(s.noticeKind, "info")) + "] " + safeIDEPlainText(s.notice)
	}
	return styles.Hint.Render(truncateRunes(text, width))
}
