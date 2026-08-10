package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// ArchiveOptions controls /archive.
type ArchiveOptions struct {
	Yes   bool // skip the confirmation prompt
	Merge bool // merge the branch/worktree back into its recorded base branch
}

// Archive ends every spec: propose a commit, commit, move the spec folder
// to specs/archive/, and optionally merge the branch back.
func (o *Orchestrator) Archive(ctx context.Context, opts ArchiveOptions) error {
	if o.spec == nil {
		return errors.New("archive: no active spec")
	}
	from := o.sess.Phase
	if from != session.PhaseReview && from != session.PhaseDocs {
		return fmt.Errorf("archive: cannot start from phase %q", from)
	}
	if err := rejectUnsupportedArchiveCheckout(ctx, o.workDir()); err != nil {
		return err
	}
	if err := o.requireCurrentReview(ctx, "archive"); err != nil {
		return err
	}
	message := o.commitMessage()
	status, err := o.git.Status(ctx)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if !status.Dirty {
		return errors.New("archive: nothing to commit (working tree clean)")
	}
	for _, file := range status.Files {
		if file.IndexState != ' ' && file.IndexState != '?' {
			return fmt.Errorf("archive: index already contains staged path %q; unstage it before Maestro creates the archive commit", file.Path)
		}
	}
	if opts.Merge {
		if err := o.preflightMerge(ctx); err != nil {
			return err
		}
	}
	fmt.Fprintln(o.out, "Changes to commit:")
	for _, f := range status.Files {
		fmt.Fprintf(o.out, "  %c%c %s\n", f.IndexState, f.Worktree, f.Path)
	}
	fmt.Fprintf(o.out, "Commit message: %q\n", message)
	if !opts.Yes && !o.confirm("Commit and archive? [y/N] ") {
		fmt.Fprintln(o.out, "Archive cancelled.")
		return nil
	}
	// Confirmation can leave the process waiting while an editor changes the
	// checkout or index. Recheck at the last read-only point before changing
	// phase, moving the spec, or staging anything.
	if err := o.requireCurrentReview(ctx, "archive"); err != nil {
		return err
	}
	status, err = o.git.Status(ctx)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if !status.Dirty {
		return errors.New("archive: nothing to commit (working tree clean)")
	}
	for _, file := range status.Files {
		if file.IndexState != ' ' && file.IndexState != '?' {
			return fmt.Errorf("archive: index already contains staged path %q; unstage it before Maestro creates the archive commit", file.Path)
		}
	}
	tx, err := o.prepareArchiveTransaction(ctx, message)
	if err != nil {
		return err
	}
	defer tx.cleanup()
	if err := o.setPhase(session.PhaseArchive); err != nil {
		// setPhase updates the in-memory phase before persisting it. Restore the
		// caller-visible state when persistence itself refuses the transition.
		o.sess.Phase = from
		return err
	}
	o.emitPhase(from, session.PhaseArchive)
	// Archive the spec folder first so the commit records the post-archive
	// tree; otherwise merging the branch back would resurrect specs/<id>.
	if err := o.store.Archive(ctx, o.spec.ID); err != nil {
		phaseErr := o.restoreArchivePhase(from)
		return errors.Join(fmt.Errorf("archive: %w", err), phaseErr)
	}
	if err := o.publishArchiveTransaction(ctx, tx); err != nil {
		if tx.published {
			return errors.Join(err, errors.New("archive: commit publication could not be rolled back; the archived spec and ARCHIVE phase were retained for safe recovery"))
		}
		return errors.Join(err, o.rollbackArchive(ctx, from))
	}
	hash, _ := o.git.CommitHash(ctx)
	fmt.Fprintf(o.out, "Committed %s\n", hash)
	fmt.Fprintf(o.out, "Archived specs/%s → specs/archive/%s\n", o.spec.ID, o.spec.ID)

	branchName := o.sess.Branch
	var mergeErr error
	if opts.Merge {
		// A stdio MCP child inherits the managed worktree as its process cwd.
		// Quiesce it before mergeBranch removes that directory (required on
		// Windows, and prevents deleted-cwd orphans everywhere else).
		closeEcosystemMCP(o.eco)
		mergeErr = o.mergeBranch(ctx, tx.hooksDir)
		if mergeErr != nil {
			fmt.Fprintf(o.out, "Archive committed on %s; automatic merge/cleanup incomplete: %v\n", branchName, mergeErr)
		}
	} else if o.sess.Branch != "" {
		baseBranch := o.sess.BaseBranch
		if baseBranch == "" {
			fmt.Fprintf(o.out, "Branch %s kept; its base branch is unknown. Merge manually after choosing the target: `git switch <base-branch> && git merge %s`.\n", o.sess.Branch, o.sess.Branch)
		} else {
			fmt.Fprintf(o.out, "Branch %s kept. Merge later with `git switch %s && git merge %s`.\n", o.sess.Branch, baseBranch, o.sess.Branch)
		}
		if o.sessionUsesLinkedWorktree() {
			fmt.Fprintf(o.out, "Worktree %s kept; remove it after merging with `git worktree remove %q`.\n", o.sess.Worktree, o.sess.Worktree)
		}
	}

	baseIdentity, identityErr := git.RepositoryIdentity(ctx, o.baseDir)
	if identityErr != nil {
		return fmt.Errorf("archive: identify post-archive workspace: %w", identityErr)
	}
	baseBranch, branchErr := git.New(baseIdentity.Worktree).CurrentBranch(ctx)
	if branchErr != nil {
		return fmt.Errorf("archive: identify post-archive branch: %w", branchErr)
	}
	o.sess.SpecID = ""
	o.sess.WorkspaceRef = "refs/heads/" + baseBranch
	o.sess.Branch = ""
	o.sess.BaseBranch = ""
	o.sess.Worktree = baseIdentity.Worktree
	o.sess.ManagedWorktree = false
	o.spec = nil
	o.sess.Draft = nil
	o.sess.DraftPrompt = ""
	o.sess.DraftDesign = ""
	o.sess.DraftTasks = ""
	o.sess.Review = nil
	o.sess.SpecContract = nil
	if err := o.setPhase(session.PhaseChat); err != nil {
		return err
	}
	o.installWorkspace(o.baseDir, git.New(o.baseDir), spec.NewStore(o.specsDir))
	_ = o.refreshGuardrails()
	o.newEcosystem()
	o.emitPhase(session.PhaseArchive, session.PhaseChat)
	if mergeErr != nil {
		return fmt.Errorf("archive committed on branch %s, but automatic merge/cleanup was incomplete: %w", branchName, mergeErr)
	}
	return nil
}

func (o *Orchestrator) rollbackArchive(ctx context.Context, phase session.Phase) error {
	if err := o.store.RestoreArchive(ctx, o.spec.ID); err != nil {
		return fmt.Errorf("archive: restore active spec after refusal: %w", err)
	}
	return o.restoreArchivePhase(phase)
}

func (o *Orchestrator) restoreArchivePhase(phase session.Phase) error {
	// ARCHIVE has no public backward transition. Internal rollback restores the
	// previously persisted phase directly only after the spec move is undone.
	o.sess.Phase = phase
	if err := o.save(); err != nil {
		return fmt.Errorf("archive: restore phase %q: %w", phase, err)
	}
	o.emitPhase(session.PhaseArchive, phase)
	return nil
}

// commitMessage builds a useful conventional-commit subject within 72 runes.
// Long generated spec IDs must not consume the whole subject and reduce the
// human-readable goal to a couple of words.
func (o *Orchestrator) commitMessage() string {
	goal := strings.Join(strings.Fields(o.spec.GoalLine()), " ")
	prefix := o.spec.Category
	if prefix == "" {
		prefix = "feat"
	}
	scope := truncateCommitPart(o.spec.ID, 24, false)
	head := prefix + ": "
	if scope != "" {
		head = fmt.Sprintf("%s(%s): ", prefix, scope)
	}
	remaining := 72 - len([]rune(head))
	if remaining <= 0 {
		return truncateCommitPart(strings.TrimSuffix(head, ": "), 72, true)
	}
	return head + truncateCommitPart(goal, remaining, true)
}

func truncateCommitPart(value string, limit int, wordBoundary bool) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if limit <= 0 {
		return ""
	}
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	candidate := string(runes[:limit-1])
	if wordBoundary {
		if i := strings.LastIndexAny(candidate, " \t\n"); i > 0 {
			candidate = candidate[:i]
		}
	} else {
		candidate = strings.TrimRight(candidate, "-_. ")
	}
	if candidate == "" {
		candidate = string(runes[:limit-1])
	}
	return candidate + "…"
}

// mergeBranch merges the session branch back into its recorded base and cleans up the
// worktree when one is in use.
func (o *Orchestrator) mergeBranch(ctx context.Context, hooksDir string) error {
	if o.sess.Branch == "" {
		return nil
	}
	baseBranch := o.sess.BaseBranch
	if baseBranch == "" {
		return unknownBaseBranchError(o.sess.Branch)
	}
	if o.sessionUsesLinkedWorktree() {
		main := git.New(o.baseDir)
		current, err := main.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("inspect merge target: %w", err)
		}
		if current != baseBranch {
			if err := switchWithoutHooks(ctx, o.baseDir, hooksDir, baseBranch); err != nil {
				return fmt.Errorf("switch merge target %s: %w", baseBranch, err)
			}
		}
		if err := mergeWithoutHooks(ctx, o.baseDir, hooksDir, o.sess.Branch); err != nil {
			_ = abortMergeWithoutHooks(context.Background(), o.baseDir, hooksDir)
			return fmt.Errorf("merge %s: %w", o.sess.Branch, err)
		}
		fmt.Fprintf(o.out, "Merged %s into %s\n", o.sess.Branch, baseBranch)
		if o.sess.ManagedWorktree {
			if err := main.WorktreeRemove(ctx, o.sess.Worktree); err != nil {
				return fmt.Errorf("remove worktree: %w", err)
			}
			fmt.Fprintf(o.out, "Removed worktree %s\n", o.sess.Worktree)
		} else {
			fmt.Fprintf(o.out, "Persistent workspace %s retained\n", o.sess.Worktree)
		}
		return nil
	}
	if err := switchWithoutHooks(ctx, o.workDir(), hooksDir, baseBranch); err != nil {
		return err
	}
	if err := mergeWithoutHooks(ctx, o.workDir(), hooksDir, o.sess.Branch); err != nil {
		_ = abortMergeWithoutHooks(context.Background(), o.workDir(), hooksDir)
		return err
	}
	fmt.Fprintf(o.out, "Merged %s into %s\n", o.sess.Branch, baseBranch)
	return nil
}

func switchWithoutHooks(ctx context.Context, dir, hooksDir, branch string) error {
	if err := validateArchiveBranch(ctx, dir, branch); err != nil {
		return err
	}
	if _, err := runIsolatedGit(ctx, dir, nil, nil, "-c", "core.hooksPath="+hooksDir, "switch", "--", branch); err != nil {
		return fmt.Errorf("git switch %s: %w", branch, err)
	}
	return nil
}

func mergeWithoutHooks(ctx context.Context, dir, hooksDir, branch string) error {
	if err := validateArchiveBranch(ctx, dir, branch); err != nil {
		return err
	}
	if _, err := runIsolatedGit(ctx, dir, nil, nil, "-c", "core.hooksPath="+hooksDir, "merge", "--", branch); err != nil {
		return fmt.Errorf("git merge %s: %w", branch, err)
	}
	return nil
}

func validateArchiveBranch(ctx context.Context, dir, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("archive: branch name must not be empty")
	}
	if _, err := runIsolatedGit(ctx, dir, nil, nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("archive: invalid persisted branch %q: %w", branch, err)
	}
	return nil
}

func abortMergeWithoutHooks(ctx context.Context, dir, hooksDir string) error {
	if _, err := runIsolatedGit(ctx, dir, nil, nil, "-c", "core.hooksPath="+hooksDir, "merge", "--abort"); err != nil {
		return fmt.Errorf("git merge --abort: %w", err)
	}
	return nil
}

func (o *Orchestrator) preflightMerge(ctx context.Context) error {
	if o.sess.Branch == "" {
		return nil
	}
	if o.sess.BaseBranch == "" {
		return unknownBaseBranchError(o.sess.Branch)
	}
	if !o.sessionUsesLinkedWorktree() {
		current, err := o.git.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("archive: inspect feature branch: %w", err)
		}
		if current != o.sess.Branch {
			return fmt.Errorf("archive: expected feature branch %q, found %q", o.sess.Branch, current)
		}
		return nil
	}
	base := git.New(o.baseDir)
	status, err := base.Status(ctx)
	if err != nil {
		return fmt.Errorf("archive: inspect merge checkout: %w", err)
	}
	if status.Dirty {
		return errors.New("archive: merge checkout has uncommitted changes; commit/stash them or archive without --merge")
	}
	return nil
}

func (o *Orchestrator) sessionUsesLinkedWorktree() bool {
	return o.sess.Worktree != "" && filepathKey(o.sess.Worktree) != filepathKey(o.baseDir)
}

func unknownBaseBranchError(branch string) error {
	return fmt.Errorf("archive: base branch for %q is unknown; automatic merge refused. Archive without --merge, then merge manually after choosing the target branch: `git switch <base-branch> && git merge %s`", branch, branch)
}

// confirm reads a y/N answer from the gate's reader.
func (o *Orchestrator) confirm(prompt string) bool {
	fmt.Fprint(o.out, prompt)
	var line string
	if _, err := fmt.Fscanln(o.in, &line); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
