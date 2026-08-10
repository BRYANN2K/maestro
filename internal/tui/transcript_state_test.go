package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestErrorStateIsLocalToTheErrorMessage(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(*Model)
	}{
		{
			name: "command completion error",
			trigger: func(m *Model) {
				m.busy = true
				m.Update(chatDoneMsg{err: errors.New("unknown command")})
			},
		},
		{
			name: "stream error",
			trigger: func(m *Model) {
				m.handleEvent(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvError, agentcore.StreamError{
					Message: "provider disconnected",
				}))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.SetSize(100, 30)
			m.chatState = "spec"

			tt.trigger(m)
			if len(m.messages) != 1 {
				t.Fatalf("error appended %d messages, want 1", len(m.messages))
			}
			errorMessage := m.messages[0]
			if errorMessage.Role != "system" || errorMessage.State != "error" {
				t.Fatalf("error metadata = role %q, state %q", errorMessage.Role, errorMessage.State)
			}
			if m.chatState != "spec" {
				t.Fatalf("error changed conversation state to %q, want spec", m.chatState)
			}

			// Exercise the observed failure sequence: an error followed by a user
			// prompt, a successful system result, and the next agent response.
			m.appendUser("try again")
			m.appendSystem("command complete")
			m.appendAssistant("ready")

			want := []struct {
				role  string
				state string
			}{
				{role: "system", state: "error"},
				{role: "user", state: "spec"},
				{role: "system", state: "spec"},
				{role: "assistant", state: "spec"},
			}
			if len(m.messages) != len(want) {
				t.Fatalf("message count = %d, want %d", len(m.messages), len(want))
			}
			for i, expected := range want {
				msg := m.messages[i]
				if msg.Role != expected.role || msg.State != expected.state {
					t.Errorf("message %d metadata = role %q, state %q; want role %q, state %q", i, msg.Role, msg.State, expected.role, expected.state)
				}
				rendered := stripANSI(m.renderRoleMessage(msg, 80))
				if i == 0 && !strings.Contains(rendered, "ERROR") {
					t.Errorf("error message lost ERROR badge: %q", rendered)
				}
				if i > 0 && strings.Contains(rendered, "ERROR") {
					t.Errorf("ERROR badge leaked into message %d: %q", i, rendered)
				}
			}

			// A later phase transition must not retroactively restyle messages.
			m.chatState = "build"
			m.invalidateMessageCaches()
			for i, expected := range want {
				if got := m.messages[i].State; got != expected.state {
					t.Errorf("phase transition changed message %d state to %q, want %q", i, got, expected.state)
				}
			}
		})
	}
}

func TestOrchestratorActivityDoesNotChangeDiscoveryMessageState(t *testing.T) {
	m, _ := newTestModel(t)
	m.SetSize(100, 30)
	m.chatState = "chat"
	m.appendUser("discover the repository")

	m.handleEvent(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "orchestrator", Status: "running", Detail: "composing",
	}))
	if m.chatState != "chat" {
		t.Fatalf("orchestrator activity changed phase state to %q", m.chatState)
	}
	m.handleEvent(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "repository summary"}))
	m.handleEvent(agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "orchestrator", Status: "done",
	}))
	m.appendSystem("command complete")
	m.appendUser("continue discovery")

	for i, msg := range m.messages {
		if msg.State != "chat" {
			t.Errorf("message %d (%s) state = %q, want chat", i, msg.Role, msg.State)
		}
		if rendered := stripANSI(m.renderRoleMessage(msg, 80)); strings.Contains(rendered, "SPEC") {
			t.Errorf("message %d leaked SPEC badge in discovery: %q", i, rendered)
		}
	}
}
