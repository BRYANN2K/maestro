package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
)

// resolvePersistedSessionWorkspace completes and validates the exact routing
// identity of a Git-backed session. Empty paths are never interpreted as "the
// checkout of this process": linked worktrees share a session namespace, so
// that interpretation can silently route a conversation into another tree.
//
// Legacy records are migrated only when Git's registry proves one unique
// target. Ambiguous records fail closed and remain untouched on disk.
func resolvePersistedSessionWorkspace(ctx context.Context, client *git.Client, sess session.Session) (session.Session, bool, error) {
	workspaces, err := client.ListWorkspaces(ctx)
	if err != nil {
		return session.Session{}, false, err
	}
	healthy := make([]git.Workspace, 0, len(workspaces))
	for _, workspace := range workspaces {
		if workspace.Healthy {
			healthy = append(healthy, workspace)
		}
	}

	// A missing managed archive worktree is a special transaction recovery
	// state. New() verifies the already-published Git history before clearing
	// it; no ordinary session may use this exception.
	if sess.Worktree != "" && sess.Phase == session.PhaseArchive && sess.ManagedWorktree {
		registered, registeredErr := client.HasWorktree(ctx, sess.Worktree)
		if registeredErr == nil && !registered {
			if sess.WorkspaceRef == "" {
				return session.Session{}, false, errors.New("managed archive recovery is missing its workspace ref")
			}
			return sess, false, nil
		}
	}

	if sess.Worktree != "" {
		wanted := filepathKey(sess.Worktree)
		for _, workspace := range workspaces {
			if filepathKey(workspace.Path) != wanted {
				continue
			}
			if !workspace.Healthy {
				return session.Session{}, false, fmt.Errorf("worktree %q is unavailable: %s", sess.Worktree, workspace.DisabledReason)
			}
			if sess.WorkspaceRef != "" && sess.WorkspaceRef != workspace.Ref {
				return session.Session{}, false, fmt.Errorf("worktree %q now has ref %q, expected %q", workspace.Path, workspace.Ref, sess.WorkspaceRef)
			}
			migrated := sess.Worktree != workspace.Path || sess.WorkspaceRef == ""
			sess.Worktree = workspace.Path
			sess.WorkspaceRef = workspace.Ref
			return sess, migrated, nil
		}
		return session.Session{}, false, fmt.Errorf("worktree %q is not registered", sess.Worktree)
	}

	if sess.WorkspaceRef != "" {
		matches := make([]git.Workspace, 0, 1)
		for _, workspace := range healthy {
			if workspace.Ref == sess.WorkspaceRef {
				matches = append(matches, workspace)
			}
		}
		if len(matches) != 1 {
			return session.Session{}, false, fmt.Errorf("workspace ref %q resolves to %d healthy worktrees; exact routing is unavailable", sess.WorkspaceRef, len(matches))
		}
		sess.Worktree = matches[0].Path
		return sess, true, nil
	}

	if len(healthy) != 1 {
		return session.Session{}, false, fmt.Errorf("legacy session has no workspace identity and repository has %d healthy worktrees", len(healthy))
	}
	sess.Worktree = healthy[0].Path
	sess.WorkspaceRef = healthy[0].Ref
	return sess, true, nil
}
