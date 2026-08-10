package editor

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bryann2k/maestro/internal/git"
)

// Sign is a git gutter sign for one line.
type Sign int

// Sign kinds.
const (
	SignNone Sign = iota
	SignAdded
	SignModified
	SignDeleted
)

// Gutter computes per-line git signs for the active buffer by parsing the
// unified diff against HEAD.
type Gutter struct {
	Client *git.Client
	Signs  map[int]Sign
	Hunks  []GutterHunk
	Path   string // last path used by Refresh; avoids refreshes during rendering
}

// GutterHunk is one changed region for [c / ]c navigation.
type GutterHunk struct {
	Start int // first changed line (1-based)
	End   int
}

// NewGutter builds a gutter bound to a repo.
func NewGutter(c *git.Client) *Gutter {
	return &Gutter{Client: c, Signs: map[int]Sign{}}
}

// Refresh recomputes the signs for path.
func (g *Gutter) Refresh(ctx context.Context, path string) {
	g.Path = path
	g.Signs = map[int]Sign{}
	g.Hunks = nil
	if g.Client == nil {
		return
	}
	diff, err := g.Client.DiffUnified(ctx, "HEAD")
	if err != nil {
		return
	}
	// Filter hunks touching path.
	rel := relative(path)
	var inFile bool
	var newLine, oldLine int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inFile = strings.Contains(line, "a/"+rel) || strings.Contains(line, " b/"+rel)
			newLine, oldLine = 0, 0
		case !inFile:
			continue
		case strings.HasPrefix(line, "@@"):
			hdr := line[2:]
			// parse "-oldStart,oldCount +newStart,newCount"
			parts := strings.Split(hdr, " ")
			if len(parts) >= 2 {
				if n, err := parseHunkStart(parts[1]); err == nil {
					newLine = n
				}
				if len(parts) >= 1 {
					if n, err := parseHunkStart(parts[0]); err == nil {
						oldLine = n
					}
				}
			}
			if newLine > 0 {
				g.Hunks = append(g.Hunks, GutterHunk{Start: newLine, End: newLine})
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			g.Signs[newLine] = SignAdded
			if len(g.Hunks) > 0 {
				g.Hunks[len(g.Hunks)-1].End = newLine
			}
			newLine++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			if newLine > 0 {
				g.Signs[newLine] = SignModified
			}
			oldLine++
		case strings.HasPrefix(line, "\\"):
			// no newline marker
		default:
			newLine++
			oldLine++
		}
	}
}

func parseHunkStart(part string) (int, error) {
	s := strings.TrimPrefix(part, "-")
	s = strings.TrimPrefix(s, "+")
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return strconv.Atoi(s)
}

func relative(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// NextHunk returns the hunk at or after line, or nil.
func (g *Gutter) NextHunk(line int) *GutterHunk {
	for i := range g.Hunks {
		if g.Hunks[i].End >= line {
			return &g.Hunks[i]
		}
	}
	return nil
}

// PrevHunk returns the hunk at or before line, or nil.
func (g *Gutter) PrevHunk(line int) *GutterHunk {
	for i := len(g.Hunks) - 1; i >= 0; i-- {
		if g.Hunks[i].Start <= line {
			return &g.Hunks[i]
		}
	}
	return nil
}

// StageHunks stages the buffer's file via git add (deterministic path).
func StageHunks(ctx context.Context, c *git.Client, path string) error {
	if err := c.Add(ctx, path); err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	return nil
}

// StatusText renders the git status line (branch + dirty state).
func StatusText(ctx context.Context, c *git.Client) string {
	if c == nil {
		return ""
	}
	branch, err := c.CurrentBranch(ctx)
	if err != nil {
		return ""
	}
	st, err := c.Status(ctx)
	if err != nil {
		return branch
	}
	if st.Dirty {
		return branch + " ●"
	}
	return branch
}
