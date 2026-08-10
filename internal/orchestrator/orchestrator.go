// Package orchestrator is Maestro's conductor: a slim state machine that
// specs, delegates, reviews, and archives. It shares one dispatch surface
// with the REPL, the CLI, and the TUI, and never writes code itself.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/agentcore/tools"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/settings"
	"github.com/bryann2k/maestro/internal/spec"
	"github.com/bryann2k/maestro/internal/vault"
)

// Options wires an Orchestrator. All fields are required except where noted.
type Options struct {
	ProjectDir   string
	SpecsDir     string // default: <ProjectDir>/specs
	SessionsDir  string // default: <home>/.maestro/sessions
	Config       *config.Config
	Keys         agentcore.KeyStore
	Settings     settings.Settings
	Model        string // default model for native runs
	In           io.Reader
	Out          io.Writer
	Gate         agentcore.Gate                       // nil → PromptGate
	DevTools     []agentcore.Tool                     // nil → default tool set for dev
	Runner       Runner                               // nil → native runner built from the registry
	TitleRunner  Runner                               // optional metadata-only runner (tests/custom integrations)
	SettingsPath string                               // settings file for engine persistence
	Catalog      map[string]agentcore.CatalogProvider // nil → models.dev + core fallback
	ModelsDev    *agentcore.ModelsDev                 // remote catalog client
	Vault        *vault.Vault                         // API key store (auth commands)
}

// Orchestrator conducts one session: conversation, spec lifecycle, build,
// review, docs, archive.
type Orchestrator struct {
	baseDir     string // original project root
	dir         string // project or worktree root (worktree wins)
	specsDir    string // spec root in the original project
	store       *spec.Store
	sessions    *session.Store
	git         *git.Client
	cfg         *config.Config
	keys        agentcore.KeyStore
	settings    settings.Settings
	model       string
	registry    *agentcore.Registry
	runner      Runner
	titleRunner Runner
	gate        agentcore.Gate
	ask         agentcore.AskFunc
	devTools    []agentcore.Tool
	in          io.Reader
	out         io.Writer

	settingsPath string
	settingsMu   sync.RWMutex
	eventMu      sync.Mutex
	eventSeq     uint64
	runMu        sync.Mutex
	runCancel    context.CancelFunc
	runActive    bool
	runID        uint64
	sessionMu    sync.Mutex
	workspaceMu  sync.RWMutex
	workspaceRev uint64
	branchMu     sync.Mutex
	branch       string
	guardrails   Guardrails
	features     *featureState
	eco          *Ecosystem
	modelsDev    *agentcore.ModelsDev
	vault        *vault.Vault

	Stream chan agentcore.StreamEvent
	sess   session.Session
	spec   *spec.Spec
}

// New loads (or creates) the latest session and wires the deps.
func New(ctx context.Context, opts Options) (*Orchestrator, error) {
	if opts.ProjectDir == "" {
		return nil, errors.New("orchestrator: ProjectDir is required")
	}
	projectRoot, err := canonicalProjectRoot(ctx, opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve project directory: %w", err)
	}
	opts.ProjectDir = projectRoot
	if opts.SpecsDir == "" {
		opts.SpecsDir = filepath.Join(opts.ProjectDir, "specs")
	}
	if opts.SessionsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("orchestrator: resolve home: %w", err)
		}
		opts.SessionsDir = filepath.Join(home, ".maestro", "sessions")
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	o := &Orchestrator{
		baseDir:      opts.ProjectDir,
		dir:          opts.ProjectDir,
		specsDir:     opts.SpecsDir,
		store:        spec.NewStore(opts.SpecsDir),
		sessions:     session.NewStore(opts.SessionsDir),
		git:          git.NewProject(opts.ProjectDir),
		cfg:          opts.Config,
		keys:         opts.Keys,
		settings:     opts.Settings,
		model:        opts.Model,
		in:           opts.In,
		out:          opts.Out,
		Stream:       make(chan agentcore.StreamEvent, 256),
		runner:       opts.Runner,
		titleRunner:  opts.TitleRunner,
		devTools:     opts.DevTools,
		settingsPath: opts.SettingsPath,
		vault:        opts.Vault,
	}
	if o.settings.RoleDefaults == nil {
		slots := o.settings.ModelSlots
		o.settings = settings.Defaults()
		if len(slots) > 0 {
			o.settings.ModelSlots = slots
		}
	}
	if o.registry == nil && o.cfg != nil {
		catalog := opts.Catalog
		if catalog == nil && opts.ModelsDev != nil {
			loaded, _, err := opts.ModelsDev.Load(ctx)
			if err == nil {
				catalog = loaded
			}
		}
		reg, err := agentcore.NewRegistry(ctx, o.cfg, o.keys, catalog)
		if err != nil && len(reg.Providers()) == 0 {
			return nil, fmt.Errorf("orchestrator: no usable provider: %w", err)
		}
		o.registry = reg
		o.modelsDev = opts.ModelsDev
	}
	if err := o.initializeReasoningSettings(ctx); err != nil {
		return nil, fmt.Errorf("orchestrator: reasoning configuration: %w", err)
	}
	project := projectSessionKey(opts.ProjectDir)
	sessionNeedsCommit := false
	sess, notice, err := o.sessions.Restore(ctx, project, opts.SpecsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Copy the latest record from the former path-keyed namespace on
			// first use. The source remains untouched for rollback and older
			// Maestro binaries; linked worktrees converge on the common key.
			legacyProject := legacyProjectSessionKey(opts.ProjectDir)
			if legacyProject != project {
				if legacy, legacyNotice, legacyErr := o.sessions.Restore(ctx, legacyProject, opts.SpecsDir); legacyErr == nil {
					legacy.Project = project
					// The destination namespace is a distinct durable record. Its
					// revision starts at zero and is committed after workspace identity
					// has also been migrated.
					legacy.Revision = 0
					sess = legacy
					notice = legacyNotice
					sessionNeedsCommit = true
				}
			}
			if sess.ID == "" {
				sess = session.New(project)
				sessionNeedsCommit = true
			}
		} else {
			return nil, fmt.Errorf("orchestrator: restore session: %w", err)
		}
	}
	o.sess = sess
	if repository, repoErr := o.git.RepositoryIdentity(ctx); repoErr == nil {
		if sessionNeedsCommit && o.sess.Worktree == "" && o.sess.WorkspaceRef == "" {
			branch, branchErr := git.New(repository.Worktree).CurrentBranch(ctx)
			if branchErr != nil {
				return nil, fmt.Errorf("orchestrator: identify fresh session workspace: %w", branchErr)
			}
			o.sess.Worktree = repository.Worktree
			o.sess.WorkspaceRef = "refs/heads/" + branch
		} else {
			resolved, migrated, resolveErr := resolvePersistedSessionWorkspace(ctx, o.git, o.sess)
			if resolveErr != nil {
				return nil, fmt.Errorf("orchestrator: resolve saved workspace: %w", resolveErr)
			}
			o.sess = resolved
			sessionNeedsCommit = sessionNeedsCommit || migrated
		}
	} else if o.sess.Worktree != "" || o.sess.WorkspaceRef != "" {
		return nil, errors.New("orchestrator: saved session has Git workspace identity but the project is not a repository")
	}
	if sessionNeedsCommit {
		committed, commitErr := o.sessions.Commit(ctx, o.sess)
		if commitErr != nil {
			return nil, fmt.Errorf("orchestrator: persist session identity: %w", commitErr)
		}
		o.sess = committed
		if activeErr := o.sessions.SetActive(ctx, project, o.sess.ID); activeErr != nil {
			return nil, fmt.Errorf("orchestrator: activate session: %w", activeErr)
		}
	}
	missingArchiveWorktree := ""
	if o.sess.Worktree != "" {
		registered, worktreeErr := o.git.HasWorktree(ctx, o.sess.Worktree)
		if worktreeErr != nil || !registered {
			unsafePath := o.sess.Worktree
			if o.sess.Phase == session.PhaseArchive && o.sess.ManagedWorktree {
				// Archive removes a managed worktree only after publishing and
				// merging its immutable commit. A crash immediately afterwards
				// leaves the durable session pointing at a path that no longer
				// exists. Route recovery through the base checkout so it can prove
				// the reviewed branch history before clearing lifecycle state.
				// If publication did not happen, recovery fails closed instead of
				// treating a missing folder as archive success.
				missingArchiveWorktree = unsafePath
				o.sess.Worktree = ""
			} else if o.sess.Phase == session.PhaseArchive {
				return nil, fmt.Errorf("orchestrator: archived session references missing persistent workspace %q; refusing lifecycle recovery", unsafePath)
			} else {
				return nil, fmt.Errorf("orchestrator: saved session references unregistered worktree %q; select or repair the session explicitly", unsafePath)
			}
		}
	}
	if notice != "" {
		o.emit(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvHITL, agentcore.HITLItem{ID: "resume", Item: notice, Status: "done"}))
	}
	o.dir = o.sess.Worktree
	if o.dir == "" {
		o.dir = opts.ProjectDir
	} else {
		// A worktree owns its own copy of the spec lifecycle. Loading the
		// original repository's store here split review/docs/archive across
		// two different trees.
		o.store = spec.NewStore(filepath.Join(o.dir, "specs"))
	}
	if o.sess.Worktree == "" {
		o.git = git.NewProject(o.dir)
	} else {
		o.git = git.New(o.dir)
	}
	o.installWorkspace(o.dir, o.git, o.store)
	if o.sess.Phase == session.PhaseArchive {
		recoveryNotice, recoveryErr := o.recoverInterruptedArchive(ctx)
		if recoveryErr != nil {
			return nil, recoveryErr
		}
		if recoveryNotice != "" {
			if missingArchiveWorktree != "" {
				recoveryNotice += fmt.Sprintf("; archived worktree %s was already removed", missingArchiveWorktree)
			}
			o.emit(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvHITL, agentcore.HITLItem{ID: "archive-recovery", Item: recoveryNotice, Status: "done"}))
		}
	}
	if o.sess.SpecID != "" {
		if sp, err := o.store.Load(ctx, o.sess.SpecID); err == nil {
			o.spec = sp
		}
	}
	o.newFeatureState()
	_ = o.refreshGuardrails()
	o.newEcosystem()
	if opts.Gate != nil {
		o.gate = o.permissionGate(opts.Gate)
	} else {
		o.gate = o.permissionGate(&PromptGate{In: o.in, Out: o.out})
	}
	return o, nil
}

func canonicalProjectDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// canonicalProjectRoot promotes a path inside a Git checkout to its top-level
// directory. Non-Git directories remain usable for the read-only/settings
// surfaces; Git-backed operations will return their own explicit repository
// error when invoked.
func canonicalProjectRoot(ctx context.Context, path string) (string, error) {
	canonical, err := canonicalProjectDir(path)
	if err != nil {
		return "", err
	}
	if root, err := git.ProjectRoot(ctx, canonical); err == nil {
		return root, nil
	}
	return canonical, nil
}

// projectSessionKey isolates repositories that happen to share the same
// basename while keeping the on-disk directory recognizable to users.
func projectSessionKey(path string) string {
	labelPath := path
	identityPath := path
	if canonical, err := canonicalProjectDir(path); err == nil {
		labelPath = canonical
		identityPath = canonical
	}
	if identity, err := git.NewProject(identityPath).RepositoryIdentity(context.Background()); err == nil {
		identityPath = identity.CommonDir
		if filepath.Base(identity.CommonDir) == ".git" {
			labelPath = filepath.Dir(identity.CommonDir)
		}
	}
	sum := sha256.Sum256([]byte(filepath.Clean(identityPath)))
	return fmt.Sprintf("%s-%x", filepath.Base(labelPath), sum[:6])
}

func legacyProjectSessionKey(path string) string {
	if canonical, err := canonicalProjectDir(path); err == nil {
		path = canonical
	}
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return fmt.Sprintf("%s-%x", filepath.Base(path), sum[:6])
}

// Session returns the current session state.
func (o *Orchestrator) Session() session.Session { return o.sess }

// SettingsSnapshot returns a copy of the current user settings for interactive
// frontends. Maps are cloned so a picker cannot mutate orchestrator state
// without going through UpdateSettings.
func (o *Orchestrator) SettingsSnapshot() settings.Settings {
	o.settingsMu.RLock()
	defer o.settingsMu.RUnlock()
	out := o.settings
	out.RoleDefaults = map[string]settings.RoleDefaults{}
	for role, defaults := range o.settings.RoleDefaults {
		out.RoleDefaults[role] = defaults
	}
	out.ModelSlots = map[string]string{}
	for slot, model := range o.settings.ModelSlots {
		out.ModelSlots[slot] = model
	}
	return out
}

// UpdateSettings applies and persists user settings for interactive frontends.
func (o *Orchestrator) UpdateSettings(ctx context.Context, next settings.Settings) error {
	// Route/settings mutations are rejected while a run is active. This makes
	// the route captured by a provider turn immutable and avoids UI/runtime
	// splits where the status bar changes before the in-flight runner does.
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.runActive {
		return errors.New("settings cannot change while an agent run is active")
	}
	o.settingsMu.Lock()
	defer o.settingsMu.Unlock()
	if err := next.Valid(); err != nil {
		return err
	}
	for role, route := range next.RoleDefaults {
		route.ReasoningEffort = agentcore.NormalizeReasoningEffort(route.ReasoningEffort)
		if route.ReasoningEffort != "" {
			route.ReasoningSet = true
		}
		engine := normalizeEngineName(route.Engine)
		if engine == "" {
			engine = "native"
		}
		validationModel := route.Model
		if engine == "native" && validationModel == "" {
			validationModel = o.model
			if validationModel == "" {
				if configured, _, ok := o.configRoleSelection(role); ok {
					validationModel = configured
				} else {
					validationModel = next.ModelSlots["large"]
				}
			}
		}
		if !containsReasoningEffort(o.ReasoningEfforts(engine, route.Agent, validationModel), route.ReasoningEffort) {
			previous := o.settings.RoleDefaults[role]
			changedRoute := previous.Engine != route.Engine || previous.Agent != route.Agent || previous.Model != route.Model
			if !changedRoute {
				return fmt.Errorf("role %s: reasoning effort %q is unsupported by the selected route", role, route.ReasoningEffort)
			}
			route.ReasoningEffort = ""
			route.ReasoningSet = false
		}
		next.RoleDefaults[role] = route
	}
	if err := next.Valid(); err != nil {
		return err
	}
	// Persist before publication. If Save fails, both the active runtime and
	// Settings UI continue to observe the previous committed snapshot.
	if o.settingsPath != "" {
		if err := next.Save(ctx, o.settingsPath); err != nil {
			return err
		}
	}
	o.settings = next
	return nil
}

// RefreshModels forces an online catalog refresh and updates the registry's
// catalog view used by the model picker.
func (o *Orchestrator) RefreshModels(ctx context.Context) error {
	if o.modelsDev == nil || o.registry == nil {
		return errors.New("model catalog refresh unavailable")
	}
	catalog, err := o.modelsDev.Refresh(ctx)
	if err != nil {
		return err
	}
	for name, provider := range catalog {
		o.registry.Catalog()[name] = provider
	}
	return nil
}

// Out returns the orchestrator's output writer.
func (o *Orchestrator) Out() io.Writer { return o.out }

// ActiveSpec returns the spec being worked on, if any.
func (o *Orchestrator) ActiveSpec() *spec.Spec { return o.spec }

// Phase returns the current phase.
func (o *Orchestrator) Phase() session.Phase { return o.sess.Phase }

// emit forwards an event to the shared stream.
func (o *Orchestrator) emit(ev agentcore.StreamEvent) {
	// The orchestrator is the stream boundary and therefore owns the one
	// canonical sequence, regardless of provider- or child-local counters.
	// Backpressure is intentional: losing Done, permission, or tool events is
	// worse than slowing a producer until the frontend catches up.
	o.eventMu.Lock()
	o.eventSeq++
	ev.Seq = o.eventSeq
	o.Stream <- ev
	o.eventMu.Unlock()
	o.trackSession(ev)
}

// setPhase transitions the state machine and persists the session.
func (o *Orchestrator) setPhase(to session.Phase) error {
	if err := phases.Transition(o.sess.Phase, to); err != nil {
		return err
	}
	o.sess.Phase = to
	return o.save()
}

// save persists the session.
func (o *Orchestrator) save() error {
	committed, err := o.sessions.Commit(context.Background(), o.sess)
	if err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	o.sess = committed
	if err := o.sessions.SetActive(context.Background(), o.sess.Project, o.sess.ID); err != nil {
		return fmt.Errorf("persist active session: %w", err)
	}
	return nil
}

// workDir returns the directory commands operate in (worktree or project).
func (o *Orchestrator) workDir() string { return o.workspaceRoute().dir }

type workspaceRoute struct {
	dir      string
	git      *git.Client
	store    *spec.Store
	revision uint64
}

func (o *Orchestrator) workspaceRoute() workspaceRoute {
	o.workspaceMu.RLock()
	defer o.workspaceMu.RUnlock()
	return workspaceRoute{dir: o.dir, git: o.git, store: o.store, revision: o.workspaceRev}
}

// installWorkspace publishes a complete routing tuple atomically. Callers
// take a snapshot and release the lock before any filesystem or git I/O.
func (o *Orchestrator) installWorkspace(dir string, client *git.Client, store *spec.Store) {
	o.workspaceMu.Lock()
	o.dir = dir
	o.git = client
	o.store = store
	o.workspaceRev++
	o.workspaceMu.Unlock()
	// MCP stdio commands inherit a concrete working directory at process start.
	// Retarget every route publication, including accepted and temporary
	// worktrees, before the next runner can observe the new workspace.
	o.retargetMCPWorkspace(dir)
}

// scopedTools returns the tool set for a role. The reviewer never mutates.
func (o *Orchestrator) scopedTools(role agentcore.Role) map[string]agentcore.Tool {
	r := tools.Default()
	// Inject the wired ask tool (channel-backed picker from the TUI).
	r.Replace("ask", tools.NewAsk(o.ask))
	if role == agentcore.RoleOrchestrator {
		// Discovery and proposal planning are read-only. Spec persistence is
		// owned exclusively by /propose + /accept, never by an LLM tool call.
		readOnly := tools.New()
		for _, name := range []string{"read", "grep", "ask"} {
			if tool, ok := r.Get(name); ok {
				if name == "read" || name == "grep" {
					tool = o.workspaceTool(tool)
				}
				readOnly.Add(tool)
			}
		}
		// MCP tools remain approval-gated even in conversational discovery.
		// Their server-supplied read-only annotations are untrusted, so none is
		// silently classified as view-only.
		for _, tool := range o.connectedMCPTools() {
			readOnly.Add(tool)
		}
		return readOnly.Map()
	}
	for _, name := range []string{"read", "grep", "write", "bash"} {
		if tool, ok := r.Get(name); ok {
			r.Replace(name, o.workspaceTool(tool))
		}
	}
	if role == agentcore.RoleDev {
		// Frontends override individual capabilities (not the entire toolset).
		// The TUI replaces only write with its proposal-staging implementation
		// while retaining the read/search/test loop an agent needs to work.
		for _, t := range o.devTools {
			r.Replace(t.Spec().Name, o.workspaceTool(t))
		}
	}
	if role == agentcore.RoleDev || role == agentcore.RoleDocs {
		for _, tool := range o.connectedMCPTools() {
			r.Add(tool)
		}
	}
	if role == agentcore.RoleReviewer {
		r = tools.New()
		r.Add(o.workspaceTool(tools.NewRead()))
		r.Add(o.workspaceTool(tools.NewGrep()))
	}
	return r.Map()
}
