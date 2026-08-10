package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/settings"
)

// ProjectName returns the base name of the project root.
func (o *Orchestrator) ProjectName() string {
	return filepath.Base(o.baseDir)
}

// WorkDirDisplay returns the working directory path (worktree when active).
func (o *Orchestrator) WorkDirDisplay() string {
	return o.workspaceRoute().dir
}

// WorkspaceSnapshot is an immutable identity for one published workspace
// route. Its fields stay opaque so frontends cannot manufacture paths; they
// may retain it while doing background work and later ask whether it is still
// current.
type WorkspaceSnapshot struct {
	dir      string
	revision uint64
}

// SnapshotWorkspace captures the active directory and its monotonic routing
// revision without holding the workspace lock during subsequent I/O.
func (o *Orchestrator) SnapshotWorkspace() WorkspaceSnapshot {
	route := o.workspaceRoute()
	return WorkspaceSnapshot{dir: route.dir, revision: route.revision}
}

// Valid reports whether the snapshot identifies a published workspace.
func (s WorkspaceSnapshot) Valid() bool { return s.dir != "" && s.revision != 0 }

// WorkDir returns the immutable directory captured by the snapshot.
func (s WorkspaceSnapshot) WorkDir() string { return s.dir }

// WorkspaceIsCurrent reports whether no session/worktree route change has
// happened since the snapshot was taken.
func (o *Orchestrator) WorkspaceIsCurrent(s WorkspaceSnapshot) bool {
	if !s.Valid() {
		return false
	}
	current := o.workspaceRoute()
	return current.revision == s.revision && current.dir == s.dir
}

// BranchDisplay returns the current git branch, or "—" outside a repo.
// It is deliberately I/O-free: Bubble Tea calls it from View(), where even a
// short git subprocess produces visible scroll stalls.
func (o *Orchestrator) BranchDisplay() string {
	o.branchMu.Lock()
	defer o.branchMu.Unlock()
	if o.branch == "" {
		return "—"
	}
	return o.branch
}

// UpdateBranchDisplay refreshes the cached branch. Frontends must invoke it
// from a worker command rather than their synchronous render path.
func (o *Orchestrator) UpdateBranchDisplay(ctx context.Context) string {
	workspace := o.SnapshotWorkspace()
	branch, err := git.New(workspace.WorkDir()).CurrentBranch(ctx)
	if err != nil {
		branch = "—"
	}
	o.branchMu.Lock()
	o.branch = branch
	o.branchMu.Unlock()
	return branch
}

// RefreshBranchDisplay invalidates the short-lived branch cache after an
// operation that may change the active worktree or branch.
func (o *Orchestrator) RefreshBranchDisplay() {
	o.branchMu.Lock()
	o.branch = ""
	o.branchMu.Unlock()
}

// ActiveModel returns the exact model the orchestrator role will execute.
// This includes a persisted task route restored at startup; a legacy "auto"
// route intentionally reports an empty model because the vendor chooses it.
func (o *Orchestrator) ActiveModel() string {
	return o.effectiveRoleRoute(string(agentcore.RoleOrchestrator)).Model
}

// ActiveReasoningEffort returns the effective orchestrator route's selected
// effort. Automatic selection is explicit even though it is omitted on disk
// and on provider requests.
func (o *Orchestrator) ActiveReasoningEffort() string {
	return defaultReasoningEffort(o.effectiveRoleRoute(string(agentcore.RoleOrchestrator)).ReasoningEffort)
}

// TaskRoute returns the exact route a role will execute, including inherited
// maestrorc model/sampling. It is read-only and safe for interactive views.
func (o *Orchestrator) TaskRoute(role string) settings.RoleDefaults {
	return o.effectiveRoleRoute(strings.TrimSpace(role))
}

// SetModel sets a process-level native fallback. It is kept for embedders and
// tests; interactive /model uses SetActiveModel so the persisted task route,
// Settings, status bar, and next execution change atomically together.
func (o *Orchestrator) SetModel(id string) {
	o.settingsMu.Lock()
	defer o.settingsMu.Unlock()
	o.model = id
}

// SetActiveModel transactionally pins the orchestrator role to a native
// model. Unlike the process fallback, this is the user-facing /model action.
func (o *Orchestrator) SetActiveModel(ctx context.Context, id string) error {
	return o.SetTaskModel(ctx, string(agentcore.RoleOrchestrator), "native", "", strings.TrimSpace(id))
}

// ContextUsage returns (used, total) context tokens for the statusline:
// used is the measured session consumption from Done usage events, total is
// the active model's context window (0 when the model is unknown).
func (o *Orchestrator) ContextUsage() (used, total int) {
	if o.eco != nil {
		o.eco.mu.Lock()
		used = o.eco.sessionTok
		o.eco.mu.Unlock()
	}
	if o.registry != nil {
		if m, ok := o.registry.ModelMetadata(o.ActiveModel()); ok {
			total = m.ContextWindow
		}
	}
	return used, total
}

// ModelCheckError preflights the configured model so the TUI can warn at
// startup instead of failing mid-turn. Discoverable providers (ollama, …)
// are exempt — their model lists are fetched live. The message points at
// the providers the user can actually use.
func (o *Orchestrator) ModelCheckError() error {
	route := o.effectiveRoleRoute(string(agentcore.RoleOrchestrator))
	if o.registry == nil || route.Engine != "native" || route.Model == "" {
		return nil
	}
	err := o.registry.CheckModel(route.Model)
	if err == nil {
		return nil
	}
	msg := err.Error()
	if suggestion := o.suggestModel(); suggestion != "" {
		msg = fmt.Sprintf("%s — closest match: %q", msg, suggestion)
	}
	if avail := o.availableProviders(); avail != "" {
		msg = msg + " — available: " + avail
	}
	return errors.New(msg)
}

// availableProviders returns a human list of providers with usable keys
// (or local providers), e.g. "opencode (api key)".
func (o *Orchestrator) availableProviders() string {
	var parts []string
	for _, p := range o.ProviderList(context.Background()) {
		if !p.RequiresKey || p.KeySet {
			auth := "api key"
			if !p.RequiresKey {
				auth = "local"
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", p.Name, auth))
		}
	}
	return strings.Join(parts, ", ")
}

// suggestModel finds the catalog model with the longest common prefix with
// the configured (unknown) model ID, to steer the user to the right name.
func (o *Orchestrator) suggestModel() string {
	want := o.ActiveModel()
	if i := strings.LastIndexByte(want, '/'); i >= 0 {
		want = want[i+1:]
	}
	best, bestScore := "", 0
	for _, m := range o.Models() {
		cand := m
		if i := strings.LastIndexByte(cand, '/'); i >= 0 {
			cand = cand[i+1:]
		}
		score := 0
		for score < len(want) && score < len(cand) && want[score] == cand[score] {
			score++
		}
		if score > bestScore {
			best, bestScore = m, score
		}
	}
	if bestScore >= 3 {
		return best
	}
	return ""
}

// SetRunner injects a runner (tests, harnesses).
func (o *Orchestrator) SetRunner(r Runner) {
	o.runner = r
}

// SetGate replaces the tool approval gate (the TUI installs its queue).
func (o *Orchestrator) SetGate(g agentcore.Gate) {
	o.gate = o.permissionGate(g)
}

// SetAsk installs the interactive ask handler for the ask tool (the TUI
// installs its channel-backed queue).
func (o *Orchestrator) SetAsk(fn agentcore.AskFunc) {
	o.ask = fn
}

// SetDevTools replaces the dev tool set (the TUI installs the staging
// write tool).
func (o *Orchestrator) SetDevTools(tools []agentcore.Tool) {
	o.devTools = tools
}

// Models lists candidate model IDs for the model picker: provider models
// plus explicit model-add entries.
func (o *Orchestrator) Models() []string {
	if o.registry == nil {
		return nil
	}
	return o.registry.ModelIDs()
}

// SpecPath returns the path of one of the active spec's files.
func (o *Orchestrator) SpecPath(file string) string {
	if o.spec == nil {
		return file
	}
	return o.workspaceRoute().store.PathFor(o.spec.ID, file)
}

// ModifiedFiles returns per-file add/remove stats for the session's working
// tree (tracked changes vs HEAD plus untracked files counted by line).
func (o *Orchestrator) ModifiedFiles(ctx context.Context) []git.NumStat {
	return o.ModifiedFilesFor(ctx, o.SnapshotWorkspace())
}

// ModifiedFilesFor computes file stats using a client local to the immutable
// snapshot. It never reads o.git/o.dir after releasing the routing lock.
func (o *Orchestrator) ModifiedFilesFor(ctx context.Context, workspace WorkspaceSnapshot) []git.NumStat {
	if !workspace.Valid() || ctx.Err() != nil {
		return nil
	}
	client := git.New(workspace.WorkDir())
	stats, err := client.DiffNumStat(ctx, "HEAD")
	if err != nil {
		return nil
	}
	untracked, err := client.UntrackedFiles(ctx)
	if err == nil {
		for _, path := range untracked {
			if ctx.Err() != nil {
				return nil
			}
			lines := 0
			if data, err := os.ReadFile(filepath.Join(workspace.WorkDir(), path)); err == nil {
				lines = strings.Count(string(data), "\n")
			}
			stats = append(stats, git.NumStat{Path: path, Additions: lines, Untracked: true})
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Path < stats[j].Path })
	return stats
}

// SessionList returns the saved session IDs for this project.
func (o *Orchestrator) SessionList() []string {
	summaries, err := o.sessions.ListSummaries(context.Background(), o.sess.Project)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	return ids
}
