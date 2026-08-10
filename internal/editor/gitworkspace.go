package editor

import (
	"context"
	"strings"

	"github.com/bryann2k/maestro/internal/git"
)

// GitWorkspace is the full-screen git UI (Space G): status entries with
// stage/unstage, commit, and branch list.
type GitWorkspace struct {
	Active  bool
	Client  *git.Client
	Entries []StatusEntry
	Sel     int
	Message string
	Branch  string
}

// StatusEntry mirrors git status for the workspace view.
type StatusEntry struct {
	Path       string
	IndexState byte
	Worktree   byte
	Staged     bool
}

// Refresh reloads the status.
func (g *GitWorkspace) Refresh(ctx context.Context) {
	if g.Client == nil {
		return
	}
	st, err := g.Client.Status(ctx)
	if err != nil {
		return
	}
	g.Branch = st.Branch
	g.Entries = nil
	for _, f := range st.Files {
		g.Entries = append(g.Entries, StatusEntry{
			Path: f.Path, IndexState: f.IndexState, Worktree: f.Worktree,
			Staged: f.IndexState != ' ' && f.IndexState != '?',
		})
	}
	if g.Sel >= len(g.Entries) {
		g.Sel = len(g.Entries) - 1
	}
}

// Update handles workspace keys.
func (g *GitWorkspace) Update(ctx context.Context, k Key) bool {
	switch k.Kind {
	case KeyEsc:
		g.Active = false
		return true
	case KeyDown:
		if g.Sel < len(g.Entries)-1 {
			g.Sel++
		}
	case KeyUp:
		if g.Sel > 0 {
			g.Sel--
		}
	case KeyRune:
		switch string(k.Runes) {
		case "S": // stage all
			_ = g.Client.Add(ctx)
			g.Refresh(ctx)
		case "U": // unstage all
			_ = g.Client.UnstageAll(ctx)
			g.Refresh(ctx)
		case "s": // stage selected
			if g.Sel >= 0 && g.Sel < len(g.Entries) {
				_ = g.Client.Add(ctx, g.Entries[g.Sel].Path)
				g.Refresh(ctx)
			}
		case "C": // commit with the typed message
			if strings.TrimSpace(g.Message) != "" {
				_ = g.Client.Commit(ctx, g.Message)
				g.Message = ""
				g.Refresh(ctx)
			}
		case "D": // discard (checkout) selected file
			if g.Sel >= 0 && g.Sel < len(g.Entries) {
				_ = g.Client.Discard(ctx, g.Entries[g.Sel].Path)
				g.Refresh(ctx)
			}
		default:
			// typing the commit message
			g.Message += string(k.Runes)
		}
	case KeyBackspace:
		if len(g.Message) > 0 {
			g.Message = g.Message[:len(g.Message)-1]
		}
	case KeySpace:
		g.Message += " "
	}
	return false
}

// View renders the workspace.
func (g *GitWorkspace) View(width int) string {
	var b strings.Builder
	b.WriteString("Git workspace — " + g.Branch + "\n\n")
	if len(g.Entries) == 0 {
		b.WriteString("  working tree clean\n")
	} else {
		for i, e := range g.Entries {
			marker := "  "
			if i == g.Sel {
				marker = "▸ "
			}
			state := " "
			if e.Staged {
				state = "+"
			}
			b.WriteString(marker + state + " " + e.Path + "\n")
		}
	}
	b.WriteString("\n  [s] stage  [a] stage all  [u] unstage  [d] discard  [c] commit\n")
	b.WriteString("  commit: " + g.Message + "\n")
	b.WriteString("  esc close")
	return b.String()
}

// updateGit routes workspace keys.
func (e *Editor) updateGit(k Key) EditAction {
	if e.Git != nil && e.Git.Update(context.Background(), k) {
		return ActNone
	}
	return ActNone
}

// StartGitWorkspace opens the workspace overlay.
func (e *Editor) StartGitWorkspace(c *git.Client) {
	e.Git = &GitWorkspace{Active: true, Client: c}
	e.Git.Refresh(context.Background())
}
