package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
)

// Chat sends a free-form message to the orchestrator model. In the spec
// phase, messages prefixed with "idea " are recorded in spec-idea.md.
func (o *Orchestrator) Chat(ctx context.Context, message string) error {
	if o.sess.Phase == session.PhaseSpec || o.sess.Phase == session.PhasePropose {
		trimmed := message
		if strings.HasPrefix(trimmed, "idea ") || strings.HasPrefix(trimmed, "/idea ") {
			idea := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "idea "), "/idea "))
			if o.spec != nil {
				if err := o.store.AppendIdea(ctx, o.spec.ID, idea); err != nil {
					return err
				}
				fmt.Fprintf(o.out, "Idea recorded in specs/%s/spec-idea.md\n", o.spec.ID)
				return nil
			}
			if o.sess.Draft != nil {
				o.sess.Draft.Body += fmt.Sprintf("\n\n## Ideas\n\n- %s\n", idea)
				return o.save()
			}
		}
	}
	task := o.chatTaskPrompt(message)
	o.ensureFallbackTitle(message)
	o.appendConversation("user", message)
	if err := o.save(); err != nil {
		return fmt.Errorf("chat: persist conversation: %w", err)
	}
	runner, err := o.runnerForRole(string(agentcore.RoleOrchestrator))
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	role := agentcore.RoleOrchestrator
	o.emit(agentcore.NewEvent(nil, role, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "orchestrator", Status: "running", Detail: "composing"}))
	ctx, cancel := o.bindBudgetKill(ctx)
	defer cancel()
	result, err := runner.Run(ctx, role, task)
	status := "done"
	detail := ""
	if err != nil {
		status = "error"
		detail = err.Error()
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
			detail = "cancelled by user"
		}
	}
	o.emit(agentcore.NewEvent(nil, role, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "orchestrator", Status: status, Detail: detail,
	}))
	// Never commit a partial answer from a cancelled run to durable context.
	responseSaved := false
	if !errors.Is(err, context.Canceled) && strings.TrimSpace(result.Summary) != "" {
		o.appendConversation("assistant", result.Summary)
		if saveErr := o.save(); saveErr != nil {
			if err == nil {
				err = fmt.Errorf("chat: persist response: %w", saveErr)
			}
		} else if err == nil {
			responseSaved = true
		}
	}
	if responseSaved {
		o.generateSessionTitle(ctx, result.Summary)
	}
	return err
}
