package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// BranchChoice describes the workspace strategy used by /accept. User-facing
// commands default to an automatically named managed worktree; stay and branch
// remain available to compatibility callers and focused tests.
type BranchChoice struct {
	Kind string // stay | branch | worktree
	Name string // branch name, auto-selected for a managed worktree when empty
}

// BranchMenu describes the choices offered after a spec is accepted.
type BranchMenu struct {
	SpecID    string
	Category  string
	Suggested string // e.g. "feat-api-go-postgres"
}

// Accept materializes the proposal into specs/<id>/{spec,design,tasks}.md
// and applies the branch choice.
func (o *Orchestrator) Accept(ctx context.Context, choice BranchChoice) (*spec.Spec, error) {
	workspace := o.workspaceRoute()
	if o.sess.Phase != session.PhasePropose {
		return nil, errors.New("accept: no proposal in flight (run /propose first)")
	}
	draft := o.sess.Draft
	if draft == nil {
		return nil, errors.New("accept: proposal is missing")
	}
	readiness := draft.ValidateReadiness()
	if !readiness.Ready() {
		return nil, fmt.Errorf("accept: spec is not ready: %s", readiness.Error())
	}
	for _, warning := range readiness.Warnings() {
		fmt.Fprintf(o.out, "warning [%s] %s\n", warning.Code, warning.Message)
	}
	if choice.Kind != "stay" && choice.Kind != "branch" && choice.Kind != "worktree" {
		return nil, fmt.Errorf("accept: unknown branch choice %q", choice.Kind)
	}
	if choice.Kind == "branch" && choice.Name == "" {
		choice.Name = prefixFor(draft.Category) + draft.ID
	}
	if _, err := os.Stat(workspace.store.Path(draft.ID)); err == nil {
		return nil, fmt.Errorf("accept: spec %q already exists", draft.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("accept: inspect spec destination: %w", err)
	}
	repositorySetup, err := o.ensureAcceptRepository(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("accept: prepare Git repository: %w", err)
	}
	if repositorySetup.Initialized {
		fmt.Fprintf(o.out, "Initialized Git repository in %s\n", o.baseDir)
	}
	if repositorySetup.BaselineCommit != "" {
		fmt.Fprintf(o.out, "Created baseline commit %s\n", repositorySetup.BaselineCommit)
	}
	if repositorySetup.ExcludedPrivate > 0 {
		fmt.Fprintf(o.out, "Kept %d private or sensitive path(s) outside the baseline commit\n", repositorySetup.ExcludedPrivate)
	}

	// Staying in the checkout (or merely switching branches) carries every
	// pre-existing index/worktree change into Maestro's eventual archive
	// commit. Refuse that ambiguous ownership boundary; a dedicated worktree
	// starts from HEAD and safely leaves the user's dirty checkout untouched.
	if choice.Kind != "worktree" {
		status, err := workspace.git.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("accept: inspect working tree: %w", err)
		}
		if status.Dirty {
			return nil, errors.New("accept: the explicit in-place branch has pre-existing changes; rerun plain /accept for an automatic managed worktree")
		}
	}

	targetStore := workspace.store
	targetGit := workspace.git
	targetDir := workspace.dir
	originalBranch := ""
	createdBranchOID := ""
	var trio *spec.TrioMaterialization
	targetIdentity := gitWorkspaceIdentity{}
	var identityErr error
	switch choice.Kind {
	case "stay":
		targetIdentity, identityErr = readGitWorkspaceIdentity(ctx, targetDir)
		if identityErr != nil {
			return nil, fmt.Errorf("accept: capture workspace identity: %w", identityErr)
		}
	case "branch":
		branch, err := workspace.git.CurrentBranch(ctx)
		if err != nil {
			return nil, fmt.Errorf("accept: determine base branch: %w", err)
		}
		if branch == "" {
			return nil, errors.New("accept: detached HEAD has no base branch; switch to a branch first")
		}
		originalBranch = branch
		if err := workspace.git.Branch(ctx, choice.Name); err != nil {
			return nil, err
		}
		createdBranchOID, err = workspace.git.BranchOID(ctx, choice.Name)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("accept: capture created branch OID: %w", err), rollbackAcceptMaterialization(acceptRollbackState{
				workspace: workspace, targetGit: targetGit, targetStore: targetStore,
				choice: choice, originalBranch: originalBranch, targetDir: targetDir,
			}))
		}
		targetIdentity, identityErr = readGitWorkspaceIdentity(ctx, targetDir)
		if identityErr != nil {
			return nil, errors.Join(fmt.Errorf("accept: capture branch identity: %w", identityErr), rollbackAcceptMaterialization(acceptRollbackState{
				workspace: workspace, targetGit: targetGit, targetStore: targetStore,
				choice: choice, originalBranch: originalBranch, targetDir: targetDir, branchOID: createdBranchOID,
			}))
		}
	case "worktree":
		branch, err := workspace.git.CurrentBranch(ctx)
		if err != nil {
			return nil, fmt.Errorf("accept: determine base branch: %w", err)
		}
		if branch == "" {
			return nil, errors.New("accept: detached HEAD has no base branch; switch to a branch first")
		}
		originalBranch = branch
		path := ""
		if choice.Name == "" {
			choice, path, createdBranchOID, err = o.createManagedAcceptWorktree(ctx, workspace, draft)
			if err != nil {
				return nil, err
			}
		} else {
			// Explicitly named worktrees retain their historical sibling path for
			// compatibility. Plain /accept always takes the managed path above.
			path = filepath.Join(filepath.Dir(o.baseDir), choice.Name)
			if err := workspace.git.WorktreeAdd(ctx, path, choice.Name); err != nil {
				return nil, err
			}
		}
		targetDir = path
		targetGit = git.New(path)
		targetStore = spec.NewStore(filepath.Join(path, "specs"))
		if createdBranchOID == "" {
			createdBranchOID, err = workspace.git.BranchOID(ctx, choice.Name)
			if err != nil {
				return nil, errors.Join(fmt.Errorf("accept: capture created worktree branch OID: %w", err), rollbackAcceptMaterialization(acceptRollbackState{
					workspace: workspace, targetGit: targetGit, targetStore: targetStore,
					choice: choice, originalBranch: originalBranch, targetDir: targetDir,
				}))
			}
		}
		targetIdentity, identityErr = readGitWorkspaceIdentity(ctx, targetDir)
		if identityErr != nil {
			return nil, errors.Join(fmt.Errorf("accept: capture worktree identity: %w", identityErr), rollbackAcceptMaterialization(acceptRollbackState{
				workspace: workspace, targetGit: targetGit, targetStore: targetStore,
				choice: choice, originalBranch: originalBranch, targetDir: targetDir, branchOID: createdBranchOID,
			}))
		}
	}
	design := o.sess.DraftDesign
	if strings.TrimSpace(design) == "" {
		design = designTemplate(draft)
	}
	tasks := o.sess.DraftTasks
	if strings.TrimSpace(tasks) == "" {
		tasks = tasksTemplate(draft)
	}
	trio, err = targetStore.WriteTrioTracked(ctx, draft, design, tasks)
	rollback := acceptRollbackState{
		workspace: workspace, targetGit: targetGit, targetStore: targetStore, trio: trio,
		choice: choice, originalBranch: originalBranch, targetDir: targetDir, branchOID: createdBranchOID,
	}
	if err != nil {
		return nil, errors.Join(err, rollbackAcceptMaterialization(rollback))
	}
	contract, err := captureSpecContract(targetStore, draft.ID)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("accept: capture accepted-trio contract: %w", err), rollbackAcceptMaterialization(rollback))
	}
	if err := phases.Transition(o.sess.Phase, session.PhaseSpec); err != nil {
		return nil, errors.Join(fmt.Errorf("accept: %w", err), rollbackAcceptMaterialization(rollback))
	}

	// Publish the accepted lifecycle as one durable session record. The
	// contract and PhaseSpec become visible together; a failed save removes the
	// newly materialized trio and branch/worktree instead of leaving an
	// unprotected spec that a later build could bless.
	candidate := o.sess
	candidate.Phase = session.PhaseSpec
	candidate.SpecID = draft.ID
	candidate.Draft = nil
	candidate.DraftPrompt = ""
	candidate.DraftDesign = ""
	candidate.DraftTasks = ""
	candidate.Review = nil
	candidate.SpecContract = contract
	candidate.WorkspaceRef = targetIdentity.ref
	candidate.ManagedWorktree = false
	if choice.Kind == "stay" {
		candidate.Branch = ""
		candidate.BaseBranch = ""
		candidate.Worktree = persistentWorkspacePath(o.baseDir, targetDir)
	} else {
		candidate.Branch = choice.Name
		candidate.BaseBranch = originalBranch
		if choice.Kind == "worktree" {
			candidate.Worktree = targetDir
			candidate.ManagedWorktree = true
		} else {
			candidate.Worktree = persistentWorkspacePath(o.baseDir, targetDir)
		}
	}
	persistedCandidate, err := o.sessions.Commit(context.Background(), candidate)
	if err != nil {
		persistErr := fmt.Errorf("accept: persist accepted spec contract: %w", err)
		return nil, errors.Join(persistErr, rollbackAcceptMaterialization(rollback))
	}
	candidate = persistedCandidate
	if err := o.sessions.SetActive(context.Background(), candidate.Project, candidate.ID); err != nil {
		persistErr := fmt.Errorf("accept: persist active session: %w", err)
		restore := o.sess
		restore.Revision = candidate.Revision
		_, restoreSessionErr := o.sessions.Commit(context.Background(), restore)
		return nil, errors.Join(persistErr, restoreSessionErr, rollbackAcceptMaterialization(rollback))
	}

	o.sess = candidate
	o.installWorkspace(targetDir, targetGit, targetStore)
	o.spec = draft
	_ = o.refreshGuardrails()
	// Persistent workspace transitions rebind SkillMgr as well as MCP. The
	// temporary isolated runner deliberately uses installWorkspace alone.
	o.newEcosystem()
	if choice.Kind == "branch" {
		fmt.Fprintf(o.out, "Switched to branch %s\n", choice.Name)
	}
	if choice.Kind == "worktree" {
		fmt.Fprintf(o.out, "Worktree %s on branch %s\n", targetDir, choice.Name)
	}
	o.emitPhase(session.PhasePropose, session.PhaseSpec)
	fmt.Fprintf(o.out, "Spec %q created. Run /build to start implementation.\n", draft.ID)
	return draft, nil
}

func persistentWorkspacePath(baseDir, targetDir string) string {
	_ = baseDir
	if target, err := canonicalProjectDir(targetDir); err == nil {
		return target
	}
	return targetDir
}

type acceptRollbackState struct {
	workspace      workspaceRoute
	targetGit      *git.Client
	targetStore    *spec.Store
	trio           *spec.TrioMaterialization
	choice         BranchChoice
	originalBranch string
	targetDir      string
	branchOID      string
}

func rollbackAcceptMaterialization(state acceptRollbackState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if state.trio != nil {
		if err := state.targetStore.RollbackTrio(ctx, state.trio); err != nil {
			// Do not proceed to branch/worktree cleanup: the changed materialization
			// belongs to a concurrent actor and must remain reachable in place.
			return fmt.Errorf("accept rollback: preserve changed spec trio: %w", err)
		}
	}
	if state.choice.Kind == "stay" {
		return nil
	}
	if state.branchOID == "" {
		return fmt.Errorf("accept rollback: preserve %s %q because its creation OID is unavailable", state.choice.Kind, state.choice.Name)
	}
	if state.targetGit == nil {
		return fmt.Errorf("accept rollback: preserve %s %q because its Git client is unavailable", state.choice.Kind, state.choice.Name)
	}
	status, err := state.targetGit.Status(ctx)
	if err != nil {
		return fmt.Errorf("accept rollback: inspect %s %q: %w", state.choice.Kind, state.choice.Name, err)
	}
	if status.Dirty {
		return fmt.Errorf("accept rollback: preserve dirty %s %q; concurrent work remains", state.choice.Kind, state.choice.Name)
	}
	identity, err := readGitWorkspaceIdentity(ctx, state.targetDir)
	if err != nil {
		return fmt.Errorf("accept rollback: preserve %s %q: verify identity: %w", state.choice.Kind, state.choice.Name, err)
	}
	expectedRef := "refs/heads/" + state.choice.Name
	if identity.ref != expectedRef || identity.head != state.branchOID {
		return fmt.Errorf("accept rollback: preserve concurrently changed %s %q (ref=%q HEAD=%s, expected ref=%q HEAD=%s)", state.choice.Kind, state.choice.Name, identity.ref, identity.head, expectedRef, state.branchOID)
	}

	switch state.choice.Kind {
	case "branch":
		if state.originalBranch == "" {
			return fmt.Errorf("accept rollback: preserve branch %q because its original branch is unknown", state.choice.Name)
		}
		if err := state.workspace.git.Switch(ctx, state.originalBranch); err != nil {
			return fmt.Errorf("accept rollback: restore branch %s: %w", state.originalBranch, err)
		}
		if err := state.workspace.git.DeleteBranchIfOID(ctx, state.choice.Name, state.branchOID); err != nil {
			return fmt.Errorf("accept rollback: CAS delete branch %s: %w", state.choice.Name, err)
		}
	case "worktree":
		// WorktreeRemove intentionally has no --force. A file appearing after
		// the clean check makes Git refuse removal and remains untouched.
		if err := state.workspace.git.WorktreeRemove(ctx, state.targetDir); err != nil {
			return fmt.Errorf("accept rollback: preserve worktree %s: %w", state.targetDir, err)
		}
		if err := state.workspace.git.DeleteBranchIfOID(ctx, state.choice.Name, state.branchOID); err != nil {
			return fmt.Errorf("accept rollback: CAS delete branch %s: %w", state.choice.Name, err)
		}
	}
	return nil
}

// DraftReadiness exposes the domain validation report to CLI and interactive
// frontends without duplicating contract rules in presentation packages.
func (o *Orchestrator) DraftReadiness() spec.ReadinessReport {
	if o.sess.Draft == nil {
		return spec.ReadinessReport{Diagnostics: []spec.Diagnostic{{
			Code: "SPEC_DRAFT_MISSING", Severity: spec.SeverityError, Path: "draft", Message: "no proposal in flight",
		}}}
	}
	return o.sess.Draft.ValidateReadiness()
}

// ValidateDraft prints proposal readiness in a frontend-neutral format.
func (o *Orchestrator) ValidateDraft(ctx context.Context) error {
	_ = ctx
	report := o.DraftReadiness()
	if len(report.Diagnostics) == 0 {
		fmt.Fprintln(o.out, "Spec ready — no readiness findings.")
		return nil
	}
	for _, diagnostic := range report.Diagnostics {
		path := ""
		if diagnostic.Path != "" {
			path = " · " + diagnostic.Path
		}
		fmt.Fprintf(o.out, "%s [%s]%s %s\n", strings.ToUpper(string(diagnostic.Severity)), diagnostic.Code, path, diagnostic.Message)
	}
	if !report.Ready() {
		return errors.New("validate: proposal is not ready")
	}
	fmt.Fprintln(o.out, "Spec ready with warnings.")
	return nil
}

// Menu describes the accept branch options for a picker.
func (o *Orchestrator) Menu() (BranchMenu, error) {
	if o.sess.Draft == nil {
		return BranchMenu{}, errors.New("no proposal in flight")
	}
	return BranchMenu{
		SpecID:    o.sess.Draft.ID,
		Category:  o.sess.Draft.Category,
		Suggested: prefixFor(o.sess.Draft.Category) + o.sess.Draft.ID,
	}, nil
}

func designTemplate(d *spec.Spec) string {
	return fmt.Sprintf(`# Design — %s

**Spec:** %s — **Status:** proposal

> Generated by /accept. Refined during the build/review rounds.

## Architecture

(TBD — filled by the dev round)

## Data models

(TBD)

## API contracts

(TBD)
`, d.Title, d.ID)
}

func tasksTemplate(d *spec.Spec) string {
	return fmt.Sprintf(`# Tasks — %s

**Spec:** %s

> Ordered task breakdown for the dev sub-agent. Mark [x] as you complete.

- [ ] T1 Implement the goal: %s
- [ ] T2 Tests for T1
`, d.Title, d.ID, d.GoalLine())
}
