package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/colorprofile"
	"github.com/muesli/termenv"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/mcp"
	"github.com/bryann2k/maestro/internal/spec"
)

func runCLI(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	return runCLIWithState(t, dir, t.TempDir(), args...)
}

func runCLIWithState(t *testing.T, dir, stateDir string, args ...string) (string, int) {
	t.Helper()
	t.Setenv("MAESTRO_SESSIONS_DIR", filepath.Join(stateDir, "sessions"))
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(stateDir, "skills"))
	t.Setenv("HOME", filepath.Join(stateDir, "home"))
	// Keep CLI tests hermetic: a developer's global provider configuration
	// must not turn an offline pipeline test into a real network request.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateDir, "config"))
	var out bytes.Buffer
	err := run(append([]string{"--dir", dir}, args...), &out, &out)
	if err == nil {
		return out.String(), 0
	}
	fmt.Fprintf(&out, "maestro: %v\n", err) // main() prints the error to stderr
	var ee *exitError
	if asErr(err, &ee) {
		return out.String(), ee.code
	}
	return out.String(), 1 // plain errors exit 1 (see main)
}

func asErr(err error, target **exitError) bool {
	for err != nil {
		if e, ok := err.(*exitError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestVersion(t *testing.T) {
	out, code := runCLI(t, t.TempDir(), "version")
	if code != 0 || !strings.Contains(out, "maestro "+effectiveVersion()) {
		t.Errorf("version = %q, code %d", out, code)
	}
}

func TestEffectiveVersionPrefersReleaseStamp(t *testing.T) {
	previous := version
	version = "v9.8.7"
	t.Cleanup(func() { version = previous })
	if got := effectiveVersion(); got != "v9.8.7" {
		t.Fatalf("effectiveVersion = %q", got)
	}
}

func TestGlobalVersionAndHelpFlags(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		out, code := runCLI(t, t.TempDir(), "--version")
		if code != 0 || !strings.Contains(out, "maestro "+effectiveVersion()) {
			t.Fatalf("--version = %q, code %d", out, code)
		}
	})
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			out, code := runCLI(t, t.TempDir(), flag)
			if code != 0 || !strings.Contains(out, "Usage:") {
				t.Fatalf("%s = %q, code %d", flag, out, code)
			}
		})
	}
}

func TestProjectDirCanonicalizesGitSubdirectoryToTopLevel(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	nested := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectDir(options{dir: nested}); got != want {
		t.Fatalf("projectDir = %q, want Git top-level %q", got, want)
	}
}

func TestHelp(t *testing.T) {
	out, code := runCLI(t, t.TempDir(), "help")
	if code != 0 || !strings.Contains(out, "propose") {
		t.Errorf("help = %q, code %d", out, code)
	}
	if strings.Contains(out, "OAuth flows") {
		t.Errorf("help advertises OAuth as usable before runtime support: %q", out)
	}
	for _, command := range []string{"/bootstrap", "/adopt", "/onboard", "/update", "maestro rename <title>", "maestro resume [id]", "maestro git list", "maestro learn status|next|done|later", "maestro skills list", "maestro skills run <id>", "maestro mcp list|status", "maestro mcp tools [server|all]", "maestro mcp reconnect <server|all>"} {
		if !strings.Contains(out, command) {
			t.Errorf("help missing %q: %q", command, out)
		}
	}
	if strings.Contains(out, "/boostrap") {
		t.Errorf("help exposes the compatibility typo alias: %q", out)
	}
}

func TestMCPCommandsHeadlessLifecycleAndSecretFreeStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "lookup",
				"description": "bounded lookup",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": result,
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	stateDir := t.TempDir()
	const secret = "headless-mcp-secret"
	rc := fmt.Sprintf("mcp add records --type http --url %q --header Authorization %q\n", server.URL, "Bearer "+secret)
	if err := os.WriteFile(filepath.Join(dir, ".maestrorc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := runCLIWithState(t, dir, stateDir, "mcp", "status")
	if code != 0 || !strings.Contains(out, "records · http · 0 tool(s) · disconnected") {
		t.Fatalf("mcp status = %q, code %d", out, code)
	}
	if strings.Contains(out, server.URL) || strings.Contains(out, secret) {
		t.Fatalf("mcp status leaked configuration: %q", out)
	}

	out, code = runCLIWithState(t, dir, stateDir, "mcp", "reconnect", "records")
	if code != 0 || !strings.Contains(out, "mcp: reconnected records") {
		t.Fatalf("mcp reconnect = %q, code %d", out, code)
	}
	out, code = runCLIWithState(t, dir, stateDir, "mcp", "tools", "records")
	if code != 0 || !strings.Contains(out, "mcp: no connected tools") {
		// Headless commands intentionally create a fresh runtime each time. This
		// verifies that disconnected integrations remain non-blocking; connected
		// catalog behavior is covered at the orchestrator/REPL integration layer.
		t.Fatalf("mcp tools fresh runtime = %q, code %d", out, code)
	}
	if strings.Contains(out, server.URL) || strings.Contains(out, secret) {
		t.Fatalf("mcp tools leaked configuration: %q", out)
	}
}

func TestSkillsCommandsHeadlessLifecycle(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "audit")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: audit\ndescription: Audit code\n---\nReview safely.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := runCLIWithState(t, dir, stateDir, "skills", "list")
	if code != 0 || !strings.Contains(out, "project:audit · enabled") {
		t.Fatalf("skills list = %q, code %d", out, code)
	}
	out, code = runCLIWithState(t, dir, stateDir, "skills", "show", "audit")
	if code != 0 || !strings.Contains(out, "Review safely") || !strings.Contains(out, "skill: project:audit") {
		t.Fatalf("skills show = %q, code %d", out, code)
	}
	out, code = runCLIWithState(t, dir, stateDir, "skills", "disable", "audit", "--scope=session")
	if code != 0 || !strings.Contains(out, "disabled for session") {
		t.Fatalf("skills disable = %q, code %d", out, code)
	}
	out, code = runCLIWithState(t, dir, stateDir, "skills", "run", "audit")
	if code != 1 || !strings.Contains(out, "disabled") {
		t.Fatalf("disabled skills run = %q, code %d", out, code)
	}
}

func TestHeadlessProjectQuestionnairesRedirectWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		command string
		slash   string
	}{
		{command: "bootstrap", slash: "/bootstrap"},
		{command: "boostrap", slash: "/bootstrap"},
		{command: "adopt", slash: "/adopt"},
		{command: "onboard", slash: "/adopt"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			dir := t.TempDir()
			out, code := runCLI(t, dir, tc.command)
			if code != 2 || !strings.Contains(out, "maestro tui") || !strings.Contains(out, tc.slash) {
				t.Fatalf("%s = %q, code %d", tc.command, out, code)
			}
			if _, err := os.Stat(filepath.Join(dir, "MAESTRO.md")); !os.IsNotExist(err) {
				t.Fatalf("%s unexpectedly wrote MAESTRO.md: %v", tc.command, err)
			}
		})
	}
}

func TestModelListFlagsWorkBeforeOrAfterSubcommand(t *testing.T) {
	dir := t.TempDir()
	rc := "provider add smoke-native --type openai-compat --base-url http://127.0.0.1:1/v1\n" +
		"model add smoke-native/smoke-model --name 'Smoke Native' --context-window 32768 --can-reason\n"
	if err := os.WriteFile(filepath.Join(dir, "maestrorc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	orders := [][]string{
		{"model", "list", "--json", "--provider", "smoke-native"},
		{"model", "--provider=smoke-native", "--json", "list"},
	}
	for _, args := range orders {
		out, code := runCLI(t, dir, args...)
		if code != 0 {
			t.Fatalf("%v = %q, code %d", args, out, code)
		}
		var models []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal([]byte(out), &models); err != nil {
			t.Fatalf("%v returned non-JSON %q: %v", args, out, err)
		}
		if len(models) != 1 || models[0].ID != "smoke-model" || models[0].Provider != "smoke-native" {
			t.Fatalf("%v models = %+v", args, models)
		}
	}
}

func TestProviderAddFlagsWorkBeforeOrAfterPositionals(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "documented positional first",
			args: []string{"provider", "add", "custom-one", "--type", "openai-compat", "--base-url", "https://one.test/v1"},
		},
		{
			name: "flags first",
			args: []string{"provider", "--type=openai-compat", "--base-url=https://two.test/v1", "add", "custom-two"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			out, code := runCLI(t, dir, tc.args...)
			if code != 0 {
				t.Fatalf("provider add = %q, code %d", out, code)
			}
			data, err := os.ReadFile(filepath.Join(dir, "maestrorc"))
			if err != nil {
				t.Fatalf("read maestrorc: %v", err)
			}
			for _, want := range []string{"provider add custom-", "--type openai-compat", "--base-url \"https://"} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("maestrorc missing %q: %q", want, data)
				}
			}
		})
	}
}

func TestInterspersedModelAndProviderFlagsFailCleanly(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown model flag", args: []string{"model", "list", "--wat"}, want: "unknown flag --wat"},
		{name: "missing model value", args: []string{"model", "list", "--provider"}, want: "--provider requires a value"},
		{name: "invalid model boolean", args: []string{"model", "list", "--json=perhaps"}, want: "--json requires true or false"},
		{name: "unknown provider flag", args: []string{"provider", "add", "bad", "--wat", "x"}, want: "unknown flag --wat"},
		{name: "missing provider value", args: []string{"provider", "add", "bad", "--type"}, want: "--type requires a value"},
		{name: "extra model positional", args: []string{"model", "list", "accidental"}, want: `unexpected positional argument "accidental"`},
		{name: "extra provider list positional", args: []string{"provider", "list", "accidental"}, want: `unexpected positional argument "accidental"`},
		{name: "extra provider add positional", args: []string{"provider", "add", "bad", "accidental", "--base-url", "https://bad.test/v1"}, want: `unexpected positional argument "accidental"`},
		{name: "extra provider remove positional", args: []string{"provider", "remove", "bad", "accidental"}, want: `unexpected positional argument "accidental"`},
		{name: "missing provider base URL", args: []string{"provider", "add", "bad"}, want: "--base-url is required"},
		{name: "add flag on provider list", args: []string{"provider", "list", "--type", "openai-compat"}, want: "--type is only valid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			out, code := runCLI(t, dir, tc.args...)
			if code != 2 || !strings.Contains(out, tc.want) {
				t.Fatalf("%v = %q, code %d; want %q", tc.args, out, code, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "maestrorc")); !os.IsNotExist(err) {
				t.Fatalf("invalid command wrote maestrorc: %v", err)
			}
		})
	}
}

func TestProviderRemoveWithAddOnlyFlagDoesNotEditConfig(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	out, code := runCLIWithState(t, dir, stateDir,
		"provider", "add", "custom", "--type", "openai-compat", "--base-url", "https://custom.test/v1")
	if code != 0 {
		t.Fatalf("provider add = %q, code %d", out, code)
	}
	out, code = runCLIWithState(t, dir, stateDir,
		"provider", "remove", "custom", "--base-url", "https://ignored.test/v1")
	if code != 2 || !strings.Contains(out, "--base-url is only valid") {
		t.Fatalf("malformed provider remove = %q, code %d", out, code)
	}
	data, err := os.ReadFile(filepath.Join(dir, "maestrorc"))
	if err != nil {
		t.Fatalf("read maestrorc: %v", err)
	}
	if !strings.Contains(string(data), "provider add custom ") {
		t.Fatalf("malformed remove edited provider config: %q", data)
	}
}

func TestProviderAddRejectsInvalidIdentifiersWithoutWriting(t *testing.T) {
	invalid := []string{
		"has space",
		"line\noption injected true",
		"has/slash",
		"-leading",
		strings.Repeat("a", 65),
	}
	for _, id := range invalid {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			dir := t.TempDir()
			out, code := runCLI(t, dir, "provider", "add", id, "--base-url", "https://invalid.test/v1")
			if code != 2 || (!strings.Contains(out, "provider name") && !strings.Contains(out, "unknown flag")) {
				t.Fatalf("invalid provider %q = %q, code %d", id, out, code)
			}
			if _, err := os.Stat(filepath.Join(dir, "maestrorc")); !os.IsNotExist(err) {
				t.Fatalf("invalid provider %q wrote maestrorc: %v", id, err)
			}
		})
	}
}

func TestTerminalDiagnosticAndStreamProjection(t *testing.T) {
	t.Run("diagnostic", func(t *testing.T) {
		raw := string([]byte("failure\r\n\t\x1b]52;c;owned\x07\xe2\x80\xae\xff"))
		var out bytes.Buffer
		writeTerminalDiagnostic(&out, "maestro: ", raw)
		got := out.String()
		if !utf8.ValidString(got) {
			t.Fatalf("diagnostic is invalid UTF-8: %q", got)
		}
		if strings.Count(got, "\n") != 1 || strings.ContainsAny(got, "\r\t\x07") || strings.Contains(got, "\x1b]52") || strings.Contains(got, "\u202e") {
			t.Fatalf("diagnostic retained terminal controls: %q", got)
		}
		long := terminalSafeDiagnostic(strings.Repeat("x", maxCLIDiagnosticRunes*2))
		if len([]rune(long)) > maxCLIDiagnosticRunes || !strings.Contains(long, "diagnostic truncated") {
			t.Fatalf("diagnostic bound not enforced: %d runes", len([]rune(long)))
		}
	})

	t.Run("stream", func(t *testing.T) {
		raw := string([]byte("first\tcolumn\n\x1b]52;c;owned\x07\x1b[2J\xe2\x81\xa6\xff"))
		event := agentcore.StreamEvent{Type: agentcore.EvTextDelta, Content: agentcore.TextDelta{Text: raw}}
		var out bytes.Buffer
		projection := newTerminalStreamProjection(&out)
		printStreamEvent(projection, event)
		got := out.String()
		if event.Content.(agentcore.TextDelta).Text != raw {
			t.Fatal("stream projection mutated the canonical event")
		}
		if !utf8.ValidString(got) || !strings.Contains(got, "first\tcolumn\n") {
			t.Fatalf("stream did not preserve valid UTF-8 layout: %q", got)
		}
		if strings.ContainsAny(got, "\x07") || strings.Contains(got, "\x1b]52") || strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\u2066") {
			t.Fatalf("stream retained terminal controls: %q", got)
		}

		out.Reset()
		projection = newTerminalStreamProjection(&out)
		projection.WriteString(strings.Repeat("x", maxCLIStreamLineRunes+32) + "\nnext")
		if got := out.String(); !strings.Contains(got, "line truncated") || !strings.HasSuffix(got, "\nnext") {
			t.Fatalf("stream line bound/layout = %q", got[len(got)-min(len(got), 100):])
		}
		out.Reset()
		projection = newTerminalStreamProjection(&out)
		projection.totalRunes = maxCLIStreamRunes - 1
		projection.WriteString("overflow")
		if !strings.Contains(out.String(), "stream output truncated") {
			t.Fatalf("stream cumulative bound not enforced: %q", out.String())
		}
	})
}

func TestMouseCellMotionIsDefault(t *testing.T) {
	t.Setenv("MAESTRO_MOUSE", "")
	if got := mouseMode(); got != "cell" {
		t.Fatalf("default mouse mode = %q, want cell", got)
	}
	t.Setenv("MAESTRO_MOUSE", "cell")
	if got := mouseMode(); got != "cell" {
		t.Fatalf("explicit mouse mode = %q, want cell", got)
	}
	t.Setenv("MAESTRO_MOUSE", "none")
	if got := mouseMode(); got != "none" {
		t.Fatalf("disabled mouse mode = %q, want none", got)
	}
	t.Setenv("MAESTRO_MOUSE", "unexpected")
	if got := mouseMode(); got != "none" {
		t.Fatalf("unknown mouse mode = %q, want safe fallback", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	_, code := runCLI(t, t.TempDir(), "frobnicate")
	if code != 2 {
		t.Errorf("unknown command code = %d, want 2", code)
	}
}

func TestBadEngine(t *testing.T) {
	_, code := runCLI(t, t.TempDir(), "--engine", "wat", "version")
	if code != 2 {
		t.Errorf("bad engine code = %d, want 2", code)
	}
}

func TestSpecListEmpty(t *testing.T) {
	out, code := runCLI(t, t.TempDir(), "spec", "list")
	if code != 0 || !strings.Contains(out, "No specs yet") {
		t.Errorf("spec list = %q, code %d", out, code)
	}
}

func TestProposeRequiresMessage(t *testing.T) {
	_, code := runCLI(t, t.TempDir(), "propose")
	if code != 2 {
		t.Errorf("propose without -m code = %d, want 2", code)
	}
}

func TestProposeThenAcceptPipeline(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	gitInit(t, dir)

	out, code := runCLIWithState(t, dir, stateDir, "propose", "-m", "Add a Postgres-backed API")
	if code != 0 || !strings.Contains(out, "Spec proposal ready") {
		t.Fatalf("propose = %q, code %d", out, code)
	}

	out, code = runCLIWithState(t, dir, stateDir, "accept", "--branch", "feat-api")
	if code != 0 || !strings.Contains(out, "Spec \"add-a-postgres-backed-api\" created") {
		t.Fatalf("accept = %q, code %d", out, code)
	}
	for _, f := range []string{"spec.md", "design.md", "tasks.md"} {
		if _, err := os.Stat(filepath.Join(dir, "specs", "add-a-postgres-backed-api", f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

func TestAcceptWithoutProposal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	out, code := runCLI(t, dir, "accept")
	if code != 1 || !strings.Contains(out, "no proposal in flight") {
		t.Errorf("accept without propose = %q, code %d", out, code)
	}
}

func TestSpecShowRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := specStore(options{dir: dir})
	s := &spec.Spec{ID: "demo", Title: "Demo", Status: spec.StatusProposal, Body: "Goal: demo.\n"}
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, code := runCLI(t, dir, "spec", "show", "demo")
	if code != 0 || !strings.Contains(out, "Goal: demo") || !strings.Contains(out, "id: demo") {
		t.Errorf("spec show = %q, code %d", out, code)
	}
}

func TestSpecShowMissing(t *testing.T) {
	_, code := runCLI(t, t.TempDir(), "spec", "show", "ghost")
	if code != 1 {
		t.Errorf("spec show missing code = %d, want 1", code)
	}
}

func TestLearnWithoutArg(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	_, code := runCLI(t, dir, "learn")
	if code != 1 {
		t.Errorf("learn without path code = %d, want 1", code)
	}
}

func TestSessionRenameAndResumeByID(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	gitInit(t, dir)

	out, code := runCLIWithState(t, dir, stateDir, "rename", "Headless", "premium", "session")
	if code != 0 || !strings.Contains(out, "session renamed: Headless premium session") {
		t.Fatalf("rename = %q, code %d", out, code)
	}

	out, code = runCLIWithState(t, dir, stateDir, "resume")
	if code != 0 || !strings.Contains(out, `title "Headless premium session"`) {
		t.Fatalf("current resume = %q, code %d", out, code)
	}
	fields := strings.Fields(out)
	if len(fields) < 2 || fields[0] != "Session" {
		t.Fatalf("could not read session id from %q", out)
	}
	id := fields[1]

	out, code = runCLIWithState(t, dir, stateDir, "resume", id)
	if code != 0 || !strings.Contains(out, "Session "+id) || !strings.Contains(out, `title "Headless premium session"`) {
		t.Fatalf("resume by id = %q, code %d", out, code)
	}
}

func TestCoachCommandsHeadlessLifecycle(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	gitInit(t, dir)
	t.Setenv("MAESTRO_LEARN_DIR", filepath.Join(stateDir, "learn"))

	steps := []struct {
		args []string
		want string
	}{
		{args: []string{"learn", "guided"}, want: "coach: mode guided"},
		{args: []string{"learn", "next"}, want: "coach lesson "},
		{args: []string{"learn", "done"}, want: "coach: completed "},
		{args: []string{"learn", "status"}, want: "coach: mode guided · 1 completed lesson(s)"},
		{args: []string{"learn", "later"}, want: "coach: snoozed for 24h0m0s"},
		{args: []string{"learn", "off"}, want: "coach: mode off"},
	}
	for _, step := range steps {
		out, code := runCLIWithState(t, dir, stateDir, step.args...)
		if code != 0 || !strings.Contains(out, step.want) {
			t.Fatalf("maestro %s = %q, code %d; want %q", strings.Join(step.args, " "), out, code, step.want)
		}
	}
}

func TestGitWorkspaceCommandsHeadless(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(stateDir, "home"))
	gitInit(t, dir)

	out, code := runCLIWithState(t, dir, stateDir, "git", "list")
	if code != 0 || !strings.Contains(out, "main") || !strings.Contains(out, dir) {
		t.Fatalf("git list = %q, code %d", out, code)
	}

	out, code = runCLIWithState(t, dir, stateDir, "git", "create", "feature/headless")
	if code != 0 || !strings.Contains(out, "workspace: feature/headless") {
		t.Fatalf("git create = %q, code %d", out, code)
	}

	out, code = runCLIWithState(t, dir, stateDir, "git", "select", dir)
	if code != 0 || !strings.Contains(out, "workspace: main") {
		t.Fatalf("git select = %q, code %d", out, code)
	}
}

func TestParseGlobal(t *testing.T) {
	tests := []struct {
		args   []string
		engine string
		sub    []string
		ok     bool
	}{
		{[]string{}, "native", []string{""}, true},
		{[]string{"chat"}, "native", []string{"chat"}, true},
		{[]string{"--engine", "legacy", "build"}, "legacy", []string{"build"}, true},
		{[]string{"--engine", "subscription", "build"}, "legacy", []string{"build"}, true},
		{[]string{"--engine=subscription", "review"}, "legacy", []string{"review"}, true},
		{[]string{"--engine=native", "spec", "list"}, "native", []string{"spec", "list"}, true},
		{[]string{"--dir", "/tmp/x", "spec", "list"}, "native", []string{"spec", "list"}, true},
		{[]string{"--bogus"}, "", nil, false},
		{[]string{"--engine"}, "", nil, false},
	}
	for _, tt := range tests {
		opts, sub, err := parseGlobal(tt.args)
		if tt.ok && err != nil {
			t.Errorf("parseGlobal(%v) = %v", tt.args, err)
			continue
		}
		if !tt.ok && err == nil {
			t.Errorf("parseGlobal(%v) should fail", tt.args)
			continue
		}
		if tt.ok && (opts.engine != tt.engine || strings.Join(sub, " ") != strings.Join(tt.sub, " ")) {
			t.Errorf("parseGlobal(%v) = engine %q sub %v", tt.args, opts.engine, sub)
		}
	}
}

func TestDefaultCommandIsTUI(t *testing.T) {
	if got := commandName([]string{""}); got != "tui" {
		t.Fatalf("empty command = %q, want tui", got)
	}
	if got := commandName([]string{"chat"}); got != "chat" {
		t.Fatalf("explicit command = %q, want chat", got)
	}
}

func TestColorProfileSelection(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want termenv.Profile
	}{
		{"override-none", map[string]string{"MAESTRO_COLOR": "none"}, termenv.Ascii},
		{"override-256", map[string]string{"MAESTRO_COLOR": "ansi256"}, termenv.ANSI256},
		{"override-truecolor", map[string]string{"MAESTRO_COLOR": "truecolor"}, termenv.TrueColor},
		{"no-color-env", map[string]string{"NO_COLOR": "1"}, termenv.Ascii},
		{"dumb-term", map[string]string{"TERM": "dumb"}, termenv.Ascii},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k := range c.env {
				t.Setenv(k, c.env[k])
			}
			if got := colorProfile(); got != c.want {
				t.Fatalf("colorProfile() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestOutputColorProfileFiltersLipglossANSI(t *testing.T) {
	var out bytes.Buffer
	w := &colorprofile.Writer{Forward: &out, Profile: outputProfile(termenv.Ascii)}
	if _, err := w.Write([]byte("\x1b[38;2;255;0;0mhello\x1b[0m")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "hello") || strings.Contains(got, "38;2") {
		t.Fatalf("filtered output = %q, expected text without truecolor", got)
	}
}

// gitInit initializes a git repo with a first commit (needed by accept's
// branch options and archive).
func gitInit(t *testing.T, dir string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@maestro.local")
	git("config", "user.name", "Maestro Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	git("add", "README.md")
	git("commit", "-m", "initial commit")
}
