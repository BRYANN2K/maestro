package agent

import (
	"context"
	"errors"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// GrokAgent wraps the Grok Build CLI (best-effort print mode).
type GrokAgent struct {
	Timeout time.Duration
	Model   string
}

// NewGrokAgent returns a GrokAgent with a 5-minute timeout.
func NewGrokAgent() *GrokAgent { return &GrokAgent{Timeout: 5 * time.Minute} }

// Name returns the agent name.
func (g *GrokAgent) Name() string { return "Grok Build" }

// Models returns the known Grok models.
func (g *GrokAgent) Models() []string { return []string{"grok-4", "grok-3"} }

// Execute runs `grok -p <task>` (print mode, blob result).
func (g *GrokAgent) Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error) {
	if opts.ReadOnly {
		return nil, errors.New("grok: enforced read-only execution is unavailable")
	}
	model := opts.Model
	if model == "" {
		model = g.Model
	}
	args := []string{"-p", task}
	if model != "" {
		args = append(args, "--model", model)
	}
	return blobParser(ctx, "grok", g.Timeout, opts.WorkDir, parseVendorBlob, args...)
}
