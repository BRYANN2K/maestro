package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/proposals"
)

// ideFocus is the IDE focus ring position.
type ideFocus int

type ideExplorerView int

const (
	ideExplorerFiles ideExplorerView = iota
	ideExplorerChanges
)

// IDE focus targets.
const (
	ideEditor ideFocus = iota
	ideChat
	ideTree
	ideHITL
)

// IDEState is the /ide mode: editor pane + file tree + HITL (§5.2).
type IDEState struct {
	Ed    *editor.Editor
	UI    *editor.UI
	Focus ideFocus

	cwPending       bool // Ctrl+W pressed, awaiting h/j/k/l
	spacePending    bool // Space pressed, awaiting e (chat) or t (theme)
	treeSel         int
	explorerView    ideExplorerView
	treeExpanded    map[string]bool
	treeScroll      scrollbar
	mouseSelecting  bool
	mouseMoved      bool
	preview         bool
	previewScroll   int
	proposalPreview *proposals.Proposal
	proposalScroll  int
	proposalHunk    int
	fileCache       []string
	treeCache       []treeEntry
	treeCacheValid  bool
	filesLoading    bool
	gutterDeferred  bool
	gutterError     string
	hydrating       bool
	hydration       ideHydrationPlan
	operation       uint64
	operationCancel context.CancelFunc
	queuedOpen      *ideOpenRequest
	project         string
	git             *git.Client
	themePicker     *themePickerState
	notify          func(level, message string)
	onOpenRejected  func()
	symbolCache     ideSymbolCache
}

type ideHydrationPlan struct {
	project    string
	keymap     string
	sessionDir string
	specPath   string
}

type ideOpenRequest struct {
	path         string
	line, column int
	manual       bool
}

type ideOperationKind uint8

const (
	ideOperationHydrate ideOperationKind = iota + 1
	ideOperationOpen
	ideOperationStage
)

const (
	ideHydrationTimeout = 5 * time.Second
	ideFileOpenTimeout  = 3 * time.Second
	ideHunkStageTimeout = 5 * time.Second
)

// ideOperationMsg is applied by Model.Update. target, operation, and
// workspace form the stale-result identity; workers never mutate the model.
type ideOperationMsg struct {
	kind      ideOperationKind
	operation uint64
	target    *IDEState
	workspace orchestrator.WorkspaceSnapshot
	editor    *editor.Editor
	buffer    *editor.Buffer
	ackCrash  bool
	path      string
	line      int
	column    int
	manual    bool
	warning   string
	err       error
}

var (
	ideEditorHydrator = hydrateIDEEditor
	ideFileLoader     = editor.LoadContext
	ideHunkStager     = editor.StageHunks
)

// themePickerState is the Space t overlay.
type themePickerState struct {
	Sel      int
	Original string
}

// View renders the theme list with palette swatches and an explicit saved vs
// preview state. Selection is a reversible live preview: Enter persists it,
// Escape restores Original.
func (t *themePickerState) View(styles Styles, width int) string {
	names := themeNames()
	var b strings.Builder
	preview := t.Original
	if t.Sel >= 0 && t.Sel < len(names) {
		preview = names[t.Sel]
	}
	b.WriteString(styles.DialogTitle("Themes") + "\n")
	b.WriteString(styles.Hint.Render("preview "+preview+" · saved "+t.Original) + "\n\n")
	for i, n := range names {
		marker := "  "
		style := styles.SidebarItem
		if i == t.Sel {
			marker = "▸ "
			style = styles.SidebarActive
		}
		state := ""
		if n == t.Original {
			state = "  saved"
		}
		line := marker + themeSwatch(n) + "  " + n + state
		b.WriteString(style.Width(max(width-2, 1)).Render(line) + "\n")
	}
	b.WriteString("\n" + styles.Hint.Render("↑/↓ preview · enter save · esc restore"))
	return b.String()
}

// NewIDE builds the IDE state for the project.
func NewIDE(m *Model, project string, g *git.Client) *IDEState {
	return newIDE(m, project, g, false)
}

// newDeferredIDE builds the immediately usable editor shell without running
// Git commands on Bubble Tea's event loop. The file tree and gutter arrive in
// the existing background workspace refresh. Runtime tab/session transitions
// use this path unconditionally: even an idle repository can have a slow
// index, filesystem, or full diff that must not own the event loop.
func newDeferredIDE(m *Model, project string, g *git.Client) *IDEState {
	return newIDE(m, project, g, true)
}

func newIDE(m *Model, project string, g *git.Client, deferGit bool) *IDEState {
	plan := snapshotIDEHydration(m, project)
	ed := editor.NewEditor(project)
	ed.SetKeymap(plan.keymap)
	ed.Sessions.SetDir(plan.sessionDir)
	ed.Crash.SetDir(plan.sessionDir)
	if !deferGit {
		if loaded, warning, ackCrash, err := hydrateIDEEditor(context.Background(), plan); err == nil && loaded != nil {
			ed = loaded
			if ackCrash {
				ed.Crash.AcknowledgeRestore()
			}
			if warning != "" {
				ed.Status = warning
			}
		} else if err != nil {
			ed.Status = "Editor recovery was unavailable: " + err.Error()
		}
	}
	if len(ed.Buffers) == 0 {
		ed.Buffers = append(ed.Buffers, editor.NewBuffer("untitled", nil))
	}
	configureIDEEditor(m, ed, g)
	palette := Charmtone().EditorPalette()
	if m != nil {
		palette = m.styles.T.EditorPalette()
	}
	ui := editor.NewUI(ed, palette)
	ui.Gutter = editor.NewGutter(g)
	if deferGit {
		ui.Gutter.Path = ed.Buffer().Path
	} else {
		ui.Gutter.Refresh(context.Background(), ed.Buffer().Path)
	}
	state := &IDEState{
		Ed: ed, UI: ui, Focus: ideEditor,
		treeExpanded: map[string]bool{},
		project:      project, git: g,
		filesLoading:   deferGit,
		gutterDeferred: deferGit,
		hydrating:      deferGit,
		hydration:      plan,
	}
	if !deferGit {
		// The public constructor is also used by benchmarks and headless tests.
		// Runtime screen transitions use newDeferredIDE and install this snapshot
		// from a tea.Cmd, keeping View observational.
		state.fileCache, _ = editor.ListFiles(context.Background(), project, 500)
	}
	if m != nil && m.status != nil {
		state.notify = func(level, message string) {
			m.status.pushToast(level, message, 5*time.Second)
		}
		if ed.Status != "" {
			state.notify("warn", ed.Status)
		}
	}
	if m != nil {
		state.onOpenRejected = func() {
			m.pendingSelection = nil
			m.selectionMenu = nil
		}
	}
	return state
}

func snapshotIDEHydration(m *Model, project string) ideHydrationPlan {
	plan := ideHydrationPlan{project: project, keymap: string(editor.KeymapStandard)}
	if m != nil && m.orch != nil {
		plan.keymap = m.orch.SettingsSnapshot().EditorMode
		if sp := m.orch.ActiveSpec(); sp != nil {
			plan.specPath = m.orch.SpecPath(sp.ID)
		}
	}
	if home, err := userHome(); err == nil {
		plan.sessionDir = filepath.Join(home, ".maestro", "editor", sanitize(project))
	}
	return plan
}

func configureIDEEditor(m *Model, ed *editor.Editor, g *git.Client) {
	if ed == nil {
		return
	}
	ed.DeferExternalIO()
	ed.OpenFile = func(path string) error {
		if err := ed.Open(path); err != nil {
			return err
		}
		return nil
	}
	ed.SaveBuffer = func(b *editor.Buffer) error {
		if err := b.WriteFile(); err != nil {
			return err
		}
		return nil
	}
	ed.StageHunks = func(b *editor.Buffer) error {
		return editor.StageHunks(context.Background(), g, b.Path)
	}
	ed.ProposalSrc = func() []editor.ReviewProposal {
		if m == nil || m.proposals == nil {
			return nil
		}
		ids, err := m.proposals.Pending()
		if err != nil {
			return nil
		}
		var out []editor.ReviewProposal
		for _, id := range ids {
			p, err := m.proposals.Load(id)
			if err != nil {
				continue
			}
			out = append(out, editor.ReviewProposal{Prop: p, Store: m.proposals})
		}
		return out
	}
}

func hydrateIDEEditor(ctx context.Context, plan ideHydrationPlan) (*editor.Editor, string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", false, err
	}
	ed := editor.NewEditor(plan.project)
	ed.SetKeymap(plan.keymap)
	ed.Sessions.SetDir(plan.sessionDir)
	ed.Crash.SetDir(plan.sessionDir)
	warning := ""
	if _, err := ed.Sessions.Load(ed); err != nil {
		warning = "Editor session recovery was skipped because its state was unsafe or unreadable."
	}
	if err := ctx.Err(); err != nil {
		return nil, warning, false, err
	}
	states, crashPresent, crashErr := ed.Crash.PreviewRestore()
	if crashErr == nil && len(states) > 0 {
		ed.RestoreBuffers(states)
	} else if crashErr != nil {
		warning = "Editor crash recovery was skipped because its state was unsafe or unreadable."
	}
	if err := ctx.Err(); err != nil {
		return nil, warning, false, err
	}
	if len(ed.Buffers) == 0 && plan.specPath != "" {
		if b, err := editor.LoadContext(ctx, plan.specPath); err == nil {
			ed.InstallLoadedBuffer(plan.specPath, b, nil)
		}
	}
	if len(ed.Buffers) == 0 || (len(ed.Buffers) == 1 && ed.Buffers[0].Path == "untitled" && !ed.Buffers[0].Dirty) {
		if starter := starterFileContext(ctx, plan.project); starter != "" {
			if b, err := editor.LoadContext(ctx, starter); err == nil {
				ed.Buffers = nil
				ed.CurBuf = 0
				ed.InstallLoadedBuffer(starter, b, nil)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, warning, false, err
	}
	if len(ed.Buffers) == 0 {
		ed.Buffers = append(ed.Buffers, editor.NewBuffer("untitled", nil))
	}
	return ed, warning, crashPresent && crashErr == nil, nil
}

func starterFileContext(ctx context.Context, project string) string {
	for _, name := range []string{"README.md", "MAESTRO.md", filepath.Join("docs", "ARCHITECTURE.md"), "CHANGELOG.md"} {
		if ctx != nil && ctx.Err() != nil {
			return ""
		}
		path := filepath.Join(project, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// IdeActive reports whether the IDE tab is selected (demos, tests).
func (m *Model) IdeActive() bool { return m.activeTab == TabIDE && m.ide != nil }

// IdeEditor exposes the IDE editor (demos, tests).
func (m *Model) IdeEditor() *editor.Editor {
	if m.ide == nil {
		return nil
	}
	return m.ide.Ed
}

// OpenFileAt opens a path (used by the file tree).
func (s *IDEState) OpenFileAt(path string) bool {
	full := filepath.Join(s.project, path)
	if err := s.Ed.Open(full); err != nil {
		s.Ed.CancelSelection()
		if s.onOpenRejected != nil {
			s.onOpenRejected()
		}
		s.Ed.Status = editor.SafeOpenError(err)
		if s.notify != nil {
			s.notify("warn", s.Ed.Status)
		}
		return false
	}
	s.clearGutter(full)
	s.UI.SetScroll(0)
	s.Focus = ideEditor
	return true
}

func (s *IDEState) cancelOperation() {
	if s == nil {
		return
	}
	if s.operationCancel != nil {
		s.operationCancel()
		s.operationCancel = nil
	}
	s.operation++
	s.hydrating = false
}

func (s *IDEState) beginOperation() (uint64, context.Context) {
	s.cancelOperation()
	ctx, cancel := context.WithCancel(context.Background())
	s.operationCancel = cancel
	return s.operation, ctx
}

func (s *IDEState) queueOpen(path string, line, column int, manual bool) {
	if s == nil || strings.TrimSpace(path) == "" {
		return
	}
	s.queuedOpen = &ideOpenRequest{path: path, line: line, column: column, manual: manual}
}

func (s *IDEState) takeQueuedOpen() *ideOpenRequest {
	if s == nil {
		return nil
	}
	request := s.queuedOpen
	s.queuedOpen = nil
	return request
}

func (m *Model) beginIDEHydration(target *IDEState) tea.Cmd {
	if m == nil || m.orch == nil || target == nil || m.quitting {
		return nil
	}
	operation, ctx := target.beginOperation()
	target.hydrating = true
	workspace := m.orch.SnapshotWorkspace()
	plan := target.hydration
	return func() tea.Msg {
		bounded, stop := context.WithTimeout(ctx, ideHydrationTimeout)
		defer stop()
		ed, warning, ackCrash, err := ideEditorHydrator(bounded, plan)
		return ideOperationMsg{
			kind: ideOperationHydrate, operation: operation, target: target,
			workspace: workspace, editor: ed, ackCrash: ackCrash, warning: warning, err: err,
		}
	}
}

func (m *Model) beginIDEOpen(target *IDEState, request ideOpenRequest) tea.Cmd {
	if m == nil || m.orch == nil || target == nil || target.Ed == nil || m.quitting {
		return nil
	}
	path := request.path
	if !filepath.IsAbs(path) {
		path = filepath.Join(target.project, filepath.FromSlash(path))
	}
	path = filepath.Clean(path)
	for i, b := range target.Ed.Buffers {
		if b == nil || filepath.Clean(b.Path) != path {
			continue
		}
		target.cancelOperation()
		target.Ed.CurBuf = i
		target.Ed.Status = "opened " + safeIDEPlainText(path)
		target.clearGutter(path)
		target.UI.SetScroll(0)
		target.Focus = ideEditor
		applyIDEOpenPosition(target, request.line, request.column)
		if request.manual {
			m.followAgent = false
		}
		return m.withPendingIDEWorkspaceRefresh(m.refreshIDEGutter())
	}
	operation, ctx := target.beginOperation()
	target.Ed.Status = "opening " + safeIDEPlainText(path) + "…"
	workspace := m.orch.SnapshotWorkspace()
	return func() tea.Msg {
		bounded, stop := context.WithTimeout(ctx, ideFileOpenTimeout)
		defer stop()
		buffer, err := ideFileLoader(bounded, path)
		return ideOperationMsg{
			kind: ideOperationOpen, operation: operation, target: target,
			workspace: workspace, buffer: buffer, path: path,
			line: request.line, column: request.column, manual: request.manual, err: err,
		}
	}
}

func (m *Model) beginQueuedIDEOpen(target *IDEState) tea.Cmd {
	if target == nil {
		return nil
	}
	request := target.takeQueuedOpen()
	if request == nil {
		return nil
	}
	return m.beginIDEOpen(target, *request)
}

func (m *Model) beginIDEHunkStage(target *IDEState, path string) tea.Cmd {
	if m == nil || m.orch == nil || target == nil || target.git == nil || path == "" || m.quitting {
		return nil
	}
	operation, ctx := target.beginOperation()
	target.Ed.Status = "staging hunks…"
	workspace := m.orch.SnapshotWorkspace()
	client := target.git
	return func() tea.Msg {
		bounded, stop := context.WithTimeout(ctx, ideHunkStageTimeout)
		defer stop()
		err := ideHunkStager(bounded, client, path)
		return ideOperationMsg{
			kind: ideOperationStage, operation: operation, target: target,
			workspace: workspace, path: path, err: err,
		}
	}
}

// finishIDEOperation is the event-loop half of every IDE effect. Model.Update
// routes ideOperationMsg here. Late completions cannot mutate a closed/replaced
// IDE or workspace.
func (m *Model) finishIDEOperation(msg ideOperationMsg) tea.Cmd {
	if m == nil || m.orch == nil || msg.target == nil || msg.operation != msg.target.operation {
		return nil
	}
	target := msg.target
	if target.operationCancel != nil {
		target.operationCancel()
		target.operationCancel = nil
	}
	// Consume this generation before applying it so a duplicated delivery is
	// stale as well as older/superseded completions.
	target.operation++
	target.hydrating = false
	if target != m.ide || !m.orch.WorkspaceIsCurrent(msg.workspace) {
		return nil
	}
	switch msg.kind {
	case ideOperationHydrate:
		if msg.err != nil {
			if !errors.Is(msg.err, context.Canceled) {
				target.Ed.Status = "Editor recovery was unavailable. The empty editor remains usable."
				if target.notify != nil {
					target.notify("warn", target.Ed.Status)
				}
			}
			return m.refreshModifiedFiles()
		}
		applied := false
		if msg.editor != nil && editorShellUnmodified(target.Ed) {
			configureIDEEditor(m, msg.editor, target.git)
			target.Ed = msg.editor
			applied = true
			width, height := target.UI.Width, target.UI.Height
			target.UI = editor.NewUI(msg.editor, m.styles.T.EditorPalette())
			target.UI.Width, target.UI.Height = width, height
			target.UI.Gutter = editor.NewGutter(target.git)
			if b := msg.editor.Buffer(); b != nil {
				target.clearGutter(b.Path)
			}
		}
		if msg.warning != "" {
			target.Ed.Status = msg.warning
			if target.notify != nil {
				target.notify("warn", msg.warning)
			}
		}
		var acknowledgeCrash tea.Cmd
		if applied && msg.ackCrash {
			crash := target.Ed.Crash
			acknowledgeCrash = func() tea.Msg {
				crash.AcknowledgeRestore()
				return nil
			}
		}
		return tea.Batch(m.refreshModifiedFiles(), acknowledgeCrash)
	case ideOperationOpen:
		if !target.Ed.InstallLoadedBuffer(msg.path, msg.buffer, msg.err) {
			target.Ed.CancelSelection()
			if target.onOpenRejected != nil {
				target.onOpenRejected()
			}
			if !errors.Is(msg.err, context.Canceled) {
				target.Ed.Status = editor.SafeOpenError(msg.err)
				if target.notify != nil {
					target.notify("warn", target.Ed.Status)
				}
			}
			return m.withPendingIDEWorkspaceRefresh(nil)
		}
		target.Ed.Status = "opened " + safeIDEPlainText(msg.path)
		target.clearGutter(msg.path)
		target.UI.SetScroll(0)
		target.Focus = ideEditor
		applyIDEOpenPosition(target, msg.line, msg.column)
		if msg.manual {
			m.followAgent = false
		}
		return m.withPendingIDEWorkspaceRefresh(m.refreshIDEGutter())
	case ideOperationStage:
		if msg.err != nil {
			if !errors.Is(msg.err, context.Canceled) {
				target.Ed.Status = "error: " + safeIDEPlainText(msg.err.Error())
				if target.notify != nil {
					target.notify("error", target.Ed.Status)
				}
			}
			return m.withPendingIDEWorkspaceRefresh(nil)
		}
		target.Ed.Status = "hunks staged"
		target.clearGutter(msg.path)
		if target.notify != nil {
			target.notify("success", "hunks staged")
		}
		return tea.Batch(m.refreshModifiedFiles(), m.refreshIDEGutter())
	}
	return nil
}

// withPendingIDEWorkspaceRefresh preserves the initial file-tree refresh when
// a user file-open or stage request supersedes hydration before it completes.
// The latest operation still owns publication, while the independent workspace
// scan fills the tree/sidebar through its own generation gate.
func (m *Model) withPendingIDEWorkspaceRefresh(cmd tea.Cmd) tea.Cmd {
	if m == nil || m.ide == nil || !m.ide.filesLoading || m.modFilesInFlight {
		return cmd
	}
	return tea.Batch(cmd, m.refreshModifiedFiles())
}

func editorShellUnmodified(ed *editor.Editor) bool {
	if ed == nil || len(ed.Buffers) != 1 || ed.Buffers[0] == nil {
		return false
	}
	b := ed.Buffers[0]
	return (b.Path == "untitled" || b.Path == "") && !b.Dirty && b.Revision() == 0
}

func applyIDEOpenPosition(target *IDEState, line, column int) {
	if target == nil || target.Ed == nil || target.Ed.Buffer() == nil || line <= 0 {
		return
	}
	b := target.Ed.Buffer()
	b.Cur.Line = clamp(line-1, 0, max(len(b.Lines)-1, 0))
	b.Cur.Col = clamp(max(column-1, 0), 0, len([]rune(b.LineText(b.Cur.Line))))
	target.UI.SetScroll(max(b.Cur.Line-max(target.UI.Height/3, 1), 0))
}

// Save persists the editor session + crash state.
func (s *IDEState) Save() {
	_, _ = s.Ed.Sessions.Save(s.Ed)
	_ = s.Ed.Crash.Save(s.Ed)
}

// userHome returns the home directory.
func userHome() (string, error) {
	return os.UserHomeDir()
}

// sanitize keeps the project name filesystem-safe.
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// Update handles IDE keys; returns true when consumed.
func (s *IDEState) Update(m *Model, msg tea.KeyMsg) (tea.Cmd, bool) {
	// Paging belongs to the focused IDE surface, not to the global Harness
	// viewport. Standard mode deliberately keeps these familiar editor keys.
	switch msg.Type {
	case tea.KeyPgUp:
		if s.Focus == ideEditor {
			if s.proposalPreview != nil {
				s.scrollProposal(-max(s.UI.Height-3, 1))
			} else if s.preview {
				s.scrollPreview(-max(s.UI.Height-3, 1))
			} else {
				s.UI.Scroll(-max(s.UI.Height-3, 1))
			}
			return nil, true
		}
	case tea.KeyPgDown:
		if s.Focus == ideEditor {
			if s.proposalPreview != nil {
				s.scrollProposal(max(s.UI.Height-3, 1))
			} else if s.preview {
				s.scrollPreview(max(s.UI.Height-3, 1))
			} else {
				s.UI.Scroll(max(s.UI.Height-3, 1))
			}
			return nil, true
		}
	case tea.KeyCtrlU:
		if s.Focus == ideEditor {
			if s.proposalPreview != nil {
				s.scrollProposal(-max(s.UI.Height/2, 1))
			} else if s.preview {
				s.scrollPreview(-max(s.UI.Height/2, 1))
			} else {
				s.UI.Scroll(-max(s.UI.Height/2, 1))
			}
			return nil, true
		}
	case tea.KeyCtrlD:
		if s.Focus == ideEditor {
			if s.proposalPreview != nil {
				s.scrollProposal(max(s.UI.Height/2, 1))
			} else if s.preview {
				s.scrollPreview(max(s.UI.Height/2, 1))
			} else {
				s.UI.Scroll(max(s.UI.Height/2, 1))
			}
			return nil, true
		}
	case tea.KeyCtrlK:
		if s.Focus == ideEditor && s.Ed.HasSelection() {
			line := s.Ed.Buffer().Cur.Line
			return nil, m.openIDESelectionMenu(8, m.ideCodeTop()+min(line, max(s.UI.Height-3, 1)))
		}
	}
	if s.proposalPreview != nil && s.Focus == ideEditor {
		if msg.Type == tea.KeyEsc {
			s.proposalPreview = nil
			s.proposalScroll = 0
			return nil, true
		}
		// Proposal previews are read-only; decisions are routed by the root
		// model before the editor sees `a` or `d`.
		return nil, true
	}
	if s.cwPending {
		s.cwPending = false
		switch msg.String() {
		case "h":
			s.Focus = ideTree
		case "l":
			s.Focus = ideEditor
		case "j", "k":
			s.Focus = ideChat
			_, _, railW := m.idePaneWidths()
			if msg.String() == "k" && railW > 0 {
				s.Focus = ideHITL
			}
		case "=":
			// balanced splits: reset sizes
			m.ideTreePct = 13
			m.ideRailPct = 20
			m.layout()
		}
		return nil, true
	}
	if s.spacePending {
		s.spacePending = false
		if msg.String() == "p" && s.Focus == ideEditor {
			s.togglePreview(m)
			return nil, true
		}
		if msg.String() == "a" && s.Focus == ideEditor {
			if s.Ed.HasSelection() {
				line := s.Ed.Buffer().Cur.Line
				return nil, m.openIDESelectionMenu(8, m.ideCodeTop()+min(line, max(s.UI.Height-3, 1)))
			}
			s.Ed.Status = "select text first: v/V then Space a"
			return nil, true
		}
		if msg.String() == "e" && s.Focus == ideChat {
			s.Focus = ideEditor
			return nil, true
		}
		if msg.String() == "t" && s.Focus == ideEditor {
			current := m.orch.SettingsSnapshot().Theme
			picker := &themePickerState{Original: current}
			for i, name := range themeNames() {
				if name == current {
					picker.Sel = i
					break
				}
			}
			s.themePicker = picker
			return nil, true
		}
		if !s.Ed.IsVim() && s.Focus == ideEditor {
			// Standard mode keeps Space as a normal character. We defer it
			// for one key so Space p can toggle Markdown preview.
			s.handleAction(m, s.UI.Update(tea.KeyMsg{Type: tea.KeySpace}))
			// Continue below and process the key that followed Space.
		} else {
			return nil, true
		}
	}
	if s.preview && s.Focus == ideEditor {
		if msg.Type == tea.KeyEsc {
			s.preview = false
			s.previewScroll = 0
			return nil, true
		}
		// The preview is deliberately read-only. Space p returns to edit;
		// all other editing keys are consumed rather than changing the file.
		return nil, true
	}
	// Theme picker keys.
	if s.themePicker != nil {
		names := themeNames()
		switch msg.Type {
		case tea.KeyEsc:
			m.applyTheme(s.themePicker.Original)
			s.themePicker = nil
		case tea.KeyUp:
			if s.themePicker.Sel > 0 {
				s.themePicker.Sel--
				m.applyTheme(names[s.themePicker.Sel])
			}
		case tea.KeyDown:
			if s.themePicker.Sel < len(names)-1 {
				s.themePicker.Sel++
				m.applyTheme(names[s.themePicker.Sel])
			}
		case tea.KeyEnter:
			if s.themePicker.Sel >= 0 && s.themePicker.Sel < len(names) {
				next := m.orch.SettingsSnapshot()
				next.Theme = names[s.themePicker.Sel]
				if err := m.orch.UpdateSettings(context.Background(), next); err != nil {
					m.applyTheme(s.themePicker.Original)
					m.status.pushToast("error", err.Error(), 4*time.Second)
				} else {
					m.applyTheme(next.Theme)
				}
			}
			s.themePicker = nil
		}
		return nil, true
	}
	switch msg.Type {
	case tea.KeyCtrlW:
		s.cwPending = true
		return nil, true
	case tea.KeySpace:
		// "Space e" toggles chat/editor — but only when the chat input is
		// empty, so typing spaces never gets swallowed.
		if s.Focus == ideChat && m.input.String() == "" {
			s.spacePending = true
			return nil, true
		}
		if s.Ed.IsVim() && s.Focus == ideEditor && s.Ed.HasSelection() {
			s.spacePending = true
			return nil, true
		}
		if s.Ed.IsVim() && s.Focus == ideEditor && s.Ed.Mode == editor.ModeNormal {
			// "Space t" opens the theme browser.
			s.spacePending = true
			return nil, true
		}
		if !s.Ed.IsVim() && s.Focus == ideEditor {
			// Defer standard-mode spaces to support Space p without
			// sacrificing ordinary typing.
			s.spacePending = true
			return nil, true
		}
		if s.Focus == ideEditor {
			// pass through to the editor (typing in insert mode)
			action := s.UI.Update(msg)
			return s.handleAction(m, action), true
		}
	case tea.KeyCtrlE:
		if s.Focus == ideChat {
			s.Focus = ideEditor
		} else {
			s.Focus = ideChat
		}
		return nil, true
	case tea.KeyTab:
		focusCount := 4
		if _, _, railW := m.idePaneWidths(); railW == 0 {
			focusCount = 3
			if s.Focus == ideHITL {
				s.Focus = ideEditor
			}
		}
		s.Focus = ideFocus((int(s.Focus) + 1) % focusCount)
		return nil, true
	}
	if msg.Alt {
		switch msg.String() {
		case "←":
			m.ideRailPct = clamp(m.ideRailPct+2, 14, 30)
			m.layout()
			return nil, true
		case "→":
			m.ideRailPct = clamp(m.ideRailPct-2, 14, 30)
			m.layout()
			return nil, true
		}
	}

	// Focus routing.
	switch s.Focus {
	case ideChat:
		// typed input handled by the chat bar
		return nil, false
	case ideTree:
		entries := s.treeEntries()
		if s.treeSel >= len(entries) {
			s.treeSel = max(len(entries)-1, 0)
		}
		switch msg.Type {
		case tea.KeyUp:
			if s.treeSel > 0 {
				s.treeSel--
			}
		case tea.KeyDown:
			if s.treeSel < len(s.files())-1 {
				s.treeSel++
			}
		case tea.KeyEnter:
			if s.treeSel >= 0 && s.treeSel < len(entries) {
				entry := entries[s.treeSel]
				if entry.Dir {
					s.toggleTree(entry.Path)
				} else {
					m.followAgent = false
					return m.beginIDEOpen(s, ideOpenRequest{path: entry.Path, manual: true}), true
				}
			}
		case tea.KeySpace:
			if s.treeSel >= 0 && s.treeSel < len(entries) && entries[s.treeSel].Dir {
				s.toggleTree(entries[s.treeSel].Path)
			}
		default:
			return nil, true
		}
		return nil, true
	case ideHITL:
		switch msg.Type {
		case tea.KeyUp:
			m.sidebar.moveSelection(-1)
		case tea.KeyDown:
			m.sidebar.moveSelection(1)
		case tea.KeySpace:
			m.toggleSelectedHITL()
		default:
			return nil, true
		}
		return nil, true
	default: // ideEditor
		if msg.Type == tea.KeyRunes && msg.Paste {
			action := s.UI.Paste(string(msg.Runes))
			return s.handleAction(m, action), true
		}
		// bubbletea groups consecutive printable chars into one message;
		// the modal editor needs one key at a time (sequences, command
		// mode, counts).
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
			var action editor.EditAction
			for _, r := range msg.Runes {
				action = s.UI.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
			return s.handleAction(m, action), true
		}
		action := s.UI.Update(msg)
		return s.handleAction(m, action), true
	}
}

func (s *IDEState) togglePreview(m *Model) {
	if s.Ed == nil || s.Ed.Buffer() == nil || !editor.IsMarkdownPath(s.Ed.Buffer().Path) {
		s.Ed.Status = "preview is available for Markdown/MDX files"
		return
	}
	s.preview = !s.preview
	s.previewScroll = 0
	if s.preview {
		s.Ed.Status = "Markdown preview · PgUp/PgDown scroll · esc edit"
	} else {
		s.Ed.Status = "editing Markdown"
	}
}

func (s *IDEState) scrollPreview(delta int) {
	if !s.preview {
		return
	}
	s.previewScroll = max(0, s.previewScroll+delta)
}

func (s *IDEState) scrollProposal(delta int) {
	if s.proposalPreview == nil {
		return
	}
	s.proposalScroll = max(0, s.proposalScroll+delta)
}

// handleAction maps editor actions to TUI behavior.
func (s *IDEState) handleAction(m *Model, action editor.EditAction) tea.Cmd {
	if queued := s.takeQueuedOpen(); queued != nil {
		return m.beginIDEOpen(s, *queued)
	}
	switch action {
	case editor.ActQuitIDE:
		m.closeIDE()
	case editor.ActQuitApp:
		return m.quitCmd()
	case editor.ActSave:
		if b := s.Ed.Buffer(); b != nil {
			s.clearGutter(b.Path)
		}
		return m.refreshModifiedFiles()
	case editor.ActOpenFile:
		m.followAgent = false
		if path, ok := s.Ed.TakeOpenRequest(); ok {
			return m.beginIDEOpen(s, ideOpenRequest{path: path, manual: true})
		}
	case editor.ActHunkStage:
		if path, ok := s.Ed.TakeHunkStageRequest(); ok {
			return m.beginIDEHunkStage(s, path)
		}
	case editor.ActAgentReview:
		// overlay renders from editor state
	case editor.ActAskAgent:
		if text, start, end, ok := s.Ed.SelectionText(); ok {
			m.pendingSelection = &selectionContext{
				Path: s.Ed.Buffer().Path, Start: start, End: end, Text: text,
			}
			m.overlay = overlayAsk
			m.overlayM = newAskOverlay()
		}
	case editor.ActGitWorkspace:
		// overlay renders from editor state
	case editor.ActPicker:
		// Open immediately from the last immutable snapshot. Refreshing the
		// workspace is a background effect and updates the next picker/tree
		// frame without holding keyboard input behind git ls-files.
		refresh := tea.Cmd(nil)
		if !s.filesLoading {
			s.filesLoading = true
			refresh = m.refreshModifiedFiles()
		}
		s.Ed.Picker.Start("Files", s.files(), func(path string) {
			s.queueOpen(path, 0, 0, true)
			m.followAgent = false
		})
		return refresh
	}
	return nil
}

// files lists the project tree for the file panel.
func (s *IDEState) files() []string {
	return s.fileCache
}

// applyFileRefresh installs a file list produced off the Bubble Tea event
// loop. A result for a previous session/worktree must not poison this IDE.
func (s *IDEState) applyFileRefresh(project string, files []string) {
	if filepath.Clean(project) != filepath.Clean(s.project) {
		return
	}
	s.fileCache = append(s.fileCache[:0], files...)
	s.filesLoading = false
	s.treeCacheValid = false
	if s.treeSel >= len(s.fileCache) {
		s.treeSel = max(len(s.fileCache)-1, 0)
	}
}

// applyGutterRefresh installs a gutter computed off the event loop only when
// it still belongs to the visible workspace and buffer.
func (s *IDEState) applyGutterRefresh(project, path string, gutter *editor.Gutter) {
	if gutter == nil || filepath.Clean(project) != filepath.Clean(s.project) || s.Ed == nil || s.Ed.Buffer() == nil {
		return
	}
	if filepath.Clean(path) != filepath.Clean(s.Ed.Buffer().Path) {
		return
	}
	s.UI.Gutter = gutter
	s.gutterDeferred = false
	s.gutterError = ""
}

func (s *IDEState) clearGutter(path string) {
	if s == nil || s.UI == nil || s.UI.Gutter == nil {
		return
	}
	s.UI.Gutter.Path = path
	s.UI.Gutter.Signs = map[int]editor.Sign{}
	s.UI.Gutter.Hunks = nil
	s.gutterDeferred = true
	s.gutterError = ""
}
