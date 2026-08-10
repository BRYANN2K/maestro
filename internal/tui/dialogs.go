package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// dialog is a modal overlay with its own key handling (B11 §11.5).
type dialog interface {
	Title() string
	View(styles Styles, width, height int) string
	Width() int
	Height() int
}

// permissionDialog is an OpenCode-style decision dock. It stays attached to
// the bottom of the current workspace instead of replacing it with a large,
// generic modal.
type permissionDialog struct {
	req       *permissionRequest
	queue     *PermissionQueue
	buttonSel int // 0 once, 1 always, 2 reject
	buttons   []permissionButtonHit
	resolved  bool
}

type permissionButtonHit struct {
	x, y, w int
}

func newPermissionDialog(req *permissionRequest, queue *PermissionQueue) *permissionDialog {
	return &permissionDialog{req: req, queue: queue}
}

func (d *permissionDialog) Title() string { return "Permission required" }

func (d *permissionDialog) Width() int  { return 78 }
func (d *permissionDialog) Height() int { return 8 }

func (d *permissionDialog) View(styles Styles, width, height int) string {
	return d.renderDock(styles, width, height)
}

// resolve returns the error the gate receives.
func (d *permissionDialog) resolve() error {
	if d.resolved {
		return fmt.Errorf("permission request already resolved")
	}
	switch d.buttonSel {
	case 0:
		return nil
	case 1:
		if d.queue != nil {
			d.queue.AllowTool(d.req.Spec.Name)
		}
		return nil
	default:
		return fmt.Errorf("denied")
	}
}

// move cycles the button selection.
func (d *permissionDialog) move(delta int) {
	d.buttonSel += delta
	if d.buttonSel < 0 {
		d.buttonSel = 2
	}
	if d.buttonSel > 2 {
		d.buttonSel = 0
	}
}

func (d *permissionDialog) buttonAt(x, y int) (int, bool) {
	for i, button := range d.buttons {
		if y == button.y && x >= button.x && x < button.x+button.w {
			return i, true
		}
	}
	return 0, false
}

func (d *permissionDialog) renderDock(styles Styles, width, screenHeight int) string {
	width = max(width, 24)
	icon, title, detail, note := permissionInfo(d.req)
	contentWidth := max(width-8, 12)
	detail = truncateRunes(detail, contentWidth)
	note = truncateRunes(note, contentWidth)

	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	titleStyle := lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster))
	muted := lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke))
	panel := lipgloss.NewStyle().Background(styles.T.Color(TokenPanel)).Width(width)

	lines := []string{
		lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Render(strings.Repeat("─", width)),
		panel.Render("  " + accent.Render("△") + " " + titleStyle.Render("Permission required")),
		panel.Render("    " + muted.Render(icon) + " " + textStyle.Render(title)),
		panel.Render("      " + textStyle.Render(detail)),
		panel.Render("      " + muted.Render(note)),
	}

	buttonDefs := []struct {
		key, label string
	}{{"a", "Allow once"}, {"s", "Always allow"}, {"d", "Reject"}}
	if width < 60 {
		buttonDefs = []struct{ key, label string }{{"a", "Once"}, {"s", "Always"}, {"d", "Reject"}}
	}
	var row strings.Builder
	row.WriteString("  ")
	d.buttons = d.buttons[:0]
	buttonY := screenHeight - 3
	for i, button := range buttonDefs {
		if i > 0 {
			row.WriteString(" ")
		}
		x := ansi.StringWidth(row.String())
		rendered, hitWidth := permissionButton(styles, button.key, button.label, d.buttonSel == i)
		row.WriteString(rendered)
		d.buttons = append(d.buttons, permissionButtonHit{x: x, y: buttonY, w: hitWidth})
	}
	lines = append(lines,
		panel.Render(row.String()),
		panel.Render("  "+styles.Hint.Render("←/→ or tab choose · enter confirm · esc reject")),
		panel.Render(""),
	)
	return strings.Join(lines, "\n")
}

func permissionButton(styles Styles, key, label string, selected bool) (string, int) {
	marker := " "
	if selected {
		marker = ">"
	}
	plain := marker + key + "  " + label + " "
	base := lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke))
	if selected {
		base = base.Foreground(styles.T.Color(TokenChar)).Background(styles.T.Color(TokenCharple)).Bold(true)
		return base.Render(plain), len([]rune(plain))
	}
	keyText := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render(" " + key)
	labelText := base.Render("  " + label + " ")
	return keyText + labelText, len([]rune(plain))
}

func permissionInfo(req *permissionRequest) (icon, title, detail, note string) {
	args := map[string]any{}
	_ = json.Unmarshal([]byte(req.Call.Args), &args)
	value := func(keys ...string) string {
		for _, key := range keys {
			if raw, ok := args[key]; ok {
				if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
		return ""
	}
	name := strings.ToLower(req.Call.Name)
	switch {
	case strings.Contains(name, "bash") || strings.Contains(name, "shell") || strings.Contains(name, "exec"):
		icon, title, detail, note = "#", "Shell command", "$ "+value("command", "cmd"), "Runs once in the current workspace"
	case strings.Contains(name, "write") || strings.Contains(name, "edit") || strings.Contains(name, "patch"):
		path := value("path", "file_path", "filepath")
		icon, title, detail, note = "→", "Change "+path, path, "This operation can modify files"
	case strings.Contains(name, "read") || strings.Contains(name, "list") || strings.Contains(name, "glob") || strings.Contains(name, "grep"):
		path := value("path", "file_path", "filepath", "pattern")
		icon, title, detail, note = "→", "Access "+path, path, "Read-only access"
	case strings.Contains(name, "web") || strings.Contains(name, "fetch"):
		target := value("url", "query")
		icon, title, detail, note = "%", "External request", target, "Connects to an external service"
	default:
		icon, title, detail, note = "⚙", "Call tool "+req.Call.Name, req.Call.Args, "The agent is waiting for your decision"
	}
	if strings.TrimSpace(detail) == "" || detail == "$ " {
		detail = compactParams(req.Call.Args, 64)
	}
	if strings.HasSuffix(strings.TrimSpace(title), "Change") || strings.HasSuffix(strings.TrimSpace(title), "Access") {
		title = "Call tool " + req.Call.Name
	}
	return icon, title, detail, note
}

// dialogStack manages stacked modals with clamped centering.
type dialogStack struct {
	items []dialog
}

func (ds *dialogStack) push(d dialog) { ds.items = append(ds.items, d) }
func (ds *dialogStack) pop() {
	if len(ds.items) > 0 {
		ds.items = ds.items[:len(ds.items)-1]
	}
}
func (ds *dialogStack) remove(target dialog) {
	for i := len(ds.items) - 1; i >= 0; i-- {
		if ds.items[i] != target {
			continue
		}
		copy(ds.items[i:], ds.items[i+1:])
		ds.items[len(ds.items)-1] = nil
		ds.items = ds.items[:len(ds.items)-1]
		return
	}
}
func (ds *dialogStack) top() (dialog, bool) {
	if len(ds.items) == 0 {
		return nil, false
	}
	return ds.items[len(ds.items)-1], true
}
func (ds *dialogStack) empty() bool { return len(ds.items) == 0 }

// render places the top dialog centered and clamped, with a drop shadow.
func (ds *dialogStack) render(styles Styles, screenW, screenH int, base string) string {
	d, ok := ds.top()
	if !ok {
		return ""
	}
	if permission, ok := d.(*permissionDialog); ok {
		dock := permission.renderDock(styles, screenW, screenH)
		return replaceBottom(base, dock, screenW, screenH)
	}
	w := d.Width()
	h := d.Height()
	if w > screenW-4 {
		w = screenW - 4
	}
	if h > screenH-4 {
		h = screenH - 4
	}
	content := d.View(styles, w, h)
	box := styles.Dialog.Width(w).Height(h).Render(content)
	canvas := addShadow(box, styles.T.Color(TokenPanel))
	return lipgloss.Place(screenW, screenH, lipgloss.Center, lipgloss.Center, canvas)
}

func replaceBottom(base, dock string, width, height int) string {
	dockLines := strings.Split(dock, "\n")
	keep := max(height-len(dockLines), 0)
	baseLines := strings.Split(base, "\n")
	if len(baseLines) > keep {
		baseLines = baseLines[:keep]
	}
	for len(baseLines) < keep {
		baseLines = append(baseLines, strings.Repeat(" ", max(width, 0)))
	}
	return strings.Join(append(baseLines, dockLines...), "\n")
}

// addShadow appends a one-cell drop-shadow band along the bottom and right
// edges of the box (opencode PlaceOverlay port): the canvas grows by one
// column and one row, so centering the canvas keeps the dialog optically
// centered.
func addShadow(box string, shadow color.Color) string {
	lines := strings.Split(box, "\n")
	sw := 0
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > sw {
			sw = w
		}
	}
	sh := len(lines)
	band := lipgloss.NewStyle().Foreground(shadow).Render("░")
	out := make([]string, 0, sh+1)
	for i := 0; i < sh; i++ {
		if i == 0 {
			out = append(out, lines[i]+" ")
		} else {
			out = append(out, lines[i]+band)
		}
	}
	out = append(out, " "+strings.Repeat(band, sw))
	return strings.Join(out, "\n")
}
