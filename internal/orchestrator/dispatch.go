package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// Command is one dispatchable action, produced by the REPL, CLI, or TUI.
type Command struct {
	Cmd   string            // propose | accept | edit | cancel | build | fix | review | docs | archive | learn | skills | mcp | rename | resume | git | chat
	Args  []string          // positional arguments
	Flags map[string]string // named options (branch, engine, agent, model, yes, merge)
}

// Dispatch routes a command through the state machine. Unknown commands are
// an error, never a silent no-op.
func (o *Orchestrator) Dispatch(ctx context.Context, cmd Command) error {
	switch cmd.Cmd {
	case "propose":
		prompt := flagAndArgs(cmd, "m", 0)
		recipe := flag(cmd, "recipe")
		if recipe == "" {
			recipe = flag(cmd, "type")
		}
		var err error
		if strings.TrimSpace(prompt) == "" {
			_, err = o.ProposeFromConversation(ctx, spec.Recipe(recipe))
		} else {
			_, err = o.ProposeWithRecipe(ctx, prompt, spec.Recipe(recipe))
		}
		return err
	case "accept":
		_, err := o.Accept(ctx, branchChoice(cmd))
		return err
	case "validate":
		return o.ValidateDraft(ctx)
	case "edit":
		note := flagAndArgs(cmd, "m", 0)
		return o.Edit(ctx, note)
	case "answer":
		if len(cmd.Args) == 0 {
			return errors.New("answer: usage: /answer Q-001 <answer>")
		}
		answer := flagAndArgs(cmd, "m", 1)
		return o.AnswerQuestion(ctx, cmd.Args[0], answer)
	case "cancel":
		return o.Cancel(ctx)
	case "build":
		return o.Build(ctx, BuildOptions{
			Engine:   flag(cmd, "engine"),
			Agent:    flag(cmd, "agent"),
			Model:    flag(cmd, "model"),
			Isolated: flag(cmd, "isolated") == "true",
		})
	case "fix":
		return o.Fix(ctx)
	case "review":
		_, err := o.Review(ctx)
		return err
	case "docs":
		return o.Docs(ctx)
	case "archive":
		return o.Archive(ctx, ArchiveOptions{
			Yes:   flag(cmd, "yes") == "true",
			Merge: flag(cmd, "merge") == "true",
		})
	case "learn":
		return o.dispatchLearn(ctx, cmd)
	case "skills", "skill":
		return o.dispatchSkills(ctx, cmd)
	case "mcp":
		return o.dispatchMCP(ctx, cmd)
	case "rewind":
		id := flag(cmd, "id")
		if id == "" && len(cmd.Args) > 0 {
			id = cmd.Args[0]
		}
		if id == "" {
			return errors.New("rewind: usage: /rewind <checkpoint> [--code] [--conv]")
		}
		return o.Rewind(ctx, id, flag(cmd, "code") == "true", flag(cmd, "conv") == "true")
	case "remember":
		fact := flagAndArgs(cmd, "m", 0)
		if fact == "" {
			return errors.New("remember: usage: /remember <fact>")
		}
		return o.Remember(ctx, fact, nil)
	case "reflect":
		return o.ReflectMemory(ctx)
	case "rules":
		return o.dispatchRules(ctx, cmd)
	case "commit":
		return o.dispatchCommit(ctx, cmd)
	case "provider":
		return o.dispatchProvider(ctx, cmd)
	case "model":
		return o.dispatchModel(ctx, cmd)
	case "auth":
		return o.dispatchAuth(ctx, cmd)
	case "rename":
		title := flagAndArgs(cmd, "title", 0)
		if title == "" {
			return errors.New("rename: usage: /rename <title>")
		}
		if err := o.RenameSession(ctx, title); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "session renamed: %s\n", terminalSafeLine(o.sess.Title))
		return nil
	case "resume":
		id := strings.TrimSpace(flag(cmd, "id"))
		if id == "" && len(cmd.Args) > 0 {
			id = strings.TrimSpace(cmd.Args[0])
		}
		if id != "" {
			if err := o.LoadSession(ctx, id); err != nil {
				return err
			}
			return o.Resume(ctx)
		}
		return o.Resume(ctx)
	case "git":
		return o.dispatchWorkspace(ctx, cmd)
	case "chat":
		msg := flagAndArgs(cmd, "m", 0)
		if msg == "" {
			return nil
		}
		return o.Chat(ctx, msg)
	default:
		return fmt.Errorf("unknown command %q", cmd.Cmd)
	}
}

// dispatchLearn keeps the historical file explainer while exposing the
// private, deterministic Coach as explicit headless subcommands. Exact Coach
// subcommand names are reserved; prefix a same-named file with ./ to explain
// it instead.
func (o *Orchestrator) dispatchLearn(ctx context.Context, cmd Command) error {
	path := strings.TrimSpace(flag(cmd, "path"))
	if path != "" {
		return o.explainPath(ctx, path, flag(cmd, "deep") == "true")
	}
	if len(cmd.Args) == 0 {
		return errors.New("learn: usage: /learn guided|challenge|off|status|next|done|later | /learn <path> [--deep]")
	}

	sub := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch sub {
	case string(CoachModeGuided), string(CoachModeChallenge), string(CoachModeOff):
		state, err := o.SetCoachMode(ctx, CoachMode(sub))
		if err != nil {
			return err
		}
		fmt.Fprintf(o.out, "coach: mode %s\n", state.Mode)
		return nil
	case "status":
		state, err := o.CoachState(ctx)
		if err != nil {
			return err
		}
		printCoachStatus(o.out, state)
		return nil
	case "next":
		lesson, err := o.CoachLesson(ctx)
		if err != nil {
			return err
		}
		if lesson == nil {
			fmt.Fprintln(o.out, "coach: no lesson available for the current phase")
			return nil
		}
		fmt.Fprintf(o.out, "Next (2 min): %s\n", terminalSafeLine(lesson.Action))
		fmt.Fprintf(o.out, "State: coach lesson %s · %s · %s · %s\n",
			terminalSafeLine(lesson.ID), terminalSafeLine(lesson.Title), lesson.Stage, lesson.Phase)
		fmt.Fprintf(o.out, "Why now: %s\n", terminalSafeLine(lesson.WhyNow))
		fmt.Fprintf(o.out, "Done when: %s\n", terminalSafeLine(lesson.DoneWhen))
		fmt.Fprintf(o.out, "Complete: maestro learn done %s\n", terminalSafeLine(lesson.ID))
		return nil
	case "done":
		lessonID := ""
		if len(cmd.Args) > 1 {
			lessonID = strings.TrimSpace(cmd.Args[1])
		}
		if lessonID == "" {
			state, err := o.CoachState(ctx)
			if err != nil {
				return err
			}
			lessonID = state.PendingLessonID
		}
		if lessonID == "" {
			return errors.New("coach: no pending lesson; run /learn next first")
		}
		state, err := o.CompleteCoachLesson(ctx, lessonID)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.out, "coach: completed %s · %d total\n", terminalSafeLine(lessonID), len(state.CompletedLessonIDs))
		return nil
	case "later":
		duration := 24 * time.Hour
		if len(cmd.Args) > 1 {
			parsed, err := time.ParseDuration(cmd.Args[1])
			if err != nil {
				return fmt.Errorf("coach: invalid snooze duration %q: %w", cmd.Args[1], err)
			}
			duration = parsed
		}
		if _, err := o.SnoozeCoach(ctx, duration); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "coach: snoozed for %s\n", duration)
		return nil
	default:
		return o.explainPath(ctx, cmd.Args[0], flag(cmd, "deep") == "true")
	}
}

func (o *Orchestrator) explainPath(ctx context.Context, path string, deep bool) error {
	out, _, err := o.Learn(ctx, path, deep)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.out, "learn: wrote %s\n", terminalSafeLine(out))
	return nil
}

func printCoachStatus(out io.Writer, state CoachState) {
	fmt.Fprintf(out, "coach: mode %s · %d completed lesson(s)\n", state.Mode, len(state.CompletedLessonIDs))
	if state.PendingLessonID != "" {
		fmt.Fprintf(out, "pending: %s · %s\n", terminalSafeLine(state.PendingLessonID), state.PendingStage)
	}
	if !state.SnoozedUntil.IsZero() && time.Now().Before(state.SnoozedUntil) {
		fmt.Fprintf(out, "snoozed until: %s\n", state.SnoozedUntil.UTC().Format(time.RFC3339))
	}
	keys := make([]string, 0, len(state.Progress))
	for skill := range state.Progress {
		keys = append(keys, skill)
	}
	sort.Strings(keys)
	for _, skill := range keys {
		progress := state.Progress[skill]
		fmt.Fprintf(out, "  %s · stage %s · mastery %d%% · %d completion(s)\n",
			terminalSafeLine(skill), progress.Stage, progress.Mastery, progress.ExplicitCompletions)
	}
}

func (o *Orchestrator) dispatchWorkspace(ctx context.Context, cmd Command) error {
	if len(cmd.Args) == 0 {
		return errors.New("git: usage: /git list | /git create <branch> | /git select <path>")
	}
	switch strings.ToLower(cmd.Args[0]) {
	case "list":
		if len(cmd.Args) != 1 {
			return errors.New("git: usage: /git list")
		}
		workspaces, err := o.WorkspaceList(ctx)
		if err != nil {
			return err
		}
		if len(workspaces) == 0 {
			fmt.Fprintln(o.out, "git: no registered workspaces")
			return nil
		}
		for _, workspace := range workspaces {
			marker := " "
			if workspace.Current {
				marker = "*"
			}
			state := "ready"
			switch {
			case !workspace.Healthy:
				state = "unavailable: " + workspace.DisabledReason
			case workspace.Dirty:
				state = "dirty"
			}
			branch := workspace.Branch
			if branch == "" {
				branch = "detached"
			}
			fmt.Fprintf(o.out, "%s %s · %s · %s\n", marker, terminalSafeLine(branch), terminalSafeLine(state), terminalSafeLine(workspace.Path))
		}
		return nil
	case "create":
		if len(cmd.Args) != 2 || strings.TrimSpace(cmd.Args[1]) == "" {
			return errors.New("git: usage: /git create <branch>")
		}
		sess, err := o.CreateWorkspace(ctx, cmd.Args[1])
		if err != nil {
			return err
		}
		printWorkspaceActivated(o.out, sess)
		return nil
	case "select":
		if len(cmd.Args) < 2 {
			return errors.New("git: usage: /git select <path>")
		}
		path := strings.TrimSpace(strings.Join(cmd.Args[1:], " "))
		if path == "" {
			return errors.New("git: usage: /git select <path>")
		}
		sess, err := o.SelectWorkspace(ctx, path)
		if err != nil {
			return err
		}
		printWorkspaceActivated(o.out, sess)
		return nil
	default:
		return errors.New("git: usage: /git list | /git create <branch> | /git select <path>")
	}
}

func printWorkspaceActivated(out io.Writer, sess session.Session) {
	branch := strings.TrimPrefix(sess.WorkspaceRef, "refs/heads/")
	if branch == "" {
		branch = sess.Branch
	}
	fmt.Fprintf(out, "workspace: %s · %s\n", terminalSafeLine(branch), terminalSafeLine(sess.Worktree))
	fmt.Fprintf(out, "session: %s\n", terminalSafeLine(sess.ID))
}

// terminalSafeLine prevents persisted paths and metadata from injecting
// terminal controls into a headless renderer. Newlines and tabs become spaces
// so every record remains exactly one visual line.
func terminalSafeLine(value string) string {
	value = strings.ToValidUTF8(value, "")
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\r', '\n', '\t':
			b.WriteByte(' ')
		default:
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// terminalSafeJSON preserves machine-readable values exactly while escaping
// Unicode format controls that encoding/json otherwise emits literally.
// JSON already escapes C0/C1 controls; representing Cf runes as \u escapes
// prevents bidi/zero-width terminal effects without changing decoded data.
func terminalSafeJSON(data []byte) []byte {
	var out strings.Builder
	out.Grow(len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			out.WriteRune(utf8.RuneError)
			data = data[1:]
			continue
		}
		data = data[size:]
		if !unicode.In(r, unicode.Cf) {
			out.WriteRune(r)
			continue
		}
		if r <= 0xffff {
			fmt.Fprintf(&out, `\u%04x`, r)
			continue
		}
		value := r - 0x10000
		hi := 0xd800 + (value >> 10)
		lo := 0xdc00 + (value & 0x3ff)
		fmt.Fprintf(&out, `\u%04x\u%04x`, hi, lo)
	}
	return []byte(out.String())
}

func flag(cmd Command, name string) string {
	if cmd.Flags == nil {
		return ""
	}
	return cmd.Flags[name]
}

// flagAndArgs assembles free-form command text without letting the syntax
// parser swallow structural positional arguments (notably /answer's question
// ID). A quoted -m value is followed by any remaining positional text.
func flagAndArgs(cmd Command, name string, argsFrom int) string {
	parts := make([]string, 0, 1+len(cmd.Args))
	if value := strings.TrimSpace(flag(cmd, name)); value != "" {
		parts = append(parts, value)
	}
	if argsFrom < 0 {
		argsFrom = 0
	}
	if argsFrom < len(cmd.Args) {
		parts = append(parts, cmd.Args[argsFrom:]...)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// dispatchRules handles /rules import|export.
func (o *Orchestrator) dispatchRules(ctx context.Context, cmd Command) error {
	sub := ""
	if len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	switch sub {
	case "import":
		rules, err := o.RulesImport(ctx)
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			fmt.Fprintln(o.out, "no rules found (.cursor/rules, .clinerules, AGENTS.md, copilot-instructions.md)")
			return nil
		}
		for _, r := range rules {
			fmt.Fprintf(o.out, "  %s (%s)\n", r.Origin, r.Kind)
		}
		fmt.Fprintf(o.out, "%d rule source(s) imported.\n", len(rules))
		return nil
	case "export":
		format := flag(cmd, "format")
		if format == "" {
			format = "AGENTS.md"
		}
		out, err := o.RulesExport(ctx, format)
		if err != nil {
			return err
		}
		fmt.Fprintln(o.out, out)
		return nil
	default:
		return errors.New("rules: usage: /rules import | /rules export [--format mdc|clinerules|AGENTS.md]")
	}
}

// dispatchCommit plans spec-mapped atomic commits (F6) and applies them
// with --yes.
func (o *Orchestrator) dispatchCommit(ctx context.Context, cmd Command) error {
	if o.spec == nil {
		return errors.New("commit: no active spec")
	}
	changes, err := o.git.AllChanges(ctx)
	if err != nil {
		return err
	}
	var changed []string
	for _, c := range changes {
		if c.Type == "D" || strings.HasPrefix(c.Path, "specs/") {
			continue
		}
		changed = append(changed, c.Path)
	}
	if len(changed) == 0 {
		fmt.Fprintln(o.out, "nothing to commit")
		return nil
	}
	sections := make([]git.Section, 0, len(o.spec.Batches))
	for _, b := range o.spec.Batches {
		sections = append(sections, git.Section{ID: b.ID, Files: b.Files})
	}
	deps := git.ImportDeps(o.workDir(), changed)
	plan, err := git.Plan(o.spec.ID, changed, sections, deps)
	if err != nil {
		return err
	}
	for _, c := range plan.Commits {
		fmt.Fprintf(o.out, "  %s  (%d files)\n", c.Message, len(c.Files))
	}
	if flag(cmd, "yes") == "true" {
		return plan.Apply(ctx, o.git)
	}
	fmt.Fprintln(o.out, "Run with --yes to apply.")
	return nil
}

// branchChoice maps a Command's flags to a BranchChoice for /accept. A plain
// /accept is intentionally isolated: users should not have to inspect the
// checkout, choose a branch name, or know Git worktree syntax before accepting
// a proposal. Explicit branch selection remains available for compatibility.
func branchChoice(cmd Command) BranchChoice {
	switch {
	case flag(cmd, "worktree") != "" && flag(cmd, "worktree") != "false":
		name := flag(cmd, "name")
		// The TUI accepts both --flag=value and --flag value. Preserve the
		// latter spelling for the legacy --worktree option even though plain
		// /accept no longer needs the option at all.
		if value := flag(cmd, "worktree"); value != "true" {
			name = value
		}
		return BranchChoice{Kind: "worktree", Name: name}
	case flag(cmd, "branch") != "":
		return BranchChoice{Kind: "branch", Name: flag(cmd, "branch")}
	default:
		return BranchChoice{Kind: "worktree"}
	}
}

// Resume prints the current session state.
func (o *Orchestrator) Resume(ctx context.Context) error {
	fmt.Fprintf(o.out, "Session %s — phase %q", o.sess.ID, o.sess.Phase)
	if o.sess.Title != "" {
		fmt.Fprintf(o.out, " — title %q", terminalSafeLine(o.sess.Title))
	}
	if o.sess.SpecID != "" {
		fmt.Fprintf(o.out, " — spec %s", o.sess.SpecID)
	}
	if o.sess.Worktree != "" {
		fmt.Fprintf(o.out, " — worktree %s", o.sess.Worktree)
	}
	fmt.Fprintln(o.out)
	return nil
}

// LoadSession switches to one explicitly selected saved session. The target
// is fully validated before any live orchestrator state changes, so a stale
// or tampered session cannot redirect tools to an arbitrary directory.
func (o *Orchestrator) LoadSession(ctx context.Context, id string) error {
	o.sessionMu.Lock()
	defer o.sessionMu.Unlock()
	if id == o.sess.ID {
		return nil
	}
	project := o.sess.Project
	target, err := o.sessions.Load(ctx, project, id)
	if err != nil {
		return err
	}
	if target.Project != project || !target.Phase.Valid() {
		return fmt.Errorf("load session %s: invalid identity or phase", id)
	}
	if _, repoErr := git.RepositoryIdentity(ctx, o.baseDir); repoErr == nil {
		resolved, migrated, resolveErr := resolvePersistedSessionWorkspace(ctx, git.New(o.baseDir), target)
		if resolveErr != nil {
			return fmt.Errorf("load session %s: resolve workspace identity: %w", id, resolveErr)
		}
		target = resolved
		if migrated {
			target, err = o.sessions.Commit(ctx, target)
			if err != nil {
				return fmt.Errorf("load session %s: persist workspace identity: %w", id, err)
			}
		}
	} else if target.Worktree != "" || target.WorkspaceRef != "" {
		return fmt.Errorf("load session %s: Git workspace identity cannot be verified", id)
	}

	workDir := o.baseDir
	targetGit := git.New(workDir)
	targetStore := spec.NewStore(o.specsDir)
	if target.Worktree != "" {
		workspaces, err := git.New(o.baseDir).ListWorkspaces(ctx)
		if err != nil {
			return fmt.Errorf("load session %s: validate worktree: %w", id, err)
		}
		var registered *git.Workspace
		for i := range workspaces {
			if filepathKey(workspaces[i].Path) == filepathKey(target.Worktree) {
				registered = &workspaces[i]
				break
			}
		}
		if registered == nil {
			return fmt.Errorf("load session %s: worktree %q is not registered", id, target.Worktree)
		}
		if !registered.Healthy {
			return fmt.Errorf("load session %s: worktree %q is unavailable: %s", id, target.Worktree, registered.DisabledReason)
		}
		canonical, err := canonicalProjectDir(target.Worktree)
		if err != nil {
			return fmt.Errorf("load session %s: resolve worktree: %w", id, err)
		}
		target.Worktree = canonical
		workDir = canonical
		targetGit = git.New(workDir)
		targetStore = spec.NewStore(filepath.Join(workDir, "specs"))
	}
	if target.WorkspaceRef != "" {
		branch, err := targetGit.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("load session %s: inspect workspace ref: %w", id, err)
		}
		if currentRef := "refs/heads/" + branch; currentRef != target.WorkspaceRef {
			return fmt.Errorf("load session %s: workspace ref %q is not checked out in %q", id, target.WorkspaceRef, workDir)
		}
	} else if target.Branch != "" {
		branch, err := targetGit.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("load session %s: inspect branch: %w", id, err)
		}
		if branch != target.Branch {
			return fmt.Errorf("load session %s: branch %q is not checked out in %q", id, target.Branch, workDir)
		}
	}

	needsSpec := target.Phase == session.PhaseSpec || target.Phase == session.PhaseBuild ||
		target.Phase == session.PhaseReview || target.Phase == session.PhaseDocs || target.Phase == session.PhaseArchive
	if needsSpec && target.SpecID == "" {
		return fmt.Errorf("load session %s: phase %q requires a spec", id, target.Phase)
	}
	var targetSpec *spec.Spec
	if target.SpecID != "" {
		loaded, err := targetStore.Load(ctx, target.SpecID)
		if err != nil {
			return fmt.Errorf("load session %s: load spec %s: %w", id, target.SpecID, err)
		}
		if needsSpec && loaded.Status == spec.StatusArchived {
			return fmt.Errorf("load session %s: spec %s is archived", id, target.SpecID)
		}
		targetSpec = loaded
	}
	if err := o.sessions.SetActive(ctx, target.Project, target.ID); err != nil {
		return fmt.Errorf("load session %s: persist active session: %w", id, err)
	}

	from := o.sess.Phase
	o.sess = target
	o.installWorkspace(workDir, targetGit, targetStore)
	o.spec = targetSpec
	o.newFeatureState()
	_ = o.refreshGuardrails()
	o.newEcosystem()
	o.RefreshBranchDisplay()
	if from != target.Phase {
		o.emitPhase(from, target.Phase)
	}
	return o.Resume(ctx)
}

// emitPhase announces a phase change on the stream.
func (o *Orchestrator) emitPhase(from, to session.Phase) {
	o.emit(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvPhaseChange, agentcore.PhaseChange{From: string(from), To: string(to)}))
}
