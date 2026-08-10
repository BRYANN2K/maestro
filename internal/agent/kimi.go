package agent

import (
	"context"
	"errors"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// KimiAgent wraps the Kimi Code CLI (best-effort print mode).
type KimiAgent struct {
	Timeout time.Duration
	Model   string
}

// NewKimiAgent returns a KimiAgent with a 5-minute timeout.
func NewKimiAgent() *KimiAgent { return &KimiAgent{Timeout: 5 * time.Minute} }

// Name returns the agent name.
func (k *KimiAgent) Name() string { return "Kimi Code" }

// Models returns the known Kimi models.
func (k *KimiAgent) Models() []string { return []string{"k2", "moonshot"} }

// Execute runs `kimi -p <task>` (print mode, blob result).
func (k *KimiAgent) Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error) {
	if opts.ReadOnly {
		return nil, errors.New("kimi: enforced read-only execution is unavailable")
	}
	model := opts.Model
	if model == "" {
		model = k.Model
	}
	args := []string{"-p", task}
	if model != "" {
		args = append(args, "--model", model)
	}
	return blobParser(ctx, "kimi", k.Timeout, opts.WorkDir, parseVendorBlob, args...)
}
