package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
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
	fileCacheValid  bool
	treeCacheValid  bool
	filesLoading    bool
	gutterDeferred  bool
	project         string
	git             *git.Client
	themePicker     *themePickerState
	notify          func(level, message string)
	onOpenRejected  func()
}

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
// the existing background workspace refresh. This path is used when an agent
// is actively streaming, where even a short synchronous Git command can make
// keyboard and mouse input appear frozen behind queued model deltas.
func newDeferredIDE(m *Model, project string, g *git.Client) *IDEState {
	return newIDE(m, project, g, true)
}

func newIDE(m *Model, project string, g *git.Client, deferGit bool) *IDEState {
	ed := editor.NewEditor(project)
	if m != nil && m.orch != nil {
		ed.SetKeymap(m.orch.SettingsSnapshot().EditorMode)
	}
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
		if m.proposals == nil {
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
	// Session + crash recovery wiring.
	home, _ := userHome()
	sessDir := filepath.Join(home, ".maestro", "editor", sanitize(project))
	ed.Sessions.SetDir(sessDir)
	ed.Crash.SetDir(sessDir)
	if _, err := ed.Sessions.Load(ed); err != nil {
		ed.Status = "Editor session recovery was skipped because its state was unsafe or unreadable."
	}
	if states, err := ed.Crash.Restore(); err == nil && len(states) > 0 {
		ed.RestoreBuffers(states)
	} else if err != nil {
		ed.Status = "Editor crash recovery was skipped because its state was unsafe or unreadable."
	}
	if len(ed.Buffers) == 0 {
		// Open the active spec as a starting buffer.
		if sp := m.orch.ActiveSpec(); sp != nil {
			_ = ed.Open(m.orch.SpecPath(sp.ID))
		}
	}
	if len(ed.Buffers) == 0 || (len(ed.Buffers) == 1 && ed.Buffers[0].Path == "untitled" && !ed.Buffers[0].Dirty) {
		if starter := starterFile(project); starter != "" {
			ed.Buffers = nil
			ed.CurBuf = 0
			_ = ed.Open(starter)
		}
	}
	if len(ed.Buffers) == 0 {
		ed.Buffers = append(ed.Buffers, editor.NewBuffer("untitled", nil))
	}
	ui := editor.NewUI(ed, m.styles.T.EditorPalette())
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
		fileCacheValid: deferGit,
		filesLoading:   deferGit,
		gutterDeferred: deferGit,
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

func starterFile(project string) string {
	for _, name := range []string{"README.md", "MAESTRO.md", filepath.Join("docs", "ARCHITECTURE.md"), "CHANGELOG.md"} {
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
	if s.gutterDeferred {
		s.clearGutter(full)
	} else {
		s.UI.Gutter.Refresh(context.Background(), full)
	}
	s.UI.SetScroll(0)
	s.Focus = ideEditor
	return true
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
					s.OpenFileAt(entry.Path)
					m.followAgent = false
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
	switch action {
	case editor.ActQuitIDE:
		m.closeIDE()
	case editor.ActQuitApp:
		return tea.Quit
	case editor.ActSave:
		s.UI.Gutter.Refresh(context.Background(), s.Ed.Buffer().Path)
		return m.refreshModifiedFiles()
	case editor.ActOpenFile:
		m.followAgent = false
		if err := s.Ed.LastOpenError(); err != nil {
			s.Ed.CancelSelection()
			m.pendingSelection = nil
			m.selectionMenu = nil
			s.Ed.Status = editor.SafeOpenError(err)
			if s.notify != nil {
				s.notify("warn", s.Ed.Status)
			}
			return nil
		}
		if b := s.Ed.Buffer(); b != nil {
			s.UI.Gutter.Refresh(context.Background(), b.Path)
		}
	case editor.ActHunkStage:
		s.UI.Gutter.Refresh(context.Background(), s.Ed.Buffer().Path)
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
		// The latest completed workspace scan already owns the cache. Starting a
		// fresh git ls-files here used to block the event loop for up to five
		// seconds while an agent was streaming.
		if !s.filesLoading && !m.streaming() {
			s.refreshFiles()
		}
		s.Ed.Picker.Start("Files", s.files(), func(path string) {
			s.OpenFileAt(path)
			m.followAgent = false
		})
	}
	return nil
}

// files lists the project tree for the file panel.
func (s *IDEState) files() []string {
	if !s.fileCacheValid {
		s.fileCache = editor.ListFiles(s.project, 500)
		s.fileCacheValid = true
		s.treeCacheValid = false
	}
	return s.fileCache
}

// refreshFiles invalidates the cached file tree after an explicit refresh
// action (opening the picker or returning to the project after a write).
func (s *IDEState) refreshFiles() {
	s.fileCache = editor.ListFiles(s.project, 500)
	s.fileCacheValid = true
	s.treeCacheValid = false
}

// applyFileRefresh installs a file list produced off the Bubble Tea event
// loop. A result for a previous session/worktree must not poison this IDE.
func (s *IDEState) applyFileRefresh(project string, files []string) {
	if filepath.Clean(project) != filepath.Clean(s.project) {
		return
	}
	s.fileCache = append(s.fileCache[:0], files...)
	s.fileCacheValid = true
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
}

func (s *IDEState) clearGutter(path string) {
	if s == nil || s.UI == nil || s.UI.Gutter == nil {
		return
	}
	s.UI.Gutter.Path = path
	s.UI.Gutter.Signs = map[int]editor.Sign{}
	s.UI.Gutter.Hunks = nil
}
