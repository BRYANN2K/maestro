// Package session persists and restores working sessions: the orchestrator
// phase, the active spec, and the permission queue, saved per project under
// the data directory.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bryann2k/maestro/internal/spec"
)

// CurrentSchemaVersion is written on every new or updated session. A zero
// value remains valid when loading records written before versioning existed.
const CurrentSchemaVersion = 3

// ErrConflict means a different Maestro process committed a newer snapshot of
// the same session. Callers must reload instead of retrying their stale state:
// retrying would erase lifecycle, conversation, or review fields owned by the
// newer process.
var ErrConflict = errors.New("session changed in another Maestro process; restart Maestro or switch sessions before continuing")

// TitleSource records who last chose a session title. User titles are never
// replaced by background model metadata.
type TitleSource string

const (
	TitleSourceFallback TitleSource = "fallback"
	TitleSourceLLM      TitleSource = "llm"
	TitleSourceUser     TitleSource = "user"
)

// Valid reports whether source is a persisted title provenance value.
func (source TitleSource) Valid() bool {
	return source == TitleSourceFallback || source == TitleSourceLLM || source == TitleSourceUser
}

// Phase is the orchestrator's current step in the pipeline.
type Phase string

// Orchestration phases, in pipeline order.
const (
	PhaseChat    Phase = "chat"
	PhasePropose Phase = "propose"
	PhaseSpec    Phase = "spec"
	PhaseBuild   Phase = "build"
	PhaseReview  Phase = "review"
	PhaseDocs    Phase = "docs"
	PhaseArchive Phase = "archive"
)

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool {
	return slices.Contains([]Phase{PhaseChat, PhasePropose, PhaseSpec, PhaseBuild, PhaseReview, PhaseDocs, PhaseArchive}, p)
}

// needsSpec reports whether the phase operates on a spec that must still
// exist and not be archived (resume validation).
func (p Phase) needsSpec() bool {
	return p == PhaseSpec || p == PhaseBuild || p == PhaseReview || p == PhaseDocs || p == PhaseArchive
}

// Permission is one pending human approval: a tool call awaiting sign-off or
// a manual human action (HITL item).
type Permission struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // tool | human_action
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Scope  string `json:"scope,omitempty"` // active spec/draft for a human action
	Status string `json:"status"`          // pending | approved | denied | done
}

// ConversationTurn is one bounded, persisted discovery turn. It gives
// /propose-without-arguments an authoritative conversation context without
// coupling the orchestrator to a particular frontend transcript model.
type ConversationTurn struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

// ReviewResult is the durable release gate for an active spec. Persisting it
// prevents a restarted process from treating the review phase alone as proof
// that the deterministic checks passed.
type ReviewResult struct {
	Level       string `json:"level"` // pass | warn | fail
	Summary     string `json:"summary,omitempty"`
	Findings    string `json:"findings,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"` // exact Git tree reviewed by the release gate
	GitRef      string `json:"git_ref,omitempty"`     // exact symbolic branch reviewed by the release gate
	GitHead     string `json:"git_head,omitempty"`    // exact parent commit reviewed by the release gate
}

// SpecContract is the durable integrity boundary around the accepted spec
// trio. Build/Fix may leave monotonic checkbox progress pending in tasks.md,
// but only a successful review advances the durable task states. Every other
// byte of spec.md, design.md, and tasks.md remains immutable. Keeping the
// contract in the session makes a crash or process restart fail closed instead
// of silently blessing a runner's unreviewed claim.
type SpecContract struct {
	Version           int    `json:"version"`
	SpecID            string `json:"spec_id"`
	SpecHash          string `json:"spec_hash"`
	DesignHash        string `json:"design_hash"`
	TasksTemplateHash string `json:"tasks_template_hash"`
	TaskStates        []bool `json:"task_states,omitempty"`
}

// Session is a resumable unit of work in a project.
type Session struct {
	SchemaVersion   int                `json:"schema_version,omitempty"`
	Revision        uint64             `json:"revision,omitempty"`
	ID              string             `json:"id"`
	Project         string             `json:"project"`
	Title           string             `json:"title,omitempty"`
	TitleSource     TitleSource        `json:"title_source,omitempty"`
	TitleSeedHash   string             `json:"title_seed_hash,omitempty"`
	Phase           Phase              `json:"phase"`
	SpecID          string             `json:"spec_id,omitempty"`
	ModelRole       string             `json:"model_role,omitempty"`
	PermQueue       []Permission       `json:"perm_queue,omitempty"`
	Draft           *spec.Spec         `json:"draft,omitempty"` // unaccepted proposal
	DraftPrompt     string             `json:"draft_prompt,omitempty"`
	DraftDesign     string             `json:"draft_design,omitempty"`
	DraftTasks      string             `json:"draft_tasks,omitempty"`
	Conversation    []ConversationTurn `json:"conversation,omitempty"`
	Review          *ReviewResult      `json:"review,omitempty"`
	SpecContract    *SpecContract      `json:"spec_contract,omitempty"`
	WorkspaceRef    string             `json:"workspace_ref,omitempty"`    // exact branch ref selected at /accept, including stay
	Branch          string             `json:"branch,omitempty"`           // branch/worktree chosen at /accept
	BaseBranch      string             `json:"base_branch,omitempty"`      // branch that was active before /accept
	Worktree        string             `json:"worktree,omitempty"`         // worktree path, when used
	ManagedWorktree bool               `json:"managed_worktree,omitempty"` // true only for ephemeral /accept --worktree checkouts
	Created         string             `json:"created"`
	Updated         string             `json:"updated"`
}

// New returns a fresh session in the chat phase.
func New(project string) Session {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Session{
		SchemaVersion: CurrentSchemaVersion,
		ID:            fmt.Sprintf("%d", time.Now().UnixNano()),
		Project:       project,
		Phase:         PhaseChat,
		Created:       now,
		Updated:       now,
	}
}

// Store persists sessions as JSON files under dir/<project>/<id>.json.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the store root.
func (s *Store) Dir() string { return s.dir }

// Save writes the session to disk, creating the project folder as needed.
//
// Save is retained for simple one-shot callers. Lifecycle owners should use
// Commit and publish its returned snapshot so their next write carries the new
// durable revision.
func (s *Store) Save(ctx context.Context, sess Session) error {
	_, err := s.Commit(ctx, sess)
	return err
}

// Commit atomically writes one complete session snapshot. Updates use an
// optimistic revision check inside the cross-process record lock, preventing a
// stale process from silently rolling back newer lifecycle state.
func (s *Store) Commit(ctx context.Context, sess Session) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var committed Session
	err := s.withRecordLock(ctx, sess.Project, sess.ID, func() error {
		var err error
		committed, err = s.saveLocked(ctx, sess)
		return err
	})
	return committed, err
}

func (s *Store) saveLocked(ctx context.Context, sess Session) (Session, error) {
	return s.saveRecordLocked(ctx, sess, true)
}

func (s *Store) saveExactLocked(ctx context.Context, sess Session) (Session, error) {
	return s.saveRecordLocked(ctx, sess, false)
}

func (s *Store) saveRecordLocked(ctx context.Context, sess Session, preservePersistedTitle bool) (Session, error) {
	if sess.ID == "" || sess.Project == "" {
		return Session{}, errors.New("session requires id and project")
	}
	if !validComponent(sess.ID) || !validComponent(sess.Project) {
		return Session{}, errors.New("session id and project must be safe path components")
	}
	if !sess.Phase.Valid() {
		return Session{}, fmt.Errorf("session %s has invalid phase %q", sess.ID, sess.Phase)
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if sess.SchemaVersion > CurrentSchemaVersion {
		return Session{}, fmt.Errorf("session %s uses unsupported schema version %d", sess.ID, sess.SchemaVersion)
	}
	sess.SchemaVersion = CurrentSchemaVersion
	sess.Title = NormalizeTitle(sess.Title)
	if sess.Title == "" {
		sess.TitleSource = ""
		sess.TitleSeedHash = ""
	} else if !sess.TitleSource.Valid() {
		// Old records with a title but no provenance are deterministic metadata,
		// not proof of a user rename.
		sess.TitleSource = TitleSourceFallback
	}
	existing, loadErr := s.loadLocked(ctx, sess.Project, sess.ID)
	switch {
	case loadErr == nil:
		if sess.Revision != existing.Revision {
			return Session{}, fmt.Errorf("save session %s: revision %d does not match durable revision %d: %w", sess.ID, sess.Revision, existing.Revision, ErrConflict)
		}
		if existing.Revision == ^uint64(0) {
			return Session{}, fmt.Errorf("save session %s: revision exhausted", sess.ID)
		}
		sess.Revision = existing.Revision + 1
		if preservePersistedTitle && existing.Title != "" {
			// Titles are independently owned metadata. Generic lifecycle saves
			// may carry a stale in-memory copy, so only the dedicated title APIs
			// are allowed to replace an already persisted title.
			sess.Title = existing.Title
			sess.TitleSource = existing.TitleSource
			sess.TitleSeedHash = existing.TitleSeedHash
		}
	case errors.Is(loadErr, os.ErrNotExist):
		if sess.Revision != 0 {
			return Session{}, fmt.Errorf("save session %s: record is missing for revision %d: %w", sess.ID, sess.Revision, ErrConflict)
		}
		sess.Revision = 1
	default:
		return Session{}, fmt.Errorf("save session %s: inspect durable revision: %w", sess.ID, loadErr)
	}
	sess.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return Session{}, fmt.Errorf("save session %s: %w", sess.ID, err)
	}
	dir := filepath.Join(s.dir, sess.Project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Session{}, fmt.Errorf("save session %s: %w", sess.ID, err)
	}
	path := filepath.Join(dir, sess.ID+".json")
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return Session{}, fmt.Errorf("save session %s: %w", sess.ID, err)
	}
	return sess, nil
}

// Load reads the session with id for project.
func (s *Store) Load(ctx context.Context, project, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(ctx, project, id)
}

func (s *Store) loadLocked(ctx context.Context, project, id string) (Session, error) {
	if !validComponent(project) || !validComponent(id) {
		return Session{}, errors.New("load session: invalid project or id")
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, project, id+".json"))
	if err != nil {
		return Session{}, fmt.Errorf("load session %s: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, fmt.Errorf("load session %s: %w", id, err)
	}
	if sess.ID != id || sess.Project != project {
		return Session{}, fmt.Errorf("load session %s: identity mismatch", id)
	}
	legacy := sess.SchemaVersion == 0
	if sess.SchemaVersion > CurrentSchemaVersion {
		return Session{}, fmt.Errorf("load session %s: unsupported schema version %d", id, sess.SchemaVersion)
	}
	if sess.SchemaVersion == 0 {
		sess.SchemaVersion = 1
	}
	if legacy && sess.Worktree != "" && sess.Branch != "" {
		// Before workspace selection existed, every persisted linked worktree
		// was created by /accept and therefore lifecycle-managed.
		sess.ManagedWorktree = true
	}
	sess.Title = NormalizeTitle(sess.Title)
	if sess.Title == "" {
		sess.TitleSource = ""
		sess.TitleSeedHash = ""
	} else if !sess.TitleSource.Valid() {
		sess.TitleSource = TitleSourceFallback
	}
	return sess, nil
}

// List returns the session IDs for project, newest first.
func (s *Store) List(ctx context.Context, project string) ([]string, error) {
	if !validComponent(project) {
		return nil, errors.New("list sessions: invalid project")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dir, project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return ids, nil
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		len(value) <= 240 && filepath.Base(value) == value &&
		!strings.ContainsAny(value, "/\\\x00")
}

// Latest returns the most recent session for project.
func (s *Store) Latest(ctx context.Context, project string) (Session, error) {
	summaries, err := s.ListSummaries(ctx, project)
	if err != nil {
		return Session{}, err
	}
	for _, summary := range summaries {
		loaded, loadErr := s.Load(ctx, project, summary.ID)
		if loadErr == nil {
			return loaded, nil
		}
	}
	return Session{}, os.ErrNotExist
}

// Restore loads the latest session and validates its phase against the
// current spec status. A phase that needs a spec whose spec is archived is
// reset to chat, and a notice explains why. A nil storeDir skips spec
// validation.
func (s *Store) Restore(ctx context.Context, project, storeDir string) (Session, string, error) {
	var pointerNotice string
	var sess Session
	activeID, activeErr := s.Active(ctx, project)
	if activeErr == nil {
		var loadErr error
		sess, loadErr = s.Load(ctx, project, activeID)
		if loadErr != nil {
			pointerNotice = fmt.Sprintf("active session %s could not be loaded; using newest valid session", activeID)
		}
	}
	if sess.ID == "" {
		var err error
		sess, err = s.Latest(ctx, project)
		if err != nil {
			return Session{}, "", err
		}
	}
	if !sess.Phase.Valid() {
		sess.Phase = PhaseChat
		return sess, fmt.Sprintf("session %s had unknown phase; reset to chat", sess.ID), nil
	}
	if storeDir != "" && sess.Phase.needsSpec() && sess.SpecID != "" {
		st := spec.NewStore(storeDir)
		sp, err := st.Load(ctx, sess.SpecID)
		if err == nil && sp.Status == spec.StatusArchived && sess.Phase != PhaseArchive {
			from := sess.Phase
			sess.Phase = PhaseChat
			sess.SpecID = ""
			return sess, fmt.Sprintf("spec is archived; phase reset from %s to chat", from), nil
		}
		if sess.Phase == PhaseArchive {
			// Folder presence alone cannot prove that the reviewed tree was
			// committed: Archive moves it before the ref/index transaction. The
			// orchestrator validates HEAD, index, and worktree before deciding
			// whether to finalize or restore the active spec.
			return sess, "interrupted archive detected; verifying Git transaction", nil
		}
	}
	if sess.Phase == PhaseArchive && sess.SpecID == "" {
		sess.Phase = PhaseChat
		return sess, "completed archive phase reset to chat", nil
	}
	return sess, pointerNotice, nil
}

// writeFileAtomic writes data to path via a temp file + rename, so a crash
// never leaves a half-written session.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
