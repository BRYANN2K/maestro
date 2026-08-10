package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/proposals"
)

func TestIDEPlainTextSurfacesCannotEmitHostileFileNames(t *testing.T) {
	m, project := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	m.ToggleIDE()

	hostile := "x\x1b]52;c;x\x07\x1b[2J\u009b\u202e" + string([]byte{0xff}) + ".go"
	buffer := editor.NewBuffer(filepath.Join(project, hostile), []byte("safe\n"))
	m.ide.Ed.Buffers = []*editor.Buffer{buffer}
	m.ide.Ed.CurBuf = 0
	m.ide.treeCache = []treeEntry{{Path: hostile, Name: hostile}}
	m.ide.treeCacheValid = true
	m.sidebar.modFiles = []git.NumStat{{Path: hostile, Untracked: true}}

	selection := &selectionContext{Source: "ide", Path: hostile, Text: "selected \x1b]52;c;x\x07\x1b[2J\u009b\u202e" + string([]byte{0xff})}
	m.selectionMenu = newSelectionMenu(selection, 1, 1)
	m.selectionAskCtx = selection
	m.selectionAsk = newInputBox(m.styles)
	m.renderStatusline()
	statusline := m.status.View(m.styles, 140, m)

	surfaces := map[string]string{
		"header":  m.renderEditorHeader(m.ide, 140),
		"tabs":    m.renderBufferTabs(m.ide, 100),
		"tree":    m.renderTree(80, 6),
		"changes": m.renderChangedTree(100, 6),
		"changes sidebar": m.sidebar.renderModifiedFile(m.styles, git.NumStat{
			Path: hostile, Untracked: true,
		}),
		"selection menu": m.renderSelectionMenu(),
		"selection ask":  m.renderSelectionAsk(),
		"statusline":     statusline,
	}
	for name, rendered := range surfaces {
		if !utf8.ValidString(rendered) {
			t.Errorf("%s rendered invalid UTF-8: %q", name, rendered)
		}
		for _, dangerous := range []string{"\x1b]52", "\x1b[2J", "\u009b", "\u202e"} {
			if strings.Contains(rendered, dangerous) {
				t.Errorf("%s emitted hostile terminal input %q: %q", name, dangerous, rendered)
			}
		}
		if !strings.Contains(rendered, "␛") {
			t.Errorf("%s did not expose a visible safe control marker: %q", name, rendered)
		}
	}
}

func TestSafeIDEPlainTextPreservesUnicodeAndMakesControlsVisible(t *testing.T) {
	raw := "é界\t\r\n\x00\x1b\x7f\u009b\u202e\u2066" + string([]byte{0xff})
	got := safeIDEPlainText(raw)
	want := "é界␉␍␊␀␛␡����"
	if got != want {
		t.Fatalf("safe IDE text = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("safe IDE text is invalid UTF-8: %q", got)
	}
}

func TestIDEMarkdownPreviewAndProposalDiffProjectUntrustedText(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 32})
	m.ToggleIDE()

	raw := "text \x1b]52;c;owned\x07 \x1b[2J \u009b\u202e" + string([]byte{0xff})
	// Bypass the disk-open guard to exercise defense in depth for restored or
	// integration-provided buffers.
	m.ide.Ed.Buffers = []*editor.Buffer{{Path: "README.md", Lines: []string{raw}}}
	m.ide.Ed.CurBuf = 0
	m.ide.UI.Height = 8

	prop := &proposals.Proposal{
		Path:      "x\x1b]52;c;path\x07.md",
		BaseLines: []string{raw},
		Hunks: []proposals.Hunk{{
			Start: 1, OldLines: []string{raw}, NewLines: []string{raw + " changed"},
		}},
	}
	m.ide.proposalPreview = prop

	surfaces := map[string]string{
		"markdown preview": m.renderIDEPreview(m.ide, 100),
		"proposal diff":    m.renderIDEProposalPreview(m.ide, 100),
		"diff no inline":   diffOldLine(m.styles, 1, raw, inlineRange{}, 100),
	}
	for name, rendered := range surfaces {
		if !utf8.ValidString(rendered) {
			t.Errorf("%s rendered invalid UTF-8: %q", name, rendered)
		}
		for _, dangerous := range []string{"\x1b]52", "\x1b[2J", "\u009b", "\u202e"} {
			if strings.Contains(rendered, dangerous) {
				t.Errorf("%s emitted hostile terminal input %q: %q", name, dangerous, rendered)
			}
		}
	}
}
