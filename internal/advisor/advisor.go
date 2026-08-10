// Package advisor implements the advisor model (§5.8, §11.3.5): a second
// pair of eyes on every sub-agent turn. The B9 advisor is deterministic —
// it checks edits against the spec's file ownership and the project's
// conventions, and emits typed notes (info / concern / blocker) that the
// main agent sees inline. A second LLM can slot in behind the same
// interface.
package advisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Level is a note's severity.
type Level string

// Note levels.
const (
	Info    Level = "info"
	Concern Level = "concern"
	Blocker Level = "blocker"
)

// Note is one advisor observation.
type Note struct {
	Level Level  `json:"level"`
	Text  string `json:"text"`
	File  string `json:"file,omitempty"`
}

// Spec is the minimal spec surface the advisor needs.
type Spec struct {
	ID      string
	Batches []Batch
}

// Batch is one spec batch with its file ownership.
type Batch struct {
	ID    string
	Files []string
}

// Advisor watches tool results.
type Advisor struct {
	Spec        *Spec
	Rules       []string // forbidden patterns from the spec's stream rules
	Conventions []string // convention patterns, e.g. `\bpanic\(`
	ProjectDir  string
	Emit        func(Note)
	disabled    bool
}

// New builds an advisor.
func New(projectDir string) *Advisor {
	return &Advisor{ProjectDir: projectDir, Emit: func(Note) {}}
}

// Disable turns the advisor off.
func (a *Advisor) Disable() { a.disabled = true }

// Enabled reports whether the advisor is on.
func (a *Advisor) Enabled() bool { return !a.disabled }

// SetRules installs the compiled convention patterns.
func (a *Advisor) SetRules(rules []string) { a.Conventions = rules }

// Observe processes one agent event (tool results, mostly writes).
func (a *Advisor) Observe(ctx context.Context, evType string, name, output, role string) {
	if a.disabled {
		return
	}
	switch evType {
	case "tool_result":
		a.observeToolResult(ctx, name, output, role)
	}
}

func (a *Advisor) observeToolResult(ctx context.Context, name, output, role string) {
	if name != "write" || role != "dev" {
		return
	}
	path := extractPath(output)
	if path == "" {
		return
	}
	a.checkScope(path)
	a.checkConventions(ctx, path)
}

// extractPath pulls the target path out of a write tool result.
func extractPath(output string) string {
	// Tool results are "wrote <path> (...)" or "staged <id> → <path> (...)".
	fields := strings.Fields(output)
	for i, f := range fields {
		if f == "→" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], "()")
		}
	}
	if len(fields) >= 2 && fields[0] == "wrote" {
		return strings.Trim(fields[1], "()")
	}
	return ""
}

// checkScope flags writes outside the spec's file ownership (blocker).
func (a *Advisor) checkScope(path string) {
	if a.Spec == nil {
		return
	}
	for _, b := range a.Spec.Batches {
		for _, f := range b.Files {
			if strings.HasPrefix(path, f) {
				return
			}
		}
	}
	if strings.HasPrefix(path, "specs/") || strings.HasPrefix(path, "maestro/") {
		return
	}
	a.note(Blocker, fmt.Sprintf("%s is outside the spec's file list — out-of-scope write", path), path)
}

// checkConventions scans the written file against the convention patterns.
func (a *Advisor) checkConventions(ctx context.Context, path string) {
	if len(a.Conventions) == 0 {
		return
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.ProjectDir, abs)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		upper := strings.ToUpper(line)
		for _, pattern := range a.Conventions {
			if re, err := regexp.Compile(pattern); err == nil && re.MatchString(line) {
				a.note(Concern, fmt.Sprintf("%s:%d matches forbidden pattern %q", path, i+1, pattern), path)
				break
			}
		}
		if strings.Contains(upper, "TODO") || strings.Contains(upper, "FIXME") {
			a.note(Info, fmt.Sprintf("%s:%d placeholder left behind", path, i+1), path)
		}
	}
}

func (a *Advisor) note(level Level, text, file string) {
	a.Emit(Note{Level: level, Text: text, File: file})
}
