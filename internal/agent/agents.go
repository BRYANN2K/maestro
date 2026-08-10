package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// CodexAgent wraps the codex CLI (exec --json streaming).
type CodexAgent struct {
	Timeout time.Duration
	Model   string
}

// NewCodexAgent returns a CodexAgent with a 5-minute timeout.
func NewCodexAgent() *CodexAgent { return &CodexAgent{Timeout: 5 * time.Minute} }

// Name returns the agent name.
func (c *CodexAgent) Name() string { return "Codex" }

// SupportsReadOnly reports that Codex exposes a process-enforced read-only
// sandbox for non-mutating Agent Skill runs.
func (c *CodexAgent) SupportsReadOnly() bool { return true }

// Models returns the known codex models.
func (c *CodexAgent) Models() []string { return []string{"gpt-4.1", "o3", "o4-mini"} }

// Execute runs Codex in an ephemeral, configuration-isolated JSON stream.
func (c *CodexAgent) Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error) {
	model := opts.Model
	if model == "" {
		model = c.Model
	}
	// Maestro supplies its own tools and MCP registry. Isolate the subprocess
	// from user-level Codex MCP entries (which may be stale) and disable ANSI
	// before its output crosses the structured-stream boundary.
	sandbox := "workspace-write"
	if opts.ReadOnly {
		sandbox = "read-only"
	}
	args := []string{
		"exec",
		"--json",
		"--sandbox", sandbox,
		"--ignore-user-config",
		"--ephemeral",
		"--color", "never",
	}
	if opts.ReadOnly {
		// Agent Skills are untrusted instruction documents. The subprocess must
		// not inherit project rules that can alter the runtime-owned envelope.
		args = append(args, "--ignore-rules")
	}
	effort := strings.ToLower(strings.TrimSpace(opts.ReasoningEffort))
	if effort == "auto" {
		effort = ""
	}
	if effort != "" {
		switch effort {
		case "minimal", "low", "medium", "high", "xhigh":
			args = append(args, "-c", `model_reasoning_effort="`+effort+`"`)
		default:
			return nil, fmt.Errorf("codex: reasoning effort %q is unsupported", opts.ReasoningEffort)
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	// `codex exec -` reads the task from stdin. Keeping prompts out of argv
	// avoids Windows command-line limits and prevents source/instruction data
	// from appearing in process listings.
	args = append(args, "-")
	return lineStreamerWithInput(ctx, "codex", c.Timeout, opts.WorkDir, strings.NewReader(task), func(line string) []agentcore.StreamEvent {
		return parseCodexLine(line)
	}, args...)
}

// ClaudeAgent wraps the Claude Code CLI (-p --output-format json).
type ClaudeAgent struct {
	Timeout time.Duration
	Model   string
}

// NewClaudeAgent returns a ClaudeAgent with a 5-minute timeout.
func NewClaudeAgent() *ClaudeAgent { return &ClaudeAgent{Timeout: 5 * time.Minute} }

// Name returns the agent name.
func (c *ClaudeAgent) Name() string { return "Claude Code" }

// Models returns the known Claude Code models.
func (c *ClaudeAgent) Models() []string { return []string{"sonnet-4.5", "opus-4.1", "haiku-4"} }

// Execute runs `claude -p <task> --output-format json`.
func (c *ClaudeAgent) Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error) {
	if opts.ReadOnly {
		return nil, errors.New("claude: enforced read-only execution is unavailable")
	}
	model := opts.Model
	if model == "" {
		model = c.Model
	}
	args := []string{"-p", task, "--output-format", "json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return blobParser(ctx, "claude", c.Timeout, opts.WorkDir, parseVendorBlob, args...)
}

// CursorAgent wraps the Cursor agent CLI (-p --output-format json --trust --force).
type CursorAgent struct {
	Timeout time.Duration
}

// NewCursorAgent returns a CursorAgent with a 10-minute timeout.
func NewCursorAgent() *CursorAgent { return &CursorAgent{Timeout: 10 * time.Minute} }

// Name returns the agent name.
func (c *CursorAgent) Name() string { return "Cursor" }

// Models returns the known Cursor models.
func (c *CursorAgent) Models() []string { return []string{"claude-sonnet-4.5", "gpt-4.1", "o4-mini"} }

// Execute runs `agent -p --output-format json --trust --force <task>`.
func (c *CursorAgent) Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error) {
	if opts.ReadOnly {
		return nil, errors.New("cursor: enforced read-only execution is unavailable")
	}
	return blobParser(ctx, "agent", c.Timeout, opts.WorkDir, parseVendorBlob, "-p", "--output-format", "json", "--trust", "--force", task)
}

// OpenCodeAgent wraps the opencode CLI (run streaming).
type OpenCodeAgent struct {
	Timeout time.Duration
	Model   string
}

// NewOpenCodeAgent returns an OpenCodeAgent with a 5-minute timeout.
func NewOpenCodeAgent() *OpenCodeAgent { return &OpenCodeAgent{Timeout: 5 * time.Minute} }

// Name returns the agent name.
func (o *OpenCodeAgent) Name() string { return "OpenCode" }

// Models returns the known OpenCode models.
func (o *OpenCodeAgent) Models() []string { return []string{"gpt-4o", "claude-sonnet-4.5", "local"} }

// Execute runs `opencode run <task> [--model m]`.
func (o *OpenCodeAgent) Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error) {
	if opts.ReadOnly {
		return nil, errors.New("opencode: enforced read-only execution is unavailable")
	}
	model := opts.Model
	if model == "" {
		model = o.Model
	}
	args := []string{"run", task}
	if model != "" {
		args = append(args, "--model", model)
	}
	return lineStreamer(ctx, "opencode", o.Timeout, opts.WorkDir, func(line string) []agentcore.StreamEvent {
		return []agentcore.StreamEvent{{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: line}}}
	}, args...)
}
