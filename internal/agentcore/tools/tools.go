// Package tools provides the built-in tool registry and tools available to
// the loop: read, grep, write, bash, and the ask stub (fully wired in B4).
package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// defaultGuard is the shared read-before-edit registry for the built-in
// tools: read stamps files, write refuses stale or unread files.
var defaultGuard = newFileGuard()

const maxReadBytes int64 = 2 << 20
const maxFileGuardEntries = 4 << 10

const (
	maxGrepFileBytes   int64 = 1 << 20
	maxGrepScanBytes         = 64 << 20
	maxGrepFiles             = 4 << 10
	maxGrepLineBytes         = 4 << 10
	maxGrepOutputBytes       = 64 << 10
	maxBashOutputBytes       = 64 << 10
)

const (
	grepLineTruncated = "… [line truncated]"
	grepOutputLimit   = "… grep output truncated"
	grepScanLimit     = "… grep scan truncated"
	bashOutputLimit   = "\n… output truncated"
)

// fileGuard enforces the read-before-edit contract: a write to an existing
// file requires a prior read, and the file must not have changed on disk
// since that read (staleness guard, borrowed from opencode). New files are
// exempt. Each default tool registry owns a guard, so authorization cannot
// leak between runs or workspaces; standalone constructors share the bounded
// fallback guard for backwards compatibility.
type fileGuard struct {
	mu     sync.Mutex
	stamps map[string]time.Time
	order  []string
	next   int
}

func newFileGuard() *fileGuard {
	return &fileGuard{stamps: map[string]time.Time{}}
}

// recordRead stamps the mod time observed by a successful read.
func (g *fileGuard) recordRead(path string, mod time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	path = canonicalGuardPath(path)
	if _, exists := g.stamps[path]; !exists {
		if len(g.order) < maxFileGuardEntries {
			g.order = append(g.order, path)
		} else {
			delete(g.stamps, g.order[g.next])
			g.order[g.next] = path
			g.next = (g.next + 1) % len(g.order)
		}
	}
	g.stamps[path] = mod
}

// recordWrite refreshes the stamp after a successful write.
func (g *fileGuard) recordWrite(path string, mod time.Time) {
	g.recordRead(path, mod)
}

// checkWrite returns an error when writing to an existing file that was
// never read or that changed on disk since the last read.
func (g *fileGuard) checkWrite(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // new file: creation needs no prior read
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	g.mu.Lock()
	stamp, read := g.stamps[canonicalGuardPath(path)]
	g.mu.Unlock()
	if !read {
		return fmt.Errorf("write %s: file exists but was never read — read it before editing", path)
	}
	if fi.ModTime().After(stamp) {
		return fmt.Errorf("write %s: file modified since it was read — re-read it before editing", path)
	}
	return nil
}

func canonicalGuardPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// Permission levels attached to ToolSpec.
const (
	PermView  = "view"
	PermEdit  = "edit"
	PermAdmin = "admin"
)

// Registry is the ordered set of tools exposed to the model.
type Registry struct {
	order []string
	m     map[string]agentcore.Tool
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{m: map[string]agentcore.Tool{}}
}

// Default returns the registry with the standard tools.
func Default() *Registry {
	r := New()
	guard := newFileGuard()
	r.Add(newRead(guard))
	r.Add(NewGrep())
	r.Add(newWrite(guard))
	r.Add(NewBash())
	r.Add(NewAsk(nil))
	return r
}

// Add registers a tool.
func (r *Registry) Add(t agentcore.Tool) {
	if _, ok := r.m[t.Spec().Name]; ok {
		return
	}
	r.order = append(r.order, t.Spec().Name)
	r.m[t.Spec().Name] = t
}

// Replace swaps a tool with the same name (used to inject the wired ask
// tool over the built-in stub). The registration order is preserved.
func (r *Registry) Replace(name string, t agentcore.Tool) {
	if _, ok := r.m[name]; !ok {
		r.order = append(r.order, name)
	}
	r.m[name] = t
}

// Get returns the tool with name.
func (r *Registry) Get(name string) (agentcore.Tool, bool) {
	t, ok := r.m[name]
	return t, ok
}

// Specs returns the tools' schemas in registration order.
func (r *Registry) Specs() []agentcore.ToolSpec {
	out := make([]agentcore.ToolSpec, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.m[n].Spec())
	}
	return out
}

// Names returns the registered tool names in order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// Map returns the tools as a name→Tool map for the loop.
func (r *Registry) Map() map[string]agentcore.Tool {
	out := make(map[string]agentcore.Tool, len(r.order))
	for _, n := range r.order {
		out[n] = r.m[n]
	}
	return out
}

func obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

// NewRead returns the read tool: read a file from disk.
func NewRead() agentcore.Tool {
	return newRead(defaultGuard)
}

func newRead(guard *fileGuard) agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name: "read", Description: "Read a file and return its contents.",
		InputSchema: obj(map[string]any{
			"path": map[string]any{"type": "string", "description": "Path of the file to read"},
		}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		path, _ := args["path"].(string)
		if path == "" {
			return "", fmt.Errorf("read: path is required")
		}
		file, err := openReadOnly(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		defer file.Close()
		fi, err := file.Stat()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("read %s: not a regular file", path)
		}
		if fi.Size() > maxReadBytes {
			return "", fmt.Errorf("read %s: file is %d bytes; limit is %d", path, fi.Size(), maxReadBytes)
		}
		data, err := io.ReadAll(io.LimitReader(file, maxReadBytes+1))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		if int64(len(data)) > maxReadBytes {
			return "", fmt.Errorf("read %s: file exceeded %d bytes while reading", path, maxReadBytes)
		}
		if current, statErr := file.Stat(); statErr == nil {
			fi = current
		}
		guard.recordRead(path, fi.ModTime())
		return string(data), nil
	})
}

// NewGrep returns the grep tool: recursive content search.
func NewGrep() agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name: "grep", Description: "Search file contents recursively for a pattern.",
		InputSchema: obj(map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Regular expression to search for"},
			"path":    map[string]any{"type": "string", "description": "Root directory, default ."},
		}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		pattern, _ := args["pattern"].(string)
		if pattern == "" {
			return "", fmt.Errorf("grep: pattern is required")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("grep: invalid regular expression: %w", err)
		}
		root, _ := args["path"].(string)
		if root == "" {
			root = "."
		}
		matches, err := grepDir(ctx, root, re, 100)
		if err != nil {
			return "", err
		}
		if len(matches) == 0 {
			return "no matches", nil
		}
		return strings.Join(matches, "\n"), nil
	})
}

func grepDir(ctx context.Context, root string, pattern *regexp.Regexp, limit int) ([]string, error) {
	return grepDirLimited(ctx, root, pattern, grepLimits{
		matches: limit,
		files:   maxGrepFiles,
		bytes:   maxGrepScanBytes,
		output:  maxGrepOutputBytes,
	})
}

type grepLimits struct {
	matches int
	files   int
	bytes   int64
	output  int
}

func grepDirLimited(ctx context.Context, root string, pattern *regexp.Regexp, limits grepLimits) ([]string, error) {
	var out []string
	var scannedBytes int64
	outputBytes, scannedFiles, matchesFound := 0, 0, 0
	appendScanLimit := func() {
		if outputBytes+len(grepScanLimit)+1 <= limits.output {
			out = append(out, grepScanLimit)
			outputBytes += len(grepScanLimit) + 1
		}
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// An explicitly selected root remains searchable even when its name
			// is "." or starts with a dot. Only hidden descendants are skipped.
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if matchesFound >= limits.matches {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxGrepFileBytes {
			return nil
		}
		if scannedFiles >= limits.files || info.Size() > limits.bytes-scannedBytes {
			appendScanLimit()
			return filepath.SkipAll
		}
		file, err := openReadOnly(path)
		if err != nil {
			return nil
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() > maxGrepFileBytes {
			_ = file.Close()
			return nil
		}
		remaining := limits.bytes - scannedBytes
		if remaining <= 0 {
			_ = file.Close()
			appendScanLimit()
			return filepath.SkipAll
		}
		readLimit := min(maxGrepFileBytes, remaining)
		limited := &io.LimitedReader{R: file, N: readLimit + 1}
		scanner := bufio.NewScanner(limited)
		scanner.Buffer(make([]byte, 0, 64*1024), int(maxGrepFileBytes)+1)
		scannedFiles++
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			if err := ctx.Err(); err != nil {
				_ = file.Close()
				return err
			}
			if matchesFound >= limits.matches {
				break
			}
			line := scanner.Text()
			if pattern.MatchString(line) {
				line = boundedToolLine(strings.TrimSpace(line), maxGrepLineBytes)
				match := fmt.Sprintf("%s:%d: %s", path, lineNumber, line)
				// Reserve room for an explicit truncation marker so the final
				// joined result never crosses the advertised byte budget.
				if outputBytes+len(match)+1+len(grepOutputLimit)+1 > limits.output {
					if outputBytes+len(grepOutputLimit)+1 <= limits.output {
						out = append(out, grepOutputLimit)
					}
					_ = file.Close()
					return filepath.SkipAll
				}
				out = append(out, match)
				outputBytes += len(match) + 1
				matchesFound++
			}
		}
		consumed := readLimit + 1 - limited.N
		scannedBytes += min(consumed, readLimit)
		_ = file.Close()
		if consumed > readLimit {
			appendScanLimit()
			return filepath.SkipAll
		}
		return nil
	})
	if matchesFound >= limits.matches && (len(out) == 0 || out[len(out)-1] != grepOutputLimit) {
		marker := fmt.Sprintf("… %d+ matches, truncated", limits.matches)
		if outputBytes+len(marker)+1 <= limits.output {
			out = append(out, marker)
		}
	}
	return out, err
}

func boundedToolLine(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	end := max(limit-len(grepLineTruncated), 0)
	// Avoid cutting through a UTF-8 continuation byte. Invalid source bytes
	// are left for the provider/terminal projection boundary to sanitize.
	for end > 0 && end < len(value) && value[end]&0xc0 == 0x80 {
		end--
	}
	return value[:end] + grepLineTruncated
}

// cappedOutput is an always-draining writer that retains only the first
// limit bytes. Using it as both stdout and stderr preserves CombinedOutput's
// useful ordering without allowing a noisy child process to grow memory
// without bound.
type cappedOutput struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(w.limit-w.buf.Len(), 0)
	if len(p) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	_, _ = w.buf.Write(p)
	return n, nil
}

func (w *cappedOutput) String() string {
	if !w.truncated {
		return w.buf.String()
	}
	return w.buf.String() + bashOutputLimit
}

// NewWrite returns the write tool: write a file. Requires approval.
func NewWrite() agentcore.Tool {
	return newWrite(defaultGuard)
}

func newWrite(guard *fileGuard) agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name: "write", Description: "Write content to a file, creating parent directories.",
		InputSchema: obj(map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		}),
		NeedsApproval: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if path == "" {
			return "", fmt.Errorf("write: path is required")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		if err := guard.checkWrite(path); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		if fi, err := os.Stat(path); err == nil {
			guard.recordWrite(path, fi.ModTime())
		}
		return fmt.Sprintf("wrote %s (%d bytes)", path, len(content)), nil
	})
}

// NewBash returns the bash tool: run a shell command. Requires approval.
func NewBash() agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name: "bash", Description: "Run a shell command and return its combined output.",
		InputSchema: obj(map[string]any{
			"command": map[string]any{"type": "string"},
			"workdir": map[string]any{"type": "string", "description": "Working directory, default ."},
		}),
		NeedsApproval: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		command, _ := args["command"].(string)
		if command == "" {
			return "", fmt.Errorf("bash: command is required")
		}
		workdir, _ := args["workdir"].(string)
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cmd, err := newShellCommand(ctx, command)
		if err != nil {
			return "", fmt.Errorf("bash: %w", err)
		}
		if workdir != "" {
			cmd.Dir = workdir
		}
		out := cappedOutput{limit: maxBashOutputBytes}
		cmd.Stdout, cmd.Stderr = &out, &out
		err = runProcessTreeCommand(cmd)
		if err != nil {
			return out.String(), fmt.Errorf("bash: %w", err)
		}
		return out.String(), nil
	})
}

// NewAsk returns the ask tool: a structured question for the human, wired
// to the interactive picker when fn is non-nil (B4).
func NewAsk(fn agentcore.AskFunc) agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name: "ask", Description: "Ask the user a structured question with options.",
		InputSchema: obj(map[string]any{
			"question":    map[string]any{"type": "string"},
			"options":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"recommended": map[string]any{"type": "integer"},
		}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		question, _ := args["question"].(string)
		if question == "" {
			return "", fmt.Errorf("ask: question is required")
		}
		var options []string
		if raw, ok := args["options"].([]any); ok {
			for _, o := range raw {
				if s, ok := o.(string); ok {
					options = append(options, s)
				}
			}
		}
		if len(options) == 0 {
			return "", fmt.Errorf("ask: at least one option is required")
		}
		recommended := 0
		switch r := args["recommended"].(type) {
		case float64:
			recommended = int(r)
		case int:
			recommended = r
		}
		if fn == nil {
			return "", fmt.Errorf("ask: no interactive picker (headless run) — options: %s", strings.Join(options, " | "))
		}
		idx, err := fn(ctx, question, options, recommended)
		if err != nil {
			return "", fmt.Errorf("ask: %w", err)
		}
		if idx < 0 || idx >= len(options) {
			return "", fmt.Errorf("ask: invalid answer index %d", idx)
		}
		return fmt.Sprintf("user answered: %s", options[idx]), nil
	})
}
