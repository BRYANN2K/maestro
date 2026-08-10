package editor

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/bryann2k/maestro/internal/proposals"
)

// ReviewProposal is one staged agent write offered by :AgentReview (§5.2.2,
// §5.1.1): hunks are accepted or rejected one at a time.
type ReviewProposal struct {
	Prop  proposals.Proposal
	Store *proposals.Store
}

// ReviewState is the :AgentReview sub-mode.
type ReviewState struct {
	Active bool
	Items  []ReviewProposal
	Sel    int // selected proposal
	Hunk   int // selected hunk within the proposal
	Status string
	Pal    Palette
}

// NewReviewState returns an idle review state.
func NewReviewState() *ReviewState { return &ReviewState{} }

// Refresh reloads the pending proposals from the source.
func (r *ReviewState) Refresh(src func() []ReviewProposal) {
	if src != nil {
		r.Items = src()
	} else {
		r.Items = nil
	}
	r.Sel = 0
	r.Hunk = 0
}

// Current returns the selected proposal.
func (r *ReviewState) Current() *ReviewProposal {
	if r.Sel < 0 || r.Sel >= len(r.Items) {
		return nil
	}
	return &r.Items[r.Sel]
}

// Update handles review keys: j/k switch proposals, up/down select hunks,
// a accept hunk, r reject hunk, esc close. Returns true when closed.
func (r *ReviewState) Update(k Key) bool {
	switch k.Kind {
	case KeyEsc:
		r.Active = false
		return true
	case KeyDown:
		if cur := r.Current(); cur != nil && r.Hunk < len(cur.Prop.Hunks)-1 {
			r.Hunk++
		}
	case KeyUp:
		if r.Hunk > 0 {
			r.Hunk--
		}
	case KeyRight:
		if r.Sel < len(r.Items)-1 {
			r.Sel++
			r.Hunk = 0
		}
	case KeyLeft:
		if r.Sel > 0 {
			r.Sel--
			r.Hunk = 0
		}
	case KeyRune:
		switch string(k.Runes) {
		case "a", "A":
			cur := r.Current()
			if cur != nil && r.Hunk < len(cur.Prop.Hunks) {
				if err := cur.Store.AcceptHunk(&cur.Prop, r.Hunk); err != nil {
					r.Status = "error: " + err.Error()
				} else {
					r.Status = fmt.Sprintf("accepted hunk %d", r.Hunk+1)
				}
				if len(cur.Prop.Hunks) == 0 {
					r.removeCurrent()
				}
			}
		case "r", "R":
			if cur := r.Current(); cur != nil && r.Hunk < len(cur.Prop.Hunks) {
				if err := cur.Store.RejectHunk(&cur.Prop, r.Hunk); err != nil {
					r.Status = "error: " + err.Error()
				} else {
					r.Status = fmt.Sprintf("rejected hunk %d", r.Hunk+1)
				}
				if len(cur.Prop.Hunks) == 0 {
					r.removeCurrent()
				}
			}
		}
	}
	return false
}

func (r *ReviewState) removeCurrent() {
	if r.Sel >= 0 && r.Sel < len(r.Items) {
		r.Items = append(r.Items[:r.Sel], r.Items[r.Sel+1:]...)
	}
	if r.Sel >= len(r.Items) {
		r.Sel = len(r.Items) - 1
	}
	r.Hunk = 0
}

// View renders the review overlay.
func (r *ReviewState) View(width int) string {
	var b strings.Builder
	b.WriteString(":AgentReview — agent write proposals\n\n")
	if len(r.Items) == 0 {
		b.WriteString("  no pending proposals\n")
		return b.String()
	}
	for i, item := range r.Items {
		marker := "  "
		if i == r.Sel {
			marker = "▸ "
		}
		fmt.Fprintf(&b, "%s%s (%d hunk(s))\n", marker, item.Prop.Path, len(item.Prop.Hunks))
	}
	b.WriteString("\n")
	if cur := r.Current(); cur != nil {
		fmt.Fprintf(&b, "%s\n", cur.Prop.Path)
		oldStyle := lipgloss.NewStyle().Foreground(r.Pal.Error)
		newStyle := lipgloss.NewStyle().Foreground(r.Pal.Success)
		hunkStyle := lipgloss.NewStyle().Foreground(r.Pal.Accent)
		for i, h := range cur.Prop.Hunks {
			marker := "  "
			if i == r.Hunk {
				marker = "▸ "
			}
			fmt.Fprintf(&b, "%s%s\n", marker, hunkStyle.Render(fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.Start, len(h.OldLines), h.Start, len(h.NewLines))))
			for _, l := range h.OldLines {
				fmt.Fprintf(&b, "    %s\n", oldStyle.Render("-"+l))
			}
			for _, l := range h.NewLines {
				fmt.Fprintf(&b, "    %s\n", newStyle.Render("+"+l))
			}
		}
		b.WriteString("\n  [a] accept hunk  [r] reject hunk  ←/→ proposal  esc close\n")
	}
	if r.Status != "" {
		b.WriteString("  " + r.Status + "\n")
	}
	return b.String()
}

// updateReview routes review keys from the editor.
func (e *Editor) updateReview(k Key) EditAction {
	if e.Review.Update(k) {
		return ActNone
	}
	return ActNone
}
