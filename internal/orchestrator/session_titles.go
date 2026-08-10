package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

const sessionTitleTimeout = 2 * time.Second

// RenameSession applies a user-owned title. Later automatic metadata updates
// are rejected durably because their expected fallback source no longer
// matches the record on disk.
func (o *Orchestrator) RenameSession(ctx context.Context, title string) error {
	title = session.NormalizeTitle(title)
	if !meaningfulSessionTitle(title) {
		return errors.New("rename session: title must contain a letter or number")
	}
	o.sessionMu.Lock()
	defer o.sessionMu.Unlock()
	previous := o.sess
	// A brand-new TUI session may not have reached its first lifecycle save
	// yet. Materialize it before applying the metadata-only title update.
	// Generic saves preserve any title already owned by another process.
	if previous.Revision == 0 {
		committed, err := o.sessions.Commit(ctx, previous)
		if err != nil {
			return fmt.Errorf("rename session: %w", err)
		}
		previous = committed
		o.sess = committed
	}
	// Publish the unchanged session pointer before changing independently owned
	// title metadata. If the pointer cannot be written, the rename has made no
	// observable title change and needs no lossy rollback (notably when the
	// previous title was empty or model-owned).
	if err := o.sessions.SetActive(ctx, previous.Project, previous.ID); err != nil {
		return fmt.Errorf("rename session: %w", err)
	}
	updated, err := o.sessions.SetUserTitle(ctx, previous.Project, previous.ID, title)
	if err != nil {
		return fmt.Errorf("rename session: %w", err)
	}
	o.sess.Title = updated.Title
	o.sess.TitleSource = updated.TitleSource
	o.sess.TitleSeedHash = updated.TitleSeedHash
	o.sess.Revision = updated.Revision
	o.sess.Updated = updated.Updated
	return nil
}

// ListSessionSummaries returns metadata sorted by the persisted Updated time.
// Worktree/spec drift disables a row before a frontend can attempt a load.
func (o *Orchestrator) ListSessionSummaries(ctx context.Context) ([]session.Summary, error) {
	summaries, err := o.sessions.ListSummaries(ctx, o.sess.Project)
	if err != nil {
		return nil, err
	}
	workspaces, workspaceErr := o.workspaceRoute().git.ListWorkspaces(ctx)
	workspaceByPath := map[string]git.Workspace{}
	if workspaceErr == nil {
		for _, workspace := range workspaces {
			workspaceByPath[filepathKey(workspace.Path)] = workspace
		}
	}
	for i := range summaries {
		if summaries[i].Disabled {
			continue
		}
		saved, loadErr := o.sessions.Load(ctx, o.sess.Project, summaries[i].ID)
		if loadErr != nil {
			disableSummary(&summaries[i], loadErr.Error())
			continue
		}
		if workspaceErr == nil && (saved.Worktree == "" || saved.WorkspaceRef == "") {
			resolved, _, resolveErr := resolvePersistedSessionWorkspace(ctx, o.workspaceRoute().git, saved)
			if resolveErr != nil {
				disableSummary(&summaries[i], "workspace identity is ambiguous; open this session from its original checkout")
				continue
			}
			saved = resolved
		}
		if saved.Worktree != "" {
			if workspaceErr != nil {
				disableSummary(&summaries[i], "Git worktree registry is unavailable")
				continue
			}
			workspace, ok := workspaceByPath[filepathKey(saved.Worktree)]
			if !ok {
				disableSummary(&summaries[i], "worktree is no longer registered")
				continue
			}
			if !workspace.Healthy {
				disableSummary(&summaries[i], workspace.DisabledReason)
				continue
			}
			if saved.WorkspaceRef != "" && workspace.Ref != saved.WorkspaceRef {
				disableSummary(&summaries[i], "worktree branch no longer matches the saved session")
				continue
			}
		}
		if phaseNeedsSpec(saved.Phase) {
			if saved.SpecID == "" {
				disableSummary(&summaries[i], "session phase requires an active spec")
				continue
			}
			store := spec.NewStore(o.specsDir)
			if saved.Worktree != "" {
				store = spec.NewStore(filepath.Join(saved.Worktree, "specs"))
			}
			if _, specErr := store.Load(ctx, saved.SpecID); specErr != nil {
				disableSummary(&summaries[i], "active spec is missing or unreadable")
			}
		}
	}
	return summaries, nil
}

func phaseNeedsSpec(phase session.Phase) bool {
	switch phase {
	case session.PhaseSpec, session.PhaseBuild, session.PhaseReview, session.PhaseDocs, session.PhaseArchive:
		return true
	default:
		return false
	}
}

func disableSummary(summary *session.Summary, reason string) {
	summary.Disabled = true
	summary.DisabledReason = session.NormalizeTitle(reason)
}

func filepathKey(path string) string {
	canonical, err := canonicalProjectDir(path)
	if err != nil {
		return path
	}
	return canonical
}

func (o *Orchestrator) ensureFallbackTitle(message string) bool {
	if o.sess.Title != "" || o.sess.TitleSource != "" {
		return false
	}
	title, seed, ok := session.FallbackTitle(message)
	if !ok {
		return false
	}
	o.sess.Title = title
	o.sess.TitleSource = session.TitleSourceFallback
	o.sess.TitleSeedHash = seed
	return true
}

// generateSessionTitle is intentionally synchronous and tightly bounded.
// This keeps the current session value race-free while the store-level CAS
// still protects against a late/custom runner result after rename or load.
func (o *Orchestrator) generateSessionTitle(parent context.Context, assistant string) {
	if o.sess.TitleSource != session.TitleSourceFallback || o.sess.TitleSeedHash == "" {
		return
	}
	firstUser := ""
	for _, turn := range o.sess.Conversation {
		if turn.Role == "user" && strings.TrimSpace(turn.Content) != "" {
			firstUser = turn.Content
			break
		}
	}
	if firstUser == "" {
		return
	}
	runner, err := o.sessionTitleRunner()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, sessionTitleTimeout)
	defer cancel()
	request, _ := json.Marshal(map[string]string{"user": firstUser, "assistant": assistant})
	result, err := runner.Run(ctx, agentcore.RoleOrchestrator, `MAESTRO_OPERATION: READ_ONLY_TASK
Create a concise session title from the JSON input. Do not call tools. Return exactly one JSON object and nothing else: {"title":"3 to 72 characters"}. Use no markdown or quotes inside the title.
INPUT_JSON:
`+string(request))
	if err != nil {
		return
	}
	title, err := decodeSafeLLMTitle(result.Summary)
	if err != nil {
		return
	}
	seed := o.sess.TitleSeedHash
	id := o.sess.ID
	updated, swapped, err := o.sessions.CompareAndSwapTitle(context.Background(), o.sess.Project, id, seed, session.TitleSourceFallback, title, session.TitleSourceLLM)
	if err != nil || !swapped || o.sess.ID != id || o.sess.TitleSeedHash != seed || o.sess.TitleSource != session.TitleSourceFallback {
		return
	}
	o.sess.Title = updated.Title
	o.sess.TitleSource = updated.TitleSource
	o.sess.Revision = updated.Revision
	o.sess.Updated = updated.Updated
}

func (o *Orchestrator) refineSessionTitleFromProposal(title string) {
	if o.sess.TitleSource != session.TitleSourceFallback || o.sess.TitleSeedHash == "" {
		return
	}
	title = session.NormalizeTitle(title)
	if !meaningfulSessionTitle(title) {
		return
	}
	seed := o.sess.TitleSeedHash
	updated, swapped, err := o.sessions.CompareAndSwapTitle(context.Background(), o.sess.Project, o.sess.ID, seed, session.TitleSourceFallback, title, session.TitleSourceLLM)
	if err == nil && swapped {
		o.sess.Title = updated.Title
		o.sess.TitleSource = updated.TitleSource
		o.sess.Revision = updated.Revision
		o.sess.Updated = updated.Updated
	}
}

func (o *Orchestrator) sessionTitleRunner() (Runner, error) {
	if o.titleRunner != nil {
		return silentStructuredRunner(o.titleRunner), nil
	}
	if o.registry == nil {
		return nil, errors.New("session title model unavailable")
	}
	model := ""
	if o.cfg != nil {
		slots, roles := agentcore.SlotsFromConfig(o.cfg)
		if resolved, _, ok := agentcore.ResolveRole(agentcore.RoleSmol, slots, roles); ok {
			model = resolved
		}
	}
	if model == "" {
		model = o.SettingsSnapshot().ModelSlots["small"]
	}
	if model == "" {
		return nil, errors.New("session title model unavailable")
	}
	return &metadataTitleRunner{o: o, model: model}, nil
}

type metadataTitleRunner struct {
	o     *Orchestrator
	model string
}

func (runner *metadataTitleRunner) Run(ctx context.Context, role agentcore.Role, prompt string) (agentcore.AgentResult, error) {
	if err := runner.o.registry.CheckModel(runner.model); err != nil {
		return agentcore.AgentResult{}, err
	}
	providerName, ok := runner.o.registry.ProviderOf(runner.model)
	if !ok {
		return agentcore.AgentResult{}, errors.New("session title model provider unavailable")
	}
	provider, ok := runner.o.registry.Provider(providerName)
	if !ok {
		return agentcore.AgentResult{}, errors.New("session title model provider unavailable")
	}
	loop, err := agentcore.Spawn(ctx, agentcore.SpawnOptions{
		Role: role, Provider: provider, Model: runner.o.canonicalModel(runner.model),
		Tools: map[string]agentcore.Tool{}, Stopper: agentcore.NewStopper(), MaxTurn: sessionTitleTimeout,
	})
	if err != nil {
		return agentcore.AgentResult{}, err
	}
	return agentcore.RunResult(ctx, loop, prompt)
}

func decodeSafeLLMTitle(value string) (string, error) {
	if strings.TrimSpace(value) != value || strings.HasPrefix(value, "```") {
		return "", errors.New("invalid title envelope")
	}
	var payload struct {
		Title string `json:"title"`
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("invalid trailing title data")
	}
	if strings.ContainsAny(payload.Title, "\r\n") || utf8.RuneCountInString(payload.Title) < 3 || utf8.RuneCountInString(payload.Title) > session.MaxTitleRunes {
		return "", errors.New("invalid title length or line count")
	}
	for _, r := range payload.Title {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", errors.New("invalid title control character")
		}
	}
	title := session.NormalizeTitle(payload.Title)
	if !meaningfulSessionTitle(title) {
		return "", errors.New("invalid title")
	}
	return title, nil
}

func meaningfulSessionTitle(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}
