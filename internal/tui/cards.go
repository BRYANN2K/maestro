package tui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/proposals"
)

// Card is one tool call rendered in the message stream (§5.1.1), with
// premium chrome and an expandable output viewer (B11 §11.8).
type Card struct {
	ID           string
	Kind         string // tool | write
	Name         string
	Status       string // running | done | error | cancelled | proposed | discarded
	Detail       string
	Full         string // full tool output (expanded view)
	ProposalPath string
	Proposal     *proposals.Proposal
	Lifecycle    string // orchestrator transition after proposal acceptance
	Expanded     bool
}

// compactParams truncates a tool summary (main param) to the width budget
// with an ellipsis — opencode-style width-budgeted param rendering; short
// values pass through untouched.
func compactParams(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len([]rune(s)) <= budget {
		return s
	}
	return truncateRunes(s, budget)
}

// actionLabel returns the human gerund for a running tool (opencode-style
// per-tool action labels).
func actionLabel(name string) string {
	switch name {
	case "bash":
		return "Running command…"
	case "read", "view":
		return "Reading file…"
	case "grep":
		return "Searching content…"
	case "write":
		return "Preparing write…"
	case "glob":
		return "Finding files…"
	case "ask":
		return "Asking…"
	default:
		return "Working…"
	}
}

// Render draws a compact activity row or an inline proposal preview. Tool
// cards use the same thin outline as the approved mockup; proposals expose
// the changed lines immediately instead of hiding the useful part behind a
// one-line warning card.
func (c *Card) Render(styles Styles, width int) string {
	if width <= 0 {
		width = 40
	}
	if c.Status == "proposed" && c.Proposal != nil {
		return c.renderProposal(styles, width)
	}
	var icon string
	var color color.Color
	rail := styles.T.Color(TokenIron)
	switch c.Status {
	case "running":
		icon, color, rail = "◌", styles.T.Color(TokenMustard), styles.T.Color(TokenMustard)
	case "done":
		icon, color = "✓", styles.T.Color(TokenJulep)
	case "error":
		icon, color, rail = "✗", styles.T.Color(TokenSash), styles.T.Color(TokenSash)
	case "cancelled":
		icon, color = "■", styles.T.Color(TokenSmoke)
	case "proposed":
		icon, color, rail = "◇", styles.T.Color(TokenCharple), styles.T.Color(TokenCharple)
	default:
		icon, color = "·", styles.T.Color(TokenSmoke)
	}

	name := safeIDEPlainText(c.Name)
	if name == "" {
		name = "tool"
	}
	left := lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render("tool  ") +
		lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Bold(true).Render(name)
	if c.Kind == "write" {
		target := safeIDEPlainText(c.Detail)
		if target == "" {
			target = name
		}
		left = lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("write") + "  " +
			lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Render(target)
	}
	if c.ProposalPath != "" {
		left += "  " + lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render(safeIDEPlainText(c.ProposalPath))
	}
	if c.Status == "running" && c.Detail == "" {
		left += "  " + styles.Hint.Render(actionLabel(c.Name))
	} else if c.Detail != "" && c.Kind != "write" {
		left += "  " + styles.Hint.Render(compactParams(safeIDEPlainText(c.Detail), max(width-24, 10)))
	}
	chevron := ""
	if c.Full != "" {
		chevron = "▸ "
		if c.Expanded {
			chevron = "▾ "
		}
	}
	right := lipgloss.NewStyle().Foreground(color).Bold(true).Render(chevron + icon)
	innerW := max(width-3, 1)
	gap := max(innerW-lipgloss.Width(left)-lipgloss.Width(right), 1)
	line := clampANSIWidth(left+strings.Repeat(" ", gap)+right, innerW)

	var body strings.Builder
	body.WriteString(line)
	if c.Expanded && c.Full != "" {
		full := terminalSafeMarkdownText(strings.TrimSpace(c.Full))
		full = xansi.Wordwrap(strings.ReplaceAll(full, "\t", "    "), innerW, "")
		body.WriteString("\n" + styles.MessageMuted.Render(clampANSIWidth(full, innerW)))
	} else if c.Status == "proposed" {
		body.WriteString("\n" + styles.Hint.Render("a accept  ·  d discard  ·  click for diff"))
	} else if c.Full != "" {
		body.WriteString("\n" + styles.Hint.Render("click to expand output"))
	}
	if c.Status == "error" {
		body.WriteString("\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("↗ fix with Maestro"))
	}
	base := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(rail).
		Background(styles.T.Color(TokenPanel)).
		PaddingLeft(1).
		Width(max(width-1, 1)).MaxWidth(max(width-1, 1))
	return base.Render(body.String())
}

func (c *Card) renderProposal(styles Styles, width int) string {
	innerW := max(width-4, 1)
	path := c.ProposalPath
	if path == "" {
		path = c.Proposal.Path
	}
	path = safeIDEPlainText(compactWorkspacePath(path))
	path = truncateRunes(path, max(innerW-18, 12))
	adds, removes := 0, 0
	for _, h := range c.Proposal.Hunks {
		adds += len(h.NewLines)
		removes += len(h.OldLines)
	}
	stats := lipgloss.NewStyle().Foreground(styles.T.Color(TokenJulep)).Render(fmt.Sprintf("+%d", adds)) + " " +
		lipgloss.NewStyle().Foreground(styles.T.Color(TokenSash)).Render(fmt.Sprintf("-%d", removes))
	title := lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Bold(true).Render(path)
	gap := max(innerW-lipgloss.Width(title)-lipgloss.Width(stats), 1)

	var body strings.Builder
	body.WriteString(clampANSIWidth(title+strings.Repeat(" ", gap)+stats, innerW))
	if c.Expanded {
		body.WriteString("\n" + c.proposalPreview(styles, innerW, 9))
	} else {
		body.WriteString("\n" + styles.Hint.Render("▸ click to expand changed document"))
	}
	body.WriteString("\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("a") + " accept  ·  ")
	body.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("d") + " discard  ·  ")
	body.WriteString(styles.Hint.Render("click for full diff"))

	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(styles.T.Color(TokenIron)).
		Padding(0, 1).
		Width(max(width-2, 1)).MaxWidth(max(width-2, 1)).
		Render(body.String())
}

func compactWorkspacePath(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) > 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return filepath.Base(path)
}

func (c *Card) proposalPreview(styles Styles, width, limit int) string {
	var lines []string
	for _, h := range c.Proposal.Hunks {
		lines = append(lines, lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render(
			fmt.Sprintf("@@ line %d", h.Start),
		))
		for i, old := range h.OldLines {
			text := fmt.Sprintf("%4d  -  %s", h.Start+i, safeIDEPlainText(old))
			lines = append(lines, lipgloss.NewStyle().Foreground(styles.T.Color(TokenSash)).Render(truncateRunes(text, width)))
			if len(lines) >= limit {
				return strings.Join(lines, "\n")
			}
		}
		for i, line := range h.NewLines {
			text := fmt.Sprintf("%4d  +  %s", h.Start+i, safeIDEPlainText(line))
			lines = append(lines, lipgloss.NewStyle().Foreground(styles.T.Color(TokenJulep)).Render(truncateRunes(text, width)))
			if len(lines) >= limit {
				return strings.Join(lines, "\n")
			}
		}
	}
	if len(lines) == 0 {
		return styles.Hint.Render("No changed lines")
	}
	return strings.Join(lines, "\n")
}
