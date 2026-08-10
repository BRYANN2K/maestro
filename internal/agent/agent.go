// Package agent wraps third-party coding-agent CLIs (codex, claude, cursor,
// opencode) as legacy sub-agents. Each wrapper translates the vendor's
// output into the shared agentcore.StreamEvent vocabulary so the pipeline is
// identical for native and legacy modes.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/agentcore"
)

const (
	// Vendor CLIs can be very noisy (Codex's rmcp worker in particular). Keep
	// enough of the tail to diagnose a failed process without allowing an
	// unbounded stderr stream to grow Maestro's memory usage.
	legacyStderrCaptureLimit = 64 << 10
	legacyStderrEventLimit   = 4 << 10
	legacyBlobOutputLimit    = 8 << 20
	legacyStreamOutputLimit  = 8 << 20
)

// Options configures one Execute call.
type Options struct {
	Model           string
	ReasoningEffort string
	WorkDir         string
	// ReadOnly requires an execution mode that is enforced by the vendor
	// process itself. Agents that cannot provide one must reject the run before
	// launching rather than treating prompt text as a security boundary.
	ReadOnly bool
}

// Agent is the legacy sub-agent contract.
type Agent interface {
	Name() string
	Models() []string
	Execute(ctx context.Context, task string, opts Options) (<-chan agentcore.StreamEvent, error)
}

// ReadOnlyCapable is implemented only by wrappers whose subprocess arguments
// enforce a read-only sandbox independently of the task prompt.
type ReadOnlyCapable interface {
	SupportsReadOnly() bool
}

// SupportsReadOnly reports whether agent can honor Options.ReadOnly. This is a
// capability check, not an inference from a vendor name or model.
func SupportsReadOnly(agent Agent) bool {
	capable, ok := agent.(ReadOnlyCapable)
	return ok && capable.SupportsReadOnly()
}

// lineStreamer runs a subprocess and translates each stdout line into
// events. One EvDone is emitted after a clean exit; a non-zero exit emits
// EvError first.
func lineStreamer(ctx context.Context, binary string, timeout time.Duration, workDir string, translate func(line string) []agentcore.StreamEvent, args ...string) (<-chan agentcore.StreamEvent, error) {
	return lineStreamerWithInput(ctx, binary, timeout, workDir, nil, translate, args...)
}

// lineStreamerWithInput is lineStreamer with an explicit subprocess stdin.
// It keeps large or sensitive structured prompts out of argv while preserving
// the existing line-oriented stdout protocol and cancellation lifecycle.
func lineStreamerWithInput(ctx context.Context, binary string, timeout time.Duration, workDir string, input io.Reader, translate func(line string) []agentcore.StreamEvent, args ...string) (<-chan agentcore.StreamEvent, error) {
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	cmd, err := commandInDir(ctx, binary, workDir, args...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", binary, err)
	}
	cmd.Stdin = input
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", binary, err)
	}
	// Bubble Tea owns the terminal while a run is active. Never let a vendor
	// process inherit Maestro's stderr: Codex and its rmcp runtime write status
	// and retry logs there, which otherwise corrupt the alternate screen.
	stderr := newTailBuffer(legacyStderrCaptureLimit)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", binary, err)
	}
	ch := make(chan agentcore.StreamEvent, 64)
	go func() {
		defer close(ch)
		defer cancel()
		var seq uint64
		var translatedDone agentcore.Done
		hasTranslatedDone := false
		var stdoutBytes int
		outputExceeded := false
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanLoop:
		for sc.Scan() {
			// Cancellation owns the terminal lifecycle. Once requested, discard
			// any line that raced with process termination instead of replaying
			// stale vendor output into the next TUI frame.
			if ctx.Err() != nil {
				break scanLoop
			}
			stdoutBytes += len(sc.Bytes()) + 1
			if stdoutBytes > legacyStreamOutputLimit {
				outputExceeded = true
				cancel()
				break scanLoop
			}
			for _, ev := range translate(sc.Text()) {
				// Vendor protocols may carry authoritative usage in their own
				// completion record. Hold it until the process exits cleanly so
				// consumers still receive exactly one terminal Done event.
				if ev.Type == agentcore.EvDone {
					if done, ok := ev.Content.(agentcore.Done); ok {
						translatedDone = done
						hasTranslatedDone = true
					}
					continue
				}
				select {
				case ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, ev.Type, ev.Content):
				case <-ctx.Done():
					break scanLoop
				}
			}
		}
		scanErr := sc.Err()
		err := cmd.Wait()
		if outputExceeded {
			ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
				Message: fmt.Sprintf("%s output exceeded %d bytes", binary, legacyStreamOutputLimit),
			})
			return
		}
		// The orchestrator turns context cancellation into one lifecycle
		// status. Do not also emit a provider error (or a misleading Done),
		// which used to render two errors after Escape-Escape.
		if ctx.Err() != nil {
			if parentCtx.Err() == nil {
				ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
					Message: fmt.Sprintf("%s timed out after %s", binary, timeout),
				})
			}
			return
		}
		if scanErr != nil {
			ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
				Message: processFailureMessage(binary, fmt.Errorf("read stdout: %w", scanErr), stderr.String()),
			})
			return
		}
		if err != nil {
			ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
				Message: processFailureMessage(binary, err, stderr.String()),
			})
			return
		}
		if !hasTranslatedDone {
			translatedDone = agentcore.Done{}
		}
		ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvDone, translatedDone)
	}()
	return ch, nil
}

// blobParser runs a subprocess to completion and translates the captured
// output blob into events.
func blobParser(ctx context.Context, binary string, timeout time.Duration, workDir string, translate func(blob string) []agentcore.StreamEvent, args ...string) (<-chan agentcore.StreamEvent, error) {
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	cmd, err := commandInDir(ctx, binary, workDir, args...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", binary, err)
	}
	stdout := newHeadBuffer(legacyBlobOutputLimit)
	stderr := newTailBuffer(legacyStderrCaptureLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	commandCtxErr := ctx.Err()
	parentCtxErr := parentCtx.Err()
	cancel()
	ch := make(chan agentcore.StreamEvent, 32)
	var seq uint64
	go func() {
		defer close(ch)
		if commandCtxErr != nil {
			// Cancellation is represented by the caller's context, not by a
			// translated partial blob or an additional provider error event.
			if parentCtxErr == nil {
				ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
					Message: fmt.Sprintf("%s timed out after %s", binary, timeout),
				})
			}
			return
		}
		if stdout.Truncated() {
			ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
				Message: fmt.Sprintf("%s output exceeded %d bytes", binary, legacyBlobOutputLimit),
			})
			return
		}
		for _, ev := range translate(stdout.String()) {
			ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, ev.Type, ev.Content)
		}
		if err != nil {
			ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvError, agentcore.StreamError{
				Message: processFailureMessage(binary, err, stderr.String()),
			})
			return
		}
		ch <- agentcore.NewEvent(&seq, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{})
	}()
	return ch, nil
}

// cappedBuffer is an io.Writer that always reports the complete write while
// retaining at most limit bytes. head buffers preserve structured stdout;
// tail buffers preserve the most recent (usually most actionable) stderr.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	tail      bool
	truncated bool
}

func newHeadBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func newTailBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit, tail: true} }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	if b.limit <= 0 {
		b.truncated = b.truncated || written > 0
		return written, nil
	}
	if !b.tail {
		remaining := b.limit - len(b.buf)
		if remaining > 0 {
			b.buf = append(b.buf, p[:min(len(p), remaining)]...)
		}
		b.truncated = b.truncated || len(p) > remaining
		return written, nil
	}
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		b.truncated = true
		return written, nil
	}
	if overflow := len(b.buf) + len(p) - b.limit; overflow > 0 {
		copy(b.buf, b.buf[overflow:])
		b.buf = b.buf[:len(b.buf)-overflow]
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return written, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.buf))
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// processFailureMessage turns captured vendor diagnostics into a bounded,
// terminal-safe StreamError. Escape sequences and control bytes must never
// be replayed by the TUI.
func processFailureMessage(binary string, err error, stderr string) string {
	message := fmt.Sprintf("%s exited: %v", binary, err)
	diagnostic := sanitizeDiagnostic(stderr)
	if diagnostic == "" {
		return message
	}
	if len(diagnostic) > legacyStderrEventLimit {
		diagnostic = strings.ToValidUTF8(diagnostic[len(diagnostic)-legacyStderrEventLimit:], "�")
		diagnostic = "… " + diagnostic
	}
	return message + ": " + diagnostic
}

func sanitizeDiagnostic(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		case '\r':
			return '\n'
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// commandInDir builds a subprocess command rooted in workDir. The directory
// is resolved and verified before it is assigned to cmd.Dir so configuration
// errors fail before a vendor CLI can start in Maestro's own process cwd.
func commandInDir(ctx context.Context, binary, workDir string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	configureProcessTree(cmd)
	if workDir == "" {
		return cmd, nil
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve work directory %q: %w", workDir, err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("validate work directory %q: %w", absDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("work directory %q is not a directory", absDir)
	}
	cmd.Dir = absDir
	return cmd, nil
}

// parseCodexLine decodes one codex exec JSONL line into events. Non-JSON
// lines fall back to a text delta so nothing is silently dropped.
func parseCodexLine(line string) []agentcore.StreamEvent {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return []agentcore.StreamEvent{{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: line}}}
	}
	t, _ := raw["type"].(string)
	switch t {
	case "turn.completed":
		usage, _ := raw["usage"].(map[string]any)
		if usage == nil {
			return []agentcore.StreamEvent{{Type: agentcore.EvDone, Content: agentcore.Done{}}}
		}
		input := jsonInt(usage["input_tokens"])
		cached := jsonInt(usage["cached_input_tokens"])
		cacheWrite := jsonInt(usage["cache_write_input_tokens"])
		// agentcore tracks cache components separately and the session meter
		// sums them. Codex reports cached/write tokens as subsets of input, so
		// keep InputTokens exclusive to avoid double-counting the context.
		uncached := max(input-cached-cacheWrite, 0)
		return []agentcore.StreamEvent{{Type: agentcore.EvDone, Content: agentcore.Done{Usage: &agentcore.Usage{
			InputTokens:       uncached,
			OutputTokens:      jsonInt(usage["output_tokens"]),
			CacheCreateTokens: cacheWrite,
			CacheHitTokens:    cached,
		}}}}
	case "item.started", "item.completed", "item.updated", "item.created", "message":
		item, _ := raw["item"].(map[string]any)
		if item == nil {
			return nil
		}
		return parseCodexItem(t, item)
	case "agent_message", "assistant_message":
		text, _ := raw["text"].(string)
		if text == "" {
			text, _ = raw["content"].(string)
		}
		if text != "" {
			return []agentcore.StreamEvent{{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: text}}}
		}
	}
	return nil
}

func jsonInt(value any) int {
	switch number := value.(type) {
	case float64:
		if number > 0 {
			return int(number)
		}
	case int:
		if number > 0 {
			return number
		}
	case json.Number:
		parsed, err := number.Int64()
		if err == nil && parsed > 0 {
			return int(parsed)
		}
	}
	return 0
}

// parseCodexItem maps a codex item object to lifecycle events based on the
// JSONL envelope. A tool call starts with EvToolCall and ends with exactly one
// EvToolResult; an empty command output is still a successful result.
func parseCodexItem(eventType string, item map[string]any) []agentcore.StreamEvent {
	itemType, _ := item["type"].(string)
	switch itemType {
	case "message", "agent_message", "assistant_message":
		text := extractText(item)
		if text != "" {
			return []agentcore.StreamEvent{{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: text}}}
		}
	case "command_execution", "command", "tool_call", "function_call", "local_shell_call":
		id, _ := item["id"].(string)
		name := itemType
		if n, ok := item["name"].(string); ok && n != "" {
			name = n
		}
		if cmd, ok := item["command"].(string); ok && cmd != "" {
			name = cmd
		}
		args := ""
		if a, ok := item["args"].(string); ok {
			args = a
		} else if a, ok := item["arguments"].(string); ok {
			args = a
		} else if arr, ok := item["args"].([]any); ok {
			parts := make([]string, 0, len(arr))
			for _, p := range arr {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			args = strings.Join(parts, " ")
		}
		status, _ := item["status"].(string)
		terminal := eventType == "item.completed"
		switch status {
		case "completed", "done", "success", "succeeded", "failed", "error", "cancelled", "canceled":
			terminal = true
		}
		if !terminal {
			return []agentcore.StreamEvent{{Type: agentcore.EvToolCall, Content: agentcore.ToolCall{ID: id, Name: name, Args: args}}}
		}

		output := firstString(item, "aggregated_output", "output", "result")
		toolErr := ""
		if status == "failed" || status == "error" || status == "cancelled" || status == "canceled" || nonZeroExitCode(item["exit_code"]) {
			toolErr = strings.TrimSpace(output)
			if toolErr == "" {
				toolErr = "tool execution failed"
			}
		}
		return []agentcore.StreamEvent{{Type: agentcore.EvToolResult, Content: agentcore.ToolResult{
			ID: id, Name: name, Output: output, Err: toolErr,
		}}}
	}
	return nil
}

func firstString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok {
			return value
		}
	}
	return ""
}

func nonZeroExitCode(value any) bool {
	switch code := value.(type) {
	case float64:
		return code != 0
	case json.Number:
		return code.String() != "0"
	default:
		return false
	}
}

func extractText(item map[string]any) string {
	if text, ok := item["text"].(string); ok && text != "" {
		return text
	}
	if content, ok := item["content"].(string); ok && content != "" {
		return content
	}
	if content, ok := item["content"].([]any); ok {
		var b strings.Builder
		for _, c := range content {
			if m, ok := c.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// vendorResult is the common shape of claude/cursor --output-format json.
type vendorResult struct {
	Result   string  `json:"result"`
	IsError  bool    `json:"is_error"`
	Session  string  `json:"session_id"`
	NumTurns int     `json:"num_turns"`
	CostUSD  float64 `json:"total_cost_usd"`
}

// parseVendorBlob decodes a single JSON result blob; falls back to the raw
// output as text.
func parseVendorBlob(blob string) []agentcore.StreamEvent {
	var resp vendorResult
	if err := json.Unmarshal([]byte(blob), &resp); err != nil {
		return []agentcore.StreamEvent{{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: strings.TrimSpace(blob)}}}
	}
	if resp.IsError {
		return []agentcore.StreamEvent{{Type: agentcore.EvError, Content: agentcore.StreamError{Message: resp.Result}}}
	}
	if resp.Result != "" {
		return []agentcore.StreamEvent{{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: resp.Result}}}
	}
	return nil
}
