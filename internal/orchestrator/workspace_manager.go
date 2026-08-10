package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// WorkspaceList returns the exact Git registry view used by selection.
func (o *Orchestrator) WorkspaceList(ctx context.Context) ([]git.Workspace, error) {
	return o.workspaceRoute().git.ListWorkspaces(ctx)
}

// SelectWorkspace routes a fresh chat session to an existing registered,
// healthy worktree from the same common Git directory.
func (o *Orchestrator) SelectWorkspace(ctx context.Context, path string) (session.Session, error) {
	o.sessionMu.Lock()
	defer o.sessionMu.Unlock()
	if err := o.workspaceChangeAllowed(); err != nil {
		return session.Session{}, err
	}
	workspaces, err := o.workspaceRoute().git.ListWorkspaces(ctx)
	if err != nil {
		return session.Session{}, fmt.Errorf("select workspace: %w", err)
	}
	wanted, err := canonicalProjectDir(path)
	if err != nil {
		return session.Session{}, fmt.Errorf("select workspace: resolve path: %w", err)
	}
	var selected git.Workspace
	found := false
	for _, workspace := range workspaces {
		candidate, candidateErr := canonicalProjectDir(workspace.Path)
		if candidateErr == nil && candidate == wanted {
			selected = workspace
			found = true
			break
		}
	}
	if !found {
		return session.Session{}, fmt.Errorf("select workspace: %q is not a registered worktree", path)
	}
	if !selected.Healthy {
		return session.Session{}, fmt.Errorf("select workspace: %s", selected.DisabledReason)
	}
	return o.activateFreshWorkspaceSession(ctx, selected)
}

// CreateWorkspace creates a persistent user workspace under Maestro's data
// directory and activates a fresh session there. It is intentionally not
// lifecycle-managed: /archive must never delete it.
func (o *Orchestrator) CreateWorkspace(ctx context.Context, branch string) (session.Session, error) {
	o.sessionMu.Lock()
	defer o.sessionMu.Unlock()
	if err := o.workspaceChangeAllowed(); err != nil {
		return session.Session{}, err
	}
	workspace := o.workspaceRoute()
	if err := workspace.git.ValidateBranchName(ctx, branch); err != nil {
		return session.Session{}, fmt.Errorf("create workspace: %w", err)
	}
	startOID, err := workspace.git.HeadOID(ctx)
	if err != nil {
		return session.Session{}, fmt.Errorf("create workspace: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return session.Session{}, fmt.Errorf("create workspace: resolve home: %w", err)
	}
	parent, err := managedWorkspaceParent(home, o.sess.Project)
	if err != nil {
		return session.Session{}, err
	}
	target := filepath.Join(parent, workspaceSlug(branch))
	repository, err := git.RepositoryIdentity(ctx, workspace.dir)
	if err != nil {
		return session.Session{}, fmt.Errorf("create workspace: %w", err)
	}
	if pathContains(repository.CommonDir, target) || pathContains(target, repository.CommonDir) {
		return session.Session{}, errors.New("create workspace: managed path overlaps the Git common directory")
	}
	created, err := workspace.git.CreateLinkedWorktree(ctx, target, branch, startOID)
	if err != nil {
		return session.Session{}, err
	}
	candidate, activateErr := o.activateFreshWorkspaceSession(ctx, created)
	if activateErr == nil {
		return candidate, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cleanupErr := workspace.git.CleanupCreatedWorktree(cleanupCtx, created)
	return session.Session{}, errors.Join(activateErr, cleanupErr)
}

func (o *Orchestrator) workspaceChangeAllowed() error {
	if o.sess.Phase != session.PhaseChat && o.sess.Phase != session.PhasePropose {
		return fmt.Errorf("workspace change requires chat or propose phase, current phase is %q", o.sess.Phase)
	}
	return nil
}

func (o *Orchestrator) activateFreshWorkspaceSession(ctx context.Context, workspace git.Workspace) (session.Session, error) {
	if !workspace.Healthy || workspace.Path == "" || workspace.Ref == "" || workspace.Detached || workspace.Locked || workspace.Prunable {
		return session.Session{}, errors.New("activate workspace: target is not a healthy attached worktree")
	}
	currentIdentity, err := git.RepositoryIdentity(ctx, o.workspaceRoute().dir)
	if err != nil {
		return session.Session{}, fmt.Errorf("activate workspace: inspect current repository: %w", err)
	}
	targetIdentity, err := git.RepositoryIdentity(ctx, workspace.Path)
	if err != nil {
		return session.Session{}, fmt.Errorf("activate workspace: inspect target repository: %w", err)
	}
	if currentIdentity.CommonDir != targetIdentity.CommonDir {
		return session.Session{}, errors.New("activate workspace: target belongs to a different Git repository")
	}
	// Re-query the target immediately before publication so a stale picker row
	// cannot route to a branch that changed hands in another process.
	refreshed, err := o.workspaceRoute().git.ListWorkspaces(ctx)
	if err != nil {
		return session.Session{}, fmt.Errorf("activate workspace: refresh registry: %w", err)
	}
	var exact git.Workspace
	found := false
	for _, item := range refreshed {
		if filepathKey(item.Path) == filepathKey(workspace.Path) {
			exact = item
			found = true
			break
		}
	}
	if !found || !exact.Healthy || exact.Ref != workspace.Ref || exact.Head != workspace.Head {
		return session.Session{}, errors.New("activate workspace: target changed after selection")
	}
	candidate := session.New(o.sess.Project)
	candidate.WorkspaceRef = exact.Ref
	candidate.Worktree = exact.Path
	candidate.ManagedWorktree = false
	committed, err := o.sessions.Commit(ctx, candidate)
	if err != nil {
		return session.Session{}, fmt.Errorf("activate workspace: persist fresh session: %w", err)
	}
	candidate = committed
	if err := o.sessions.SetActive(ctx, candidate.Project, candidate.ID); err != nil {
		deleteErr := o.sessions.Delete(context.Background(), candidate.Project, candidate.ID)
		return session.Session{}, errors.Join(fmt.Errorf("activate workspace: persist active pointer: %w", err), deleteErr)
	}
	targetStore := spec.NewStore(filepath.Join(exact.Path, "specs"))
	o.sess = candidate
	o.spec = nil
	o.installWorkspace(exact.Path, git.New(exact.Path), targetStore)
	o.newFeatureState()
	_ = o.refreshGuardrails()
	o.newEcosystem()
	o.RefreshBranchDisplay()
	return candidate, nil
}

func managedWorkspaceParent(home, project string) (string, error) {
	if filepath.Base(project) != project || strings.ContainsAny(project, "/\\\x00") {
		return "", errors.New("create workspace: unsafe project key")
	}
	home, err := canonicalProjectDir(home)
	if err != nil {
		return "", fmt.Errorf("create workspace: resolve home: %w", err)
	}
	maestroDir := filepath.Join(home, ".maestro")
	if err := ensureDirectoryWithoutSymlink(maestroDir, 0o700, false); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	worktreesDir := filepath.Join(maestroDir, "worktrees")
	if err := ensureDirectoryWithoutSymlink(worktreesDir, 0o700, true); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	projectDir := filepath.Join(worktreesDir, project)
	if err := ensureDirectoryWithoutSymlink(projectDir, 0o700, true); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return projectDir, nil
}

func ensureDirectoryWithoutSymlink(path string, mode os.FileMode, enforceMode bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("managed directory %q is not a real directory", path)
	}
	if enforceMode {
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}

func workspaceSlug(branch string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(branch) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	value := strings.Trim(b.String(), "-")
	if value == "" {
		value = "workspace"
	}
	runes := []rune(value)
	if len(runes) > 64 {
		value = string(runes[:64])
	}
	return value
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
