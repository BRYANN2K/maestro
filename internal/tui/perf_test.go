package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/git"
)

// BenchmarkChatScrollFrame protects the steady-state scroll path. Keep the
// transcript realistic enough to exercise ANSI clipping and pane assembly.
func BenchmarkChatScrollFrame(b *testing.B) {
	m := benchmarkChatModel(b)
	m.viewport.GotoBottom()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.viewport.ScrollUp(3)
		_ = m.View()
		m.viewport.ScrollDown(3)
	}
}

func benchmarkChatModel(b *testing.B) *Model {
	b.Helper()
	m, _ := newTestModel(b)
	feed(m, tea.WindowSizeMsg{Width: 190, Height: 60})
	body := strings.Repeat("A transcript line with `code`, punctuation, and enough text to wrap.\n", 6)
	m.messages = make([]*Message, 120)
	for i := range m.messages {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		m.messages[i] = &Message{Role: role, Text: fmt.Sprintf("message %d\n%s", i, body)}
	}
	m.renderMessages()
	return m
}

// BenchmarkChatTranscriptRebuild captures the one-off transition from the
// streaming tail to the complete scrollback buffer.
func BenchmarkChatTranscriptRebuild(b *testing.B) {
	m := benchmarkChatModel(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.renderMessages()
	}
}

// BenchmarkIDEEditorFrame covers the complete IDE frame after a wheel event,
// including the explorer, companion rail, statusline and surface painter.
func BenchmarkIDEEditorFrame(b *testing.B) {
	m, dir := newTestModel(b)
	feed(m, tea.WindowSizeMsg{Width: 190, Height: 60})
	m.activeTab = TabIDE
	m.ide = NewIDE(m, dir, git.New(dir))
	buf := m.ide.Ed.Buffer()
	buf.Path = dir + "/audit.go"
	buf.Lines = make([]string, 1200)
	for i := range buf.Lines {
		buf.Lines[i] = fmt.Sprintf("func RenderLine%d(ctx context.Context, input string) error { return fmt.Errorf(\"line %%d: %%w\", %d, err) } // comment", i, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ide.UI.Scroll(3)
		_ = m.View()
	}
}

// BenchmarkIDEStreamingFrame protects the expensive real-world case where an
// agent keeps streaming a long response while the user inspects code. The IDE
// must render only the bounded companion summary, never rebuild the hidden chat
// transcript on every frame.
func BenchmarkIDEStreamingFrame(b *testing.B) {
	m, dir := newTestModel(b)
	feed(m, tea.WindowSizeMsg{Width: 190, Height: 60})
	m.activeTab = TabIDE
	m.ide = NewIDE(m, dir, git.New(dir))
	m.messages = append(m.messages, &Message{
		Role: "assistant",
		Text: strings.Repeat("Streaming agent output with a referenced internal/store.go file. ", 4000),
		busy: true,
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}
