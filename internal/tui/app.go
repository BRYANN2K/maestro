package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/agentcore/tools"
	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/projectprofile"
	"github.com/bryann2k/maestro/internal/proposals"
	"github.com/bryann2k/maestro/internal/session"
)

const (
	pulseSpan            = 20
	activityTickInterval = time.Second
)

// Message is one conversation line in the left pane.
type Message struct {
	Role  string // user | assistant | system
	Text  string
	State string // chat | spec | build | review | edit | docs | error
	Cards []*Card
	busy  bool
	ts    time.Time

	// Turn metadata for the finish footer "model · duration" (S6).
	Model      string
	StartedAt  time.Time
	FinishedAt time.Time

	// Render cache: finished messages render once per width (Phase 1
	// fluidity — streaming cost is O(delta), not O(transcript)).
	cachedRendered string
	cachedWidth    int
	cachedValid    bool

	// Concealed code fences (Phase 2): collapsed long blocks plus the
	// source snapshot they were parsed from.
	concealed  []concealedBlock
	concealSrc string

	// Turn thinking summary (Phase 3): the current sub-agent working line.
	think     *thinkingState
	toolCount int
}

// thinkingState is the collapsible working summary of a turn.
type thinkingState struct {
	Role      string
	Status    string // running | done | error
	Detail    string
	Note      string
	Started   time.Time
	Done      time.Time
	Expanded  bool
	Reasoning bool // provider reasoning rather than a generic sub-agent status
}

// overlayKind enumerates the overlay states.
type overlayKind int

// Overlay kinds.
const (
	overlayNone overlayKind = iota
	overlayKeymap
	overlayPalette
	overlayModelPicker
	overlayProviders
	overlaySessionPicker
	overlayPermission
	overlayEngine
	overlaySettings
	overlayAsk
	overlayAuth
	overlayDiff
	overlayWhichKey
	overlayAtFile
	overlayTimeline
	overlayAgentDetail
	overlayCheckpoints
	overlayForm
	overlayCoachMode
	overlayCoach
	overlayGit
)

// FocusTarget is the focus ring position.
type FocusTarget int

// Focus targets.
const (
	FocusInput FocusTarget = iota
	FocusViewport
	FocusSidebar
)

// Model is the root Bubble Tea model. The conversation is the primary
// surface; operational detail lives in an on-demand activity panel.
type Model struct {
	orch         *orchestrator.Orchestrator
	styles       Styles
	width        int
	height       int
	compact      bool
	activityOpen bool

	events  chan agentcore.StreamEvent
	permReq chan *permissionRequest

	messages    []*Message
	viewport    viewport.Model
	scroll      scrollbar
	input       *inputBox
	busy        bool
	cancelRun   context.CancelFunc
	cancelling  bool
	runStart    time.Time
	escapeArmed bool

	sidebar    *Sidebar
	proposals  *proposals.Store
	pending    []*Card
	cardRows   map[string]int
	cardSeq    uint64
	toolCards  map[string]*Card
	commandOut interface{ Drain() string }

	modFilesRequested  uint64
	modFilesApplied    uint64
	modFilesRunning    uint64
	modFilesInFlight   bool
	modFilesCompletion uint8
	workspaceRequest   uint64
	sessionRequest     uint64
	coachRequest       uint64
	interactionRequest uint64
	workspaceListStop  context.CancelFunc
	sessionListStop    context.CancelFunc
	coachStop          context.CancelFunc

	pulse             int
	lastStreamPulse   time.Time // event-driven polish; never schedules a repaint
	thinkSecond       int64     // last second painted into the live thinking summary
	activityTickArmed bool      // exactly one slow animation timer may be in flight
	toastTickArmed    bool      // toast expiry follows the same single-timer invariant
	perm              *PermissionQueue
	askQ              *tools.AskQueue
	pendingAsk        *tools.AskRequest
	askSel            int

	status        *statusBar
	md            *markdownRenderer
	dialogs       dialogStack
	sessionStart  time.Time
	sessionTitle  string
	chatState     string
	spacePending  bool
	slashSelected int

	focus         FocusTarget
	overlay       overlayKind
	overlayM      overlayModel
	formAction    formAction
	formBase      *projectprofile.Answers
	formProfile   *projectprofile.ProjectProfile
	formWorkspace orchestrator.WorkspaceSnapshot
	coachOffer    *coachOffer
	pendingCmd    *orchestrator.Command
	ide           *IDEState
	ideTreePct    int // IDE explorer width; the editor consumes the remaining center
	ideRailPct    int // IDE Agent/HITL companion rail width
	activeTab     Tab
	editFile      string // temp file backing ctrl+e $EDITOR

	resizing         bool
	resizeTarget     int
	resizeStartX     int
	resizeStartY     int
	resizeTreePct    int
	resizeRailPct    int
	lastResizeAt     time.Time
	pendingSelection *selectionContext
	contextRefs      []selectionContext
	followAgent      bool

	hoverMsg string
	regions  []Region

	selectionMenu     *selectionMenuState
	selectionEdit     *inputBox
	selectionAsk      *inputBox
	selectionAskCtx   *selectionContext
	selectionOverlayX int
	selectionOverlayY int
	chatRows          []chatRow
	transcriptLines   []string // width-normalized rows; scroll slices this without Lipgloss
	chatSelecting     bool
	chatAnchor        chatPoint
	chatCursor        chatPoint
	lastContent       string
	tailMode          bool
	followOutput      bool           // live output stays pinned until the user scrolls up
	blockRows         map[string]int // "msgIdx:blockIdx" → content row
	thinkRows         map[int]int    // msgIdx → content row of working summary
	inputH            int            // dynamic prompt height
	focused           bool           // terminal focus (OS notifications)
	msgRows           []int          // message → content start row (timeline)
	forceFullRender   bool           // timeline renders the whole transcript
}

// New builds the TUI model for an orchestrator.
func New(orch *orchestrator.Orchestrator, propStore *proposals.Store, perm *PermissionQueue) *Model {
	themeName := "charmtone"
	if orch != nil {
		themeName = orch.SettingsSnapshot().Theme
	}
	theme := ThemeForName(themeName)
	styles := NewStyles(theme)
	md, _ := newMarkdownRenderer(styles)
	m := &Model{
		orch:         orch,
		styles:       styles,
		activityOpen: true,
		events:       make(chan agentcore.StreamEvent, 64),
		permReq:      make(chan *permissionRequest, 4),
		viewport:     viewport.New(0, 0),
		input:        newInputBox(styles),
		sidebar:      NewSidebar(theme),
		proposals:    propStore,
		perm:         perm,
		status:       newStatusBar(),
		md:           md,
		sessionStart: time.Now(),
		sessionTitle: safeIDEPlainText(orch.Session().Title),
		cardRows:     map[string]int{},
		toolCards:    map[string]*Card{},
		blockRows:    map[string]int{},
		thinkRows:    map[int]int{},
		followOutput: true,
		followAgent:  true,
		activeTab:    TabHarness,
		ideTreePct:   15,
		ideRailPct:   20,
		chatState:    stateForPhase(string(orch.Phase())),
	}
	if perm != nil {
		perm.SetMode(orch.SettingsSnapshot().PermissionMode)
		// The gate and the Bubble Tea pump must share the exact same queue.
		// A second channel leaves Authorize waiting forever for a response.
		m.permReq = perm.req
	}
	// The ask tool blocks on the channel-backed queue until the user
	// answers the dialog; headless runs keep the nil handler error.
	if orch != nil {
		m.askQ = tools.NewAskQueue(4)
		orch.SetAsk(m.askQ.Ask)
		if output, ok := orch.Out().(interface{ Drain() string }); ok {
			m.commandOut = output
		}
	}
	// Startup preflight: an unknown configured model fails fast with a
	// clean warning (plus a catalog suggestion) instead of a raw provider
	// error dump on the first turn.
	if err := orch.ModelCheckError(); err != nil {
		m.status.pushToast("warn", truncateRunes(err.Error(), 60), 8*time.Second)
		m.messages = append(m.messages, &Message{
			Role: "system", State: "error",
			Text: "warning: " + err.Error(),
			ts:   time.Now(),
		})
	}
	return m
}

// SetSize forces the layout dimensions (used by tests and headless drivers).
func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	m.layout()
	m.renderMessages()
}

// InputValue exposes the input (demos, tests).
func (m *Model) InputValue() string { return m.input.Value() }

// InputSet replaces the input value (draft restore, demos, tests).
func (m *Model) InputSet(s string) {
	m.input.Set(s)
	m.inputChanged()
}

// LastAssistantText returns the current assistant message (tests, demos).
func (m *Model) LastAssistantText() string {
	if last := m.lastAssistant(); last != nil {
		return last.Text
	}
	return ""
}

// ---- events -------------------------------------------------------------

type streamMsg struct{ ev agentcore.StreamEvent }
type chatDoneMsg struct {
	err            error
	systemText     string
	successToast   string
	card           *Card
	sessionID      string
	sessionWorkDir string
}
type uiOperationDoneMsg struct {
	err          error
	systemText   string
	successToast string
	card         *Card
	sessionID    string
	workDir      string
	refreshFiles bool
	sessionTitle string
}
type editorFinishedMsg struct{ err error }
type modFilesMsg struct {
	files         []git.NumStat
	ideFiles      []string
	ideFilesReady bool
	revision      uint64
	workspace     orchestrator.WorkspaceSnapshot
}

const (
	modFilesCompletionEvent uint8 = 1 << iota
	modFilesCompletionWorker
)

type permRequestMsg struct{ req *permissionRequest }
type askRequestMsg struct{ req *tools.AskRequest }
type toastTickMsg struct{}
type activityTickMsg struct{ at time.Time }
type modelsRefreshedMsg struct{ err error }
type projectFormMsg struct {
	action    formAction
	profile   projectprofile.ProjectProfile
	answers   projectprofile.Answers
	workspace orchestrator.WorkspaceSnapshot
	note      string
	err       error
}
type workspaceListMsg struct {
	request    uint64
	workspaces []git.Workspace
	err        error
}
type sessionListMsg struct {
	request   uint64
	summaries []session.Summary
	err       error
}
type coachResultMsg struct {
	request     uint64
	interaction uint64
	action      string
	sessionID   string
	workspace   orchestrator.WorkspaceSnapshot
	overlay     overlayKind
	input       string
	state       orchestrator.CoachState
	lesson      *orchestrator.CoachLesson
	err         error
}
type subscriptionActionDoneMsg struct {
	provider string
	action   string
	err      error
}
type branchDisplayMsg struct{}

func (m *Model) pumpStream() {
	for ev := range m.orch.Stream {
		m.events <- ev
	}
}

func (m *Model) eventPump() tea.Cmd {
	return func() tea.Msg { return streamMsg{ev: <-m.events} }
}

func (m *Model) permPump() tea.Cmd {
	return func() tea.Msg { return permRequestMsg{req: <-m.permReq} }
}

// askPump is the single-consumer pump for the ask tool queue (one pending
// pump at a time, re-armed only after a question was consumed).
func (m *Model) askPump() tea.Cmd {
	return func() tea.Msg { return askRequestMsg{req: <-m.askQ.NextCh()} }
}

// Init starts the subscriptions.
func (m *Model) Init() tea.Cmd {
	go m.pumpStream()
	m.sidebar.refresh(m.orch)
	return tea.Batch(m.eventPump(), m.permPump(), m.askPump(), m.refreshBranchDisplay(), m.frameTicks())
}

func (m *Model) refreshBranchDisplay() tea.Cmd {
	if m.orch == nil {
		return nil
	}
	return func() tea.Msg {
		m.orch.UpdateBranchDisplay(context.Background())
		return branchDisplayMsg{}
	}
}

// frameTicks arms at most one slow activity timer. The former spinner command
// was re-issued immediately from every Update, producing a tight repaint loop
// during streams. One guarded 1 Hz timer is enough for elapsed thinking time
// and the deliberately calm orchestral phrase.
func (m *Model) frameTicks() tea.Cmd {
	var cmds []tea.Cmd
	if m.busy && !m.activityTickArmed {
		m.activityTickArmed = true
		cmds = append(cmds, tea.Tick(activityTickInterval, func(at time.Time) tea.Msg {
			return activityTickMsg{at: at}
		}))
	}
	if len(m.status.toasts) > 0 && !m.toastTickArmed {
		m.toastTickArmed = true
		cmds = append(cmds, toastTick())
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func toastTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toastTickMsg{} })
}

// arm re-arms the animation ticks after every update. It deliberately does
// NOT re-arm the event/perm pumps: they are single-consumer, re-armed only
// by the message cases that consumed one event. Re-arming them here would
// let several pump cmds block on the same channel concurrently and deliver
// stream events to Update out of order (scrambled streaming text).
func (m *Model) arm(cmds ...tea.Cmd) tea.Cmd {
	return tea.Batch(append(cmds, m.frameTicks())...)
}

// ---- Update -------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.renderMessages()
		return m, m.arm()
	case tea.KeyMsg:
		// Bracketed paste bypasses the terminal-noise detector because newlines
		// and tabs are legitimate editor/chat content. Sanitize it once at the
		// model boundary so every destination (including picker filters and API
		// key dialogs) is protected from pasted ANSI/C0/C1 control sequences.
		if msg.Paste {
			msg.Runes = []rune(sanitizePastedInput(string(msg.Runes)))
			if len(msg.Runes) == 0 {
				return m, nil
			}
		}
		if terminalNoiseKey(msg) {
			return m, nil
		}
		m.interactionRequest++
		before := m.overlay
		updated, cmd := m.updateKey(msg)
		m.cancelClosedPickerRequest(before)
		return updated, cmd
	case editorFinishedMsg:
		return m, m.handleExecFinished(msg.err)
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress {
			m.interactionRequest++
		}
		return m.updateMouse(msg)
	case tea.FocusMsg:
		m.focused = true
		return m, tea.Batch(m.arm(), m.refreshBranchDisplay())
	case tea.BlurMsg:
		m.focused = false
		return m, m.arm()
	case streamMsg:
		m.advanceStreamPulse(time.Now())
		m.handleEvent(msg.ev)
		if msg.ev.Type == agentcore.EvDone {
			// End of a turn: re-diff the working tree for the sidebar
			// "Changed" panel, off the event loop, and force a full
			// terminal repaint so no streaming diff residue survives.
			return m, tea.Batch(m.arm(), m.eventPump(), m.refreshAfterCompletion(modFilesCompletionEvent), clearScreenCmd())
		}
		return m, tea.Batch(m.arm(), m.eventPump())
	case chatDoneMsg:
		cancelled := errors.Is(msg.err, context.Canceled)
		m.busy = false
		m.cancelRun = nil
		m.cancelling = false
		m.runStart = time.Time{}
		m.sessionTitle = safeIDEPlainText(m.orch.Session().Title)
		if msg.err == nil && msg.sessionID != "" {
			m.applyLoadedSession(msg.sessionID, msg.sessionWorkDir)
		}
		// Commands run in Bubble Tea worker goroutines. Apply their UI payload
		// here, on the event loop, instead of mutating the model in the worker.
		if msg.card != nil {
			m.pending = append(m.pending, msg.card)
			m.appendSystemCard(msg.card)
		}
		if msg.systemText != "" {
			m.appendSystem(msg.systemText)
		}
		toolStatus := "done"
		if cancelled {
			toolStatus = "cancelled"
		} else if msg.err != nil {
			toolStatus = "error"
		}
		m.finalizeLastAssistantWithToolStatus(toolStatus)
		if cancelled {
			if !m.lastSystemMessageIs("task cancelled") {
				m.appendSystem("task cancelled")
			}
			m.status.pushToast("info", "task cancelled", 3*time.Second)
		} else if msg.err != nil {
			errorText := "error: " + msg.err.Error()
			if focused, ok := focusLearningError(msg.err); ok {
				errorText = focused
			}
			if !m.lastSystemMessageIs(errorText) {
				m.appendError(errorText)
			}
			m.status.pushToast("error", truncateRunes(safeIDEPlainText(msg.err.Error()), 60), 5*time.Second)
		} else if msg.successToast != "" {
			m.status.pushToast("success", msg.successToast, 2*time.Second)
		}
		m.renderMessages() // flush the full transcript (tail window ends)
		// Full repaint: clears any cell-diff residue left by streaming
		// frames (misaligned graphemes/emoji can otherwise persist).
		var coach tea.Cmd
		if msg.err == nil {
			coach = m.coachBreakpointCmd()
		}
		return m, tea.Batch(m.arm(), clearScreenCmd(), m.refreshAfterCompletion(modFilesCompletionWorker), m.refreshBranchDisplay(), coach)
	case uiOperationDoneMsg:
		m.busy = false
		m.cancelRun = nil
		m.cancelling = false
		m.runStart = time.Time{}
		if msg.sessionTitle != "" {
			m.sessionTitle = safeIDEPlainText(msg.sessionTitle)
		}
		if msg.err == nil && msg.sessionID != "" {
			m.applyLoadedSession(msg.sessionID, msg.workDir)
		}
		if msg.card != nil {
			m.pending = append(m.pending, msg.card)
			m.appendSystemCard(msg.card)
		}
		if msg.systemText != "" {
			m.appendSystem(msg.systemText)
		}
		if msg.err != nil {
			errorText := "error: " + msg.err.Error()
			if !m.lastSystemMessageIs(errorText) {
				m.appendError(errorText)
			}
			m.status.pushToast("error", truncateRunes(msg.err.Error(), 60), 5*time.Second)
		} else if msg.successToast != "" {
			m.status.pushToast("success", msg.successToast, 2*time.Second)
		}
		m.renderMessages()
		var refresh tea.Cmd
		if msg.refreshFiles {
			refresh = m.refreshModifiedFiles()
		}
		return m, tea.Batch(m.arm(), clearScreenCmd(), refresh, m.refreshBranchDisplay())
	case permRequestMsg:
		// A cancelled run can leave its already-delivered Bubble Tea message
		// behind after the gate has returned through ctx.Done. Never resurrect
		// that expired decision as an actionable dialog.
		if err := msg.req.contextErr(); err != nil {
			msg.req.Respond <- err
			return m, tea.Batch(m.arm(), m.permPump())
		}
		m.dialogs.push(newPermissionDialog(msg.req, m.perm))
		if !m.focused {
			m.orch.Notifier().Notify("Maestro", "tool permission requested: "+msg.req.Call.Name)
		}
		// Re-arm the single perm pump (one consumer at a time, like events).
		return m, tea.Batch(m.arm(), m.permPump())
	case askRequestMsg:
		m.pendingAsk = msg.req
		m.askSel = 0
		// Re-arm the single ask pump.
		return m, tea.Batch(m.arm(), m.askPump())
	case activityTickMsg:
		m.activityTickArmed = false
		m.pulse = (m.pulse + 1) % pulseSpan
		// The duration and orchestral phrase live in the active Maestro message.
		// During reasoning there may be no text deltas, so repaint that one live
		// tail once per second. The hidden transcript is never touched in IDE.
		if m.activeTab != TabIDE {
			nowSecond := msg.at.Unix()
			last := m.lastAssistant()
			if nowSecond != m.thinkSecond && last != nil && last.busy {
				m.thinkSecond = nowSecond
				attached := m.followOutput
				if attached {
					m.viewport.GotoBottom()
				}
				m.renderMessages()
				if attached {
					m.viewport.GotoBottom()
				}
			}
		}
		return m, m.arm()
	case toastTickMsg:
		m.toastTickArmed = false
		m.status.tick(time.Now())
		return m, m.arm()
	case modFilesMsg:
		return m, m.arm(m.finishModifiedFilesRefresh(msg))
	case modelsRefreshedMsg:
		if msg.err != nil {
			m.status.pushToast("error", msg.err.Error(), 4*time.Second)
		} else {
			if m.overlay == overlayProviders {
				m.overlayM = newProvidersOverlay(m.orch, "")
			} else {
				m.overlayM = newTaskModelOverlay(m.orch)
			}
			m.status.pushToast("success", "model catalog refreshed", 2*time.Second)
		}
		return m, m.arm()
	case settingsProvidersLoadedMsg:
		if m.overlay == overlaySettings && m.overlayM == msg.target &&
			msg.target.accepts(m, msg.generation, msg.workspace, msg.sessionID) {
			msg.target.finishAcceptedAction()
			msg.target.applyProviders(msg.subscriptions)
		}
		return m, m.arm()
	case settingsActionDoneMsg:
		if m.overlay == overlaySettings && m.overlayM == msg.target &&
			msg.target.accepts(m, msg.generation, msg.workspace, msg.sessionID) {
			msg.target.finishAction(m.orch, msg)
		}
		return m, m.arm()
	case projectFormMsg:
		m.busy = false
		m.cancelRun = nil
		m.runStart = time.Time{}
		if msg.err != nil {
			m.appendError("error: " + msg.err.Error())
			m.status.pushToast("error", truncateRunes(msg.err.Error(), 60), 5*time.Second)
			return m, m.arm(clearScreenCmd())
		}
		if !m.orch.WorkspaceIsCurrent(msg.workspace) {
			m.appendError("error: workspace changed while preparing the project questionnaire; run the command again")
			m.status.pushToast("error", "workspace changed — run the command again", 4*time.Second)
			return m, m.arm(clearScreenCmd())
		}
		m.openProjectForm(msg.action, msg.profile, msg.answers, msg.workspace)
		if msg.note != "" {
			m.status.pushToast("info", msg.note, 3*time.Second)
		}
		return m, m.arm(clearScreenCmd())
	case workspaceListMsg:
		if msg.request != m.workspaceRequest || m.overlay != overlayGit {
			return m, m.arm()
		}
		m.finishWorkspaceListRequest()
		if msg.err != nil {
			m.overlay = overlayNone
			m.overlayM = nil
			m.status.pushToast("error", truncateRunes(msg.err.Error(), 60), 5*time.Second)
			return m, m.arm()
		}
		m.overlayM = newWorkspacePickerOverlay(msg.workspaces, m.orch.WorkDirDisplay())
		return m, m.arm()
	case sessionListMsg:
		if msg.request != m.sessionRequest || m.overlay != overlaySessionPicker {
			return m, m.arm()
		}
		m.finishSessionListRequest()
		if msg.err != nil {
			m.overlay = overlayNone
			m.overlayM = nil
			m.status.pushToast("error", truncateRunes(msg.err.Error(), 60), 5*time.Second)
			return m, m.arm()
		}
		m.overlayM = newSessionSummaryPickerOverlay(msg.summaries, m.orch.Session().ID)
		return m, m.arm()
	case coachResultMsg:
		if msg.request != m.coachRequest {
			return m, m.arm()
		}
		m.finishCoachRequest()
		if !m.coachResultIsCurrent(msg) {
			return m, m.arm()
		}
		if msg.err != nil {
			if m.overlay == overlayCoachMode || m.overlay == overlayCoach {
				m.overlay = overlayNone
				m.overlayM = nil
			}
			errorText := "error: " + msg.err.Error()
			if focused, ok := focusLearningError(msg.err); ok {
				errorText = focused
			}
			m.appendError(errorText)
			m.status.pushToast("error", truncateRunes(safeIDEPlainText(msg.err.Error()), 60), 5*time.Second)
			return m, m.arm()
		}
		switch msg.action {
		case "menu":
			if m.overlay == overlayCoachMode {
				query := ""
				if loading, ok := m.overlayM.(*listOverlay); ok {
					query = loading.query
				}
				menu := newCoachModeOverlay(msg.state)
				menu.query = query
				menu.ensureSelectable()
				m.overlayM = menu
			}
		case "status":
			m.overlay = overlayNone
			m.overlayM = nil
			m.appendSystem(coachStatusText(msg.state))
		case "guided", "challenge", "off":
			m.overlay = overlayNone
			m.overlayM = nil
			m.setCoachLesson(msg.lesson, msg.state.Mode)
			m.appendSystem("Coach mode: " + string(msg.state.Mode))
			m.status.pushToast("success", "Coach "+string(msg.state.Mode), 2*time.Second)
		case "next":
			m.setCoachLesson(msg.lesson, msg.state.Mode)
			if m.coachOffer == nil {
				m.overlay = overlayNone
				m.overlayM = nil
				m.appendSystem("Coach: no lesson is due at this breakpoint.")
				m.status.pushToast("info", "no Coach lesson due", 2*time.Second)
			} else {
				m.overlay = overlayCoach
				m.overlayM = newCoachOverlay(*m.coachOffer)
			}
		case "done":
			m.coachOffer = nil
			m.overlay = overlayNone
			m.overlayM = nil
			m.appendSystem("Coach lesson completed explicitly.")
			m.status.pushToast("success", "lesson complete", 2*time.Second)
		case "later":
			m.coachOffer = nil
			m.overlay = overlayNone
			m.overlayM = nil
			m.appendSystem("Coach snoozed for 24 hours.")
			m.status.pushToast("info", "Coach snoozed", 2*time.Second)
		case "refresh":
			m.setCoachLesson(msg.lesson, msg.state.Mode)
		}
		return m, m.arm()
	case subscriptionActionDoneMsg:
		if msg.err != nil {
			m.status.pushToast("error", msg.provider+": "+msg.err.Error(), 5*time.Second)
		} else {
			verb := "connected"
			if msg.action == "logout" {
				verb = "signed out"
			}
			m.status.pushToast("success", msg.provider+" "+verb, 3*time.Second)
		}
		m.overlay = overlayProviders
		m.overlayM = newProvidersOverlay(m.orch, msg.provider)
		return m, m.arm()
	case branchDisplayMsg:
		return m, m.arm()
	default:
		return m, m.arm()
	}
}

// advanceStreamPulse gives real stream progress a restrained visual response
// without creating another animation loop. Bursty token deltas are capped at
// <3 Hz; the existing 1 Hz activity tick remains responsible for elapsed time
// while a model is thinking but not emitting events.
func (m *Model) advanceStreamPulse(now time.Time) {
	const minimumGap = 350 * time.Millisecond
	if m.lastStreamPulse.IsZero() || now.Sub(m.lastStreamPulse) >= minimumGap {
		m.pulse = (m.pulse + 1) % pulseSpan
		m.lastStreamPulse = now
	}
}

// handleEvent routes a stream event into the UI.
func (m *Model) handleEvent(ev agentcore.StreamEvent) {
	switch ev.Type {
	case agentcore.EvReasoningDelta:
		if rd, ok := ev.Content.(agentcore.ReasoningDelta); ok && rd.Text != "" {
			last := m.lastAssistant()
			if last == nil || !last.busy {
				m.appendAssistant("")
				last = m.lastAssistant()
			}
			last.busy = true
			created := false
			if last.think == nil {
				last.think = &thinkingState{
					Role: "orchestrator", Status: "running", Started: time.Now(), Reasoning: true,
				}
				created = true
			} else if !last.think.Reasoning {
				// Replace the generic sub-agent activity detail with the provider's
				// actual reasoning stream; mixing both produced a raw, unreadable blob.
				last.think.Reasoning = true
				last.think.Detail = ""
				last.think.Started = time.Now()
				last.think.Done = time.Time{}
				created = true
			}
			last.think.Reasoning = true
			last.think.Status = "running"
			last.think.Detail += rd.Text
			last.cachedValid = false
			// Closed reasoning is intentionally cheap: accumulate deltas without
			// rebuilding the transcript. Paint the shell once, then only stream
			// the body when the user explicitly expands it.
			if created || last.think.Expanded {
				m.scrollToBottomIfAttached()
			}
		}
	case agentcore.EvTextDelta:
		if td, ok := ev.Content.(agentcore.TextDelta); ok {
			last := m.lastAssistant()
			if last == nil || !last.busy {
				m.appendAssistant("")
				last = m.lastAssistant()
			}
			last.busy = true
			if last.think != nil && last.think.Reasoning && last.think.Status == "running" {
				last.think.Status = "done"
				last.think.Done = time.Now()
			}
			last.Text += td.Text
			m.scrollToBottomIfAttached()
		}
	case agentcore.EvToolResult:
		m.addToolCard(ev)
	case agentcore.EvToolCall:
		m.addToolCallCard(ev)
	case agentcore.EvSubAgent:
		if sa, ok := ev.Content.(agentcore.SubAgentStatus); ok {
			m.sidebar.setAgent(sa)
			last := m.lastAssistant()
			if last == nil || !last.busy {
				// A fresh assistant message opens only for a NEW running
				// sub-agent. The trailing done/error event is emitted by
				// Chat AFTER the loop's EvDone finalized the message, so it
				// must never mint an empty duplicate ("worked 0s" ghost).
				if sa.Status != "running" {
					m.renderMessages()
					return
				}
				last = &Message{Role: "assistant", State: m.chatState, busy: true, ts: time.Now()}
				m.messages = append(m.messages, last)
			}
			if last.think == nil {
				last.think = &thinkingState{Role: sa.Role, Status: sa.Status, Started: time.Now()}
			} else if last.think.Status != "running" && last.think.Role != sa.Role {
				// A new sub-agent starts after the previous one finished.
				last.think = &thinkingState{Role: sa.Role, Status: sa.Status, Started: time.Now()}
			} else {
				last.think.Status = sa.Status
				if !last.think.Reasoning || last.think.Detail == "" {
					last.think.Detail = sa.Detail
				}
				if sa.Status == "done" || sa.Status == "error" || sa.Status == "cancelled" {
					last.think.Done = time.Now()
				}
			}
			m.renderMessages()
		}
	case agentcore.EvPhaseChange:
		if pc, ok := ev.Content.(agentcore.PhaseChange); ok {
			m.chatState = stateForPhase(pc.To)
			m.appendSystem(fmt.Sprintf("phase: %s → %s", pc.From, pc.To))
			m.sidebar.refresh(m.orch)
		}
	case agentcore.EvHITL:
		if it, ok := ev.Content.(agentcore.HITLItem); ok {
			m.sidebar.setItem(it)
		}
	case agentcore.EvAdvisorNote:
		if n, ok := ev.Content.(agentcore.AdvisorNote); ok {
			m.appendSystem("advisor [" + n.Level + "]: " + n.Note)
		}
	case agentcore.EvDone:
		m.finalizeLastAssistant()
		m.renderMessages()
	case agentcore.EvError:
		if se, ok := ev.Content.(agentcore.StreamError); ok {
			m.finalizeLastAssistantWithToolStatus("error")
			m.appendError("error: " + safeIDEPlainText(se.Message))
			m.status.pushToast("error", truncateRunes(safeIDEPlainText(se.Message), 60), 5*time.Second)
		}
	}
}

// addToolCard renders a tool result as a card; writes become proposals.
func (m *Model) addToolCard(ev agentcore.StreamEvent) {
	tr, ok := ev.Content.(agentcore.ToolResult)
	if !ok {
		return
	}
	card := m.toolCards[tr.ID]
	if card == nil {
		card = &Card{ID: m.nextCardID(), Name: tr.Name}
		m.attachToolCard(card)
		if tr.ID != "" {
			m.toolCards[tr.ID] = card
		}
	}
	card.Name = tr.Name
	card.Status = cardStatus(tr)
	if card.Status == "error" {
		card.Detail = tr.Err
	} else if tr.Output != "" {
		// Keep the document body captured from the write call. The result is
		// usually only a staging confirmation and must not replace expandable
		// content with raw protocol text.
		if card.Kind == "write" {
			if card.Detail == "" {
				card.Detail = filepathOf(tr.Output)
			}
			if card.Full == "" {
				card.Full = tr.Output
			}
		} else {
			card.Detail = summarizeOutput(tr.Output)
			card.Full = tr.Output
		}
	}
	if tr.Name == "write" && strings.Contains(tr.Output, "staged") {
		card.Kind = "write"
		card.Status = "proposed"
		card.ProposalPath = filepathOf(tr.Output)
		if id := proposalIDFrom(tr.Output); id != "" {
			if prop, err := m.proposals.Load(id); err == nil {
				card.Proposal = &prop
			}
		}
		if !containsCard(m.pending, card) {
			m.pending = append(m.pending, card)
			// A new staged write invalidates the previous diff decision. The
			// item will be completed again when every pending write is accepted
			// or discarded.
			m.setHITLChecked("diff", false)
		}
	} else {
		if card.Kind == "" {
			card.Kind = "tool"
		}
	}
	if card.Status != "running" && tr.ID != "" {
		delete(m.toolCards, tr.ID)
	}
	m.scrollToBottomIfAttached()
}

func (m *Model) addToolCallCard(ev agentcore.StreamEvent) {
	tc, ok := ev.Content.(agentcore.ToolCall)
	if !ok {
		return
	}
	if tc.ID != "" {
		if _, exists := m.toolCards[tc.ID]; exists {
			return
		}
	}
	// A provider may call a tool before emitting answer text. Keep that
	// activity under Maestro instead of leaking it as a raw System message.
	last := m.lastAssistant()
	if last == nil || !last.busy {
		m.appendAssistant("")
		last = m.lastAssistant()
		last.busy = true
	}
	kind, detail, full := toolCallPresentation(tc.Name, tc.Args)
	card := &Card{
		ID:     m.nextCardID(),
		Kind:   kind,
		Name:   tc.Name,
		Status: "running",
		Detail: detail,
		Full:   full,
	}
	if tc.ID != "" {
		m.toolCards[tc.ID] = card
	}
	m.attachToolCard(card)
	if m.activeTab == TabIDE && m.followAgent {
		if path, line, column := toolCallLocation(tc.Args); path != "" {
			m.openWorkspaceLocation(path, line, column, false)
		}
	}
	m.scrollToBottomIfAttached()
}

func toolCallLocation(args string) (path string, line, column int) {
	var payload map[string]any
	if json.Unmarshal([]byte(args), &payload) != nil {
		return "", 0, 0
	}
	for _, key := range []string{"path", "file", "file_path", "filename"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			path, line, column = parseWorkspaceLocation(value)
			break
		}
	}
	readNumber := func(keys ...string) int {
		for _, key := range keys {
			switch value := payload[key].(type) {
			case float64:
				return int(value)
			case string:
				var parsed int
				if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
					return parsed
				}
			}
		}
		return 0
	}
	if explicit := readNumber("line", "line_number", "start_line"); explicit > 0 {
		line = explicit
	}
	if explicit := readNumber("column", "col"); explicit > 0 {
		column = explicit
	}
	return
}

func toolCallPresentation(name, args string) (kind, detail, full string) {
	kind = "tool"
	detail = summarizeOutput(args)
	full = args
	if name != "write" && name != "edit" && name != "patch" && name != "write_file" {
		return
	}
	kind = "write"
	var payload map[string]any
	if json.Unmarshal([]byte(args), &payload) != nil {
		return
	}
	for _, key := range []string{"path", "file", "filename"} {
		if value, ok := payload[key].(string); ok && value != "" {
			detail = compactWorkspacePath(value)
			break
		}
	}
	for _, key := range []string{"content", "text", "patch"} {
		if value, ok := payload[key].(string); ok && value != "" {
			full = value
			break
		}
	}
	return
}

func (m *Model) nextCardID() string {
	m.cardSeq++
	return fmt.Sprintf("card-%d", m.cardSeq)
}

func (m *Model) attachToolCard(card *Card) {
	if last := m.lastAssistant(); last != nil && last.busy {
		last.Cards = append(last.Cards, card)
		last.toolCount++
		last.cachedValid = false
		return
	}
	m.appendSystemCard(card)
}

func containsCard(cards []*Card, target *Card) bool {
	for _, card := range cards {
		if card == target {
			return true
		}
	}
	return false
}

func cardStatus(tr agentcore.ToolResult) string {
	if tr.Err != "" {
		return "error"
	}
	return "done"
}

// groupedCard renders a verb-grouped card ("3 × read") from its first card.
func groupedCard(first *Card, n int) *Card {
	g := *first
	g.ID = fmt.Sprintf("%s-group", first.ID)
	if n > 1 {
		g.Name = fmt.Sprintf("%d × %s", n, first.Name)
	}
	g.Expanded = false
	g.Proposal = nil
	return &g
}

func summarizeOutput(out string) string {
	out = strings.TrimSpace(out)
	if i := strings.IndexByte(out, '\n'); i > 0 {
		out = out[:i] + " …"
	}
	return out
}

func filepathOf(out string) string {
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "→" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], "()")
		}
	}
	return ""
}

func proposalIDFrom(out string) string {
	fields := strings.Fields(out)
	if len(fields) > 1 && fields[0] == "staged" {
		return fields[1]
	}
	return ""
}

func (m *Model) appendUser(text string) {
	m.messages = append(m.messages, &Message{Role: "user", Text: text, State: m.chatState, ts: time.Now()})
	m.renderMessages()
}

func (m *Model) appendAssistant(text string) {
	m.messages = append(m.messages, &Message{
		Role: "assistant", Text: text, State: m.chatState, ts: time.Now(),
		Model: m.orch.ActiveModel(), StartedAt: time.Now(),
	})
	m.renderMessages()
}

func (m *Model) appendSystem(text string) {
	m.messages = append(m.messages, &Message{Role: "system", Text: text, State: m.chatState, ts: time.Now()})
	m.renderMessages()
}

// appendError gives an error its own presentation state without replacing the
// current conversation phase. Severity belongs to the message: letting it
// escape into chatState labels every later user, system, and assistant message
// as ERROR until another stream event happens to change the phase.
func (m *Model) appendError(text string) {
	m.messages = append(m.messages, &Message{Role: "system", Text: text, State: "error", ts: time.Now()})
	m.renderMessages()
}

func (m *Model) lastSystemMessageIs(text string) bool {
	if len(m.messages) == 0 {
		return false
	}
	last := m.messages[len(m.messages)-1]
	return last.Role == "system" && last.Text == text
}

func (m *Model) appendSystemCard(card *Card) {
	m.messages = append(m.messages, &Message{Role: "system", State: m.chatState, Cards: []*Card{card}, ts: time.Now()})
	m.renderMessages()
}

func (m *Model) lastAssistant() *Message {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == "assistant" {
			return m.messages[i]
		}
	}
	return nil
}

// finalizeLastAssistant closes an in-flight assistant turn on terminal
// paths that bypass EvDone (errors, cancellation, cmd completion): the
// message leaves the busy state so glamour renders the accumulated text,
// the finish footer appears, and the render cache refreshes.
func (m *Model) finalizeLastAssistant() {
	m.finalizeLastAssistantWithToolStatus("done")
}

// finalizeLastAssistantWithToolStatus closes the transcript and every tool
// card owned by the turn. Providers are not all equally detailed: some end a
// subprocess with EvDone without emitting a matching EvToolResult. A terminal
// turn must never retain a live spinner in either case.
func (m *Model) finalizeLastAssistantWithToolStatus(toolStatus string) {
	last := m.lastAssistant()
	if last == nil {
		return
	}
	if toolStatus != "error" && toolStatus != "cancelled" {
		toolStatus = "done"
	}
	toolChanged := false
	for _, card := range last.Cards {
		if card.Status != "running" {
			continue
		}
		card.Status = toolStatus
		toolChanged = true
		for id, pending := range m.toolCards {
			if pending == card {
				delete(m.toolCards, id)
			}
		}
	}
	if toolChanged {
		last.cachedValid = false
	}
	if last.busy {
		last.busy = false
		if last.FinishedAt.IsZero() {
			last.FinishedAt = time.Now()
		}
		last.cachedValid = false
		if last.think != nil && (last.think.Status == "running" || toolStatus == "cancelled" || toolStatus == "error") {
			last.think.Status = toolStatus
			last.think.Done = time.Now()
		}
	}
	m.renderMessages()
}

func (m *Model) scrollToBottomIfAttached() {
	if m.activeTab == TabIDE {
		return
	}
	if m.followOutput {
		// Reattach against the old content before rebuilding; AtBottom becomes
		// transiently false as soon as a streamed line changes total height.
		m.viewport.GotoBottom()
		m.renderMessages()
		m.viewport.GotoBottom()
		return
	}
	// Keep an intentionally-scrolled transcript current without moving it.
	m.renderMessages()
}

// renderMessages re-renders the message stream into the viewport.
//
// While a turn is streaming and the viewport is attached to the bottom,
// only a tail window (≈2 viewport heights) is rendered — finished messages
// are served from their render cache, so each text delta costs O(window)
// instead of O(transcript). Scrolling up or finishing the turn renders the
// full transcript.
func (m *Model) renderMessages() {
	// The transcript is not visible in the IDE. Re-rendering Glamour and the
	// complete chat row map for every streaming token made editor scrolling
	// contend with agent output. The accumulated messages remain authoritative
	// and are rebuilt once when the user returns to the Agent tab.
	if m.activeTab == TabIDE {
		m.sidebar.queue = len(m.pending)
		return
	}
	width := m.viewport.Width
	if width <= 0 {
		width = 60
	}
	start := m.messagesWindowStart(width)
	if m.forceFullRender {
		start = 0
	}
	m.tailMode = start > 0
	var b strings.Builder
	row := 0
	m.cardRows = map[string]int{}
	m.blockRows = map[string]int{}
	m.thinkRows = map[int]int{}
	m.chatRows = nil
	m.msgRows = m.msgRows[:0]
	if m.tailMode {
		marker := m.styles.MessageMuted.Render(fmt.Sprintf("↑ %d older message(s) — pgup to scroll", start))
		b.WriteString(marker + "\n")
		m.chatRows = append(m.chatRows, chatRow{})
		row++
	}
	for i := start; i < len(m.messages); i++ {
		msg := m.messages[i]
		rendered := m.renderRoleMessage(msg, width-2)
		startRow := row
		m.msgRows = append(m.msgRows, startRow)
		line := rendered + "\n"
		plainLines := strings.Split(ansi.Strip(rendered), "\n")
		rawLines := strings.Split(msg.Text, "\n")
		// Rendered rows: row 0 is the role header, then the thinking block
		// (if any), then the body, then the finish footer (if any). Map
		// every row to the raw text line it belongs to so click/hover
		// selection aligns with the source (B6).
		thinkH := 0
		if msg.think != nil && msg.think.Role != "" {
			thinkH = lipgloss.Height(renderThinking(m.styles, m.pulse, msg.think, msg.toolCount, width-2))
		}
		footerH := 0
		if m.finishFooter(msg) != "" {
			footerH = 1
		}
		pCount := 0
		for li := range plainLines {
			textLine := -1
			if li >= 1+thinkH && li < len(plainLines)-footerH {
				textLine = li - 1 - thinkH
			}
			if textLine >= len(rawLines) {
				textLine = len(rawLines) - 1
			}
			m.chatRows = append(m.chatRows, chatRow{Message: msg, TextLine: textLine})
			if strings.Contains(plainLines[li], "[code ·") {
				// The p-th placeholder is the p-th collapsed block of the
				// message (expanded blocks emit no placeholder).
				m.blockRows[fmt.Sprintf("%d:%d", i, pCount)] = startRow + li
				pCount++
			}
		}
		b.WriteString(line)
		row += strings.Count(line, "\n")
		if msg.think != nil && msg.think.Role != "" {
			// The working summary sits on the first content row (after the
			// role header) whenever it is present.
			m.thinkRows[i] = startRow + 1
		}
		for ci := 0; ci < len(msg.Cards); ci++ {
			card := msg.Cards[ci]
			if card.Status == "done" && ci+1 < len(msg.Cards) && msg.Cards[ci+1].Name == card.Name && msg.Cards[ci+1].Status == "done" {
				run := 1
				for ci+run < len(msg.Cards) && msg.Cards[ci+run].Name == card.Name && msg.Cards[ci+run].Status == "done" {
					run++
				}
				card = groupedCard(card, run)
				ci += run - 1
			}
			cardLine := card.Render(m.styles, width-2) + "\n"
			b.WriteString(cardLine)
			lines := strings.Count(cardLine, "\n")
			for j := 0; j < lines; j++ {
				m.chatRows = append(m.chatRows, chatRow{})
			}
			m.cardRows[card.ID] = row + lines - 1
			row += lines
		}
		b.WriteString("\n")
		m.chatRows = append(m.chatRows, chatRow{})
		row++
	}
	if len(m.messages) == 0 {
		b.WriteString(welcomeMessage(m.styles, width-2))
	}
	content := strings.TrimSuffix(b.String(), "\n")
	if content != m.lastContent {
		m.viewport.SetContent(content)
		m.transcriptLines = normalizeTranscriptLines(content, m.viewport.Width)
		m.lastContent = content
	}
	m.sidebar.queue = len(m.pending)
	if m.followOutput {
		m.viewport.GotoBottom()
	}
	m.scroll.set(m.viewport.Height, m.viewport.Height, m.viewport.TotalLineCount(), m.viewport.YOffset)
}

// messagesWindowStart returns the first message index to render. Zero means
// the full transcript. See renderMessages.
func (m *Model) messagesWindowStart(width int) int {
	if !m.streaming() || !m.viewport.AtBottom() || len(m.messages) <= 4 {
		return 0
	}
	limit := max(m.viewport.Height*2, 40)
	rows := 0
	for i := len(m.messages) - 1; i >= 0; i-- {
		rows += m.estimateMessageRows(m.messages[i], width-2)
		if rows >= limit {
			return i
		}
	}
	return 0
}

// estimateMessageRows cheaply bounds the rendered height of a message
// without rendering it (used to size the streaming tail window).
func (m *Model) estimateMessageRows(msg *Message, width int) int {
	rows := 2 // role header + trailing blank
	text := msg.Text
	if w := max(width, 1); len(text) > 0 {
		newlines := strings.Count(text, "\n") + 1
		wrapped := (len([]rune(text)) + w - 1) / w
		rows += max(newlines, wrapped)
	}
	rows += len(msg.Cards) * 3
	return rows
}

// streaming reports whether a turn is still producing output.
func (m *Model) streaming() bool {
	if m.busy {
		return true
	}
	if last := m.lastAssistant(); last != nil {
		return last.busy
	}
	return false
}

// ---- keyboard -----------------------------------------------------------

// terminalNoiseKey reports whether a key message is terminal control noise
// that must never reach the editor, chat input, or overlay filters. When the
// terminal splits or variants a mouse/CSI sequence, bubbletea can fall back
// to Alt+runes (e.g. Alt+"[<35;10;10M") — or plain runes when the ESC was
// consumed separately — which would otherwise be inserted as visible
// characters on every mouse move. Legitimate Alt bindings (workspace tabs and
// IDE pane resizing) pass through; arbitrary Alt-runes remain suspect.
func terminalNoiseKey(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes {
		return false
	}
	// Bubble Tea has already framed bracketed paste explicitly. Its text may
	// legitimately contain newlines and tabs; destination widgets sanitize
	// controls before rendering or storing it.
	if msg.Paste {
		return false
	}
	if msg.Alt {
		switch string(msg.Runes) {
		case "1", "2", "←", "→", "↑", "↓":
			// Keep the documented workspace and IDE resize bindings usable.
		default:
			return true
		}
	}
	for _, r := range msg.Runes {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	// SGR mouse payloads delivered without their ESC prefix. Some terminal
	// adapters also drop the CSI '[' byte, concatenate several reports, or
	// leave only the numeric tail. Treat the message as noise only when the
	// whole payload is made of reports; embedded reports are stripped by the
	// input filter before a text widget sees them.
	s := string(msg.Runes)
	if stripTerminalReports(s) == "" && mouseReportRe.MatchString(s) {
		return true
	}
	if csiPayloadRe.MatchString(s) || ((strings.HasPrefix(s, "[<") || strings.HasPrefix(s, "<")) && strings.Contains(s, ";")) {
		return true
	}
	return false
}

// csiPayloadRe matches a CSI/SGR payload shape ("[<35;10;10M" or "[0;10;10M")
// that is never legitimate typed text.
var csiPayloadRe = regexp.MustCompile(`^(?:\[?<)?[0-9]+;[0-9]+;[0-9]+[Mm]$`)

// mouseReportRe finds complete SGR/X10-shaped reports after Bubble Tea or a
// terminal proxy has partially consumed their control prefix. The optional
// leading m covers the release byte occasionally glued to the next report.
var mouseReportRe = regexp.MustCompile(`(?:\x1b)?(?:\[)?<[0-9]+;[0-9]+;[0-9]+[Mm]|(?:\x1b)?\[[0-9]+;[0-9]+;[0-9]+[Mm]|(?:[mM])?[0-9]+;[0-9]+;[0-9]+[Mm]`)

func stripTerminalReports(s string) string {
	return mouseReportRe.ReplaceAllString(s, "")
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Esc Esc is the universal task abort gesture. The first press still does
	// its normal local job (close an overlay, dismiss a picker, etc.); a second
	// consecutive press cancels the active run. Any other key disarms it.
	if msg.Type == tea.KeyEsc {
		if m.escapeArmed && m.busy && m.cancelRun != nil {
			m.escapeArmed = false
			return m.cancelActiveTask(), nil
		}
		m.escapeArmed = m.busy && m.cancelRun != nil
		if m.escapeArmed {
			m.status.pushToast("info", "press esc again to cancel task", 2*time.Second)
		}
	} else {
		m.escapeArmed = false
	}
	// Below the minimum usable canvas the regular Agent/IDE widgets are
	// intentionally hidden. Keep only the controls that remain safe without a
	// visible focus target: quit, the compact help card, workspace switching,
	// and the existing Esc-Esc cancellation gesture above.
	if m.terminalTooSmall() {
		if msg.Type == tea.KeyCtrlQ {
			return m, tea.Quit
		}
		if msg.Type == tea.KeyEsc {
			if settings, ok := m.overlayM.(*settingsOverlay); ok && m.overlay == overlaySettings {
				// Settings owns a transactional theme preview. Even when the
				// terminal is too small to draw the overlay, closing it must run
				// the same rollback path as a normally sized Escape.
				settings.close(m)
			} else {
				m.cancelOverlayRequest(m.overlay)
				m.overlay = overlayNone
				m.overlayM = nil
			}
			m.spacePending = false
			return m, nil
		}
		if m.spacePending {
			m.spacePending = false
			if msg.Type == tea.KeyRunes && msg.String() == "?" {
				m.overlay = overlayKeymap
				return m, nil
			}
		}
		if msg.Type == tea.KeySpace {
			m.spacePending = true
			return m, nil
		}
		if tab, ok := tabForKey(msg); ok {
			m.switchTab(tab)
		}
		return m, nil
	}
	if m.pendingAsk != nil {
		return m.updateAskKey(msg)
	}
	if !m.dialogs.empty() {
		return m.updateDialogKey(msg)
	}
	if m.selectionEdit != nil {
		return m.updateSelectionEditKey(msg)
	}
	if m.selectionAsk != nil {
		return m.updateSelectionAskKey(msg)
	}
	if m.selectionMenu != nil {
		return m.updateSelectionMenuKey(msg)
	}
	if m.overlay != overlayNone {
		return m.updateOverlayKey(msg)
	}
	if m.isCtrlTab(msg) {
		m.cycleTab()
		return m, nil
	}
	if tab, ok := tabForKey(msg); ok {
		m.switchTab(tab)
		return m, nil
	}
	// Proposal decisions are valid from the empty Agent composer and from the
	// IDE's HITL rail. Previously those focused widgets swallowed `a`/`d`
	// even though the proposal card advertised the shortcuts.
	if msg.Type == tea.KeyRunes && m.proposalShortcutAvailable() {
		switch msg.String() {
		case "a":
			return m.acceptLatestPending(), m.refreshModifiedFiles()
		case "d":
			return m.discardLatestPending(), nil
		case "[":
			return m.cycleProposalHunk(-1), nil
		case "]":
			return m.cycleProposalHunk(1), nil
		case "{":
			return m.cycleProposal(-1), nil
		case "}":
			return m.cycleProposal(1), nil
		}
	}
	if m.activeTab == TabIDE && msg.Type == tea.KeyCtrlL {
		m.overlay = overlayModelPicker
		m.overlayM = newTaskModelOverlay(m.orch)
		return m, nil
	}
	if m.activeTab == TabIDE && m.ide != nil {
		cmd, consumed := m.ide.Update(m, msg)
		if consumed {
			return m, cmd
		}
		if m.ide.Focus == ideChat {
			if msg.Type == tea.KeyEnter && m.input.Value() != "" {
				return m, m.send()
			}
			m.input.update(msg)
			m.inputChanged()
			return m, nil
		}
		return m, nil
	}
	// Bubble Tea represents a physical spacebar as KeySpace, not KeyRunes.
	// Route it directly when the real Agent activity rail owns focus.
	if msg.Type == tea.KeySpace && m.focus == FocusSidebar {
		m.toggleSelectedHITL()
		return m, nil
	}
	if model, cmd, consumed := m.updateSlashPreviewKey(msg); consumed {
		return model, cmd
	}
	if m.spacePending {
		m.spacePending = false
		if msg.Type == tea.KeyRunes && msg.String() == "?" {
			m.overlay = overlayKeymap
			return m, nil
		}
		if m.focus == FocusInput {
			m.input.update(tea.KeyMsg{Type: tea.KeySpace})
			m.inputChanged()
		}
	}
	if msg.Type == tea.KeySpace && m.focus == FocusInput && m.input.Value() == "" {
		// Leader preview: space alone opens the which-key group; the next
		// key runs the chosen command (space ? keys, space t theme, …).
		m.spacePending = true
		m.overlay = overlayWhichKey
		return m, nil
	}
	// Non-TTY input (pipes, harnesses) groups \r with runes; split at each
	// \r: text segments insert, each \r sends.
	if msg.Type == tea.KeyRunes && strings.ContainsRune(string(msg.Runes), '\r') {
		parts := strings.Split(string(msg.Runes), "\r")
		for i, part := range parts {
			if part != "" {
				m.input.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(part)})
				m.inputChanged()
				m.syncSlashPreview()
			}
			if i < len(parts)-1 && m.input.Value() != "" {
				return m, m.send()
			}
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		// Sidebar HITL navigation with j/k when the sidebar is focused.
		if m.focus == FocusSidebar {
			switch msg.String() {
			case "j":
				m.sidebar.moveSelection(1)
				return m, nil
			case "k":
				m.sidebar.moveSelection(-1)
				return m, nil
			case " ":
				m.toggleSelectedHITL()
				return m, nil
			}
		}
		switch string(msg.Runes) {
		case "v":
			if m.focus != FocusInput {
				return m.toggleNextConcealed(), nil
			}
		case "t":
			if m.focus != FocusInput {
				return m.toggleLastThinking(), nil
			}
		}
	}
	action, ok := ActionFor(msg)
	if !ok {
		if m.focus == FocusInput {
			m.input.update(msg)
			m.inputChanged()
			m.syncSlashPreview()
			if m.activeTab == TabHarness {
				m.maybeOpenAtFile()
			}
		}
		return m, nil
	}
	switch action {
	case ActionSend:
		if m.focus != FocusInput {
			return m, nil
		}
		value := m.input.Value()
		if strings.HasSuffix(value, "\\") {
			// Trailing backslash = literal newline: strip it and keep editing
			// (opencode convention).
			m.input.Set(strings.TrimSuffix(value, "\\"))
			m.input.insertNewline()
			m.inputChanged()
			return m, nil
		}
		if value == "" {
			return m, nil
		}
		return m, m.send()
	case ActionNewline:
		if m.focus == FocusInput {
			m.input.insertNewline()
			m.inputChanged()
			m.syncSlashPreview()
		}
		return m, nil
	case ActionScrollUp:
		m.followOutput = false
		m.viewport.ScrollUp(3)
		if m.tailMode {
			m.renderMessages() // leaving the streaming tail renders the full transcript
		}
		return m, nil
	case ActionScrollDown:
		m.viewport.ScrollDown(3)
		if m.tailMode {
			m.renderMessages()
		}
		if m.viewport.AtBottom() {
			m.followOutput = true
		}
		return m, nil
	case ActionFocusNext:
		focusCount := 2
		if m.showActivityRail() {
			focusCount = 3
		}
		m.focus = FocusTarget((int(m.focus) + 1) % focusCount)
		return m, nil
	case ActionToggleActivity:
		m.toggleActivity()
		return m, nil
	case ActionPalette:
		m.overlay = overlayPalette
		m.overlayM = newPaletteOverlay(m.orch)
		return m, nil
	case ActionModelPicker:
		m.overlay = overlayModelPicker
		m.overlayM = newTaskModelOverlay(m.orch)
		return m, nil
	case ActionTimeline:
		return m.openTimeline(), nil
	case ActionEditExternal:
		return m, m.openEditor()
	case ActionSessionPicker:
		return m, m.openSessionPicker()
	case ActionCancelTour:
		if m.busy && m.cancelRun != nil {
			return m.cancelActiveTask(), nil
		}
		return m, tea.Quit
	case ActionQuit:
		return m, tea.Quit
	case ActionKeymap:
		m.overlay = overlayKeymap
		return m, nil
	case ActionEscape:
		return m, nil
	case ActionToggleHITL:
		m.toggleSelectedHITL()
		return m, nil
	case ActionSidebarUp:
		if m.focus == FocusSidebar {
			m.sidebar.moveSelection(-1)
		}
		return m, nil
	case ActionSidebarDown:
		if m.focus == FocusSidebar {
			m.sidebar.moveSelection(1)
		}
		return m, nil
	case ActionApprovePerm, ActionDenyPerm:
		return m, nil
	}
	return m, nil
}

func (m *Model) cancelActiveTask() tea.Model {
	if m.cancelRun == nil {
		return m
	}
	m.cancelRun()
	m.cancelRun = nil
	// Permission prompts belong to the run being cancelled. Resolve them
	// fail-closed and remove their hit targets before the terminal can resize
	// back from the tiny recovery screen.
	m.rejectPermissionDialogs(context.Canceled)
	// Cancellation is cooperative. Keep the model busy until the worker sends
	// its chatDoneMsg; otherwise a second run can start while the first one is
	// still unwinding and its late completion can corrupt the new run's UI.
	m.cancelling = true
	m.appendSystem("cancellation requested")
	m.status.pushToast("info", "cancelling task…", 2*time.Second)
	return m
}

// updateDialogKey routes keys to the top dialog.
func (m *Model) updateDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d, _ := m.dialogs.top()
	pd, ok := d.(*permissionDialog)
	if !ok {
		m.dialogs.pop()
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.finishPermission(pd, fmt.Errorf("denied"))
	case tea.KeyLeft, tea.KeyShiftTab:
		pd.move(-1)
	case tea.KeyRight, tea.KeyTab:
		pd.move(1)
	case tea.KeyEnter:
		m.finishPermission(pd, pd.resolve())
	case tea.KeyRunes:
		switch msg.String() {
		case "h":
			pd.move(-1)
			return m, nil
		case "l":
			pd.move(1)
			return m, nil
		case "a":
			pd.buttonSel = 0
		case "s":
			pd.buttonSel = 1
		case "d":
			pd.buttonSel = 2
		}
		if msg.String() == "a" || msg.String() == "s" || msg.String() == "d" {
			m.finishPermission(pd, pd.resolve())
		}
	}
	return m, m.arm()
}

func (m *Model) finishPermission(dialog *permissionDialog, err error) {
	if dialog == nil || dialog.resolved {
		return
	}
	dialog.resolved = true
	dialog.buttons = nil
	dialog.req.Respond <- err
	m.dialogs.remove(dialog)
}

func (m *Model) rejectPermissionDialogs(err error) {
	// Copy because finishPermission removes each matching item from the stack.
	for _, item := range append([]dialog(nil), m.dialogs.items...) {
		if permission, ok := item.(*permissionDialog); ok {
			m.finishPermission(permission, err)
		}
	}
}

// updateAskKey handles the structured-question dialog (ask tool): arrows +
// enter pick an option, digits jump, esc cancels (the tool receives the
// answer and the loop resumes).
func (m *Model) updateAskKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.pendingAsk
	if r == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyUp, tea.KeyShiftTab:
		m.askSel = (m.askSel + len(r.Options) - 1) % len(r.Options)
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		m.askSel = (m.askSel + 1) % len(r.Options)
		return m, nil
	case tea.KeyEnter, tea.KeySpace:
		m.askQ.Answer(r, m.askSel)
		m.pendingAsk = nil
		return m, m.arm()
	case tea.KeyEsc:
		m.askQ.Answer(r, -1)
		m.pendingAsk = nil
		return m, m.arm()
	case tea.KeyRunes:
		if len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '9' {
			idx := int(msg.Runes[0] - '1')
			if idx < len(r.Options) {
				m.askQ.Answer(r, idx)
				m.pendingAsk = nil
				return m, m.arm()
			}
		}
		return m, nil
	}
	return m, nil
}

// renderAskDialog overlays the structured question centered on the body.
func (m *Model) renderAskDialog(body string) string {
	r := m.pendingAsk
	if r == nil {
		return body
	}
	var b strings.Builder
	contentWidth := min(max(m.width-10, 8), 52)
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(m.styles.T.Color(TokenCharple)).Render("Question") + "\n\n")
	b.WriteString(m.styles.MessageAssistant.Width(contentWidth).MaxWidth(contentWidth).Render(r.Question) + "\n\n")
	for i, opt := range r.Options {
		marker := "  "
		style := m.styles.SidebarItem
		if i == m.askSel {
			marker, style = "▸ ", m.styles.SidebarActive
		}
		rec := ""
		if i == r.Recommended {
			rec = "  " + m.styles.Hint.Render("(recommended)")
		}
		line := clampANSIWidth(fmt.Sprintf(" %s %d. %s%s", marker, i+1, opt, rec), contentWidth)
		b.WriteString(style.Width(contentWidth).MaxWidth(contentWidth).Render(line) + "\n")
	}
	b.WriteString("\n" + m.styles.Hint.Render("↑/↓ select · enter answer · esc cancel"))
	box := m.styles.Dialog.Render(b.String())
	box = addShadow(box, m.styles.T.Color(TokenPanel))
	overlayW, overlayH := lipgloss.Width(box), lipgloss.Height(box)
	x := max((m.width-overlayW)/2, 0)
	y := max((m.bodyHeight()-overlayH)/2, 0)
	return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
}

func (m *Model) updateOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay {
	case overlayForm:
		form, ok := m.overlayM.(*formOverlay)
		if !ok {
			m.overlay = overlayNone
			m.formAction = formActionNone
			return m, nil
		}
		submitted, cancelled := form.update(msg)
		if cancelled {
			m.overlay = overlayNone
			m.overlayM = nil
			m.formAction = formActionNone
			m.formBase = nil
			m.formProfile = nil
			m.formWorkspace = orchestrator.WorkspaceSnapshot{}
			return m, nil
		}
		if submitted {
			action := m.formAction
			values := form.values()
			m.overlay = overlayNone
			m.overlayM = nil
			m.formAction = formActionNone
			return m, m.submitForm(action, values)
		}
		return m, nil
	case overlayCoach:
		coach, ok := m.overlayM.(*coachOverlay)
		if !ok {
			m.overlay = overlayNone
			return m, nil
		}
		switch coach.update(msg) {
		case coachStart:
			m.overlay = overlayNone
			m.overlayM = nil
			m.switchTab(TabHarness)
			prompt := coach.offer.Composer
			if prompt == "" {
				prompt = coach.offer.Prompt
			}
			m.input.Set(prompt)
			m.inputChanged()
			m.focus = FocusInput
			return m, nil
		case coachComplete:
			m.overlay = overlayNone
			m.overlayM = nil
			return m, m.runCoachAction("done", "", coach.offer.ID)
		case coachSnooze:
			m.overlay = overlayNone
			m.overlayM = nil
			return m, m.runCoachAction("later", "", coach.offer.ID)
		case coachClose:
			m.overlay = overlayNone
			m.overlayM = nil
		}
		return m, nil
	case overlayKeymap:
		if msg.Type == tea.KeyEsc || msg.Type == tea.KeySpace {
			m.overlay = overlayNone
		}
	case overlayPermission:
		// replaced by dialogs
	case overlaySettings:
		if settings, ok := m.overlayM.(*settingsOverlay); ok {
			return m, settings.update(m, msg)
		}
	case overlayAuth:
		if auth, ok := m.overlayM.(*authOverlay); ok {
			return m, auth.update(m, msg)
		}
	case overlayModelPicker:
		if workspace, ok := m.overlayM.(*taskModelOverlay); ok {
			return m, workspace.update(m, msg)
		}
	case overlayProviders:
		if providers, ok := m.overlayM.(*providersOverlay); ok {
			return m, providers.update(m, msg)
		}
	case overlayDiff:
		if diff, ok := m.overlayM.(*diffOverlay); ok {
			switch msg.Type {
			case tea.KeyEsc:
				m.overlay = overlayNone
			case tea.KeyUp, tea.KeyCtrlU:
				diff.scrollBy(-1)
			case tea.KeyDown, tea.KeyCtrlD:
				diff.scrollBy(1)
			case tea.KeyPgUp:
				diff.scrollBy(-10)
			case tea.KeyPgDown:
				diff.scrollBy(10)
			case tea.KeyRunes:
				switch msg.String() {
				case "i":
					m.openProposalInIDE(diff.prop)
					return m, nil
				case "a":
					m.overlay = overlayNone
					return m.acceptPendingProposal(diff.prop), m.refreshModifiedFiles()
				case "d":
					m.overlay = overlayNone
					return m.discardPendingProposal(diff.prop), nil
				}
			}
		}
		return m, nil
	case overlayWhichKey:
		// The leader preview closes on the next key and runs the command.
		m.overlay = overlayNone
		if !m.spacePending {
			return m, nil
		}
		m.spacePending = false
		switch {
		case msg.Type == tea.KeyEsc:
			return m, nil
		case msg.Type == tea.KeyRunes && msg.String() == "?":
			m.overlay = overlayKeymap
			return m, nil
		case msg.Type == tea.KeyRunes && msg.String() == "t":
			m.cycleTheme()
			return m, nil
		case msg.Type == tea.KeyRunes && msg.String() == "v":
			return m.toggleNextConcealed(), nil
		case msg.Type == tea.KeyRunes && msg.String() == "a" && m.proposalShortcutAvailable():
			return m.acceptLatestPending(), m.refreshModifiedFiles()
		case msg.Type == tea.KeyRunes && msg.String() == "d" && m.proposalShortcutAvailable():
			return m.discardLatestPending(), nil
		}
		if m.focus == FocusInput {
			m.input.update(tea.KeyMsg{Type: tea.KeySpace})
			m.inputChanged()
		}
		return m.updateKey(msg)
	case overlayAtFile:
		if list, ok := m.overlayM.(*listOverlay); ok {
			before, query, ok := atQuery(m.input.Value())
			if !ok {
				m.overlay = overlayNone
				return m, nil
			}
			list.query = query
			list.selected = 0
			list.ensureSelectable()
			switch msg.Type {
			case tea.KeyEsc:
				m.overlay = overlayNone
			case tea.KeyUp:
				list.up()
			case tea.KeyDown:
				list.down()
			case tea.KeyEnter:
				if sel := list.selectedValue(); sel != "" {
					m.input.Set(before + sel)
					m.inputChanged()
					m.overlay = overlayNone
					m.focus = FocusInput
				}
			default:
				m.input.update(msg)
				m.inputChanged()
				if _, q, ok := atQuery(m.input.Value()); !ok || strings.Contains(q, " ") {
					m.overlay = overlayNone
				}
			}
		}
		return m, nil
	case overlayTimeline:
		if tl, ok := m.overlayM.(*timelineOverlay); ok {
			switch msg.Type {
			case tea.KeyEsc:
				return m.closeTimeline(), nil
			case tea.KeyUp, tea.KeyCtrlU:
				tl.up()
			case tea.KeyDown, tea.KeyCtrlD:
				tl.down()
			case tea.KeyEnter:
				if idx, ok := tl.selectedIndex(); ok {
					return m.jumpToMessage(idx), nil
				}
			}
		}
		return m, nil
	case overlayAgentDetail:
		if msg.Type == tea.KeyEsc {
			m.overlay = overlayNone
			return m, nil
		}
		if msg.Type == tea.KeyRunes && msg.String() == "c" {
			m.orch.CancelRun()
			if m.cancelRun != nil {
				m.cancelRun()
				m.cancelRun = nil
			}
			m.cancelling = true
			m.overlay = overlayNone
			m.appendSystem("sub-agent cancellation requested")
			return m, nil
		}
		return m, nil
	default:
		if list, ok := overlayList(m.overlayM); ok {
			list.ensureSelectable()
			if msg.Type == tea.KeyEsc {
				closing := m.overlay
				m.overlay = overlayNone
				m.overlayM = nil
				m.cancelOverlayRequest(closing)
				return m, nil
			}
			if msg.Type == tea.KeyUp {
				list.up()
			}
			if msg.Type == tea.KeyDown {
				list.down()
			}
			if msg.Type == tea.KeyBackspace {
				list.backspace()
			}
			if m.overlay == overlayModelPicker && msg.Type == tea.KeyRunes && msg.String() == "r" && list.query == "" {
				return m, func() tea.Msg {
					return modelsRefreshedMsg{err: m.orch.RefreshModels(context.Background())}
				}
			}
			if m.overlay == overlayModelPicker && list.groupPageable() && list.query == "" && msg.Type == tea.KeyRunes {
				switch msg.String() {
				case "h":
					list.switchGroup(-1)
					list.ensureSelectable()
					return m, nil
				case "l":
					list.switchGroup(1)
					list.ensureSelectable()
					return m, nil
				}
			}
			if msg.Type == tea.KeySpace {
				list.query += " "
				list.selected = 0
			}
			if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
				list.query += sanitizeSingleLineInput(string(msg.Runes))
				list.selected = 0
			}
			if msg.Type == tea.KeyEnter {
				cmd := m.selectOverlay(list)
				return m, cmd
			}
		}
	}
	return m, nil
}

// selectOverlay applies the picker selection.
func (m *Model) selectOverlay(list *listOverlay) tea.Cmd {
	switch m.overlay {
	case overlayPalette:
		sel := list.selectedValue()
		if strings.HasPrefix(sel, "skill:") {
			m.overlay = overlayNone
			if m.busy || m.streaming() {
				m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
				return nil
			}
			m.busy = true
			m.runStart = time.Now()
			name := strings.TrimPrefix(sel, "skill:")
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelRun = cancel
			return m.startRun(func() tea.Msg {
				summary, err := m.orch.SkillRun(ctx, name)
				if err != nil {
					return m.doneMessage(err)
				}
				return chatDoneMsg{systemText: joinOutput(m.drainCommandOutput(), "skill "+name+" done: "+summary)}
			})
		}
		if sel != "" {
			m.input.Set(sel)
			m.inputChanged()
			m.focus = FocusInput
		}
		m.overlay = overlayNone
		return nil
	case overlayModelPicker:
		if id := list.selectedValue(); id != "" {
			provider := id
			if i := strings.IndexByte(provider, '/'); i >= 0 {
				provider = provider[:i]
			}
			if info, ok := m.orch.ProviderInfo(context.Background(), provider); ok && info.RequiresKey && !info.KeySet {
				m.overlay = overlayAuth
				m.overlayM = newAuthOverlay(provider, id)
				return nil
			}
			if !modelKnown(m.orch, id) {
				m.status.pushToast("error", "model "+id+" not available — run 'maestro model list'", 4*time.Second)
				return nil
			}
			if err := m.orch.SetActiveModel(m.ctx(), id); err != nil {
				m.appendError("error: " + err.Error())
				return nil
			}
			m.appendSystem("model: " + id)
			m.status.pushToast("info", "model: "+id, 2*time.Second)
		}
		m.overlay = overlayNone
		return nil
	case overlaySessionPicker:
		m.invalidateSessionListRequest()
		if id := list.selectedValue(); id != "" {
			return m.loadSession(id)
		}
		m.overlay = overlayNone
		return nil
	case overlayCoachMode:
		action := list.selectedValue()
		m.overlay = overlayNone
		m.overlayM = nil
		if action != "" {
			return m.runCoachAction(action, orchestrator.CoachMode(action), "")
		}
		return nil
	case overlayGit:
		m.invalidateWorkspaceListRequest()
		value := list.selectedValue()
		switch {
		case value == createWorkspacePickerValue:
			m.overlay = overlayForm
			m.formAction = formActionCreateWorkspace
			m.overlayM = newFormOverlay("Create Git workspace", []formField{{
				Key: "branch", Label: "New branch", Required: true,
				Placeholder: "feature/session-titles",
				Help:        "Starts from current HEAD; uncommitted changes are not copied.",
			}})
			return nil
		case value == "":
			m.overlay = overlayNone
			return nil
		case sameFilesystemPath(value, m.orch.WorkDirDisplay()):
			m.overlay = overlayNone
			m.status.pushToast("info", "workspace already active", 2*time.Second)
			return nil
		default:
			m.overlay = overlayNone
			return m.selectWorkspace(value)
		}
	case overlayCheckpoints:
		if id := list.selectedValue(); id != "" {
			m.input.Set("/rewind " + id + " --code")
			m.inputChanged()
			m.focus = FocusInput
			if m.activeTab == TabIDE && m.ide != nil {
				m.ide.Focus = ideChat
			}
			m.status.pushToast("warn", "review the rewind command, then press enter", 4*time.Second)
		}
		m.overlay = overlayNone
		return nil
	case overlayEngine:
		if m.pendingCmd != nil {
			if eng, ok := m.overlayM.(*engineOverlay); ok {
				if choice, ok := eng.selectedChoice(); ok {
					m.pendingCmd.Flags["engine"] = choice.Engine
					if choice.Agent != "" {
						m.pendingCmd.Flags["agent"] = choice.Agent
					}
					cmd := *m.pendingCmd
					m.pendingCmd = nil
					m.overlay = overlayNone
					return m.dispatch(cmd)
				}
			}
		}
		m.overlay = overlayNone
		return nil
	case overlayAsk:
		action := strings.Fields(list.selectedValue())
		if len(action) == 0 || m.pendingSelection == nil {
			m.overlay = overlayNone
			return nil
		}
		prompt := selectionPrompt(action[0], m.pendingSelection)
		m.pendingSelection = nil
		m.overlay = overlayNone
		m.switchTab(TabHarness)
		m.input.Set(prompt)
		m.focus = FocusInput
		return m.send()
	}
	return nil
}

// send runs a chat turn in the background, or dispatches a slash command.
// clearScreenCmd asks bubbletea to clear the terminal and repaint the next
// frame in full, wiping any cell-diff residue from streaming frames.
func clearScreenCmd() tea.Cmd {
	return func() tea.Msg { return tea.ClearScreen() }
}

// refreshModifiedFiles re-diffs the working tree off the event loop for the
// sidebar "Changed" panel.
func (m *Model) refreshModifiedFiles() tea.Cmd {
	m.modFilesRequested++
	return m.beginModifiedFilesRefresh()
}

// refreshAfterCompletion treats the streamed EvDone and the worker's
// chatDoneMsg as the two halves of one terminal signal. Bubble Tea may deliver
// them in either order; only the first requests a scan for this run.
func (m *Model) refreshAfterCompletion(source uint8) tea.Cmd {
	alreadyRequested := m.modFilesCompletion != 0
	m.modFilesCompletion |= source
	if alreadyRequested {
		return nil
	}
	return m.refreshModifiedFiles()
}

// beginModifiedFilesRefresh maintains a single scan in flight. Requests that
// arrive while it runs only advance the desired revision; completion starts
// exactly one scan for the newest revision.
func (m *Model) beginModifiedFilesRefresh() tea.Cmd {
	if m.modFilesInFlight || m.orch == nil {
		return nil
	}
	revision := m.modFilesRequested
	workspace := m.orch.SnapshotWorkspace()
	m.modFilesInFlight = true
	m.modFilesRunning = revision
	return func() tea.Msg {
		return modFilesMsg{
			files:         m.orch.ModifiedFilesFor(context.Background(), workspace),
			ideFiles:      editor.ListFiles(workspace.WorkDir(), 500),
			ideFilesReady: true,
			revision:      revision,
			workspace:     workspace,
		}
	}
}

func (m *Model) finishModifiedFilesRefresh(msg modFilesMsg) tea.Cmd {
	// A result from an already-settled scan can be delivered late by a test
	// harness or terminal shutdown. It must not disturb the active scan.
	if msg.revision != 0 && msg.revision <= m.modFilesApplied {
		return nil
	}
	if m.modFilesInFlight && msg.revision != m.modFilesRunning {
		return nil
	}
	if m.modFilesInFlight {
		m.modFilesInFlight = false
		m.modFilesRunning = 0
	}

	workspaceCurrent := !msg.workspace.Valid() || m.orch.WorkspaceIsCurrent(msg.workspace)
	latest := msg.revision == 0 || msg.revision == m.modFilesRequested
	if latest && workspaceCurrent {
		m.modFilesApplied = max(m.modFilesApplied, msg.revision)
		m.sidebar.setFiles(msg.files)
		if msg.ideFilesReady && m.ide != nil {
			m.ide.applyFileRefresh(msg.workspace.WorkDir(), msg.ideFiles)
		}
		return nil
	}

	// A route switch without an explicit UI request still needs a current
	// rerun. Normal event-driven mutations already advanced the generation.
	if !workspaceCurrent && m.modFilesRequested <= msg.revision {
		m.modFilesRequested = msg.revision + 1
	}
	return m.beginModifiedFilesRefresh()
}

// openEditor suspends the TUI, opens the current prompt in $EDITOR (vi when
// unset), and re-injects the buffer on exit (opencode-style ctrl+e).
func (m *Model) openEditor() tea.Cmd {
	if m.busy {
		m.status.pushToast("info", "agent busy — cancel before editing", 2*time.Second)
		return nil
	}
	f, err := os.CreateTemp("", "maestro-msg-*.md")
	if err != nil {
		m.status.pushToast("error", "editor: "+err.Error(), 3*time.Second)
		return nil
	}
	path := f.Name()
	if _, err := f.WriteString(m.input.Value()); err != nil {
		f.Close()
		os.Remove(path)
		m.status.pushToast("error", "editor: "+err.Error(), 3*time.Second)
		return nil
	}
	f.Close()
	m.editFile = path
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		return editorFinishedMsg{err: err}
	})
}

// handleExecFinished reads the $EDITOR buffer back into the prompt.
func (m *Model) handleExecFinished(err error) tea.Cmd {
	if m.editFile == "" {
		return nil
	}
	path := m.editFile
	m.editFile = ""
	defer os.Remove(path)
	if err != nil {
		m.status.pushToast("error", "editor: "+err.Error(), 3*time.Second)
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		m.status.pushToast("error", "editor read: "+err.Error(), 3*time.Second)
		return nil
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		m.status.pushToast("info", "editor closed with an empty buffer", 2*time.Second)
		return nil
	}
	m.input.Set(text)
	return nil
}

func (m *Model) send() tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
		return nil
	}
	text := m.input.Value()
	m.followOutput = true
	m.input.pushHistory(text)
	m.input.Set("")
	m.slashSelected = 0
	m.layout()
	m.appendUser(text)
	if strings.HasPrefix(text, "/") {
		cmd, err := parseSlash(text)
		if err != nil {
			m.appendError("error: " + err.Error())
			return nil
		}
		switch cmd.Cmd {
		case "ide":
			m.ToggleIDE()
			return nil
		case "follow":
			m.followAgent = !m.followAgent
			if len(cmd.Args) > 0 {
				switch strings.ToLower(cmd.Args[0]) {
				case "on", "true":
					m.followAgent = true
				case "off", "false":
					m.followAgent = false
				}
			}
			state := "off"
			if m.followAgent {
				state = "on"
			}
			m.status.pushToast("info", "Follow Maestro: "+state, 2*time.Second)
			return nil
		case "checkpoints":
			m.overlay = overlayCheckpoints
			m.overlayM = newCheckpointOverlay(m.orch)
			return nil
		case "bootstrap":
			return m.loadProjectForm(formActionBootstrap)
		case "onboard":
			return m.loadProjectForm(formActionOnboard)
		case "git":
			return m.openWorkspacePicker()
		case "resume":
			id := cmd.Flags["id"]
			if id == "" && len(cmd.Args) > 0 {
				id = cmd.Args[0]
			}
			if id != "" {
				return m.loadSession(id)
			}
			return m.openSessionPicker()
		case "rename":
			title := strings.TrimSpace(strings.Join(cmd.Args, " "))
			if title == "" {
				m.formAction = formActionRenameSession
				m.overlay = overlayForm
				m.overlayM = newFormOverlay("Rename session", []formField{{
					Key: "title", Label: "Title", Required: true,
					Value: m.orch.Session().Title, Placeholder: "Authentication hardening",
					Help: "A short title you can recognize in /resume.",
				}})
				return nil
			}
			return m.renameSession(title)
		case "model":
			if len(cmd.Args) > 0 && cmd.Args[0] != "list" {
				id := cmd.Args[0]
				if !modelKnown(m.orch, id) {
					m.appendError("error: model " + id + " is not available")
					return nil
				}
				provider := id
				if i := strings.IndexByte(provider, '/'); i >= 0 {
					provider = provider[:i]
				}
				if info, ok := m.orch.ProviderInfo(context.Background(), provider); ok && info.RequiresKey && !info.KeySet {
					m.overlay = overlayAuth
					m.overlayM = newAuthOverlay(provider, id)
					return nil
				}
				if err := m.orch.SetActiveModel(m.ctx(), id); err != nil {
					m.appendError("error: " + err.Error())
					return nil
				}
				m.appendSystem("model: " + id)
				m.status.pushToast("info", "model: "+id, 2*time.Second)
				return nil
			}
			m.overlay = overlayModelPicker
			m.overlayM = newTaskModelOverlay(m.orch)
			return nil
		case "providers":
			m.overlay = overlayProviders
			m.overlayM = newProvidersOverlay(m.orch, "")
			return nil
		case "help":
			m.appendSystem(slashHelpText())
			return nil
		case "settings":
			return m.openSettings(settingsGeneral, true)
		case "mcp":
			if len(cmd.Args) == 0 {
				return m.openSettings(settingsIntegrations, false)
			}
			return m.dispatch(cmd)
		case "skills":
			if len(cmd.Args) == 0 {
				return m.openSettings(settingsSkills, false)
			}
			return m.dispatch(cmd)
		case "usage":
			m.appendSystem(m.usageSummary())
			return nil
		case "quit", "exit":
			return tea.Quit
		}
		if cmd.Cmd == "learn" {
			path, explicitPath := cmd.Flags["path"]
			if path == "" && len(cmd.Args) > 0 {
				path = cmd.Args[0]
			}
			if path == "" {
				return m.openCoachMenu()
			}
			if explicitPath {
				return m.runLearn(path, cmd.Flags["deep"] == "true")
			}
			switch strings.ToLower(path) {
			case "guided", "challenge", "off", "status", "next", "done", "later":
				return m.runCoachAction(strings.ToLower(path), orchestrator.CoachMode(strings.ToLower(path)), "")
			}
			return m.runLearn(path, cmd.Flags["deep"] == "true")
		}
		if cmd.Cmd == "docs" {
			return m.runDocs()
		}
		if cmd.Cmd == "build" && cmd.Flags["engine"] == "" && cmd.Flags["agent"] == "" {
			m.pendingCmd = &cmd
			m.overlay = overlayEngine
			m.overlayM = newEngineOverlay(m.orch, "dev")
			return nil
		}
		return m.dispatch(cmd)
	}
	requestText := promptWithContext(text, m.contextRefs)
	m.contextRefs = nil
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		err := m.orch.Chat(ctx, requestText)
		return m.doneMessage(err)
	})
}

// runLearn generates the explanation and stages it as a proposal card.
func (m *Model) runLearn(path string, deep bool) tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	// Structured Learn output stays private until it passes strict decoding
	// and source-anchor validation. Give the user deterministic progress here,
	// on the event loop, so hiding transport JSON never makes the TUI static.
	m.appendAssistant("")
	progress := m.lastAssistant()
	progress.busy = true
	progress.think = &thinkingState{
		Role: "coach", Status: "running", Detail: "generating a focused lesson", Started: m.runStart,
	}
	progress.cachedValid = false
	m.scrollToBottomIfAttached()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		out, formatted, err := m.orch.LearnDraft(ctx, path, deep)
		if err != nil {
			return m.doneMessage(err)
		}
		if m.proposals == nil {
			return m.doneMessage(errors.New("learn proposal: staging is unavailable"))
		}
		prop, err := m.proposals.Stage(out, formatted)
		if err != nil {
			return m.doneMessage(fmt.Errorf("learn proposal: %w", err))
		}
		card := &Card{
			ID: "learn-" + prop.ID, Kind: "write", Name: "learn",
			Status: "proposed", Detail: out, Proposal: &prop,
		}
		return chatDoneMsg{
			card: card, systemText: m.drainCommandOutput(), successToast: "lesson ready",
		}
	})
}

func (m *Model) submitForm(action formAction, values map[string]string) tea.Cmd {
	switch action {
	case formActionBootstrap, formActionOnboard:
		if m.formBase == nil || m.formProfile == nil || !m.orch.WorkspaceIsCurrent(m.formWorkspace) {
			m.status.pushToast("error", "project questionnaire expired", 3*time.Second)
			m.formBase = nil
			m.formProfile = nil
			m.formWorkspace = orchestrator.WorkspaceSnapshot{}
			return nil
		}
		answers := *m.formBase
		profile := *m.formProfile
		m.formBase = nil
		m.formProfile = nil
		m.formWorkspace = orchestrator.WorkspaceSnapshot{}
		answers.Name = strings.TrimSpace(values["name"])
		answers.Purpose = strings.TrimSpace(values["purpose"])
		answers.Stacks = splitStackFormList(values["stack"])
		answers.NonGoals = collectProjectRuleFields(values, "non_goals")
		answers.Verification = collectProjectRuleFields(values, "verification")
		answers.Safety = collectProjectRuleFields(values, "safety")
		return m.runProjectManifest(action, profile, answers)
	case formActionRenameSession:
		return m.renameSession(values["title"])
	case formActionCreateWorkspace:
		return m.createWorkspace(values["branch"])
	default:
		m.status.pushToast("error", "form action is unavailable", 3*time.Second)
		return nil
	}
}

func (m *Model) loadProjectForm(action formAction) tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	workspace := m.orch.SnapshotWorkspace()
	return m.startRun(func() tea.Msg {
		switch action {
		case formActionBootstrap:
			profile, answers, err := m.orch.ProjectBootstrapDefaults(ctx)
			return projectFormMsg{
				action: action, profile: profile, answers: answers, workspace: workspace,
				note: fmt.Sprintf("new project · %d detected stack(s)", len(profile.Stacks)), err: err,
			}
		case formActionOnboard:
			profile, answers, err := m.orch.ProjectOnboardProfile(ctx)
			return projectFormMsg{
				action: action, profile: profile, answers: answers, workspace: workspace,
				note: fmt.Sprintf("repository analysed · %d unit(s) · %d command(s)", len(profile.Units), len(profile.Commands)), err: err,
			}
		default:
			return projectFormMsg{action: action, err: errors.New("unknown project questionnaire")}
		}
	})
}

func (m *Model) openProjectForm(action formAction, profile projectprofile.ProjectProfile, answers projectprofile.Answers, workspace orchestrator.WorkspaceSnapshot) {
	title := "Bootstrap project"
	if action == formActionOnboard {
		title = "Onboard repository"
	}
	m.formAction = action
	m.formBase = &answers
	m.formProfile = &profile
	m.formWorkspace = workspace
	m.overlay = overlayForm
	fields := []formField{
		{Key: "name", Label: "Project name", Value: answers.Name, Placeholder: "acme-api", Required: true, Help: "Stable product or repository name."},
		{Key: "purpose", Label: "Purpose", Value: answers.Purpose, Placeholder: "Who it helps and what outcome it creates", Required: true, Help: "One clear outcome; implementation details belong later."},
		{Key: "stack", Label: "Stack", Value: strings.Join(answers.Stacks, ", "), Placeholder: "Go, PostgreSQL, React", Required: true, Help: "Comma-separated languages, frameworks and infrastructure."},
	}
	fields = append(fields, projectRuleFields("non_goals", "Non-goal", answers.NonGoals, "Mobile client or multi-region deployment", false)...)
	fields = append(fields, projectRuleFields("verification", "Verification", answers.Verification, "go test ./... or manual acceptance check", true)...)
	fields = append(fields, projectRuleFields("safety", "Safety boundary", answers.Safety, "Never commit secrets; preserve public APIs", true)...)
	m.overlayM = newFormOverlay(title, fields)
}

// Stack values intentionally remain concise CSV. Prose rules do not use this
// parser: each rule gets an independent form field so commas remain data.
func splitStackFormList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := strings.ToLower(part)
		if part == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, part)
	}
	return out
}

// projectRuleFields represents each prose list entry as one editable "chip".
// One trailing empty chip lets users add an item without introducing an
// ambiguous delimiter into commands or natural-language rules.
func projectRuleFields(key, label string, values []string, placeholder string, required bool) []formField {
	fields := make([]formField, 0, len(values)+1)
	for index, value := range values {
		fieldKey := key
		if index > 0 {
			fieldKey = fmt.Sprintf("%s_%d", key, index+1)
		}
		fields = append(fields, formField{
			Key: fieldKey, Label: fmt.Sprintf("%s %d", label, index+1), Value: value,
			Placeholder: placeholder, Required: required && index == 0,
			Help: "One rule per field; commas are preserved. The final empty field adds another rule.",
		})
	}
	index := len(values)
	fieldKey := key
	if index > 0 {
		fieldKey = fmt.Sprintf("%s_%d", key, index+1)
	}
	fields = append(fields, formField{
		Key: fieldKey, Label: fmt.Sprintf("%s %d", label, index+1), Placeholder: placeholder,
		Required: required && len(values) == 0,
		Help:     "One rule per field; commas are preserved. Leave this final field empty to keep the current list.",
	})
	return fields
}

func collectProjectRuleFields(values map[string]string, key string) []string {
	var out []string
	for index := 0; ; index++ {
		fieldKey := key
		if index > 0 {
			fieldKey = fmt.Sprintf("%s_%d", key, index+1)
		}
		value, ok := values[fieldKey]
		if !ok {
			break
		}
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (m *Model) runProjectManifest(action formAction, profile projectprofile.ProjectProfile, answers projectprofile.Answers) tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
		return nil
	}
	if m.proposals == nil {
		m.status.pushToast("error", "proposal store unavailable", 4*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		var (
			path    string
			content []byte
			err     error
			name    = "bootstrap"
		)
		if action == formActionOnboard {
			name = "onboard"
		}
		path, content, err = projectprofile.Draft(ctx, profile, answers)
		if err != nil {
			return uiOperationDoneMsg{err: err}
		}
		prop, err := m.proposals.Stage(path, string(content))
		if err != nil {
			return uiOperationDoneMsg{err: err}
		}
		if len(prop.Hunks) == 0 {
			m.proposals.Discard(prop)
			return uiOperationDoneMsg{systemText: "MAESTRO.md already matches the reviewed project contract", successToast: "project contract is current"}
		}
		return uiOperationDoneMsg{
			card: &Card{
				ID: name + "-" + prop.ID, Kind: "write", Name: name,
				Status: "proposed", Detail: path, Proposal: &prop,
			},
			systemText:   "Review the MAESTRO.md contract, then accept or discard the proposal.",
			successToast: "MAESTRO.md ready for review",
		}
	})
}

func (m *Model) renameSession(title string) tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel before renaming", 3*time.Second)
		return nil
	}
	title = sanitizeSingleLineInput(title)
	if strings.TrimSpace(title) == "" {
		m.status.pushToast("error", "session title is required", 3*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		err := m.orch.RenameSession(ctx, title)
		if err != nil {
			return uiOperationDoneMsg{err: err}
		}
		return uiOperationDoneMsg{
			systemText:   "session renamed: " + title,
			successToast: "session renamed",
			sessionTitle: m.orch.Session().Title,
		}
	})
}

func (m *Model) beginSessionListRequest() (uint64, context.Context) {
	if m.sessionListStop != nil {
		m.sessionListStop()
	}
	m.sessionRequest++
	ctx, cancel := context.WithCancel(context.Background())
	m.sessionListStop = cancel
	return m.sessionRequest, ctx
}

func (m *Model) finishSessionListRequest() {
	if m.sessionListStop != nil {
		m.sessionListStop()
		m.sessionListStop = nil
	}
}

func (m *Model) invalidateSessionListRequest() {
	m.finishSessionListRequest()
	m.sessionRequest++
}

func (m *Model) beginWorkspaceListRequest() (uint64, context.Context) {
	if m.workspaceListStop != nil {
		m.workspaceListStop()
	}
	m.workspaceRequest++
	ctx, cancel := context.WithCancel(context.Background())
	m.workspaceListStop = cancel
	return m.workspaceRequest, ctx
}

func (m *Model) finishWorkspaceListRequest() {
	if m.workspaceListStop != nil {
		m.workspaceListStop()
		m.workspaceListStop = nil
	}
}

func (m *Model) invalidateWorkspaceListRequest() {
	m.finishWorkspaceListRequest()
	m.workspaceRequest++
}

func (m *Model) cancelOverlayRequest(kind overlayKind) {
	switch kind {
	case overlaySettings:
		if settings, ok := m.overlayM.(*settingsOverlay); ok {
			settings.cancelAction()
		}
	case overlaySessionPicker:
		m.invalidateSessionListRequest()
	case overlayGit:
		m.invalidateWorkspaceListRequest()
	case overlayCoachMode:
		m.invalidateCoachRequest()
	}
}

func (m *Model) cancelClosedPickerRequest(before overlayKind) {
	if before == m.overlay {
		return
	}
	switch before {
	case overlaySessionPicker:
		m.invalidateSessionListRequest()
	case overlayGit:
		m.invalidateWorkspaceListRequest()
	}
}

func (m *Model) openSessionPicker() tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel before resuming", 3*time.Second)
		return nil
	}
	const loading = "Loading sessions…"
	m.overlay = overlaySessionPicker
	m.overlayM = &listOverlay{
		title: "Resume session", items: []string{loading},
		disabled: map[string]bool{loading: true},
	}
	request, ctx := m.beginSessionListRequest()
	return func() tea.Msg {
		summaries, err := m.orch.ListSessionSummaries(ctx)
		return sessionListMsg{request: request, summaries: summaries, err: err}
	}
}

func (m *Model) openWorkspacePicker() tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel before switching workspace", 3*time.Second)
		return nil
	}
	if m.hasDirtyEditorBuffers() {
		m.status.pushToast("warn", "save or discard every editor buffer before switching workspace", 4*time.Second)
		return nil
	}
	const loading = "Loading Git workspaces…"
	m.overlay = overlayGit
	m.overlayM = &listOverlay{title: "Git workspaces", items: []string{loading}, disabled: map[string]bool{loading: true}}
	request, ctx := m.beginWorkspaceListRequest()
	return func() tea.Msg {
		workspaces, err := m.orch.WorkspaceList(ctx)
		return workspaceListMsg{request: request, workspaces: workspaces, err: err}
	}
}

func (m *Model) selectWorkspace(path string) tea.Cmd {
	return m.changeWorkspace("select", func(ctx context.Context) (string, string, error) {
		sess, err := m.orch.SelectWorkspace(ctx, path)
		return sess.ID, m.orch.WorkDirDisplay(), err
	})
}

func (m *Model) createWorkspace(branch string) tea.Cmd {
	branch = strings.TrimSpace(sanitizeSingleLineInput(branch))
	if branch == "" {
		m.status.pushToast("error", "branch name is required", 3*time.Second)
		return nil
	}
	return m.changeWorkspace("create", func(ctx context.Context) (string, string, error) {
		sess, err := m.orch.CreateWorkspace(ctx, branch)
		return sess.ID, m.orch.WorkDirDisplay(), err
	})
}

func (m *Model) changeWorkspace(action string, change func(context.Context) (string, string, error)) tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel before switching workspace", 3*time.Second)
		return nil
	}
	if m.hasDirtyEditorBuffers() {
		m.status.pushToast("warn", "save or discard every editor buffer before switching workspace", 4*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		id, workDir, err := change(ctx)
		if err != nil {
			return uiOperationDoneMsg{err: err}
		}
		verb := "selected"
		if action == "create" {
			verb = "created"
		}
		return uiOperationDoneMsg{
			sessionID: id, workDir: workDir, refreshFiles: true,
			systemText:   "Git workspace " + verb + ": " + workDir,
			successToast: "workspace " + verb,
		}
	})
}

func (m *Model) beginCoachRequest(action string) (context.Context, coachResultMsg) {
	if m.coachStop != nil {
		m.coachStop()
	}
	m.coachRequest++
	ctx, cancel := context.WithCancel(context.Background())
	m.coachStop = cancel
	return ctx, coachResultMsg{
		request:     m.coachRequest,
		interaction: m.interactionRequest,
		action:      action,
		sessionID:   m.orch.Session().ID,
		workspace:   m.orch.SnapshotWorkspace(),
		overlay:     m.overlay,
		input:       m.input.Value(),
	}
}

func (m *Model) finishCoachRequest() {
	if m.coachStop != nil {
		m.coachStop()
		m.coachStop = nil
	}
}

func (m *Model) invalidateCoachRequest() {
	m.finishCoachRequest()
	m.coachRequest++
}

func (m *Model) coachResultIsCurrent(msg coachResultMsg) bool {
	if msg.sessionID != m.orch.Session().ID || !m.orch.WorkspaceIsCurrent(msg.workspace) {
		return false
	}
	if msg.overlay != m.overlay {
		return false
	}
	// Loading the Coach menu in-place is safe even if the user has already
	// typed a filter. Preserve that filter when replacing the loading row.
	if msg.action == "menu" {
		return m.overlay == overlayCoachMode
	}
	// Every other Coach result may close a surface, append transcript text, or
	// open a lesson. Once the user has interacted or edited the composer, that
	// result is stale and must become a silent no-op.
	return msg.interaction == m.interactionRequest && msg.input == m.input.Value() &&
		m.dialogs.empty() && m.pendingAsk == nil && m.selectionMenu == nil &&
		m.selectionAsk == nil && m.selectionEdit == nil
}

func (m *Model) openCoachMenu() tea.Cmd {
	const loading = "Loading private progress…"
	m.overlay = overlayCoachMode
	m.overlayM = &listOverlay{title: "Maestro Coach", items: []string{loading}, disabled: map[string]bool{loading: true}}
	ctx, result := m.beginCoachRequest("menu")
	return func() tea.Msg {
		out := result
		out.state, out.err = m.orch.CoachState(ctx)
		return out
	}
}

func (m *Model) runCoachAction(action string, mode orchestrator.CoachMode, lessonID string) tea.Cmd {
	ctx, result := m.beginCoachRequest(action)
	return func() tea.Msg {
		var (
			state  orchestrator.CoachState
			lesson *orchestrator.CoachLesson
			err    error
		)
		switch action {
		case "guided", "challenge", "off":
			state, err = m.orch.SetCoachMode(ctx, mode)
			if err == nil && mode != orchestrator.CoachModeOff {
				lesson, err = m.orch.CoachLesson(ctx)
			}
		case "status":
			state, err = m.orch.CoachState(ctx)
		case "next", "refresh":
			state, err = m.orch.CoachState(ctx)
			if err == nil {
				lesson, err = m.orch.CoachLesson(ctx)
			}
		case "done":
			if lessonID == "" {
				state, err = m.orch.CoachState(ctx)
				lessonID = state.PendingLessonID
			}
			if err == nil {
				state, err = m.orch.CompleteCoachLesson(ctx, lessonID)
			}
		case "later":
			state, err = m.orch.SnoozeCoach(ctx, 24*time.Hour)
		default:
			err = fmt.Errorf("unknown Coach action %q", action)
		}
		out := result
		out.state, out.lesson, out.err = state, lesson, err
		return out
	}
}

func (m *Model) coachBreakpointCmd() tea.Cmd {
	if strings.TrimSpace(m.input.Value()) != "" || m.overlay != overlayNone || !m.dialogs.empty() ||
		m.pendingAsk != nil || m.selectionMenu != nil || m.selectionAsk != nil || m.selectionEdit != nil {
		return nil
	}
	return m.runCoachAction("refresh", "", "")
}

func (m *Model) setCoachLesson(lesson *orchestrator.CoachLesson, mode orchestrator.CoachMode) {
	if lesson == nil || mode == orchestrator.CoachModeOff {
		m.coachOffer = nil
		return
	}
	hint := coachLessonHint(lesson.Prompt)
	m.coachOffer = &coachOffer{
		ID: lesson.ID, Title: lesson.Title, Prompt: lesson.Action,
		Composer: "Coach exercise — " + lesson.Title + "\nNext: " + lesson.Action +
			"\nDone when: " + lesson.DoneWhen + "\n\nMy reasoning: ",
		Why:      lesson.WhyNow,
		DoneWhen: lesson.DoneWhen,
		Hint:     hint,
		Duration: "2 min",
		Mode:     string(mode),
	}
}

func (m *Model) visibleCoachOffer() *coachOffer {
	if m.coachOffer == nil || m.busy || strings.TrimSpace(m.input.Value()) != "" ||
		m.overlay != overlayNone || !m.dialogs.empty() || m.pendingAsk != nil ||
		m.selectionMenu != nil || m.selectionAsk != nil || m.selectionEdit != nil {
		return nil
	}
	return m.coachOffer
}

func coachLessonHint(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		for _, prefix := range []string{"Worked example: ", "Hint: ", "Check: "} {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return "Explain your reasoning before asking Maestro for the answer."
}

func coachStatusText(state orchestrator.CoachState) string {
	completed, mastery := 0, 0
	for _, progress := range state.Progress {
		completed += progress.ExplicitCompletions
		mastery += progress.Mastery
	}
	average := 0
	if len(state.Progress) > 0 {
		average = mastery / len(state.Progress)
	}
	pending := "none"
	if state.PendingSkillID != "" {
		pending = state.PendingSkillID + " · " + string(state.PendingStage)
	}
	return fmt.Sprintf("Coach: %s · %d explicit completion(s) · %d%% average mastery · pending %s", state.Mode, completed, average, pending)
}

func (m *Model) hasDirtyEditorBuffers() bool {
	if m.ide == nil || m.ide.Ed == nil {
		return false
	}
	for _, buffer := range m.ide.Ed.Buffers {
		if buffer != nil && buffer.Dirty {
			return true
		}
	}
	return false
}

func sameFilesystemPath(a, b string) bool {
	canonical := func(path string) string {
		path = filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = filepath.Clean(resolved)
		}
		return path
	}
	return canonical(a) == canonical(b)
}

// runDocs generates an ADR without writing it and stages the artifact as a
// proposal. The docs phase advances only when the user accepts the card.
func (m *Model) runDocs() tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		path, content, err := m.orch.DocsDraft(ctx)
		if err != nil {
			return m.doneMessage(err)
		}
		if m.proposals == nil {
			return m.doneMessage(fmt.Errorf("docs: proposal store unavailable"))
		}
		prop, err := m.proposals.Stage(path, content)
		if err != nil {
			return m.doneMessage(err)
		}
		return chatDoneMsg{card: &Card{
			ID: "docs-" + prop.ID, Kind: "write", Name: "docs", Status: "proposed",
			Detail: path, Proposal: &prop, Lifecycle: "docs",
		}, systemText: m.drainCommandOutput()}
	})
}

// dispatch runs an orchestrator command in the background.
func (m *Model) dispatch(cmd orchestrator.Command) tea.Cmd {
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel", 2*time.Second)
		return nil
	}
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		err := m.orch.Dispatch(ctx, cmd)
		return m.doneMessage(err)
	})
}

func (m *Model) doneMessage(err error) chatDoneMsg {
	return chatDoneMsg{err: err, systemText: m.drainCommandOutput()}
}

func (m *Model) drainCommandOutput() string {
	if m.commandOut == nil {
		return ""
	}
	return strings.TrimSpace(m.commandOut.Drain())
}

func (m *Model) usageSummary() string {
	used, total := m.orch.ContextUsage()
	model := safeIDEPlainText(m.orch.ActiveModel())
	if model == "" {
		model = "not configured"
	}
	contextText := fmt.Sprintf("%d tokens", used)
	if total > 0 {
		contextText = fmt.Sprintf("%d/%d tokens (%d%%)", used, total, min(used*100/total, 100))
	}
	tools := m.orch.SessionToolCalls()
	toolLabel := "tool calls"
	if tools == 1 {
		toolLabel = "tool call"
	}
	return fmt.Sprintf(
		"usage: model %s · reasoning %s · context %s · cost $%.4f · %d %s",
		model, safeIDEPlainText(m.orch.ActiveReasoningEffort()), contextText, m.orch.SessionCost(), tools, toolLabel,
	)
}

func (m *Model) loadSession(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	m.invalidateSessionListRequest()
	if id != "" && id == m.orch.Session().ID {
		m.overlay = overlayNone
		m.overlayM = nil
		m.status.pushToast("info", "session already active", 2*time.Second)
		return nil
	}
	if m.busy || m.streaming() {
		m.status.pushToast("info", "agent busy — wait or cancel before switching session", 3*time.Second)
		m.overlay = overlayNone
		m.overlayM = nil
		return nil
	}
	if m.hasDirtyEditorBuffers() {
		m.status.pushToast("warn", "save or discard every editor buffer before switching session", 4*time.Second)
		m.overlay = overlayNone
		m.overlayM = nil
		return nil
	}
	m.overlay = overlayNone
	m.overlayM = nil
	m.busy = true
	m.runStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	return m.startRun(func() tea.Msg {
		err := m.orch.LoadSession(ctx, id)
		msg := m.doneMessage(err)
		if err == nil {
			msg.sessionID = id
			msg.sessionWorkDir = m.orch.WorkDirDisplay()
		}
		return msg
	})
}

func (m *Model) applyLoadedSession(id, workDir string) {
	if workDir == "" {
		return
	}
	if settings, ok := m.overlayM.(*settingsOverlay); ok {
		settings.cancelAction()
		m.overlay = overlayNone
		m.overlayM = nil
	}
	m.invalidateSessionListRequest()
	m.invalidateWorkspaceListRequest()
	m.invalidateCoachRequest()
	m.coachOffer = nil
	if m.ide != nil {
		m.ide.Save()
		m.ide = NewIDE(m, workDir, git.New(workDir))
	}
	m.proposals = nil
	if home, err := userHome(); err == nil {
		m.proposals = proposals.NewWorkspaceProposalStore(
			filepath.Join(home, ".maestro", "proposals", id),
			m.orch.WorkDirDisplay,
		)
	}
	m.pending = nil
	m.pendingCmd = nil
	m.contextRefs = nil
	m.cardRows = map[string]int{}
	m.toolCards = map[string]*Card{}
	m.messages = nil
	m.sessionTitle = safeIDEPlainText(m.orch.Session().Title)
	m.chatState = stateForPhase(string(m.orch.Phase()))
	now := time.Now()
	for _, turn := range m.orch.Session().Conversation {
		if turn.Role != "user" && turn.Role != "assistant" {
			continue
		}
		m.messages = append(m.messages, &Message{Role: turn.Role, Text: turn.Content, State: m.chatState, ts: now})
	}
	m.sidebar = NewSidebar(m.styles.T)
	m.sidebar.refresh(m.orch)
	m.layout()
	m.renderMessages()
}

func joinOutput(parts ...string) string {
	var nonEmpty []string
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n")
}

// startRun arms the first slow activity tick together with a background run.
// Without this, an idle TUI has no timer pending until the provider emits its
// first event (or forever for commands that do not stream).
func (m *Model) startRun(run tea.Cmd) tea.Cmd {
	m.modFilesCompletion = 0
	return tea.Batch(run, m.frameTicks())
}

// acceptLatestPending / discardLatestPending act on the newest proposal card.
func (m *Model) acceptLatestPending() tea.Model {
	return m.acceptPendingCard(m.pendingDecisionCard())
}

func (m *Model) discardLatestPending() tea.Model {
	return m.discardPendingCard(m.pendingDecisionCard())
}

// A diff overlay is a snapshot of one exact proposal. Resolve that proposal,
// never whichever card happened to become newest while the overlay was open.
func (m *Model) acceptPendingProposal(prop *proposals.Proposal) tea.Model {
	return m.acceptPendingCard(m.pendingProposalCard(prop))
}

func (m *Model) discardPendingProposal(prop *proposals.Proposal) tea.Model {
	return m.discardPendingCard(m.pendingProposalCard(prop))
}

func (m *Model) pendingProposalCard(prop *proposals.Proposal) *Card {
	if prop == nil || prop.ID == "" {
		return nil
	}
	for _, card := range m.pending {
		if card.Status == "proposed" && card.Proposal != nil && card.Proposal.ID == prop.ID {
			return card
		}
	}
	return nil
}

func (m *Model) acceptPendingCard(c *Card) tea.Model {
	if c == nil || c.Proposal == nil {
		m.status.pushToast("info", "proposal is no longer pending", 2*time.Second)
		return m
	}
	m.acceptProposalCard(c)
	if c.Status == "error" {
		m.status.pushToast("error", c.Detail, 4*time.Second)
		m.invalidateMessageCaches()
		m.renderMessages()
		return m
	}
	c.Detail = fmt.Sprintf("applied %d hunk(s) in %s", len(c.Proposal.Hunks), c.Proposal.Path)
	m.status.pushToast("success", "proposal accepted", 2*time.Second)
	m.removePending(c)
	m.completeProposalReviewIfSettled()
	m.invalidateMessageCaches()
	m.renderMessages()
	return m
}

func (m *Model) discardPendingCard(c *Card) tea.Model {
	if c == nil || c.Proposal == nil {
		m.status.pushToast("info", "proposal is no longer pending", 2*time.Second)
		return m
	}
	m.proposals.Discard(*c.Proposal)
	c.Status = "discarded"
	c.Detail = "discarded"
	m.removePending(c)
	m.completeProposalReviewIfSettled()
	m.invalidateMessageCaches()
	m.renderMessages()
	return m
}

func (m *Model) pendingDecisionCard() *Card {
	if m.activeTab == TabIDE && m.ide != nil && m.ide.proposalPreview != nil {
		id := m.ide.proposalPreview.ID
		for _, card := range m.pending {
			if card.Status == "proposed" && card.Proposal != nil && card.Proposal.ID == id {
				return card
			}
		}
	}
	for i := len(m.pending) - 1; i >= 0; i-- {
		if m.pending[i].Status == "proposed" {
			return m.pending[i]
		}
	}
	return nil
}

// proposalShortcutAvailable prevents review keys from stealing normal text
// entry. In the Agent workspace they become active when the composer is
// empty; in the IDE they are scoped to the HITL companion rail.
func (m *Model) proposalShortcutAvailable() bool {
	if len(m.pending) == 0 {
		return false
	}
	if m.activeTab == TabIDE {
		return m.ide != nil && (m.ide.Focus == ideHITL || m.ide.proposalPreview != nil)
	}
	return m.focus != FocusInput || strings.TrimSpace(m.input.Value()) == ""
}

func (m *Model) completeProposalReviewIfSettled() {
	if m.ide != nil && m.ide.proposalPreview != nil {
		previewID := m.ide.proposalPreview.ID
		stillPending := false
		for _, card := range m.pending {
			if card.Proposal != nil && card.Proposal.ID == previewID {
				stillPending = true
				break
			}
		}
		if !stillPending {
			m.ide.proposalPreview = nil
			m.ide.proposalScroll = 0
			if len(m.pending) > 0 {
				m.ide.proposalPreview = m.pending[len(m.pending)-1].Proposal
			}
		}
	}
	if len(m.pending) == 0 && m.sidebar != nil {
		m.setHITLChecked("diff", true)
	}
}

func (m *Model) setHITLChecked(id string, done bool) {
	if done {
		m.sidebar.complete(id)
	} else {
		m.sidebar.reopen(id)
	}
	m.persistHITLToggle(id, done)
}

func (m *Model) toggleSelectedHITL() {
	id, done, ok := m.sidebar.toggleSelected()
	if ok {
		m.persistHITLToggle(id, done)
	}
}

func (m *Model) toggleHITLAt(index int) {
	id, done, ok := m.sidebar.toggleAt(index)
	if ok {
		m.persistHITLToggle(id, done)
	}
}

func (m *Model) persistHITLToggle(id string, done bool) {
	if m.orch == nil {
		return
	}
	sess := m.orch.Session()
	if m.orch.ActiveSpec() == nil && sess.Draft == nil {
		return
	}
	if err := m.orch.SetHITLStatus(context.Background(), id, done); err != nil {
		// Persistence is part of the checkbox contract: keep the visible state
		// aligned with the durable one if the atomic session write fails.
		m.sidebar.checked[id] = !done
		m.status.pushToast("error", "human action: "+err.Error(), 4*time.Second)
	}
}

func (m *Model) openProposalInIDE(prop *proposals.Proposal) {
	if prop == nil {
		m.status.pushToast("error", "proposal preview unavailable", 3*time.Second)
		return
	}
	m.overlay = overlayNone
	m.switchTab(TabIDE)
	m.ide.proposalPreview = prop
	m.ide.proposalScroll = 0
	m.ide.proposalHunk = 0
	m.ide.Focus = ideEditor
}

func (m *Model) cycleProposal(delta int) tea.Model {
	if m.ide == nil || len(m.pending) == 0 {
		return m
	}
	index := 0
	if m.ide.proposalPreview != nil {
		for i, card := range m.pending {
			if card.Proposal != nil && card.Proposal.ID == m.ide.proposalPreview.ID {
				index = i
				break
			}
		}
	}
	index = (index + delta + len(m.pending)) % len(m.pending)
	m.ide.proposalPreview = m.pending[index].Proposal
	m.ide.proposalScroll = 0
	m.ide.proposalHunk = 0
	return m
}

func (m *Model) cycleProposalHunk(delta int) tea.Model {
	if m.ide == nil || m.ide.proposalPreview == nil || len(m.ide.proposalPreview.Hunks) == 0 {
		return m
	}
	if proposalRequiresAtomicDecision(m.ide.proposalPreview) {
		m.status.pushToast("info", "MAESTRO.md is reviewed as one atomic contract", 3*time.Second)
		return m
	}
	total := len(m.ide.proposalPreview.Hunks)
	m.ide.proposalHunk = (m.ide.proposalHunk + delta + total) % total
	m.ide.proposalScroll = 0
	return m
}

func (m *Model) decideProposalHunk(accept bool) tea.Model {
	card := m.pendingDecisionCard()
	if card == nil || card.Proposal == nil || m.proposals == nil || len(card.Proposal.Hunks) == 0 {
		return m
	}
	if proposalRequiresAtomicDecision(card.Proposal) {
		m.status.pushToast("info", "MAESTRO.md is atomic — accept or decline the whole contract", 4*time.Second)
		return m
	}
	index := 0
	if m.ide != nil {
		index = clamp(m.ide.proposalHunk, 0, len(card.Proposal.Hunks)-1)
	}
	var err error
	if accept {
		err = m.proposals.AcceptHunk(card.Proposal, index)
	} else {
		err = m.proposals.RejectHunk(card.Proposal, index)
	}
	if err != nil {
		m.status.pushToast("error", err.Error(), 4*time.Second)
		return m
	}
	verb := "accepted"
	if !accept {
		verb = "declined"
	}
	m.status.pushToast("success", "hunk "+verb, 2*time.Second)
	if len(card.Proposal.Hunks) == 0 {
		card.Status = "done"
		card.Detail = verb + " by hunk review"
		m.removePending(card)
		m.completeProposalReviewIfSettled()
	} else if m.ide != nil {
		m.ide.proposalHunk = min(index, len(card.Proposal.Hunks)-1)
		m.ide.proposalScroll = 0
	}
	m.invalidateMessageCaches()
	m.renderMessages()
	return m
}

// toggleNextConcealed expands the first collapsed code block of the latest
// assistant message (keyboard "v").
func (m *Model) toggleNextConcealed() tea.Model {
	msg := m.lastAssistant()
	if msg == nil || len(msg.concealed) == 0 {
		m.status.pushToast("info", "no code block to expand", 2*time.Second)
		return m
	}
	for i := range msg.concealed {
		if !msg.concealed[i].Expanded {
			m.toggleConcealedBlock(msg, i)
			return m
		}
	}
	// All expanded: collapse them again.
	for i := range msg.concealed {
		msg.concealed[i].Expanded = false
	}
	msg.cachedValid = false
	m.renderMessages()
	return m
}

// toggleConcealedAt toggles the p-th collapsed block of message index i
// (mouse region target "i:p").
func (m *Model) toggleConcealedAt(target string) tea.Model {
	var i, p int
	if _, err := fmt.Sscanf(target, "%d:%d", &i, &p); err != nil {
		return m
	}
	if i < 0 || i >= len(m.messages) {
		return m
	}
	msg := m.messages[i]
	// The p-th placeholder is the p-th collapsed block in declaration order.
	count := 0
	for bi := range msg.concealed {
		if msg.concealed[bi].Expanded {
			continue
		}
		if count == p {
			m.toggleConcealedBlock(msg, bi)
			return m
		}
		count++
	}
	return m
}

// toggleLastThinking expands or collapses the working summary of the
// latest assistant message (keyboard "t").
func (m *Model) toggleLastThinking() tea.Model {
	msg := m.lastAssistant()
	if msg == nil || msg.think == nil {
		m.status.pushToast("info", "no working summary to expand", 2*time.Second)
		return m
	}
	msg.think.Expanded = !msg.think.Expanded
	msg.cachedValid = false
	m.renderMessages()
	return m
}

// toggleThinkingAt toggles the working summary of message index i (mouse).
func (m *Model) toggleThinkingAt(i int) tea.Model {
	if i < 0 || i >= len(m.messages) {
		return m
	}
	msg := m.messages[i]
	if msg.think == nil {
		return m
	}
	msg.think.Expanded = !msg.think.Expanded
	msg.cachedValid = false
	m.renderMessages()
	return m
}

// atQuery extracts the "@mention" query from the prompt: the text after the
// last @ that begins a word. Returns the prefix before the @, the query, and
// whether an active mention exists.
func atQuery(value string) (before, query string, ok bool) {
	i := strings.LastIndexByte(value, '@')
	if i < 0 {
		return "", "", false
	}
	if i > 0 && value[i-1] != ' ' && value[i-1] != '\n' {
		return "", "", false
	}
	return value[:i], value[i+1:], true
}

// maybeOpenAtFile opens the file-mention picker when the prompt contains an
// active "@" query (opencode-style @file frecency, simplified to the
// project file list with the existing fuzzy matcher).
func (m *Model) maybeOpenAtFile() {
	if m.overlay != overlayNone {
		return
	}
	_, query, ok := atQuery(m.input.Value())
	if !ok {
		return
	}
	project := m.orch.WorkDirDisplay()
	items := editorListFiles(project)
	if items == nil {
		m.status.pushToast("error", "directory listing timed out", 3*time.Second)
		return
	}
	m.overlay = overlayAtFile
	m.overlayM = &listOverlay{title: "Files · @mention", items: items, query: query}
}

// editorListFiles wraps the editor's file walker with a 5s timeout so a
// hung mount can never freeze the picker (kept separate for tests).
var editorListFiles = func(project string) []string {
	ch := make(chan []string, 1)
	go func() { ch <- editor.ListFiles(project, 500) }()
	select {
	case items := <-ch:
		return items
	case <-time.After(5 * time.Second):
		return nil
	}
}

// modelKnown reports whether the qualified model ID is served by the
// registry (model picker validation).
func modelKnown(orch *orchestrator.Orchestrator, id string) bool {
	for _, m := range orch.Models() {
		if m == id {
			return true
		}
	}
	return false
}

// cycleTheme advances to the next built-in theme and applies it.
func (m *Model) cycleTheme() {
	names := ThemeNames()
	current := m.orch.SettingsSnapshot().Theme
	idx := 0
	for i, n := range names {
		if n == current {
			idx = i
			break
		}
	}
	next := names[(idx+1)%len(names)]
	snap := m.orch.SettingsSnapshot()
	snap.Theme = next
	if err := m.orch.UpdateSettings(context.Background(), snap); err != nil {
		m.status.pushToast("error", err.Error(), 4*time.Second)
		return
	}
	m.applyTheme(next)
	m.status.pushToast("success", "theme: "+next, 2*time.Second)
}

func (m *Model) removePending(c *Card) {
	for i, p := range m.pending {
		if p == c {
			m.pending = append(m.pending[:i], m.pending[i+1:]...)
			return
		}
	}
}

// layout splits the screen: header + messages + input + status/help.
func (m *Model) layout() {
	m.compact = m.width < 120 || m.height < 30
	// The final surface painter owns the terminal background. Keeping the
	// viewport transparent prevents nested SGR resets from producing black
	// rectangles after styled content on terminals with a non-black default.
	inputWidth := m.inputWidth()
	m.inputH = m.input.resize(inputWidth, max(m.height/3, 3))
	if m.activeTab == TabIDE {
		m.ensureIDEProportions()
		editorW, treeW, railW := m.idePaneWidths()
		if railW == 0 && m.ide != nil && m.ide.Focus == ideHITL {
			m.ide.Focus = ideEditor
		}
		m.viewport.Width = max(editorW-4, 10)
		m.viewport.Height = max(m.height-9-(m.inputH-2), 1)
		m.sidebar.width = max(railW-4, 10)
		m.sidebar.height = max(m.bodyHeight()-3, 3)
		_ = treeW
		return
	}
	leftW, rightW := m.harnessPaneWidths()
	m.viewport.Width = max(leftW-1, 10)
	// The agent workspace is a transcript plus a one-rule composer. Reserve
	// exactly the rendered composer height instead of carrying the old boxed
	// input's six rows of chrome.
	viewportHeight := m.bodyHeight() - m.composerHeight()
	viewportHeight -= m.slashPreviewHeight()
	if !m.showActivityRail() {
		// The conversation-first layout adds one compact activity summary.
		viewportHeight--
	}
	m.viewport.Height = max(viewportHeight, 1)
	if rightW > 0 {
		m.sidebar.width = max(rightW-1, 10)
	}
	m.sidebar.height = max(m.bodyHeight(), 3)
}

// inputWidth is the actual textarea width inside the capped composer dock.
// Keeping this identical to renderInputBox prevents the textarea from
// painting one long clipped line when the pane is wider than the dock.
func (m *Model) inputWidth() int {
	paneWidth := 0
	if m.activeTab == TabIDE {
		editorW, _, _ := m.idePaneWidths()
		paneWidth = max(editorW-1, 10)
	} else {
		paneWidth, _ = m.harnessPaneWidths()
	}
	dockW := min(max(paneWidth-4, 20), 112)
	return max(dockW-7, 10) // border + padding + "✦  " prompt
}

func (m *Model) inputChanged() {
	wasBottom := m.followOutput
	oldHeight := m.inputH
	m.layout()
	if m.activeTab == TabHarness && oldHeight != m.inputH {
		m.renderMessages()
		if wasBottom {
			m.viewport.GotoBottom()
		}
	}
}

// View renders the full screen.
func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	m.regions = nil
	if m.terminalTooSmall() {
		return m.renderTinyTerminal()
	}
	m.renderStatusline()
	tabs := m.renderTabBar()
	var body string
	if m.activeTab == TabIDE && m.ide != nil {
		body = m.renderIDE()
	} else if m.showActivityRail() {
		body = m.renderSplit()
	} else {
		body = m.renderCompact()
	}
	statusWidth := m.width
	if m.activeTab == TabHarness && m.showActivityRail() {
		statusWidth, _ = m.harnessPaneWidths()
	}
	footer := m.status.View(m.styles, statusWidth, m)
	if statusWidth < m.width {
		footer = lipgloss.NewStyle().Width(m.width).MaxWidth(m.width).Render(footer)
	}
	out := tabs + "\n" + body + "\n" + footer
	if !m.dialogs.empty() {
		out = m.dialogs.render(m.styles, m.width, m.height, out)
	} else if m.pendingAsk != nil {
		out = m.renderAskDialog(out)
	}
	return paintSurface(out, m.width, m.height, m.styles.T.Color(TokenSurface), m.styles.T.Color(TokenOyster))
}

const (
	minimumTerminalWidth  = 40
	minimumTerminalHeight = 10
)

func (m *Model) terminalTooSmall() bool {
	return m.width > 0 && m.height > 0 &&
		(m.width < minimumTerminalWidth || m.height < minimumTerminalHeight)
}

// renderTinyTerminal is deliberately sparse. Trying to preserve the normal
// pane hierarchy below 40x10 makes focus, labels and actions disappear; a
// dedicated recovery screen is both clearer and safer. It is adaptive down
// to the smallest terminal sizes used by the stress harness.
func (m *Model) renderTinyTerminal() string {
	width, height := max(m.width, 1), max(m.height, 1)
	help := m.overlay == overlayKeymap
	lines := make([]string, 0, 6)
	if width >= 18 {
		title := maestroCompactMark
		if help {
			title += " / HELP"
		}
		lines = append(lines, m.styles.Header.Render(title))
		if help {
			lines = append(lines,
				"ctrl+q  quit",
				"esc     close help",
				"alt+1/2 switch workspace",
			)
			if m.busy {
				lines = append(lines, "esc esc stop active task")
			}
		} else {
			lines = append(lines,
				m.styles.Hint.Render("Terminal too small"),
				fmt.Sprintf("%dx%d · minimum %dx%d", width, height, minimumTerminalWidth, minimumTerminalHeight),
				"Resize the window to continue",
				"ctrl+q quit · space ? help",
			)
			if m.busy {
				lines = append(lines, "esc esc stop active task")
			}
		}
	} else {
		if help {
			last := "min 40x10"
			if m.busy {
				last = "esc esc"
			}
			lines = append(lines, "HELP", "^Q quit", "esc close", last)
		} else {
			last := "^Q quit"
			if m.busy {
				last = "esc esc"
			}
			// Keep one printable safety cell at the right edge. Some terminals
			// defer a glyph written in the final column until the next write; the
			// former ten-cell "need 40x10" therefore appeared as "need 40x1" in
			// the real 10x4 smoke test.
			lines = append(lines, "MAESTRO", "TOO SMALL", "min 40x10", last)
		}
	}
	content := clampANSIHeight(clampANSIWidth(strings.Join(lines, "\n"), width), height)
	return paintSurface(content, width, height, m.styles.T.Color(TokenSurface), m.styles.T.Color(TokenOyster))
}

// renderSplit is the full 70/30 layout.
func (m *Model) renderSplit() string {
	left := m.renderLeft()
	_, rightW := m.harnessPaneWidths()
	right := m.renderHarnessRail(m.renderRight(), rightW, m.bodyHeight())
	m.registerSidebarRegions()
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		left,
		right,
	)
	if m.overlay != overlayNone {
		return m.renderOverlay(body)
	}
	if m.selectionMenu != nil {
		box := m.styles.Dialog.Render(m.renderSelectionMenu())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	if m.selectionAsk != nil {
		box := m.styles.Dialog.Render(m.renderSelectionAsk())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	return body
}

func (m *Model) renderHarnessRail(content string, width, height int) string {
	innerW := max(width-1, 1)
	content = clampANSIHeight(clampANSIWidth(content, innerW), max(height, 1))
	border := m.styles.T.Color(TokenIron)
	borderShape := lipgloss.NormalBorder()
	if m.focus == FocusSidebar {
		border = m.styles.T.Color(TokenCharple)
		borderShape.Left = "┃"
	}
	return lipgloss.NewStyle().
		Border(borderShape, false, false, false, true).
		BorderForeground(border).
		Background(m.styles.T.Color(TokenPanel)).
		Width(innerW).MaxWidth(innerW).
		Height(max(height, 1)).MaxHeight(max(height, 1)).
		Render(content)
}

// renderCompact collapses the sidebar to a single status-like line.
func (m *Model) renderCompact() string {
	left := m.renderLeft()
	var sb strings.Builder
	sb.WriteString(left)
	sb.WriteString(m.styles.Hint.Render(fmt.Sprintf(
		" %s · %s · %d agent(s) · %d action(s) · %d queued · ctrl+b activity",
		m.orch.ProjectName(), m.orch.BranchDisplay(),
		len(m.sidebar.agents), len(m.sidebar.hitl), len(m.pending),
	)) + "\n")
	body := sb.String()
	body = lipgloss.NewStyle().Width(m.width).MaxWidth(m.width).Render(clampANSIWidth(body, m.width))
	if m.overlay != overlayNone {
		return m.renderOverlay(body)
	}
	if m.selectionAsk != nil {
		box := m.styles.Dialog.Render(m.renderSelectionAsk())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	if m.selectionMenu != nil {
		box := m.styles.Dialog.Render(m.renderSelectionMenu())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	return body
}

func (m *Model) renderLeft() string {
	leftW := m.harnessChatWidth()
	var b strings.Builder

	// Messages + scrollbar (the bar is a side column, one cell wide — the
	// viewport width already reserves it).
	msgPane := m.renderTranscriptViewport()
	if bar := m.scroll.View(m.styles); bar != "" {
		msgPane = lipgloss.JoinHorizontal(lipgloss.Top, msgPane, bar)
	}
	b.WriteString(strings.TrimSuffix(msgPane, "\n") + "\n")
	m.registerTranscriptFileRegions()
	m.registerViewportCardRegions()

	// Input box + inline slash-command preview.
	inputBox := m.renderInputBox(leftW)
	b.WriteString(inputBox + "\n")
	if preview := m.renderSlashPreview(leftW, tabBarRows+m.viewport.Height+lipgloss.Height(inputBox)); preview != "" {
		b.WriteString(preview + "\n")
	}
	content := strings.TrimSuffix(b.String(), "\n")
	// The pane must never exceed its allotted height: the input box and its
	// frame would otherwise slide under the statusline (broken frame).
	content = keepLastLines(content, m.bodyHeight())
	// The viewport and composer are already width-bounded. Re-wrapping the
	// complete ANSI block here made every wheel frame parse the same visible
	// rows a second time and generated tens of thousands of allocations.
	return content
}

func normalizeTranscriptLines(content string, width int) []string {
	width = max(width, 1)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		line = stripBrokenANSI(ansi.Truncate(line, width, ""))
		if pad := width - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lines[i] = line
	}
	return lines
}

// renderTranscriptViewport is the hot scroll path. bubbles/viewport.View
// feeds every visible ANSI row back through a complete Lipgloss sizing and
// wrapping pipeline. The transcript is already normalized when its content
// changes, so scrolling only needs a slice and blank-row padding.
func (m *Model) renderTranscriptViewport() string {
	height := max(m.viewport.Height, 1)
	top := clamp(m.viewport.YOffset, 0, max(len(m.transcriptLines)-1, 0))
	bottom := min(top+height, len(m.transcriptLines))
	lines := make([]string, 0, height)
	if top < bottom {
		lines = append(lines, m.transcriptLines[top:bottom]...)
	}
	blank := strings.Repeat(" ", max(m.viewport.Width, 1))
	for len(lines) < height {
		lines = append(lines, blank)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderInputBox(width int) string {
	border := m.styles.T.Color(TokenIron)
	if m.focus == FocusInput {
		border = m.styles.T.Color(TokenCharple)
	}
	dockW := min(max(width-4, 20), 112)
	inset := max((width-dockW)/2, 2)
	contentW := max(dockW-4, 1)
	promptMark := "·"
	if m.focus == FocusInput {
		promptMark = "✦"
	}
	prompt := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenCharple)).Bold(true).Render(promptMark + "  ")
	input := ""
	if m.input.Value() == "" {
		input = m.styles.InputHint.Render(m.composerPlaceholder(false))
		if m.focus == FocusInput {
			input += lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Render(" ▏")
		}
	} else {
		input = m.input.sizedView(max(contentW-lipgloss.Width(prompt), 1), m.inputH)
		input = strings.ReplaceAll(input, "\n", "\n"+strings.Repeat(" ", lipgloss.Width(prompt)))
	}
	main := prompt + clampANSIWidth(input, max(contentW-lipgloss.Width(prompt), 1))
	actions, positions := m.renderComposerActions(contentW, "")
	content := main + "\n" + actions

	inputTop := tabBarRows + m.viewport.Height
	actionY := inputTop + 2 + m.inputH
	for _, action := range positions {
		m.regions = append(m.regions, Region{
			X: action.X + inset + 2, Y: actionY, W: action.W, H: 1,
			Action: action.Action, Label: action.Label, Binding: action.Binding,
		})
	}
	dock := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Background(m.styles.T.Color(TokenPanel)).
		Padding(0, 1).
		Width(dockW).MaxWidth(dockW).
		Render(content)
	return strings.Repeat(" ", width) + "\n" + lipgloss.NewStyle().PaddingLeft(inset).Width(width).MaxWidth(width).Render(dock)
}

func (m *Model) composerHeight() int { return m.inputH + 4 }

type composerActionRegion struct {
	X, W    int
	Action  ActionID
	Label   string
	Binding string
}

func (m *Model) composerPlaceholder(ide bool) string {
	if m.busy {
		return "Maestro is working…"
	}
	if ide {
		return "Ask Maestro about this code…"
	}
	return "Discuss the change, then use /propose…"
}

// renderComposerActions creates the quiet, mouse-complete action row. It
// progressively drops labels as the terminal narrows while retaining the
// one-cell Unicode affordances.
func (m *Model) renderComposerActions(width int, selection string) (string, []composerActionRegion) {
	muted := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke))
	accent := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Bold(true)
	separator := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenIron)).Render("  ·  ")

	var left string
	var regions []composerActionRegion
	if selection != "" {
		selection = truncateRunes(selection, max(width/2, 12))
		left = accent.Render("⌖ ") + muted.Render(selection)
		regions = append(regions, composerActionRegion{X: 0, W: lipgloss.Width(left), Action: ActionIDESelection, Label: "selection actions", Binding: "space a"})
	} else if len(m.contextRefs) > 0 {
		latest := m.contextRefs[len(m.contextRefs)-1]
		label := compactWorkspacePath(latest.Path)
		if label == "" || label == "chat" {
			label = "conversation"
		}
		contextLabel := accent.Render(fmt.Sprintf("@ %d", len(m.contextRefs))) + muted.Render("  "+truncateRunes(label, max(width/3, 12)))
		commandLabel := accent.Render("/") + muted.Render(" commands")
		left = contextLabel + separator + commandLabel
		regions = append(regions,
			composerActionRegion{X: 0, W: lipgloss.Width(contextLabel), Action: ActionAddContext, Label: "add another context", Binding: "@"},
			composerActionRegion{X: lipgloss.Width(contextLabel) + lipgloss.Width(separator), W: lipgloss.Width(commandLabel), Action: ActionPalette, Label: "open commands", Binding: "ctrl+p"},
		)
	} else {
		contextLabel := accent.Render("@") + muted.Render(" context")
		commandLabel := accent.Render("/") + muted.Render(" commands")
		if width < 42 {
			contextLabel, commandLabel = accent.Render("@"), accent.Render("/")
		}
		left = contextLabel + separator + commandLabel
		regions = append(regions,
			composerActionRegion{X: 0, W: lipgloss.Width(contextLabel), Action: ActionAddContext, Label: "add file context", Binding: "@"},
			composerActionRegion{X: lipgloss.Width(contextLabel) + lipgloss.Width(separator), W: lipgloss.Width(commandLabel), Action: ActionPalette, Label: "open commands", Binding: "ctrl+p"},
		)
	}

	sendStyle := lipgloss.NewStyle().
		Foreground(m.styles.T.Color(TokenOyster)).
		Background(m.styles.T.Blend(TokenPanel, TokenCharple, 0.25)).
		Padding(0, 1)
	if strings.TrimSpace(m.input.Value()) != "" {
		sendStyle = sendStyle.
			Foreground(m.styles.T.Color(TokenChar)).
			Background(m.styles.T.Color(TokenCharple)).
			Bold(true)
	}
	right := sendStyle.Render("enter ↵")
	if m.busy {
		right = lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSash)).Bold(true).Render("esc esc  stop")
	} else if width < 34 {
		right = sendStyle.Render("↵")
	}
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	line := clampANSIWidth(left+strings.Repeat(" ", gap)+right, width)
	if !m.busy {
		regions = append(regions, composerActionRegion{
			X: max(width-lipgloss.Width(right), 0), W: lipgloss.Width(right),
			Action: ActionSend, Label: "send message", Binding: "enter",
		})
	}
	return line, regions
}

// keepLastLines keeps the bottom n lines of a block so the input box and
// its frame always stay on screen, even when the content above overflows.
func keepLastLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func (m *Model) renderRight() string {
	m.sidebar.coach = m.visibleCoachOffer()
	return m.sidebar.View(m.styles, m.orch)
}

func (m *Model) renderOverlay(out string) string {
	if settings, ok := m.overlayM.(*settingsOverlay); ok && m.overlay == overlaySettings {
		w := min(max(m.width-4, 4), 144)
		h := min(max(m.bodyHeight()-2, 3), 40)
		content := settings.viewSized(m.styles, w, h)
		x := max((m.width-lipgloss.Width(content))/2, 0)
		y := max((m.bodyHeight()-lipgloss.Height(content))/2, 0)
		settings.originX, settings.originY = x, y+tabBarRows
		return overlayAt(out, m.width, m.bodyHeight(), x, y, content)
	}
	if workspace, ok := m.overlayM.(*taskModelOverlay); ok && m.overlay == overlayModelPicker {
		w := min(max(m.width-4, 1), 136)
		h := min(max(m.bodyHeight()-2, 1), 34)
		content := workspace.viewSized(m.styles, w, h)
		x := max((m.width-lipgloss.Width(content))/2, 0)
		y := max((m.bodyHeight()-lipgloss.Height(content))/2, 0)
		workspace.originX, workspace.originY = x, y+tabBarRows
		return overlayAt(out, m.width, m.bodyHeight(), x, y, content)
	}
	if providers, ok := m.overlayM.(*providersOverlay); ok && m.overlay == overlayProviders {
		w := min(max(m.width-4, 1), 132)
		h := min(max(m.bodyHeight()-2, 1), 34)
		content := providers.viewSized(m.styles, w, h)
		x := max((m.width-lipgloss.Width(content))/2, 0)
		y := max((m.bodyHeight()-lipgloss.Height(content))/2, 0)
		providers.originX, providers.originY = x, y+tabBarRows
		return overlayAt(out, m.width, m.bodyHeight(), x, y, content)
	}
	var content string
	dialogW := min(max(m.width-6, 8), 100)
	switch m.overlay {
	case overlayKeymap:
		content = KeymapView(m.styles, min(dialogW, 60))
	case overlayPalette, overlayModelPicker, overlaySessionPicker, overlayEngine, overlayAsk, overlayCheckpoints, overlayGit, overlayCoachMode:
		if list, ok := overlayList(m.overlayM); ok {
			pickerWidth := min(dialogW, 40)
			if m.overlay == overlayModelPicker {
				pickerWidth = min(dialogW, 96)
			}
			content = list.View(m.styles, pickerWidth)
		}
	case overlayAuth:
		content = m.overlayM.View(m.styles, min(dialogW, 78))
	case overlayWhichKey:
		content = whichKeyView(m.styles)
	case overlayTimeline:
		if tl, ok := m.overlayM.(*timelineOverlay); ok {
			content = tl.View(m.styles, min(dialogW, 80))
		}
	case overlayAgentDetail:
		if ag, ok := m.overlayM.(*agentDetailOverlay); ok {
			content = ag.View(m.styles, min(dialogW, 70))
		}
	case overlayAtFile:
		if list, ok := overlayList(m.overlayM); ok {
			content = list.View(m.styles, min(dialogW, 72))
		}
	case overlayDiff:
		if diff, ok := m.overlayM.(*diffOverlay); ok {
			w := dialogW
			if h := m.bodyHeight(); h > 0 {
				// The full dialog reserves eight rows for border, padding,
				// scroll markers and footer. Tiny terminals use a denser two-row
				// chrome so at least one diff line remains visible.
				if h < 14 {
					diff.maxLines = max(h-6, 1)
				} else {
					diff.maxLines = max(h-8, 1)
				}
			}
			content = diff.View(m.styles, w)
		}
	case overlayForm:
		if m.overlayM != nil {
			content = m.overlayM.View(m.styles, min(dialogW, 72))
		}
	case overlayCoach:
		if coach, ok := m.overlayM.(*coachOverlay); ok {
			// Dialog adds two border and two vertical-padding rows.
			content = coach.ViewSized(m.styles, min(dialogW, 72), max(m.bodyHeight()-4, 1))
		}
	}
	box := m.styles.Dialog.Render(content)
	if diff, ok := m.overlayM.(*diffOverlay); ok && m.overlay == overlayDiff {
		m.registerDiffOverlayRegions(diff, content, box)
	}
	return lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) harnessPaneWidths() (chatW, sidebarW int) {
	if !m.showActivityRail() {
		return max(m.width, 1), 0
	}
	sidebarW = max(m.width*31/100, 28)
	chatW = max(m.width-sidebarW, 20)
	if chatW+sidebarW > m.width {
		chatW = max(m.width-sidebarW, 1)
	}
	return chatW, sidebarW
}

func (m *Model) showActivityRail() bool {
	return m.activityOpen && !m.compact && m.activeTab == TabHarness
}

func (m *Model) toggleActivity() {
	m.activityOpen = !m.activityOpen
	if !m.activityOpen && m.focus == FocusSidebar {
		m.focus = FocusInput
	}
	if !m.activityOpen && m.ide != nil && m.ide.Focus == ideHITL {
		m.ide.Focus = ideEditor
	}
	m.layout()
	m.invalidateMessageCaches()
	m.renderMessages()
}

func (m *Model) harnessChatWidth() int {
	chatW, _ := m.harnessPaneWidths()
	return chatW
}

func (m *Model) bodyHeight() int { return max(m.height-2, 1) }

// ctx returns a background context for non-interactive ops.
func (m *Model) ctx() context.Context { return context.Background() }

// timelineOverlay lists the conversation messages for quick jumping
// (ctrl+t). Enter scrolls the transcript to the selected message.
type timelineOverlay struct {
	messages []*Message
	sel      int
	scroll   int
}

// newTimelineOverlay snapshots the transcript.
func newTimelineOverlay(messages []*Message) *timelineOverlay {
	return &timelineOverlay{messages: messages}
}

func (o *timelineOverlay) up() {
	if o.sel > 0 {
		o.sel--
	}
}

func (o *timelineOverlay) down() {
	if o.sel < len(o.messages)-1 {
		o.sel++
	}
}

func (o *timelineOverlay) selectedIndex() (int, bool) {
	if o.sel < 0 || o.sel >= len(o.messages) {
		return 0, false
	}
	return o.sel, true
}

// View renders the timeline with a scrolling window.
func (o *timelineOverlay) View(styles Styles, width int) string {
	visible := 14
	if len(o.messages) <= visible {
		o.scroll = 0
	} else if o.sel < o.scroll {
		o.scroll = o.sel
	} else if o.sel >= o.scroll+visible {
		o.scroll = o.sel - visible + 1
	}
	var b strings.Builder
	b.WriteString(styles.DialogTitle("Timeline") + "\n")
	b.WriteString(styles.Hint.Render("↑/↓ · enter jump · esc close") + "\n\n")
	if len(o.messages) == 0 {
		b.WriteString(styles.SidebarItem.Render("(empty)") + "\n")
		return b.String()
	}
	if o.scroll > 0 {
		b.WriteString(styles.Hint.Render("↑ …") + "\n")
	}
	for i := o.scroll; i < len(o.messages) && i < o.scroll+visible; i++ {
		msg := o.messages[i]
		marker := "  "
		st := styles.SidebarItem
		if i == o.sel {
			marker = "▸ "
			st = styles.SidebarActive
		}
		preview := truncateRunes(strings.ReplaceAll(msg.Text, "\n", " "), max(width-24, 10))
		role := roleLabel(msg.Role)
		line := fmt.Sprintf("%s%-9s %s", marker, role, preview)
		b.WriteString(st.Width(max(width-2, 1)).Render(line) + "\n")
	}
	if o.scroll+visible < len(o.messages) {
		b.WriteString(styles.Hint.Render("↓ …") + "\n")
	}
	return b.String()
}

// openTimeline forces a full transcript render and opens the jump list.
func (m *Model) openTimeline() tea.Model {
	m.forceFullRender = true
	m.renderMessages()
	m.overlay = overlayTimeline
	m.overlayM = newTimelineOverlay(m.messages)
	return m
}

// closeTimeline resumes the streaming view.
func (m *Model) closeTimeline() tea.Model {
	m.overlay = overlayNone
	m.forceFullRender = false
	m.renderMessages()
	return m
}

// jumpToMessage scrolls the transcript to the selected message.
func (m *Model) jumpToMessage(idx int) tea.Model {
	m.overlay = overlayNone
	m.forceFullRender = false
	if idx < 0 || idx >= len(m.msgRows) {
		m.renderMessages()
		return m
	}
	m.viewport.YOffset = clamp(m.msgRows[idx], 0, max(m.viewport.TotalLineCount()-1, 0))
	m.renderMessages()
	m.status.pushToast("info", "jumped to message", 2*time.Second)
	return m
}

// agentDetailOverlay is the sub-agent drill-down (click a sidebar row).
type agentDetailOverlay struct {
	Role   string
	Status string
	Detail string
}

// View renders the agent detail card.
func (o *agentDetailOverlay) View(styles Styles, width int) string {
	var b strings.Builder
	b.WriteString(styles.DialogTitle("Sub-agent · "+safeIDEPlainText(o.Role)) + "\n\n")
	statusColor := styles.T.Color(TokenSmoke)
	switch o.Status {
	case "running":
		statusColor = styles.T.Color(TokenCharple)
	case "done":
		statusColor = styles.T.Color(TokenJulep)
	case "error":
		statusColor = styles.T.Color(TokenSash)
	}
	b.WriteString(lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render("  "+safeIDEPlainText(o.Status)) + "\n\n")
	if o.Detail != "" {
		b.WriteString(styles.MessageMuted.Render("  "+truncateRunes(safeIDEPlainText(o.Detail), max(width-8, 20))) + "\n\n")
	}
	if o.Status == "running" {
		b.WriteString(styles.Hint.Render("  [c] cancel sub-agent"))
	} else {
		b.WriteString(styles.Hint.Render("  esc close"))
	}
	return b.String()
}

// whichKeyView is the space-leader preview: the commands reachable from
// the pending Space.
func whichKeyView(styles Styles) string {
	var b strings.Builder
	b.WriteString(styles.DialogTitle("Space leader") + "\n\n")
	rows := []struct{ key, desc string }{
		{"space ?", "keymap viewer"},
		{"space t", "cycle theme"},
		{"space v", "expand code block"},
		{"space a", "accept proposal"},
		{"space d", "discard proposal"},
	}
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-9s %s\n", styles.Hint.Render(r.key), r.desc))
	}
	b.WriteString("\n" + styles.Hint.Render("release with any key · esc cancels"))
	return b.String()
}

// welcomeMessage is the empty-state greeting.
func welcomeMessage(styles Styles, width int) string {
	return maestroLogo(styles, width, 24)
}
