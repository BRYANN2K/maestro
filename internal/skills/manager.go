package skills

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// EnableScope selects where an enabled/disabled override is persisted.
type EnableScope string

const (
	EnableProject EnableScope = "project"
	EnableSession EnableScope = "session"
)

// Summary is the stable, body-free surface consumed by settings and CLIs.
// Error is per-entry so one malformed repository skill cannot hide the rest.
type Summary struct {
	ID            string
	Name          string
	Description   string
	Source        string
	Scope         Scope
	Valid         bool
	Enabled       bool
	UserInvokable bool
	Warning       string
	Error         string
}

// Manager owns one project/session catalog and its private enablement state.
type Manager struct {
	mu         sync.RWMutex
	home       string
	projectDir string
	extra      []string
	projectKey string
	sessionID  string
	store      *StateStore
	catalog    Catalog
}

// ManagerOptions configures one isolated project skill registry.
type ManagerOptions struct {
	Home       string
	ProjectDir string
	ExtraPaths []string
	StateDir   string
	ProjectKey string
	SessionID  string
}

// NewManager performs bounded metadata discovery and integrity hashing but
// never retains or injects a skill body.
func NewManager(options ManagerOptions) *Manager {
	manager := &Manager{
		home: options.Home, projectDir: options.ProjectDir,
		extra: append([]string(nil), options.ExtraPaths...), projectKey: options.ProjectKey,
		sessionID: options.SessionID, store: NewStateStore(options.StateDir, options.ProjectKey),
	}
	manager.catalog = DiscoverCatalog(manager.home, manager.projectDir, manager.extra)
	return manager
}

// Refresh replaces the complete metadata snapshot atomically.
func (m *Manager) Refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	catalog := DiscoverCatalog(m.home, m.projectDir, m.extra)
	m.mu.Lock()
	m.catalog = catalog
	m.mu.Unlock()
	return nil
}

func (m *Manager) catalogSnapshot() Catalog {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := Catalog{
		Skills: append([]Skill(nil), m.catalog.Skills...),
		Issues: append([]Issue(nil), m.catalog.Issues...),
	}
	for index := range out.Skills {
		out.Skills[index].Metadata = cloneMetadata(out.Skills[index].Metadata)
	}
	return out
}

// Summaries reports valid skills and invalid entries in stable ID order.
func (m *Manager) Summaries(ctx context.Context) []Summary {
	catalog := m.catalogSnapshot()
	state, stateErr := m.store.Load(ctx)
	nameCount := make(map[string]int, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		nameCount[skill.Name]++
	}
	out := make([]Summary, 0, len(catalog.Skills)+len(catalog.Issues)+1)
	for _, skill := range catalog.Skills {
		summary := Summary{
			ID: skill.ID, Name: skill.Name, Description: skill.Description,
			Source: skill.Source, Scope: skill.Scope, UserInvokable: skill.UserInvokable,
			Valid: true, Enabled: effectiveEnabled(state, m.sessionID, skill.ID),
		}
		if stateErr != nil {
			summary.Enabled = false
		}
		if nameCount[skill.Name] > 1 {
			summary.Warning = "name collision; use the qualified skill ID"
		}
		out = append(out, summary)
	}
	for _, issue := range catalog.Issues {
		out = append(out, Summary{
			ID: issue.ID, Name: issue.Name, Source: issue.Source, Scope: issue.Scope,
			Valid: false, Enabled: false, UserInvokable: false, Error: issue.Error,
		})
	}
	if stateErr != nil && !errors.Is(stateErr, context.Canceled) {
		out = append(out, Summary{
			ID: "invalid:state", Name: "skill settings", Source: "private state",
			Valid: false, Enabled: false, Error: stateErr.Error(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SkillList returns only enabled, explicitly invokable skills. When names
// collide the display/invocation name becomes the stable qualified ID so an
// older palette cannot accidentally select a different source.
func (m *Manager) SkillList(ctx context.Context) []Skill {
	catalog := m.catalogSnapshot()
	state, err := m.store.Load(ctx)
	if err != nil {
		return nil
	}
	counts := make(map[string]int, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		counts[skill.Name]++
	}
	var out []Skill
	for _, skill := range catalog.Skills {
		if !skill.UserInvokable || !effectiveEnabled(state, m.sessionID, skill.ID) {
			continue
		}
		copySkill := skill
		if counts[skill.Name] > 1 {
			copySkill.Name = skill.ID
		}
		out = append(out, copySkill)
	}
	return out
}

// Inspect loads full source only after an explicit request. Invalid discovery
// entries cannot be inspected as instructions.
func (m *Manager) Inspect(ctx context.Context, ref string) (Inspection, error) {
	return m.catalogSnapshot().Inspect(ctx, ref)
}

// SetEnabled persists an override for one validated skill.
func (m *Manager) SetEnabled(ctx context.Context, ref string, enabled bool, scope EnableScope) error {
	skill, err := m.catalogSnapshot().Resolve(ref)
	if err != nil {
		return err
	}
	if scope == "" {
		scope = EnableProject
	}
	if scope != EnableProject && scope != EnableSession {
		return errors.New("skill enablement scope must be project or session")
	}
	if scope == EnableSession && strings.TrimSpace(m.sessionID) == "" {
		return errors.New("session skill enablement requires an active session")
	}
	return m.store.SetEnabled(ctx, skill.ID, enabled, scope, m.sessionID)
}

// EnabledInspection resolves, checks effective state and user invocation, then
// loads the reviewed source for an explicit run.
func (m *Manager) EnabledInspection(ctx context.Context, ref string) (Inspection, error) {
	catalog := m.catalogSnapshot()
	skill, err := catalog.Resolve(ref)
	if err != nil {
		return Inspection{}, err
	}
	state, err := m.store.Load(ctx)
	if err != nil {
		return Inspection{}, err
	}
	if !effectiveEnabled(state, m.sessionID, skill.ID) {
		return Inspection{}, errors.New("skill is disabled for this project or session")
	}
	if !skill.UserInvokable {
		return Inspection{}, errors.New("skill declares user-invocable: false")
	}
	return catalog.Inspect(ctx, skill.ID)
}

func effectiveEnabled(state State, sessionID, id string) bool {
	if overrides := state.Sessions[sessionID]; overrides != nil {
		if enabled, ok := overrides[id]; ok {
			return enabled
		}
	}
	if enabled, ok := state.Project[id]; ok {
		return enabled
	}
	return true
}
