package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestParseCodexLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want agentcore.EventType
		text string
	}{
		{"message item", `{"type":"item.completed","item":{"type":"message","text":"hello"}}`, agentcore.EvTextDelta, "hello"},
		{"agent_message", `{"type":"agent_message","text":"hi"}`, agentcore.EvTextDelta, "hi"},
		{"tool call", `{"type":"item.started","item":{"id":"call-1","type":"command_execution","name":"bash","args":"ls -la","status":"in_progress"}}`, agentcore.EvToolCall, "bash"},
		{"tool call args array", `{"type":"item.created","item":{"id":"call-2","type":"tool_call","name":"read","args":["a.go"]}}`, agentcore.EvToolCall, "read"},
		{"thread started ignored", `{"type":"thread.started","thread_id":"t1"}`, "", ""},
		{"content array", `{"type":"item.completed","item":{"type":"message","content":[{"type":"text","text":"multi"}]}}`, agentcore.EvTextDelta, "multi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := parseCodexLine(tt.line)
			if tt.want == "" {
				if len(evs) != 0 {
					t.Errorf("events = %+v, want none", evs)
				}
				return
			}
			if len(evs) != 1 || evs[0].Type != tt.want {
				t.Fatalf("events = %+v", evs)
			}
			switch c := evs[0].Content.(type) {
			case agentcore.TextDelta:
				if c.Text != tt.text {
					t.Errorf("text = %q, want %q", c.Text, tt.text)
				}
			case agentcore.ToolCall:
				if c.Name != tt.text {
					t.Errorf("tool name = %q, want %q", c.Name, tt.text)
				}
			}
		})
	}
}

func TestParseCodexToolLifecycle(t *testing.T) {
	started := parseCodexLine(`{"type":"item.started","item":{"id":"item-7","type":"command_execution","command":"/bin/zsh -lc pwd","status":"in_progress"}}`)
	if len(started) != 1 || started[0].Type != agentcore.EvToolCall {
		t.Fatalf("started events = %+v", started)
	}
	call := started[0].Content.(agentcore.ToolCall)
	if call.ID != "item-7" || call.Name != "/bin/zsh -lc pwd" {
		t.Fatalf("tool call = %+v", call)
	}

	completed := parseCodexLine(`{"type":"item.completed","item":{"id":"item-7","type":"command_execution","command":"/bin/zsh -lc pwd","aggregated_output":"","exit_code":0,"status":"completed"}}`)
	if len(completed) != 1 || completed[0].Type != agentcore.EvToolResult {
		t.Fatalf("completed events = %+v", completed)
	}
	result := completed[0].Content.(agentcore.ToolResult)
	if result.ID != "item-7" || result.Name != "/bin/zsh -lc pwd" || result.Output != "" || result.Err != "" {
		t.Fatalf("tool result = %+v", result)
	}
}

func TestParseCodexFailedToolIsTerminalError(t *testing.T) {
	events := parseCodexLine(`{"type":"item.completed","item":{"id":"item-8","type":"command_execution","command":"false","aggregated_output":"boom","exit_code":1,"status":"failed"}}`)
	if len(events) != 1 || events[0].Type != agentcore.EvToolResult {
		t.Fatalf("events = %+v", events)
	}
	result := events[0].Content.(agentcore.ToolResult)
	if result.Err != "boom" {
		t.Fatalf("tool result = %+v", result)
	}
}

func TestParseCodexTurnCompletedCarriesUsageWithoutDoubleCountingCache(t *testing.T) {
	events := parseCodexLine(`{"type":"turn.completed","usage":{"input_tokens":15432,"cached_input_tokens":8960,"cache_write_input_tokens":12,"output_tokens":9,"reasoning_output_tokens":3}}`)
	if len(events) != 1 || events[0].Type != agentcore.EvDone {
		t.Fatalf("events = %+v, want one Done", events)
	}
	done, ok := events[0].Content.(agentcore.Done)
	if !ok || done.Usage == nil {
		t.Fatalf("Done content = %#v", events[0].Content)
	}
	if got, want := done.Usage.InputTokens, 6460; got != want {
		t.Fatalf("uncached input = %d, want %d", got, want)
	}
	if done.Usage.CacheHitTokens != 8960 || done.Usage.CacheCreateTokens != 12 || done.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", *done.Usage)
	}
}

func TestLineStreamerEmitsTranslatedDoneExactlyOnce(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "codex", `printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":5}}'`))
	ch, err := lineStreamer(t.Context(), "codex", time.Second, "", parseCodexLine)
	if err != nil {
		t.Fatalf("lineStreamer: %v", err)
	}
	var done []agentcore.Done
	for ev := range ch {
		if ev.Type != agentcore.EvDone {
			continue
		}
		value, ok := ev.Content.(agentcore.Done)
		if !ok {
			t.Fatalf("Done content = %#v", ev.Content)
		}
		done = append(done, value)
	}
	if len(done) != 1 || done[0].Usage == nil {
		t.Fatalf("Done events = %+v, want exactly one with usage", done)
	}
	if got := done[0].Usage.InputTokens + done[0].Usage.CacheHitTokens + done[0].Usage.OutputTokens; got != 105 {
		t.Fatalf("accounted tokens = %d, want 105", got)
	}
}

func TestLineStreamerRejectsCumulativeMultiLineOutput(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "noisy-agent", `
i=0
while [ "$i" -lt 18 ]; do
  printf '%524288s\n' x
  i=$((i + 1))
done
`))
	ch, err := lineStreamer(t.Context(), "noisy-agent", 5*time.Second, "", func(string) []agentcore.StreamEvent {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectAgentEvents(t, ch)
	if len(events) != 1 || events[0].Type != agentcore.EvError {
		t.Fatalf("events = %+v, want one bounded error", events)
	}
	message := events[0].Content.(agentcore.StreamError).Message
	if !strings.Contains(message, "output exceeded") || len(message) > 128 {
		t.Fatalf("output limit error = %q", message)
	}
}

func TestParseCodexNonJSON(t *testing.T) {
	evs := parseCodexLine("plain output line")
	if len(evs) != 1 || evs[0].Type != agentcore.EvTextDelta {
		t.Errorf("events = %+v", evs)
	}
}

func TestParseVendorBlob(t *testing.T) {
	tests := []struct {
		name string
		blob string
		want []agentcore.EventType
	}{
		{"result", `{"result":"ok","num_turns":2,"total_cost_usd":0.1}`, []agentcore.EventType{agentcore.EvTextDelta}},
		{"is_error", `{"result":"boom","is_error":true}`, []agentcore.EventType{agentcore.EvError}},
		{"not json", "plain text", []agentcore.EventType{agentcore.EvTextDelta}},
		{"empty result", `{"result":""}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := parseVendorBlob(tt.blob)
			if len(evs) != len(tt.want) {
				t.Fatalf("events = %+v, want %d", evs, len(tt.want))
			}
			for i, ev := range evs {
				if ev.Type != tt.want[i] {
					t.Errorf("event %d = %s, want %s", i, ev.Type, tt.want[i])
				}
			}
		})
	}
}

func TestFactory(t *testing.T) {
	for _, name := range Names() {
		a, err := Create(name)
		if err != nil || a.Name() == "" {
			t.Errorf("Create(%s) = %v, %v", name, a, err)
		}
		if len(a.Models()) == 0 {
			t.Errorf("%s: no models", name)
		}
	}
	if _, err := Create("grok"); err != nil {
		t.Errorf("grok should exist in B9: %v", err)
	}
	if _, err := Create("kimi"); err != nil {
		t.Errorf("kimi should exist in B9: %v", err)
	}
	if _, err := Create("bogus"); err == nil {
		t.Error("bogus agent should fail")
	}
}

// fakeBin writes an executable shim on PATH and returns the PATH value.
func fakeBin(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return dir + ":" + os.Getenv("PATH")
}

func collectAgentEvents(t *testing.T, ch <-chan agentcore.StreamEvent) []agentcore.StreamEvent {
	t.Helper()
	var evs []agentcore.StreamEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestLegacyAgentsExecuteInConfiguredWorkDir(t *testing.T) {
	tests := []struct {
		name   string
		binary string
		script string
		new    func() Agent
	}{
		{
			name:   "codex",
			binary: "codex",
			script: `printf '{"type":"agent_message","text":"%s"}\n' "$PWD"`,
			new:    func() Agent { return NewCodexAgent() },
		},
		{
			name:   "claude",
			binary: "claude",
			script: `printf '{"result":"%s"}\n' "$PWD"`,
			new:    func() Agent { return NewClaudeAgent() },
		},
		{
			name:   "cursor",
			binary: "agent",
			script: `printf '{"result":"%s"}\n' "$PWD"`,
			new:    func() Agent { return NewCursorAgent() },
		},
		{
			name:   "opencode",
			binary: "opencode",
			script: `pwd`,
			new:    func() Agent { return NewOpenCodeAgent() },
		},
		{
			name:   "kimi",
			binary: "kimi",
			script: `printf '{"result":"%s"}\n' "$PWD"`,
			new:    func() Agent { return NewKimiAgent() },
		},
		{
			name:   "grok",
			binary: "grok",
			script: `printf '{"result":"%s"}\n' "$PWD"`,
			new:    func() Agent { return NewGrokAgent() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			t.Setenv("PATH", fakeBin(t, tt.binary, tt.script+"\n"))
			ch, err := tt.new().Execute(t.Context(), "task", Options{WorkDir: workDir})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var got string
			for _, ev := range collectAgentEvents(t, ch) {
				if delta, ok := ev.Content.(agentcore.TextDelta); ok {
					got = delta.Text
					break
				}
			}
			gotInfo, err := os.Stat(got)
			if err != nil {
				t.Fatalf("agent cwd %q: %v", got, err)
			}
			wantInfo, err := os.Stat(workDir)
			if err != nil {
				t.Fatalf("configured cwd %q: %v", workDir, err)
			}
			if !os.SameFile(gotInfo, wantInfo) {
				t.Fatalf("agent cwd = %q, want %q", got, workDir)
			}
		})
	}
}

func TestLegacyAgentRejectsInvalidWorkDirBeforeStart(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "codex", "touch \"$MAESTRO_SHOULD_NOT_START\"\n"))
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("MAESTRO_SHOULD_NOT_START", started)
	invalidFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for _, workDir := range []string{
		filepath.Join(t.TempDir(), "missing"),
		invalidFile,
	} {
		if _, err := NewCodexAgent().Execute(t.Context(), "task", Options{WorkDir: workDir}); err == nil {
			t.Fatalf("Execute WorkDir=%q succeeded", workDir)
		}
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatalf("legacy subprocess started despite invalid cwd: %v", err)
	}
}

func TestBlobAgentReportsProcessFailureAsExit(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "claude", "echo failed >&2\nexit 7\n"))
	ch, err := NewClaudeAgent().Execute(t.Context(), "task", Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	events := collectAgentEvents(t, ch)
	if len(events) == 0 || events[len(events)-1].Type != agentcore.EvError {
		t.Fatalf("events = %+v", events)
	}
	streamErr, ok := events[len(events)-1].Content.(agentcore.StreamError)
	if !ok || !strings.Contains(streamErr.Message, "exited") || strings.Contains(streamErr.Message, "cancelled") {
		t.Fatalf("process error = %+v", events[len(events)-1].Content)
	}
	if !strings.Contains(streamErr.Message, "failed") {
		t.Fatalf("process error omitted captured stderr: %q", streamErr.Message)
	}
}

func TestStreamingAgentNeverWritesVendorStderrToTerminal(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "codex", `
echo 'Reading additional input from stdin...' >&2
echo 'ERROR rmcp::transport::worker: http://127.0.0.1:53343/mcp' >&2
echo '{"type":"agent_message","text":"done"}'
`))

	terminalRead, terminalWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStderr := os.Stderr
	os.Stderr = terminalWrite
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = terminalWrite.Close()
		_ = terminalRead.Close()
	})

	ch, err := NewCodexAgent().Execute(t.Context(), "task", Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	events := collectAgentEvents(t, ch)
	os.Stderr = previousStderr
	if err := terminalWrite.Close(); err != nil {
		t.Fatal(err)
	}
	terminalBytes, err := io.ReadAll(terminalRead)
	if err != nil {
		t.Fatal(err)
	}
	if len(terminalBytes) != 0 {
		t.Fatalf("vendor stderr leaked to terminal: %q", terminalBytes)
	}
	if len(events) != 2 || events[0].Type != agentcore.EvTextDelta || events[1].Type != agentcore.EvDone {
		t.Fatalf("events = %+v", events)
	}
}

func TestStreamingAgentFailureEmitsBoundedSafeStderr(t *testing.T) {
	script := `
i=0
while [ "$i" -lt 7000 ]; do
  printf 'rmcp retry noise 0123456789\n' >&2
  i=$((i + 1))
done
printf '\033[31mFINAL MCP FAILURE\033[0m\n' >&2
exit 7
`
	t.Setenv("PATH", fakeBin(t, "codex", script))
	ch, err := NewCodexAgent().Execute(t.Context(), "task", Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	events := collectAgentEvents(t, ch)
	if len(events) != 1 || events[0].Type != agentcore.EvError {
		t.Fatalf("events = %+v", events)
	}
	streamErr := events[0].Content.(agentcore.StreamError).Message
	if !strings.Contains(streamErr, "FINAL MCP FAILURE") {
		t.Fatalf("captured stderr lost actionable tail: %q", streamErr)
	}
	if strings.ContainsRune(streamErr, '\x1b') {
		t.Fatalf("captured stderr retained terminal escape: %q", streamErr)
	}
	if len(streamErr) > legacyStderrEventLimit+256 {
		t.Fatalf("stream error is not bounded: %d bytes", len(streamErr))
	}
}

func TestBlobAgentSeparatesSuccessfulStdoutFromStderr(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "claude", `
echo 'vendor warning that is not JSON' >&2
echo '{"result":"done","is_error":false}'
`))
	ch, err := NewClaudeAgent().Execute(t.Context(), "task", Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	events := collectAgentEvents(t, ch)
	if len(events) != 2 || events[0].Type != agentcore.EvTextDelta || events[1].Type != agentcore.EvDone {
		t.Fatalf("events = %+v", events)
	}
	if got := events[0].Content.(agentcore.TextDelta).Text; got != "done" {
		t.Fatalf("result = %q, want stdout JSON only", got)
	}
}

func TestProcessFailureMessageSanitizesControls(t *testing.T) {
	got := processFailureMessage("codex", errors.New("boom"), "\x1b[31mred\x1b[0m\x00\rnext")
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x00') {
		t.Fatalf("unsafe controls remained: %q", got)
	}
	if !strings.Contains(got, "red\nnext") {
		t.Fatalf("diagnostic content lost: %q", got)
	}
}

func TestCodexExecuteStreams(t *testing.T) {
	script := `echo '{"type":"item.completed","item":{"type":"message","text":"building"}}'
echo '{"type":"item.started","item":{"id":"tool-1","type":"command_execution","name":"bash","args":"ls","status":"in_progress"}}'
echo '{"type":"item.completed","item":{"id":"tool-1","type":"command_execution","name":"bash","args":"ls","aggregated_output":"","exit_code":0,"status":"completed"}}'
exit 0
`
	ctx := context.Background()
	t.Setenv("PATH", fakeBin(t, "codex", script))
	a := NewCodexAgent()
	ch, err := a.Execute(ctx, "do the thing", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := collectAgentEvents(t, ch)
	if len(evs) != 4 {
		t.Fatalf("events = %d: %+v", len(evs), evs)
	}
	if evs[0].Type != agentcore.EvTextDelta || evs[1].Type != agentcore.EvToolCall || evs[2].Type != agentcore.EvToolResult || evs[3].Type != agentcore.EvDone {
		t.Errorf("event types = %s, %s, %s, %s", evs[0].Type, evs[1].Type, evs[2].Type, evs[3].Type)
	}
	tc := evs[1].Content.(agentcore.ToolCall)
	if tc.ID != "tool-1" || tc.Name != "bash" || tc.Args != "ls" {
		t.Errorf("tool call = %+v", tc)
	}
	tr := evs[2].Content.(agentcore.ToolResult)
	if tr.ID != "tool-1" || tr.Err != "" || tr.Output != "" {
		t.Errorf("tool result = %+v", tr)
	}
	if evs[0].Role != agentcore.RoleDev {
		t.Errorf("role = %s", evs[0].Role)
	}
}

func TestCodexExecuteIsolatesUserConfigAndDisablesColor(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("MAESTRO_CODEX_ARGS", argsPath)
	t.Setenv("PATH", fakeBin(t, "codex", `
printf '%s\n' "$@" > "$MAESTRO_CODEX_ARGS"
echo '{"type":"agent_message","text":"done"}'
`))
	ch, err := NewCodexAgent().Execute(t.Context(), "task with spaces", Options{Model: "gpt-test", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(t, ch)
	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{
		"exec",
		"--json",
		"--sandbox", "workspace-write",
		"--ignore-user-config",
		"--ephemeral",
		"--color", "never",
		"-c", `model_reasoning_effort="high"`,
		"--model", "gpt-test",
		"-",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codex argv = %#v, want %#v", got, want)
	}
}

func TestCodexExecuteReadOnlySkillProfileUsesExactStrictArgs(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("MAESTRO_CODEX_ARGS", argsPath)
	t.Setenv("PATH", fakeBin(t, "codex", `
printf '%s\n' "$@" > "$MAESTRO_CODEX_ARGS"
echo '{"type":"agent_message","text":"done"}'
`))
	ch, err := NewCodexAgent().Execute(t.Context(), "skill envelope", Options{
		Model: "gpt-test", ReasoningEffort: "high", ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(t, ch)
	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{
		"exec",
		"--json",
		"--sandbox", "read-only",
		"--ignore-user-config",
		"--ephemeral",
		"--color", "never",
		"--ignore-rules",
		"-c", `model_reasoning_effort="high"`,
		"--model", "gpt-test",
		"-",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codex read-only argv = %#v, want %#v", got, want)
	}
}

func TestCodexExecuteTransportsPromptOnlyThroughStdin(t *testing.T) {
	large := strings.Repeat("source line with unicode Δ and spaces\n", 5_000)
	if len(large) <= 128<<10 {
		t.Fatalf("large prompt fixture = %d bytes, want > 128 KiB", len(large))
	}
	tests := []struct {
		name     string
		prompt   string
		readOnly bool
	}{
		{name: "large", prompt: large},
		{name: "empty", prompt: "", readOnly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captureDir := t.TempDir()
			argsPath := filepath.Join(captureDir, "args")
			stdinPath := filepath.Join(captureDir, "stdin")
			t.Setenv("MAESTRO_CODEX_ARGS", argsPath)
			t.Setenv("MAESTRO_CODEX_STDIN", stdinPath)
			t.Setenv("PATH", fakeBin(t, "codex", `
printf '%s\0' "$@" > "$MAESTRO_CODEX_ARGS"
cat > "$MAESTRO_CODEX_STDIN"
printf '%s\n' '{"type":"agent_message","text":"done"}'
printf '%s\n' '{"type":"turn.completed"}'
`))

			ch, err := NewCodexAgent().Execute(t.Context(), tt.prompt, Options{
				Model: "gpt-5.6-luna", ReasoningEffort: "high", ReadOnly: tt.readOnly,
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			events := collectAgentEvents(t, ch)
			if len(events) < 2 || events[len(events)-1].Type != agentcore.EvDone {
				t.Fatalf("Codex events = %+v", events)
			}
			stdin, err := os.ReadFile(stdinPath)
			if err != nil {
				t.Fatalf("read captured stdin: %v", err)
			}
			if !bytes.Equal(stdin, []byte(tt.prompt)) {
				t.Fatalf("Codex stdin differs: got %d bytes, want %d", len(stdin), len(tt.prompt))
			}
			rawArgs, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatalf("read captured argv: %v", err)
			}
			parts := bytes.Split(bytes.TrimSuffix(rawArgs, []byte{0}), []byte{0})
			got := make([]string, len(parts))
			for i, part := range parts {
				got[i] = string(part)
			}
			want := []string{
				"exec", "--json", "--sandbox", "workspace-write",
				"--ignore-user-config", "--ephemeral", "--color", "never",
				"-c", `model_reasoning_effort="high"`, "--model", "gpt-5.6-luna", "-",
			}
			if tt.readOnly {
				want[3] = "read-only"
				want = append(want[:8], append([]string{"--ignore-rules"}, want[8:]...)...)
			}
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("Codex argv = %#v, want %#v", got, want)
			}
			for _, arg := range got {
				if arg != "-" && tt.prompt != "" && strings.Contains(tt.prompt, arg) {
					t.Fatalf("prompt data appeared in argv element %q", arg)
				}
			}
		})
	}
}

func TestNonCodexAgentsRejectReadOnlyBeforeLaunch(t *testing.T) {
	for _, candidate := range []Agent{
		NewClaudeAgent(), NewCursorAgent(), NewOpenCodeAgent(), NewGrokAgent(), NewKimiAgent(),
	} {
		t.Run(candidate.Name(), func(t *testing.T) {
			if SupportsReadOnly(candidate) {
				t.Fatalf("%s unexpectedly advertises read-only", candidate.Name())
			}
			if _, err := candidate.Execute(t.Context(), "task", Options{ReadOnly: true}); err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("Execute error = %v, want fail-closed read-only refusal", err)
			}
		})
	}
}

func TestCodexExecuteRejectsUnsupportedReasoningBeforeLaunch(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "codex", "exit 99\n"))
	if _, err := NewCodexAgent().Execute(t.Context(), "task", Options{ReasoningEffort: "max"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Execute error = %v, want unsupported reasoning", err)
	}
}

func TestCodexExecuteFailure(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "codex", "exit 2\n"))
	a := NewCodexAgent()
	ch, err := a.Execute(context.Background(), "task", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := collectAgentEvents(t, ch)
	last := evs[len(evs)-1]
	if last.Type != agentcore.EvError {
		t.Errorf("last event = %s, want error", last.Type)
	}
}

func TestClaudeExecuteBlob(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "claude", `echo '{"result":"implemented","session_id":"s1","num_turns":1}'`+"\n"))
	a := NewClaudeAgent()
	ch, err := a.Execute(context.Background(), "task", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := collectAgentEvents(t, ch)
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Type != agentcore.EvTextDelta || evs[0].Content.(agentcore.TextDelta).Text != "implemented" {
		t.Errorf("event 0 = %+v", evs[0])
	}
	if evs[1].Type != agentcore.EvDone {
		t.Errorf("event 1 = %s", evs[1].Type)
	}
}

func TestClaudeExecuteError(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "claude", `echo '{"result":"nope","is_error":true}'`+"\n"))
	a := NewClaudeAgent()
	ch, err := a.Execute(context.Background(), "task", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := collectAgentEvents(t, ch)
	if len(evs) != 2 || evs[0].Type != agentcore.EvError {
		t.Errorf("events = %+v", evs)
	}
	if !strings.Contains(evs[0].Content.(agentcore.StreamError).Message, "nope") {
		t.Errorf("error = %+v", evs[0].Content)
	}
}

func TestOpenCodeExecuteStreams(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "opencode", "echo line1\necho line2\n"))
	a := NewOpenCodeAgent()
	ch, err := a.Execute(context.Background(), "task", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := collectAgentEvents(t, ch)
	if len(evs) != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Content.(agentcore.TextDelta).Text != "line1" {
		t.Errorf("event 0 = %+v", evs[0])
	}
}

func TestCursorExecute(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "agent", `echo '{"result":"done","is_error":false}'`+"\n"))
	a := NewCursorAgent()
	ch, err := a.Execute(context.Background(), "task", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := collectAgentEvents(t, ch)
	if len(evs) != 2 || evs[0].Content.(agentcore.TextDelta).Text != "done" {
		t.Errorf("events = %+v", evs)
	}
}
