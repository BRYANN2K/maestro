package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore/tools"
)

func TestAskQueueRoundTrip(t *testing.T) {
	q := tools.NewAskQueue(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		idx, err := q.Ask(ctx, "which?", []string{"a", "b"}, 0)
		if err != nil {
			done <- -99
			return
		}
		done <- idx
	}()
	// The request is queued for the TUI (poll until the goroutine enqueues).
	var r *tools.AskRequest
	deadline := time.Now().Add(2 * time.Second)
	for r == nil && time.Now().Before(deadline) {
		r = q.Next()
		time.Sleep(5 * time.Millisecond)
	}
	if r == nil || r.Question != "which?" || len(r.Options) != 2 {
		t.Fatalf("queued request = %+v", r)
	}
	// The TUI answers with option 1.
	q.Answer(r, 1)
	select {
	case idx := <-done:
		if idx != 1 {
			t.Errorf("answer = %d, want 1", idx)
		}
	case <-ctx.Done():
		t.Fatal("ask did not resolve")
	}
}

func TestAskQueueCancel(t *testing.T) {
	q := tools.NewAskQueue(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := q.Ask(ctx, "q?", []string{"a"}, 0)
		done <- err
	}()
	var r *tools.AskRequest
	deadline := time.Now().Add(2 * time.Second)
	for r == nil && time.Now().Before(deadline) {
		r = q.Next()
		time.Sleep(5 * time.Millisecond)
	}
	q.Answer(r, -1)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("cancel err = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("cancel did not resolve")
	}
}

func TestAskDialogFlow(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	// A question arrives from the ask tool queue.
	r := &tools.AskRequest{Question: "which engine?", Options: []string{"native", "legacy: codex"}, Recommended: 0, Respond: make(chan int, 1)}
	m.askQ.Enqueue(r)
	feed(m, askRequestMsg{req: r})
	if m.pendingAsk != r {
		t.Fatal("question must be pending")
	}
	// The dialog is visible with the options.
	view := stripANSI(m.View())
	if !strings.Contains(view, "which engine?") || !strings.Contains(view, "native") || !strings.Contains(view, "codex") {
		t.Errorf("ask dialog missing content: %q", view)
	}
	// Down + enter picks option 2.
	feed(m, tea.KeyMsg{Type: tea.KeyDown})
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingAsk != nil {
		t.Fatal("question must close after answering")
	}
	select {
	case idx := <-r.Respond:
		if idx != 1 {
			t.Errorf("answer = %d, want 1", idx)
		}
	default:
		t.Fatal("answer not delivered")
	}
}

func TestAskDialogEscCancels(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	r := &tools.AskRequest{Question: "q?", Options: []string{"a", "b"}, Respond: make(chan int, 1)}
	m.askQ.Enqueue(r)
	feed(m, askRequestMsg{req: r})
	feed(m, tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case idx := <-r.Respond:
		if idx != -1 {
			t.Errorf("answer = %d, want -1 (cancel)", idx)
		}
	default:
		t.Fatal("cancel not delivered")
	}
}
