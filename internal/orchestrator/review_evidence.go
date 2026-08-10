package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bryann2k/maestro/internal/spec"
)

// legacyReviewEvidence gives subscription-backed reviewers the same explicit
// spec trio and complete worktree patch that native reviewers receive through
// Spawn. In particular, WorktreeDiff includes new untracked implementation
// and test files that ordinary `git diff HEAD` omits.
func (o *Orchestrator) legacyReviewEvidence(ctx context.Context) (string, error) {
	if o.spec == nil {
		return "", fmt.Errorf("review evidence: no active spec")
	}
	route := o.workspaceRoute()
	var b strings.Builder
	for _, name := range []string{spec.FileSpec, spec.FileDesign, spec.FileTasks} {
		path := route.store.PathFor(o.spec.ID, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("review evidence: read %s: %w", name, err)
		}
		fmt.Fprintf(&b, "\n\n=== FILE: %s ===\n%s", path, data)
	}
	diff, err := route.git.WorktreeDiff(ctx, "HEAD")
	if err != nil {
		return "", fmt.Errorf("review evidence: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		diff = "(clean worktree)\n"
	}
	fmt.Fprintf(&b, "\n\n=== COMPLETE GIT WORKTREE DIFF (TRACKED + UNTRACKED) ===\n%s", diff)
	return b.String(), nil
}
