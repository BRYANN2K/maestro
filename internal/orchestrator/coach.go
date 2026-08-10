package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bryann2k/maestro/internal/session"
)

const (
	coachStateVersion = 1
	coachStateMaxSize = 128 << 10
	coachCooldown     = 20 * time.Minute
	coachMaxSnooze    = 30 * 24 * time.Hour
	coachHistoryLimit = 256
	coachCounterLimit = 10_000
)

// CoachMode controls whether contextual lessons are offered and how much
// scaffolding they include. Lessons are advisory and never gate a phase.
type CoachMode string

const (
	CoachModeOff       CoachMode = "off"
	CoachModeGuided    CoachMode = "guided"
	CoachModeChallenge CoachMode = "challenge"
)

func (mode CoachMode) valid() bool {
	return mode == CoachModeOff || mode == CoachModeGuided || mode == CoachModeChallenge
}

// CoachStage follows cognitive apprenticeship from modelling through
// independent performance, then spaced retrieval and reflection.
type CoachStage string

const (
	CoachStageObserve     CoachStage = "observe"
	CoachStageSelfExplain CoachStage = "self_explain"
	CoachStageComplete    CoachStage = "complete"
	CoachStagePerform     CoachStage = "perform"
	CoachStageRetrieval   CoachStage = "retrieval"
	CoachStageReflection  CoachStage = "reflection"
)

var coachStages = []CoachStage{
	CoachStageObserve,
	CoachStageSelfExplain,
	CoachStageComplete,
	CoachStagePerform,
	CoachStageRetrieval,
	CoachStageReflection,
}

func (stage CoachStage) valid() bool { return slices.Contains(coachStages, stage) }

// CoachProgress records demonstrated, explicit lesson completions. It is
// never updated from model text or inferred tool activity.
type CoachProgress struct {
	Stage               CoachStage `json:"stage"`
	ExplicitCompletions int        `json:"explicit_completions"`
	Mastery             int        `json:"mastery"`
	Retrievals          int        `json:"retrievals"`
	NextRetrievalAt     time.Time  `json:"next_retrieval_at,omitempty"`
	LastCompletedAt     time.Time  `json:"last_completed_at,omitempty"`
}

// CoachState is the complete private, per-project learning record. It stores
// identifiers and progress only: no source, prompts, conversation or secrets.
type CoachState struct {
	Version            int                      `json:"version"`
	Mode               CoachMode                `json:"mode"`
	Progress           map[string]CoachProgress `json:"progress"`
	CompletedLessonIDs []string                 `json:"completed_lesson_ids,omitempty"`
	CooldownUntil      time.Time                `json:"cooldown_until,omitempty"`
	SnoozedUntil       time.Time                `json:"snoozed_until,omitempty"`
	LastOfferedPhase   session.Phase            `json:"last_offered_phase,omitempty"`
	LastOfferedAt      time.Time                `json:"last_offered_at,omitempty"`
	PendingLessonID    string                   `json:"pending_lesson_id,omitempty"`
	PendingSkillID     string                   `json:"pending_skill_id,omitempty"`
	PendingStage       CoachStage               `json:"pending_stage,omitempty"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

// CoachLesson is an ephemeral, deterministic prompt. Only its identifiers
// are persisted, so lesson wording cannot capture project content.
type CoachLesson struct {
	ID        string        `json:"id"`
	SkillID   string        `json:"skill_id"`
	Phase     session.Phase `json:"phase"`
	Stage     CoachStage    `json:"stage"`
	Title     string        `json:"title"`
	Prompt    string        `json:"prompt"`
	Action    string        `json:"action"`
	WhyNow    string        `json:"why_now"`
	DoneWhen  string        `json:"done_when"`
	Scaffold  int           `json:"scaffold"`
	Retrieval bool          `json:"retrieval"`
}

type coachTopic struct {
	ID      string
	Title   string
	Focus   string
	Example string
	Starter string
	Hint    string
	Why     map[session.Phase]string
}

var coachTopics = map[string]coachTopic{
	"intent-non-goals": {
		ID: "intent-non-goals", Title: "Intent and non-goals", Focus: "intent, success boundary, and explicit non-goals",
		Example: "Intent: shorten review feedback; non-goal: redesign the release flow; evidence: reviewers find the target decision faster.",
		Starter: "Intent: __. Observable evidence: __. Non-goal: __.",
		Hint:    "Name one outcome, one observable signal, and one tempting but excluded expansion.",
		Why: map[session.Phase]string{
			session.PhaseChat: "discovery is where a crisp boundary prevents prompt-only scope drift",
		},
	},
	"spec-scenarios": {
		ID: "spec-scenarios", Title: "Executable spec scenarios", Focus: "happy path, boundary case, and failure scenario",
		Example: "Given an unauthenticated request, when a protected action is attempted, then access is denied without changing state.",
		Starter: "Given __, when __, then __; on failure, __ remains unchanged.",
		Hint:    "Use Given/When/Then and make the final state or evidence observable.",
		Why: map[session.Phase]string{
			session.PhasePropose: "the proposal is becoming a contract, so examples expose ambiguity cheaply",
			session.PhaseSpec:    "acceptance scenarios now determine what implementation and review must prove",
		},
	},
	"code-tracing": {
		ID: "code-tracing", Title: "Trace code in the IDE", Focus: "one input through callers, state changes, and returned output",
		Example: "Start at the public entry point, follow one concrete input, note each state mutation, then verify the final return path.",
		Starter: "Entry __ -> call __ -> state change __ -> returned output __.",
		Hint:    "Use symbol references and write a four-step trace before asking the assistant to explain it.",
		Why: map[session.Phase]string{
			session.PhaseBuild: "implementation is active, so tracing a real path connects the spec to code structure",
		},
	},
	"tests": {
		ID: "tests", Title: "Tests as evidence", Focus: "a test that distinguishes the requirement from a plausible wrong implementation",
		Example: "A cancellation test asserts both the returned cancellation error and that no write occurred.",
		Starter: "Plausible bug: __. Arrange __; act __; assert behavior __ and side effect __.",
		Hint:    "State the bug the test would catch before writing the assertion.",
		Why: map[session.Phase]string{
			session.PhaseBuild: "the implementation is changing, so discriminating tests turn intent into durable evidence",
		},
	},
	"threat-model": {
		ID: "threat-model", Title: "Threats, secrets, and dependencies", Focus: "assets, trust boundaries, attacker-controlled input, secrets, and dependency risk",
		Example: "Asset: API key; boundary: config file to process; abuse: symlink redirects a read; control: no-follow plus bounded regular-file validation.",
		Starter: "Asset __; attacker input __; boundary __; abuse __; control and evidence __.",
		Hint:    "List one asset, one attacker-controlled input, one boundary, and one verifiable control.",
		Why: map[session.Phase]string{
			session.PhaseBuild:  "implementation choices are creating trust boundaries that are cheapest to secure now",
			session.PhaseReview: "review is the last cheap point to demand evidence for secrets, inputs, and dependencies",
		},
	},
	"review-evidence": {
		ID: "review-evidence", Title: "Review with evidence", Focus: "claim, exact evidence, residual risk, and a falsifying check",
		Example: "Claim: traversal is refused; evidence: outside-root test plus canonical-path check; residual risk: filesystem race; check: symlink adversarial test.",
		Starter: "Claim __; exact evidence __; residual risk __; falsifying check __.",
		Hint:    "For each claim, cite a test, diff location, or command result and name what it still does not prove.",
		Why: map[session.Phase]string{
			session.PhaseReview: "the work is being judged now, so claims need reproducible evidence rather than model confidence",
		},
	},
	"docs-tradeoffs": {
		ID: "docs-tradeoffs", Title: "Document tradeoffs", Focus: "user-facing behavior, chosen tradeoff, rejected alternative, and operational consequence",
		Example: "We cap explanations at 256 KiB to bound model input; large generated files are excluded and must be inspected another way.",
		Starter: "Decision __; chosen tradeoff __ because __; rejected alternative __; user consequence __.",
		Hint:    "Document why the constraint exists and what a user should do when they hit it.",
		Why: map[session.Phase]string{
			session.PhaseDocs: "behavior is stable enough to explain its tradeoffs before project knowledge decays",
		},
	},
}

var coachPhaseTopics = map[session.Phase][]string{
	session.PhaseChat:    {"intent-non-goals"},
	session.PhasePropose: {"spec-scenarios"},
	session.PhaseSpec:    {"spec-scenarios"},
	session.PhaseBuild:   {"code-tracing", "tests", "threat-model"},
	session.PhaseReview:  {"review-evidence", "threat-model"},
	session.PhaseDocs:    {"docs-tradeoffs"},
	session.PhaseArchive: {"intent-non-goals", "spec-scenarios", "code-tracing", "tests", "threat-model", "review-evidence", "docs-tradeoffs"},
}

var coachStateMu sync.Mutex

// CoachState loads the private per-project state, migrating old or corrupt
// records to a safe current representation.
func (o *Orchestrator) CoachState(ctx context.Context) (CoachState, error) {
	coachStateMu.Lock()
	defer coachStateMu.Unlock()
	return o.loadCoachStateLocked(ctx, time.Now().UTC())
}

// SetCoachMode explicitly changes the per-project coaching mode.
func (o *Orchestrator) SetCoachMode(ctx context.Context, mode CoachMode) (CoachState, error) {
	if !mode.valid() {
		return CoachState{}, fmt.Errorf("coach: invalid mode %q", mode)
	}
	coachStateMu.Lock()
	defer coachStateMu.Unlock()
	now := time.Now().UTC()
	state, err := o.loadCoachStateLocked(ctx, now)
	if err != nil {
		return CoachState{}, err
	}
	changed := state.Mode != mode
	state.Mode = mode
	if mode != CoachModeOff {
		// Choosing Guided or Challenge is an explicit "resume now" action.
		// An earlier Later/cooldown must not make that successful command look
		// inert for minutes or hours. A pending lesson is preserved so the
		// user's place in the curriculum is not lost.
		changed = changed || !state.SnoozedUntil.IsZero() || !state.CooldownUntil.IsZero()
		state.SnoozedUntil = time.Time{}
		state.CooldownUntil = time.Time{}
		if state.PendingLessonID == "" {
			state.LastOfferedPhase = ""
			state.LastOfferedAt = time.Time{}
		}
	}
	if !changed {
		return state, nil
	}
	state.UpdatedAt = now
	if err := o.saveCoachStateLocked(ctx, state); err != nil {
		return CoachState{}, err
	}
	return state, nil
}

// CoachLesson returns at most one contextual lesson for the active phase.
// Repeated reads return the same pending lesson; they do not create another
// interruption. No tool or workflow action is run by this method.
func (o *Orchestrator) CoachLesson(ctx context.Context) (*CoachLesson, error) {
	return o.coachLessonAt(ctx, time.Now().UTC())
}

func (o *Orchestrator) coachLessonAt(ctx context.Context, now time.Time) (*CoachLesson, error) {
	coachStateMu.Lock()
	defer coachStateMu.Unlock()
	now = now.UTC()
	state, err := o.loadCoachStateLocked(ctx, now)
	if err != nil {
		return nil, err
	}
	if state.Mode == CoachModeOff || now.Before(state.SnoozedUntil) {
		return nil, nil
	}
	phase := o.Phase()
	if !phase.Valid() {
		return nil, nil
	}
	if state.PendingLessonID != "" && state.LastOfferedPhase == phase {
		lesson, ok := lessonFromPending(state, phase)
		if ok {
			return &lesson, nil
		}
	}
	if state.LastOfferedPhase == phase || now.Before(state.CooldownUntil) {
		return nil, nil
	}
	topic, progress, ok := chooseCoachTopic(state, phase, now)
	if !ok {
		return nil, nil
	}
	lesson := makeCoachLesson(state.Mode, phase, topic, progress)
	state.PendingLessonID = lesson.ID
	state.PendingSkillID = lesson.SkillID
	state.PendingStage = lesson.Stage
	state.LastOfferedPhase = phase
	state.LastOfferedAt = now
	state.CooldownUntil = now.Add(coachCooldown)
	state.UpdatedAt = now
	if err := o.saveCoachStateLocked(ctx, state); err != nil {
		return nil, err
	}
	return &lesson, nil
}

// CompleteCoachLesson records one explicit user completion. Only the exact
// pending lesson can advance progress; repeated completion is idempotent.
func (o *Orchestrator) CompleteCoachLesson(ctx context.Context, lessonID string) (CoachState, error) {
	return o.completeCoachLessonAt(ctx, lessonID, time.Now().UTC())
}

func (o *Orchestrator) completeCoachLessonAt(ctx context.Context, lessonID string, now time.Time) (CoachState, error) {
	coachStateMu.Lock()
	defer coachStateMu.Unlock()
	now = now.UTC()
	state, err := o.loadCoachStateLocked(ctx, now)
	if err != nil {
		return CoachState{}, err
	}
	if slices.Contains(state.CompletedLessonIDs, lessonID) {
		return state, nil
	}
	if lessonID == "" || lessonID != state.PendingLessonID {
		return CoachState{}, errors.New("coach: only the explicitly offered lesson can be completed")
	}
	topic, known := coachTopics[state.PendingSkillID]
	if !known || !state.PendingStage.valid() || lessonID != coachLessonID(topic.ID, state.PendingStage, state.Progress[topic.ID].ExplicitCompletions) {
		return CoachState{}, errors.New("coach: pending lesson state is invalid")
	}
	progress := state.Progress[topic.ID]
	if progress.Stage == "" {
		progress.Stage = CoachStageObserve
	}
	if progress.Stage != state.PendingStage {
		return CoachState{}, errors.New("coach: pending lesson no longer matches progress")
	}
	progress.ExplicitCompletions++
	progress.LastCompletedAt = now
	switch progress.Stage {
	case CoachStageObserve:
		progress.Stage = CoachStageSelfExplain
	case CoachStageSelfExplain:
		progress.Stage = CoachStageComplete
	case CoachStageComplete:
		progress.Stage = CoachStagePerform
	case CoachStagePerform:
		progress.Stage = CoachStageRetrieval
		progress.NextRetrievalAt = now.Add(24 * time.Hour)
	case CoachStageRetrieval:
		progress.Stage = CoachStageReflection
		progress.Retrievals++
		progress.NextRetrievalAt = time.Time{}
	case CoachStageReflection:
		progress.Stage = CoachStageRetrieval
		progress.NextRetrievalAt = now.Add(retrievalInterval(progress.Retrievals))
	}
	progress.Mastery = masteryFor(progress)
	state.Progress[topic.ID] = progress
	state.CompletedLessonIDs = append(state.CompletedLessonIDs, lessonID)
	if len(state.CompletedLessonIDs) > coachHistoryLimit {
		state.CompletedLessonIDs = append([]string(nil), state.CompletedLessonIDs[len(state.CompletedLessonIDs)-coachHistoryLimit:]...)
	}
	state.PendingLessonID = ""
	state.PendingSkillID = ""
	state.PendingStage = ""
	state.UpdatedAt = now
	if err := o.saveCoachStateLocked(ctx, state); err != nil {
		return CoachState{}, err
	}
	return state, nil
}

// SnoozeCoach suppresses the pending lesson without losing it. Once the
// explicit snooze expires, the same lesson may be shown again.
func (o *Orchestrator) SnoozeCoach(ctx context.Context, duration time.Duration) (CoachState, error) {
	if duration <= 0 || duration > coachMaxSnooze {
		return CoachState{}, fmt.Errorf("coach: snooze must be between 1ns and %s", coachMaxSnooze)
	}
	coachStateMu.Lock()
	defer coachStateMu.Unlock()
	now := time.Now().UTC()
	state, err := o.loadCoachStateLocked(ctx, now)
	if err != nil {
		return CoachState{}, err
	}
	state.SnoozedUntil = now.Add(duration)
	state.UpdatedAt = now
	if err := o.saveCoachStateLocked(ctx, state); err != nil {
		return CoachState{}, err
	}
	return state, nil
}

func chooseCoachTopic(state CoachState, phase session.Phase, now time.Time) (coachTopic, CoachProgress, bool) {
	ids := coachPhaseTopics[phase]
	type candidate struct {
		topic    coachTopic
		progress CoachProgress
		order    int
	}
	var candidates []candidate
	for order, id := range ids {
		topic := coachTopics[id]
		progress := state.Progress[id]
		if progress.Stage == "" {
			progress.Stage = CoachStageObserve
		}
		if progress.Stage == CoachStageRetrieval && (progress.NextRetrievalAt.IsZero() || now.Before(progress.NextRetrievalAt)) {
			continue
		}
		candidates = append(candidates, candidate{topic: topic, progress: progress, order: order})
	}
	if len(candidates) == 0 {
		return coachTopic{}, CoachProgress{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftRetrieval := left.progress.Stage == CoachStageRetrieval || left.progress.Stage == CoachStageReflection
		rightRetrieval := right.progress.Stage == CoachStageRetrieval || right.progress.Stage == CoachStageReflection
		if leftRetrieval != rightRetrieval {
			return leftRetrieval
		}
		if left.progress.ExplicitCompletions != right.progress.ExplicitCompletions {
			return left.progress.ExplicitCompletions < right.progress.ExplicitCompletions
		}
		return left.order < right.order
	})
	return candidates[0].topic, candidates[0].progress, true
}

func lessonFromPending(state CoachState, phase session.Phase) (CoachLesson, bool) {
	topic, ok := coachTopics[state.PendingSkillID]
	if !ok || !state.PendingStage.valid() {
		return CoachLesson{}, false
	}
	progress := state.Progress[topic.ID]
	if progress.Stage == "" {
		progress.Stage = CoachStageObserve
	}
	lesson := makeCoachLesson(state.Mode, phase, topic, progress)
	return lesson, lesson.ID == state.PendingLessonID && lesson.Stage == state.PendingStage
}

func makeCoachLesson(mode CoachMode, phase session.Phase, topic coachTopic, progress CoachProgress) CoachLesson {
	stage := progress.Stage
	if stage == "" {
		stage = CoachStageObserve
	}
	action := coachAction(stage, topic)
	why := topic.Why[phase]
	if phase == session.PhaseArchive {
		why = "the cycle is closing, so retrieving this skill now strengthens transfer to the next project"
	}
	scaffold := scaffoldFor(progress.ExplicitCompletions)
	if mode == CoachModeChallenge {
		scaffold = 0
	}
	doneWhen := "you have written one concrete response tied to current project evidence"
	prompt := "Next (2 min): " + action + "\nWhy now: " + why +
		"\nDone when: " + doneWhen + "."
	if scaffold >= 3 {
		prompt += "\nWorked example: " + topic.Example
	} else if scaffold == 2 {
		prompt += "\nHint: " + topic.Hint
	} else if scaffold == 1 {
		prompt += "\nCheck: explain what evidence would prove your answer wrong."
	}
	return CoachLesson{
		ID:        coachLessonID(topic.ID, stage, progress.ExplicitCompletions),
		SkillID:   topic.ID,
		Phase:     phase,
		Stage:     stage,
		Title:     topic.Title,
		Prompt:    prompt,
		Action:    action,
		WhyNow:    why,
		DoneWhen:  doneWhen,
		Scaffold:  scaffold,
		Retrieval: stage == CoachStageRetrieval || stage == CoachStageReflection,
	}
}

func coachAction(stage CoachStage, topic coachTopic) string {
	switch stage {
	case CoachStageObserve:
		return "Compare the worked example with the current task and point out the decision, boundary, and evidence pattern for " + topic.Focus + "."
	case CoachStageSelfExplain:
		return "Without copying the example, explain in your own words how " + topic.Focus + " changes the next engineering decision."
	case CoachStageComplete:
		return "Complete this frame for the current work: " + topic.Starter
	case CoachStagePerform:
		return "Create your own concise " + topic.Focus + " for the current work, then verify it against the spec or code."
	case CoachStageRetrieval:
		return "From memory, write the steps for " + topic.Focus + "; only then compare them with current project evidence."
	case CoachStageReflection:
		return "Name one place your use of " + topic.Focus + " changed a decision and one adjustment you will transfer to the next task."
	default:
		return "Explain " + topic.Focus + "."
	}
}

func scaffoldFor(explicitCompletions int) int {
	switch {
	case explicitCompletions <= 0:
		return 3
	case explicitCompletions == 1:
		return 2
	case explicitCompletions == 2:
		return 1
	default:
		return 0
	}
}

func masteryFor(progress CoachProgress) int {
	mastery := progress.ExplicitCompletions*15 + progress.Retrievals*10
	if mastery > 100 {
		return 100
	}
	return mastery
}

func retrievalInterval(retrievals int) time.Duration {
	switch {
	case retrievals <= 1:
		return 3 * 24 * time.Hour
	case retrievals == 2:
		return 7 * 24 * time.Hour
	default:
		return 14 * 24 * time.Hour
	}
}

func coachLessonID(skill string, stage CoachStage, completions int) string {
	return fmt.Sprintf("%s:%s:%d", skill, stage, completions)
}

func defaultCoachState(now time.Time) CoachState {
	return CoachState{
		Version:   coachStateVersion,
		Mode:      CoachModeOff,
		Progress:  map[string]CoachProgress{},
		UpdatedAt: now.UTC(),
	}
}

func (o *Orchestrator) coachStatePath() (string, error) {
	base := strings.TrimSpace(os.Getenv("MAESTRO_LEARN_DIR"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("coach: resolve home: %w", err)
		}
		base = filepath.Join(home, ".maestro", "learn")
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("coach: resolve state directory: %w", err)
	}
	project := coachProjectComponent(o.sess.Project)
	if project == "" {
		project = coachProjectComponent(projectSessionKey(o.baseDir))
	}
	if project == "" {
		return "", errors.New("coach: project identity is unavailable")
	}
	return filepath.Join(filepath.Clean(base), project, "state.json"), nil
}

func safeCoachComponent(value string) string {
	if len(value) == 0 || len(value) > 120 {
		return ""
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return ""
		}
	}
	return value
}

// coachProjectComponent keeps historical ASCII-safe keys stable. Project
// labels containing dots, spaces, or Unicode are mapped to a readable ASCII
// slug plus a hash of the complete identity, so distinct repositories cannot
// collide merely because their display labels normalize alike.
func coachProjectComponent(value string) string {
	if safe := safeCoachComponent(value); safe != "" {
		return safe
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var slug strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			dash = false
			slug.WriteRune(r)
			if slug.Len() >= 48 {
				break
			}
			continue
		}
		dash = true
	}
	label := strings.Trim(slug.String(), "-")
	if label == "" {
		label = "project"
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-%x", label, sum[:8])
}

func (o *Orchestrator) loadCoachStateLocked(ctx context.Context, now time.Time) (CoachState, error) {
	if err := ctx.Err(); err != nil {
		return CoachState{}, err
	}
	path, err := o.coachStatePath()
	if err != nil {
		return CoachState{}, err
	}
	if err := secureCoachDir(filepath.Dir(filepath.Dir(path))); err != nil {
		return CoachState{}, err
	}
	if err := secureCoachDir(filepath.Dir(path)); err != nil {
		return CoachState{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		state := defaultCoachState(now)
		if err := o.saveCoachStatePath(ctx, path, state); err != nil {
			return CoachState{}, err
		}
		return state, nil
	}
	if err != nil {
		return CoachState{}, fmt.Errorf("coach: inspect state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return CoachState{}, errors.New("coach: state is not a regular file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return CoachState{}, fmt.Errorf("coach: protect state: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return CoachState{}, fmt.Errorf("coach: read state: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, coachStateMaxSize+1))
	closeErr := f.Close()
	if readErr != nil {
		return CoachState{}, fmt.Errorf("coach: read state: %w", readErr)
	}
	if closeErr != nil {
		return CoachState{}, fmt.Errorf("coach: read state: %w", closeErr)
	}
	migrated := false
	state := CoachState{}
	if len(data) > coachStateMaxSize || decodeCoachState(data, &state) != nil {
		state = defaultCoachState(now)
		migrated = true
	} else {
		migrated = normalizeCoachState(&state, now)
	}
	if migrated {
		if err := o.saveCoachStatePath(ctx, path, state); err != nil {
			return CoachState{}, err
		}
	}
	return state, nil
}

func decodeCoachState(data []byte, state *CoachState) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(state); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing coach state data")
	}
	return nil
}

func normalizeCoachState(state *CoachState, now time.Time) bool {
	changed := false
	if state.Version != coachStateVersion {
		state.Version = coachStateVersion
		changed = true
	}
	if !state.Mode.valid() {
		state.Mode = CoachModeOff
		changed = true
	}
	cleanProgress := make(map[string]CoachProgress, len(state.Progress))
	for id, progress := range state.Progress {
		if _, known := coachTopics[id]; !known {
			changed = true
			continue
		}
		if !progress.Stage.valid() {
			progress.Stage = CoachStageObserve
			changed = true
		}
		if progress.ExplicitCompletions < 0 {
			progress.ExplicitCompletions = 0
			changed = true
		} else if progress.ExplicitCompletions > coachCounterLimit {
			progress.ExplicitCompletions = coachCounterLimit
			changed = true
		}
		if progress.Retrievals < 0 {
			progress.Retrievals = 0
			changed = true
		} else if progress.Retrievals > coachCounterLimit {
			progress.Retrievals = coachCounterLimit
			changed = true
		}
		mastery := masteryFor(progress)
		if progress.Mastery != mastery {
			progress.Mastery = mastery
			changed = true
		}
		cleanProgress[id] = progress
	}
	if state.Progress == nil || len(cleanProgress) != len(state.Progress) {
		changed = true
	}
	state.Progress = cleanProgress
	cleanIDs := make([]string, 0, min(len(state.CompletedLessonIDs), coachHistoryLimit))
	seen := map[string]struct{}{}
	for _, id := range state.CompletedLessonIDs {
		if !validCoachLessonID(id) {
			changed = true
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			changed = true
			continue
		}
		seen[id] = struct{}{}
		cleanIDs = append(cleanIDs, id)
	}
	if len(cleanIDs) > coachHistoryLimit {
		cleanIDs = cleanIDs[len(cleanIDs)-coachHistoryLimit:]
		changed = true
	}
	state.CompletedLessonIDs = cleanIDs
	if state.PendingLessonID != "" {
		progress := state.Progress[state.PendingSkillID]
		if _, known := coachTopics[state.PendingSkillID]; !known || !state.PendingStage.valid() ||
			state.PendingLessonID != coachLessonID(state.PendingSkillID, state.PendingStage, progress.ExplicitCompletions) {
			state.PendingLessonID = ""
			state.PendingSkillID = ""
			state.PendingStage = ""
			changed = true
		}
	} else if state.PendingSkillID != "" || state.PendingStage != "" {
		state.PendingSkillID = ""
		state.PendingStage = ""
		changed = true
	}
	if state.LastOfferedPhase != "" && !state.LastOfferedPhase.Valid() {
		state.LastOfferedPhase = ""
		changed = true
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = now.UTC()
		changed = true
	}
	return changed
}

func validCoachLessonID(id string) bool {
	if len(id) == 0 || len(id) > 180 {
		return false
	}
	parts := strings.Split(id, ":")
	if len(parts) != 3 {
		return false
	}
	if _, known := coachTopics[parts[0]]; !known {
		return false
	}
	return CoachStage(parts[1]).valid() && parts[2] != "" && strings.IndexFunc(parts[2], func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func (o *Orchestrator) saveCoachStateLocked(ctx context.Context, state CoachState) error {
	path, err := o.coachStatePath()
	if err != nil {
		return err
	}
	return o.saveCoachStatePath(ctx, path, state)
}

func (o *Orchestrator) saveCoachStatePath(ctx context.Context, path string, state CoachState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state.Version = coachStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("coach: encode state: %w", err)
	}
	if len(data) > coachStateMaxSize {
		return errors.New("coach: state exceeds size limit")
	}
	dir := filepath.Dir(path)
	if err := secureCoachDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("coach: state is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("coach: inspect state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".coach-state-*")
	if err != nil {
		return fmt.Errorf("coach: create state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("coach: protect state: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("coach: write state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("coach: sync state: %w", err)
	}
	if err := ctx.Err(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("coach: write state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("coach: replace state: %w", err)
	}
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

func secureCoachDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("coach: create private directory: %w", err)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("coach: inspect private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("coach: private state path is not a directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("coach: protect private directory: %w", err)
	}
	return nil
}
