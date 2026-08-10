package orchestrator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxContextFiles     = 240
	maxContextFileBytes = 12 << 10
	maxContextBytes     = 48 << 10
)

var repositoryContextFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	filepath.Join(".github", "copilot-instructions.md"),
	"go.mod",
	"package.json",
	"Cargo.toml",
	"pyproject.toml",
	"README.md",
}

// buildRepositoryContext collects deterministic, bounded, read-only project
// context for proposal planning. It never executes repository code and avoids
// files commonly used to hold credentials.
func buildRepositoryContext(root string) string {
	var sections []string
	remaining := maxContextBytes
	for _, relative := range repositoryContextFiles {
		if remaining <= 0 || sensitiveContextPath(relative) {
			continue
		}
		path := filepath.Join(root, relative)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		limit := min(maxContextFileBytes, remaining)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(data) > limit {
			data = append(data[:limit], []byte("\n[truncated]")...)
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			continue
		}
		sections = append(sections, fmt.Sprintf("### %s\n%s", filepath.ToSlash(relative), strings.TrimSpace(string(data))))
		remaining -= len(data)
	}

	var inventory []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return nil
		}
		if entry.IsDir() {
			if skippedContextDir(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(inventory) >= maxContextFiles || sensitiveContextPath(relative) {
			return nil
		}
		inventory = append(inventory, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(inventory)
	if len(inventory) > 0 {
		sections = append(sections, "### File inventory (bounded)\n"+strings.Join(inventory, "\n"))
	}
	if len(sections) == 0 {
		return "(empty repository context)"
	}
	return strings.Join(sections, "\n\n")
}

func skippedContextDir(relative string) bool {
	base := strings.ToLower(filepath.Base(relative))
	switch base {
	case ".git", "node_modules", "vendor", "dist", "build", "coverage", "__pycache__", ".cache", ".next":
		return true
	}
	return filepath.ToSlash(relative) == "specs/archive"
}

func sensitiveContextPath(relative string) bool {
	base := strings.ToLower(filepath.Base(relative))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.Contains(base, "credential") || strings.Contains(base, "secret")
}
