package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Section is one spec batch with its file ownership list.
type Section struct {
	ID    string   // batch ID, e.g. "b1"
	Files []string // paths owned by the section (patterns allowed: prefixes)
}

// PlannedCommit is one atomic commit: one spec section, its files, ordered
// by dependency.
type PlannedCommit struct {
	Section string
	Message string // "<cat>(<spec-id>/<section>): ..."
	Files   []string
}

// CommitPlan is the ordered, cycle-checked commit sequence (F6, §11.1).
type CommitPlan struct {
	Commits []PlannedCommit
}

// Plan splits the changed files into spec-mapped atomic commits. Files are
// grouped by their owning section (longest prefix match); sections are
// topologically ordered by import dependencies (a file importing another
// section's file commits after it). A dependency cycle rejects the plan.
func Plan(specID string, changed []string, sections []Section, deps map[string][]string) (*CommitPlan, error) {
	// Map files → sections.
	fileSection := map[string]string{}
	sectionFiles := map[string][]string{}
	for _, f := range changed {
		owner := ownerSection(f, sections)
		fileSection[f] = owner
		sectionFiles[owner] = append(sectionFiles[owner], f)
	}

	// Dependency graph between sections.
	depGraph := map[string]map[string]bool{}
	for _, f := range changed {
		owner := fileSection[f]
		if depGraph[owner] == nil {
			depGraph[owner] = map[string]bool{}
		}
		for _, dep := range deps[f] {
			if depOwner, ok := fileSection[dep]; ok && depOwner != owner {
				depGraph[owner][depOwner] = true
			}
		}
	}

	order, err := topoSort(depGraph)
	if err != nil {
		return nil, fmt.Errorf("plan commits: %w", err)
	}

	plan := &CommitPlan{}
	for _, sec := range order {
		files := sectionFiles[sec]
		sort.Strings(files)
		plan.Commits = append(plan.Commits, PlannedCommit{
			Section: sec,
			Message: fmt.Sprintf("%s(%s/%s)", categoryOf(specID), specID, sec),
			Files:   files,
		})
	}
	return plan, nil
}

// ownerSection finds the owning section by longest prefix match.
func ownerSection(path string, sections []Section) string {
	best, bestLen := "", -1
	for _, s := range sections {
		for _, f := range s.Files {
			if strings.HasPrefix(path, f) && len(f) > bestLen {
				best, bestLen = s.ID, len(f)
			}
		}
	}
	if best == "" {
		return "misc"
	}
	return best
}

// topoSort returns the sections in dependency order; rejects cycles.
func topoSort(depGraph map[string]map[string]bool) ([]string, error) {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var order []string
	var visit func(sec string) error
	visit = func(sec string) error {
		switch state[sec] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("dependency cycle at section %q", sec)
		}
		state[sec] = visiting
		deps := depGraph[sec]
		keys := make([]string, 0, len(deps))
		for d := range deps {
			keys = append(keys, d)
		}
		sort.Strings(keys)
		for _, d := range keys {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[sec] = done
		order = append(order, sec)
		return nil
	}
	secs := make([]string, 0, len(depGraph))
	for s := range depGraph {
		secs = append(secs, s)
	}
	sort.Strings(secs)
	for _, s := range secs {
		if err := visit(s); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// categoryOf maps the spec ID prefix to a commit category.
func categoryOf(specID string) string {
	for _, cat := range []string{"fix", "docs", "chore"} {
		if strings.HasPrefix(specID, cat+"-") {
			return cat
		}
	}
	return "feat"
}

// ImportDeps parses Go import dependencies between the changed files:
// path → list of other changed files it imports.
func ImportDeps(root string, changed []string) map[string][]string {
	deps := map[string][]string{}
	importRe := regexp.MustCompile(`"([^"]+)"`)
	byPkg := map[string]string{}
	for _, f := range changed {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		byPkg[pkgPath(root, f)] = f
	}
	for _, f := range changed {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		for _, m := range importRe.FindAllStringSubmatch(string(data), -1) {
			imp := m[1]
			if target, ok := byPkg[imp]; ok {
				deps[f] = append(deps[f], target)
			}
		}
	}
	return deps
}

// pkgPath guesses the package path of a file from the module + dir.
func pkgPath(root, file string) string {
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	module := ""
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.HasPrefix(line, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	rel := filepath.ToSlash(filepath.Dir(file))
	if rel == "." {
		return module
	}
	return module + "/" + rel
}

// Apply executes the plan: stage each commit's files and commit in order.
func (p *CommitPlan) Apply(ctx context.Context, c *Client) error {
	for _, commit := range p.Commits {
		if len(commit.Files) == 0 {
			continue
		}
		if err := c.Add(ctx, commit.Files...); err != nil {
			return fmt.Errorf("apply %s: %w", commit.Section, err)
		}
		if err := c.CommitOnly(ctx, commit.Message, commit.Files...); err != nil {
			return fmt.Errorf("apply %s: %w", commit.Section, err)
		}
	}
	return nil
}
