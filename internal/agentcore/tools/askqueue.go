package tools

import (
	"context"
	"errors"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// AskRequest is one structured question the loop is waiting on.
type AskRequest struct {
	Question    string
	Options     []string
	Recommended int
	Respond     chan int
}

// AskQueue is a channel-backed handler for the ask tool (§5.4): the loop
// blocks on Ask until the TUI dialog answers. It mirrors the permission
// queue so headless runs fail cleanly instead of hanging.
type AskQueue struct {
	req chan *AskRequest
}

// NewAskQueue builds a queue with capacity n.
func NewAskQueue(n int) *AskQueue {
	if n <= 0 {
		n = 4
	}
	return &AskQueue{req: make(chan *AskRequest, n)}
}

// Ask implements agentcore.AskFunc.
func (q *AskQueue) Ask(ctx context.Context, question string, options []string, recommended int) (int, error) {
	respond := make(chan int, 1)
	select {
	case q.req <- &AskRequest{Question: question, Options: options, Recommended: recommended, Respond: respond}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case idx := <-respond:
		if idx < 0 {
			return 0, errAskCancelled
		}
		return idx, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// NextCh exposes the blocking request channel for the TUI pump.
func (q *AskQueue) NextCh() <-chan *AskRequest { return q.req }

// Enqueue pushes a question directly (tests, demos).
func (q *AskQueue) Enqueue(r *AskRequest) { q.req <- r }

// Next returns the next pending question, or nil when none is queued.
func (q *AskQueue) Next() *AskRequest {
	select {
	case r := <-q.req:
		return r
	default:
		return nil
	}
}

// Answer resolves a pending question. A negative index cancels it.
func (q *AskQueue) Answer(r *AskRequest, idx int) {
	if r == nil || r.Respond == nil {
		return
	}
	r.Respond <- idx
}

var _ agentcore.AskFunc = (*AskQueue)(nil).Ask

var errAskCancelled = errors.New("cancelled by user")
