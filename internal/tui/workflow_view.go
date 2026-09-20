package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

type workflowTask struct {
	ID         string
	Objective  string
	Criteria   []string
	WritePaths []string `json:"write_paths"`
}
type workflowSnapshot struct {
	Tasks     []workflowTask
	Available bool
	ChangeID  string
	Phase     string
	Error     string
	Cycle     []struct{ Label, Status, Detail string }
	Jobs      []struct {
		TaskID      string `json:"task_id"`
		Status      string
		Acceptance  string
		Objective   string
		Criteria    []string
		WritePaths  []string `json:"write_paths"`
		ActualModel string   `json:"actual_model"`
		Review      struct{ Reason string }
	}
	Changes []struct {
		ID, Phase string
		Archived  bool
	}
}

type workflowLoadedMsg struct {
	target   *workflowOverlay
	snapshot workflowSnapshot
	err      error
}
type workflowArtifactMsg struct {
	target *workflowOverlay
	text   string
	err    error
}
type workflowOverlay struct {
	phase        int
	artifact     string
	artifactOpen bool
	scroll       int
	snapshot     workflowSnapshot
	loading      bool
	err          string
	selected     int
	height       int
}

func (m *Model) openWorkflow() tea.Cmd { return m.openWorkflowChange("") }
func (m *Model) openWorkflowChange(id string) tea.Cmd {
	target := &workflowOverlay{loading: true, height: max(m.bodyHeight()-4, 8)}
	m.overlay = overlayWorkflow
	m.overlayM = target
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx(), 20*time.Second)
		defer cancel()
		data, err := m.orch.WorkflowSnapshotFor(ctx, id)
		var snapshot workflowSnapshot
		if err == nil {
			err = json.Unmarshal(data, &snapshot)
		}
		return workflowLoadedMsg{target, snapshot, err}
	}
}
func (w *workflowOverlay) View(styles Styles, width int) string {
	width = max(width, 16)
	inner := max(width-6, 10)
	t := styles.T
	title := lipgloss.NewStyle().Foreground(t.Color(TokenOyster)).Bold(true)
	muted := lipgloss.NewStyle().Foreground(t.Color(TokenSmoke))
	accent := lipgloss.NewStyle().Foreground(t.Color(TokenCharple)).Bold(true)
	rule := lipgloss.NewStyle().Foreground(t.Color(TokenIron)).Render(strings.Repeat("─", inner))
	lines := []string{title.Render("STIPULATE") + muted.Render("  /  MAESTRO"), muted.Render("Contract → execution → evidence"), rule}
	if w.artifactOpen {
		title := "Phase artifact"
		if w.phase < len(w.snapshot.Cycle) {
			title = w.snapshot.Cycle[w.phase].Label
		}
		lines = append(lines, accent.Render(title), "")
		// Preserve document line structure while neutralizing terminal controls.
		content := strings.Split(terminalSafeWorkflowText(w.artifact), "\n")
		start := min(w.scroll, max(len(content)-1, 0))
		end := min(start+max(w.height-11, 2), len(content))
		lines = append(lines, content[start:end]...)
	} else if w.loading {
		lines = append(lines, "", "Loading the current contract…")
	} else if w.err != "" {
		lines = append(lines, "", safeIDEPlainText(w.err))
	} else if !w.snapshot.Available {
		lines = append(lines, "", title.Render("Give the change a contract."), "", "1  /workflow bootstrap", "2  /workflow explore <change>", "3  Write the spec and execution plan", "4  Validate and approve the current version", "", muted.Render("Existing specifications remain available in your project."))
	} else {
		lines = append(lines, accent.Render(safeIDEPlainText(w.snapshot.ChangeID))+"  "+muted.Render(safeIDEPlainText(w.snapshot.Phase)))
		var phases []string
		for i, phase := range w.snapshot.Cycle {
			label := phase.Label
			if i == w.phase {
				label = "[" + label + "]"
			}
			switch phase.Status {
			case "complete":
				label = "✓ " + label
			case "active":
				label = "● " + label
			default:
				label = "· " + label
			}
			if phase.Status == "active" {
				phases = append(phases, accent.Render(label))
			} else {
				phases = append(phases, muted.Render(label))
			}
		}
		ribbon := ""
		for _, phase := range phases {
			candidate := phase
			if ribbon != "" {
				candidate = ribbon + "  " + phase
			}
			if lipgloss.Width(candidate) > inner && ribbon != "" {
				lines = append(lines, ribbon)
				ribbon = phase
			} else {
				ribbon = candidate
			}
		}
		if ribbon != "" {
			lines = append(lines, ribbon)
		}

		if w.snapshot.Error != "" {
			lines = append(lines, safeIDEPlainText(w.snapshot.Error))
		}
		lines = append(lines, "", title.Render("TASKS"), muted.Render("Returned contributions still require review."))
		if len(w.snapshot.Jobs) == 0 {
			if len(w.snapshot.Tasks) == 0 {
				lines = append(lines, "Create and approve an execution plan to launch workers.")
			} else {
				visible := max(min(w.height-len(lines)-6, 7), 1)
				top := max(w.selected-visible+1, 0)
				for i := top; i < min(top+visible, len(w.snapshot.Tasks)); i++ {
					task := w.snapshot.Tasks[i]
					line := fmt.Sprintf("  %-20s queued", safeIDEPlainText(task.ID))
					if i == w.selected {
						line = accent.Render("›" + strings.TrimPrefix(line, " "))
					}
					lines = append(lines, line)
				}
			}
		} else {
			detailRows := 0
			if w.height >= 26 {
				detailRows = 6
			}
			visible := max(min(w.height-len(lines)-6-detailRows, 7), 1)
			top := max(w.selected-visible+1, 0)
			for i := top; i < min(top+visible, len(w.snapshot.Jobs)); i++ {
				job := w.snapshot.Jobs[i]
				line := fmt.Sprintf("  %-20s %-10s %s", safeIDEPlainText(job.TaskID), safeIDEPlainText(job.Status), safeIDEPlainText(job.Acceptance))
				if i == w.selected {
					line = accent.Render("›" + strings.TrimPrefix(line, " "))
				}
				lines = append(lines, line)
			}
			if w.height >= 26 {
				job := w.snapshot.Jobs[clamp(w.selected, 0, len(w.snapshot.Jobs)-1)]
				lines = append(lines, "", rule, title.Render(safeIDEPlainText(job.Objective)), muted.Render("MODEL  "+safeIDEPlainText(job.ActualModel)), muted.Render("CRITERIA  "+safeIDEPlainText(strings.Join(job.Criteria, ", "))), muted.Render("WRITES  "+safeIDEPlainText(strings.Join(job.WritePaths, ", "))))
			}
		}
	}
	if len(lines) > max(w.height-6, 1) {
		lines = lines[:max(w.height-6, 1)]
	}
	lines = append(lines, "", muted.Render("←→ phase  enter inspect  ↑↓ scroll  c change  r refresh  esc back"))
	body := clampANSIWidth(strings.Join(lines, "\n"), inner)
	return lipgloss.NewStyle().Background(t.Color(TokenPanel)).Border(lipgloss.RoundedBorder()).BorderForeground(t.Color(TokenIron)).Padding(1, 2).Width(width).Render(body)
}

func terminalSafeWorkflowText(value string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = safeIDEPlainText(lines[i])
	}
	return strings.Join(lines, "\n")
}
func (m *Model) inspectWorkflow(w *workflowOverlay) tea.Cmd {
	w.artifactOpen = true
	w.artifact = "Loading artifact…"
	w.scroll = 0
	phase, id := w.phase, w.snapshot.ChangeID
	return func() tea.Msg {
		text, err := m.orch.WorkflowArtifact(m.ctx(), id, phase)
		return workflowArtifactMsg{w, text, err}
	}
}
