package tui

import (
	"context"
	"errors"
	"sync"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// permissionRequest is one tool approval the loop is waiting on.
type permissionRequest struct {
	ctx     context.Context
	Call    agentcore.ToolCall
	Spec    agentcore.ToolSpec
	Respond chan error
}

func (r *permissionRequest) contextErr() error {
	if r == nil || r.ctx == nil {
		return nil
	}
	return r.ctx.Err()
}

// PermissionQueue is a channel-backed Gate (§5.3, §5.4.7): the loop blocks
// on Authorize until the TUI answers. Read-only tools pass without asking.
type PermissionQueue struct {
	req          chan *permissionRequest
	mu           sync.RWMutex
	mode         string
	allowedTools map[string]bool
}

// NewPermissionQueue builds a queue with capacity n.
func NewPermissionQueue(n int) *PermissionQueue {
	if n <= 0 {
		n = 4
	}
	return &PermissionQueue{req: make(chan *permissionRequest, n), mode: "ask", allowedTools: map[string]bool{}}
}

// AllowTool grants one tool for the rest of the current Maestro session.
func (q *PermissionQueue) AllowTool(name string) {
	q.mu.Lock()
	q.allowedTools[name] = true
	q.mu.Unlock()
}

func (q *PermissionQueue) toolAllowed(name string) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.allowedTools[name]
}

// SetMode changes the live permission policy used by the gate.
func (q *PermissionQueue) SetMode(mode string) {
	q.mu.Lock()
	q.mode = mode
	q.mu.Unlock()
}

func (q *PermissionQueue) currentMode() string {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.mode
}

// Authorize implements agentcore.Gate.
func (q *PermissionQueue) Authorize(ctx context.Context, call agentcore.ToolCall, spec agentcore.ToolSpec) error {
	if !spec.NeedsApproval {
		return nil
	}
	if q.toolAllowed(spec.Name) {
		return nil
	}
	switch q.currentMode() {
	case "yolo", "allow":
		return nil
	case "deny":
		return errors.New("permission denied by settings")
	}
	respond := make(chan error, 1)
	select {
	case q.req <- &permissionRequest{ctx: ctx, Call: call, Spec: spec, Respond: respond}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-respond:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ agentcore.Gate = (*PermissionQueue)(nil)
