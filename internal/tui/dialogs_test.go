package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestAddShadow(t *testing.T) {
	styles := NewStyles(Charmtone())
	// lipgloss pads each row to the styled width.
	box := lipgloss.NewStyle().Width(6).Height(3).Render("ab\ncd\nef")
	canvas := addShadow(box, styles.T.Color(TokenPanel))
	lines := strings.Split(canvas, "\n")
	if len(lines) != 4 {
		t.Fatalf("canvas height = %d, want 4 (shadow row)", len(lines))
	}
	clean := stripANSI(canvas)
	if strings.Contains(clean, "ab░") {
		t.Errorf("row 0 must not carry the shadow band: %q", clean)
	}
	// Rows 1-2: right-edge band.
	for i := 1; i <= 2; i++ {
		if !strings.HasSuffix(stripANSI(lines[i]), "░") {
			t.Errorf("row %d must end with the band: %q", i, clean)
		}
	}
	// Bottom row: band under the full box width (6 cells + the leading cell).
	last := stripANSI(lines[3])
	if strings.Count(last, "░") != 6 {
		t.Errorf("bottom band width = %d, want 6: %q", strings.Count(last, "░"), last)
	}
}

func TestPermissionDockUsesOpenCodeHierarchy(t *testing.T) {
	styles := NewStyles(Charmtone())
	dialog := newPermissionDialog(&permissionRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: `{"command":"go test ./..."}`},
	}, NewPermissionQueue(1))
	view := stripANSI(dialog.renderDock(styles, 72, 24))
	for _, want := range []string{"△ Permission required", "# Shell command", "$ go test ./...", "Allow once", "Always allow", "Reject"} {
		if !strings.Contains(view, want) {
			t.Errorf("permission dock missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Tool:") || strings.Contains(view, "Args:") {
		t.Fatalf("legacy key/value form leaked into dock:\n%s", view)
	}
}

func TestPermissionDockStaysWithinNarrowTerminal(t *testing.T) {
	styles := NewStyles(Charmtone())
	dialog := newPermissionDialog(&permissionRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: `{"command":"a very long command that should be truncated before it overflows the terminal width"}`},
	}, NewPermissionQueue(1))
	view := dialog.renderDock(styles, 42, 20)
	for i, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 42 {
			t.Errorf("line %d width = %d, want <= 42: %q", i, width, stripANSI(line))
		}
	}
}

func TestPermissionAlwaysAllowIsToolScoped(t *testing.T) {
	queue := NewPermissionQueue(2)
	dialog := newPermissionDialog(&permissionRequest{
		Call: agentcore.ToolCall{Name: "bash"}, Spec: agentcore.ToolSpec{Name: "bash", NeedsApproval: true},
	}, queue)
	dialog.buttonSel = 1
	if err := dialog.resolve(); err != nil {
		t.Fatal(err)
	}
	if !queue.toolAllowed("bash") || queue.toolAllowed("write") {
		t.Fatalf("scoped grants: bash=%v write=%v", queue.toolAllowed("bash"), queue.toolAllowed("write"))
	}
}
