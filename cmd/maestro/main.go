// Command maestro is the Maestro entry point: the premium TUI by default and
// scriptable subcommands for the spec pipeline. This file stays thin — all
// logic lives in internal/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/colorprofile"
	"github.com/muesli/termenv"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	maestrogit "github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/mcp"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/proposals"
	"github.com/bryann2k/maestro/internal/repl"
	"github.com/bryann2k/maestro/internal/settings"
	"github.com/bryann2k/maestro/internal/spec"
	"github.com/bryann2k/maestro/internal/tui"
	"github.com/bryann2k/maestro/internal/vault"
)

// version is overridden by release builds through -ldflags. A plain `go
// build` deliberately reports "dev" instead of impersonating a tagged build.
var version = "dev"

// exitError carries a process exit code through the error chain.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func exitf(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			writeTerminalDiagnostic(os.Stderr, "maestro: ", ee.err.Error())
			os.Exit(ee.code)
		}
		writeTerminalDiagnostic(os.Stderr, "maestro: ", err.Error())
		os.Exit(1)
	}
}

type options struct {
	engine string // native | legacy internally; subscription is the public alias
	dir    string // project root
}

func run(args []string, out, errOut io.Writer) error {
	buildVersion := effectiveVersion()
	agentcore.Version = buildVersion
	mcp.Version = buildVersion
	opts, sub, err := parseGlobal(args)
	if err != nil {
		return exitf(2, "%v", err)
	}
	if opts.engine != "native" && opts.engine != "legacy" {
		return exitf(2, "--engine must be native or subscription (got %q)", opts.engine)
	}

	switch commandName(sub) {
	case "chat":
		return runChat(sub[1:], opts, out, errOut)
	case "tui":
		return runTUI(opts, out, errOut)
	case "spec":
		return runSpec(sub[1:], opts, out)
	case "propose":
		return runPropose(sub[1:], opts, out, errOut)
	case "bootstrap", "boostrap":
		return exitf(2, "bootstrap is a reviewed interactive questionnaire; run 'maestro tui', then use /bootstrap")
	case "onboard":
		return exitf(2, "onboard is a reviewed interactive repository questionnaire; run 'maestro tui', then use /onboard")
	case "accept", "validate", "answer", "build", "review", "fix", "docs", "archive", "resume",
		"rename", "git", "rewind", "remember", "reflect", "rules", "commit", "learn",
		"skills", "skill", "mcp", "provider", "model", "auth":
		return runPipeline(sub[0], sub[1:], opts, out, errOut)
	case "help", "-h", "--help":
		printUsage(out, buildVersion)
		return nil
	case "version", "--version":
		fmt.Fprintln(out, "maestro", buildVersion)
		return nil
	}

	return exitf(2, "unknown command %q — try 'maestro help'", sub[0])
}

// commandName keeps the default launch path explicit and testable without
// starting an interactive Bubble Tea program from a unit test.
func commandName(sub []string) string {
	if len(sub) == 0 || sub[0] == "" {
		return "tui"
	}
	return sub[0]
}

// parseGlobal splits --engine and --dir from the subcommand.
func parseGlobal(args []string) (options, []string, error) {
	opts := options{engine: "native"}
	rest := args
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		switch rest[0] {
		case "-h", "--help", "--version":
			return opts, []string{rest[0]}, nil
		case "--engine":
			if len(rest) < 2 {
				return opts, nil, errors.New("--engine requires a value (native|subscription)")
			}
			opts.engine = normalizeCLIEngine(rest[1])
			rest = rest[2:]
		case "--dir":
			if len(rest) < 2 {
				return opts, nil, errors.New("--dir requires a value")
			}
			opts.dir = rest[1]
			rest = rest[2:]
		case "--engine=native", "--engine=legacy", "--engine=subscription":
			opts.engine = normalizeCLIEngine(strings.TrimPrefix(rest[0], "--engine="))
			rest = rest[1:]
		default:
			return opts, nil, fmt.Errorf("unknown global flag %q", rest[0])
		}
	}
	if len(rest) == 0 {
		rest = []string{""}
	}
	return opts, rest, nil
}

func normalizeCLIEngine(engine string) string {
	if strings.EqualFold(strings.TrimSpace(engine), "subscription") {
		return "legacy"
	}
	return strings.ToLower(strings.TrimSpace(engine))
}

func projectDir(opts options) string {
	dir := opts.dir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "."
		}
		dir = cwd
	}
	if root, err := maestrogit.RepositoryRoot(context.Background(), dir); err == nil {
		return root
	}
	return dir
}

// keyStore resolves API keys: an environment variable <NAME>_API_KEY first,
// then the vault entry "key:<name>".
type keyStore struct {
	vault *vault.Vault
}

// Key implements agentcore.KeyStore.
func (k keyStore) Key(name string) (string, bool) {
	if env := os.Getenv(strings.ToUpper(name) + "_API_KEY"); env != "" {
		return env, true
	}
	if k.vault != nil {
		if v, ok := k.vault.Get("key:" + name); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// loadEnv loads config + vault for a project.
func loadEnv(dir string, errOut io.Writer) (*config.Config, *vault.Vault) {
	cfg, err := config.Load(context.Background(), dir)
	if err != nil {
		writeTerminalDiagnostic(errOut, "maestro: warning: ", err.Error())
	}
	var v *vault.Vault
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".maestro", "vault.json")
		if opened, err := vault.OpenAES(context.Background(), path, func(w string) {
			writeTerminalDiagnostic(errOut, "maestro: warning: ", w)
		}); err == nil {
			v = opened
		} else {
			writeTerminalDiagnostic(errOut, "maestro: warning: encrypted vault unavailable: ", err.Error())
		}
	}
	return cfg, v
}

// runChat starts the interactive REPL or, with -m, a one-shot chat. It remains
// available explicitly as `maestro chat`; the default entry point is the TUI.
func runChat(args []string, opts options, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	msg := fs.String("m", "", "one-shot message: send and exit")
	model := fs.String("model", "", "active model ID (default: resolved from maestrorc)")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return exitf(2, "chat: %v", err)
	}
	dir := projectDir(opts)
	cfg, v := loadEnv(dir, errOut)
	st, settingsPath := loadSettings()
	return repl.Run(context.Background(), repl.Options{
		Dir:          dir,
		In:           os.Stdin,
		Out:          out,
		Config:       cfg,
		Keys:         keyStore{vault: v},
		Model:        *model,
		Once:         *msg,
		Settings:     st,
		SettingsPath: settingsPath,
		ModelsDev:    newModelsDev(cfg),
	})
}

// sessionsDir returns the session store root: MAESTRO_SESSIONS_DIR when set,
// else ~/.maestro/sessions.
func sessionsDir() string {
	if d := os.Getenv("MAESTRO_SESSIONS_DIR"); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".maestro", "sessions")
	}
	return ""
}

// newOrchestrator builds the pipeline controller for CLI subcommands.
func newOrchestrator(opts options, out, errOut io.Writer) (*orchestrator.Orchestrator, error) {
	dir := projectDir(opts)
	cfg, v := loadEnv(dir, errOut)
	st, settingsPath := loadSettings()
	dev := newModelsDev(cfg)
	return orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir:   dir,
		SessionsDir:  sessionsDir(),
		Config:       cfg,
		Keys:         keyStore{vault: v},
		Settings:     st,
		SettingsPath: settingsPath,
		ModelsDev:    dev,
		Vault:        v,
		Model:        "",
		In:           os.Stdin,
		Out:          out,
	})
}

// newModelsDev builds the remote catalog client from config options
// (§10.2): option models-url, option provider-auto-update, and the
// MAESTRO_DISABLE_MODELS_FETCH env override.
func newModelsDev(cfg *config.Config) *agentcore.ModelsDev {
	if cfg == nil {
		return nil
	}
	opts := agentcore.ModelsDevOptions{}
	if u, ok := cfg.Options["models-url"]; ok && u != "" {
		opts.URL = u
	}
	if os.Getenv("MAESTRO_DISABLE_MODELS_FETCH") != "" {
		opts.Disabled = true
	} else if v, ok := cfg.Options["provider-auto-update"]; ok && v == "false" {
		opts.Disabled = true
	}
	if path, err := agentcore.DefaultCachePath(); err == nil {
		opts.CachePath = path
	}
	return agentcore.NewModelsDev(opts)
}

// loadSettings reads user settings and returns the file path for
// persistence (engine choices per role, §5.2).
func loadSettings() (settings.Settings, string) {
	path, err := settings.DefaultPath()
	if err != nil {
		return settings.Defaults(), ""
	}
	st, err := settings.Load(context.Background(), path)
	if err != nil {
		return settings.Defaults(), path
	}
	return st, path
}

// runPipeline dispatches one pipeline subcommand and drains the stream.
func runPipeline(cmd string, args []string, opts options, out, errOut io.Writer) error {
	orch, err := newOrchestrator(opts, out, errOut)
	if err != nil {
		return err
	}
	defer func() { _ = orch.Close() }()
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var m, branch, agent, model, id, path, title, format, typeFlag, baseURL, apiKey, provider, scope string
	var worktree, yes, merge, isolated, deep, rewindCode, rewindConv, asJSON bool
	providerFlagUsed := map[string]bool{}
	if cmd == "skills" || cmd == "skill" {
		var extractErr error
		args, scope, extractErr = extractValueFlag(args, "scope")
		if extractErr != nil {
			return exitf(2, "%s: %v", cmd, extractErr)
		}
	}
	// The standard flag package stops at the first positional token, while
	// Maestro documents subcommand-first forms such as
	// `model list --json` and `provider add NAME --type ...`. Extract only
	// the flags owned by those commands so both orders work without accepting
	// unknown options or guessing whether a positional is a flag value.
	if cmd == "model" {
		var extractErr error
		args, provider, extractErr = extractValueFlag(args, "provider")
		if extractErr == nil {
			args, asJSON, extractErr = extractBoolFlag(args, "json")
		}
		if extractErr != nil {
			return exitf(2, "%s: %v", cmd, extractErr)
		}
		if err := rejectCommandFlags(args); err != nil {
			return exitf(2, "%s: %v", cmd, err)
		}
	}
	if cmd == "provider" {
		for _, name := range []string{"type", "base-url", "api-key"} {
			providerFlagUsed[name] = namedFlagPresent(args, name)
		}
		var extractErr error
		args, typeFlag, extractErr = extractValueFlag(args, "type")
		if extractErr == nil {
			args, baseURL, extractErr = extractValueFlag(args, "base-url")
		}
		if extractErr == nil {
			args, apiKey, extractErr = extractValueFlag(args, "api-key")
		}
		if extractErr != nil {
			return exitf(2, "%s: %v", cmd, extractErr)
		}
		if err := rejectCommandFlags(args); err != nil {
			return exitf(2, "%s: %v", cmd, err)
		}
	}
	switch cmd {
	case "accept":
		fs.StringVar(&branch, "branch", "", "create a branch with this name")
		fs.BoolVar(&worktree, "worktree", false, "create a git worktree (name from -branch or auto)")
		fs.StringVar(&branch, "name", "", "branch/worktree name")
	case "rewind":
		fs.StringVar(&id, "id", "", "checkpoint id")
		fs.BoolVar(&rewindCode, "code", false, "rewind the code")
		fs.BoolVar(&rewindConv, "conv", false, "rewind the conversation")
	case "learn":
		fs.BoolVar(&deep, "deep", false, "line-by-line depth")
		fs.StringVar(&path, "path", "", "file to explain")
	case "skills", "skill":
		fs.StringVar(&scope, "scope", scope, "enablement scope (project or session)")
	case "rename":
		fs.StringVar(&title, "title", "", "new session title")
	case "resume":
		fs.StringVar(&id, "id", "", "saved session id")
	case "rules":
		fs.StringVar(&format, "format", "", "export format (mdc|clinerules|AGENTS.md)")
	case "commit":
		fs.BoolVar(&yes, "yes", false, "apply the plan")
	case "provider":
		fs.StringVar(&typeFlag, "type", typeFlag, "provider type (openai, openai-compat, anthropic, ollama, ...)")
		fs.StringVar(&baseURL, "base-url", baseURL, "API base URL")
		fs.StringVar(&apiKey, "api-key", apiKey, "API key (stored only in the encrypted vault; prefer `maestro auth login` to avoid shell history)")
	case "model":
		fs.StringVar(&provider, "provider", provider, "filter by provider")
		fs.BoolVar(&asJSON, "json", asJSON, "output JSON")
	case "build":
		fs.StringVar(&agent, "agent", "", "subscription agent name (codex, claude, cursor, opencode)")
		fs.StringVar(&model, "model", "", "model override")
		fs.BoolVar(&isolated, "isolated", false, "run in a dedicated git worktree")
	case "archive":
		fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
		fs.BoolVar(&merge, "merge", false, "merge the branch back into main")
	}
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return exitf(2, "%s: %v", cmd, err)
	}
	if err := validateProviderModelPositionals(cmd, fs.Args()); err != nil {
		return exitf(2, "%v", err)
	}
	if cmd == "provider" {
		if fs.Args()[0] != "add" {
			for _, name := range []string{"type", "base-url", "api-key"} {
				if providerFlagUsed[name] {
					return exitf(2, "provider %s: --%s is only valid with 'provider add'", fs.Args()[0], name)
				}
			}
		}
		switch fs.Args()[0] {
		case "add":
			if typeFlag == "" {
				typeFlag = "openai-compat"
			}
			if err := config.ValidateProvider(config.Provider{
				Name: fs.Args()[1], Type: typeFlag, BaseURL: baseURL,
			}); err != nil {
				return exitf(2, "provider add: %v", err)
			}
		case "remove":
			if err := config.ValidateProviderID(fs.Args()[1]); err != nil {
				return exitf(2, "provider remove: %v", err)
			}
		}
	}
	flags := map[string]string{
		"engine":   opts.engine,
		"m":        m,
		"branch":   branch,
		"name":     branch,
		"agent":    agent,
		"model":    model,
		"yes":      fmt.Sprintf("%v", yes),
		"merge":    fmt.Sprintf("%v", merge),
		"worktree": fmt.Sprintf("%v", worktree),
		"isolated": fmt.Sprintf("%v", isolated),
		"id":       id,
		"code":     fmt.Sprintf("%v", rewindCode),
		"conv":     fmt.Sprintf("%v", rewindConv),
		"deep":     fmt.Sprintf("%v", deep),
		"path":     path,
		"title":    title,
		"format":   format,
		"type":     typeFlag,
		"base-url": baseURL,
		"api-key":  apiKey,
		"provider": provider,
		"scope":    scope,
		"json":     fmt.Sprintf("%v", asJSON),
	}
	ctx := context.Background()
	err = withStreamDrain(ctx, orch, out, func() error {
		return orch.Dispatch(ctx, orchestrator.Command{Cmd: cmd, Args: fs.Args(), Flags: flags})
	})
	if err != nil {
		return err
	}
	return nil
}

func namedFlagPresent(args []string, name string) bool {
	long := "--" + name
	short := "-" + name
	for _, arg := range args {
		if arg == long || arg == short || strings.HasPrefix(arg, long+"=") || strings.HasPrefix(arg, short+"=") {
			return true
		}
	}
	return false
}

func validateProviderModelPositionals(command string, args []string) error {
	switch command {
	case "model":
		if len(args) == 1 && args[0] == "list" {
			return nil
		}
		if len(args) > 1 && args[0] == "list" {
			return fmt.Errorf("model list: unexpected positional argument %q", args[1])
		}
		return errors.New("model: usage: maestro model list [--json] [--provider X]")
	case "provider":
		if len(args) == 0 {
			return errors.New("provider: usage: list | add <name> | remove <name>")
		}
		want := 0
		switch args[0] {
		case "list":
			want = 1
		case "add", "remove":
			want = 2
		default:
			return errors.New("provider: usage: list | add <name> | remove <name>")
		}
		if len(args) < want {
			return fmt.Errorf("provider %s: usage: maestro provider %s %s", args[0], args[0], map[string]string{"add": "<name>", "remove": "<name>", "list": ""}[args[0]])
		}
		if len(args) > want {
			return fmt.Errorf("provider %s: unexpected positional argument %q", args[0], args[want])
		}
	}
	return nil
}

// extractValueFlag permits command-local flags after positional subcommands,
// e.g. `maestro skills disable audit --scope=session`. The standard flag
// package stops at the first positional argument.
func extractValueFlag(args []string, name string) ([]string, string, error) {
	var out []string
	var value string
	seen := false
	long := "--" + name
	short := "-" + name
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, long+"=") || strings.HasPrefix(arg, short+"="):
			if seen {
				return nil, "", fmt.Errorf("--%s may be set only once", name)
			}
			seen = true
			value = strings.SplitN(arg, "=", 2)[1]
		case arg == long || arg == short:
			if seen {
				return nil, "", fmt.Errorf("--%s may be set only once", name)
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, "", fmt.Errorf("--%s requires a value", name)
			}
			seen = true
			i++
			value = args[i]
		default:
			out = append(out, arg)
		}
	}
	return out, value, nil
}

// extractBoolFlag is the boolean counterpart to extractValueFlag. It accepts
// `--name`, `-name`, and explicit `=true|false` forms in any command-local
// position while rejecting duplicates and malformed values.
func extractBoolFlag(args []string, name string) ([]string, bool, error) {
	out := make([]string, 0, len(args))
	value := false
	seen := false
	long := "--" + name
	short := "-" + name
	for _, arg := range args {
		switch {
		case arg == long || arg == short:
			if seen {
				return nil, false, fmt.Errorf("--%s may be set only once", name)
			}
			seen = true
			value = true
		case strings.HasPrefix(arg, long+"=") || strings.HasPrefix(arg, short+"="):
			if seen {
				return nil, false, fmt.Errorf("--%s may be set only once", name)
			}
			seen = true
			parsed, err := strconv.ParseBool(strings.SplitN(arg, "=", 2)[1])
			if err != nil {
				return nil, false, fmt.Errorf("--%s requires true or false", name)
			}
			value = parsed
		default:
			out = append(out, arg)
		}
	}
	return out, value, nil
}

// rejectCommandFlags prevents the positional tail from swallowing an
// unknown option after Go's flag parser stops. Recognized flags have already
// been removed by the extractors above.
func rejectCommandFlags(args []string) error {
	for _, arg := range args {
		if arg != "-" && strings.HasPrefix(arg, "-") {
			name := strings.SplitN(arg, "=", 2)[0]
			return fmt.Errorf("unknown flag %s", name)
		}
	}
	return nil
}

// withStreamDrain keeps one consumer attached for the complete synchronous
// command. This gives the orchestrator real backpressure without deadlocking
// headless commands or truncating streams at the channel capacity.
func withStreamDrain(ctx context.Context, orch *orchestrator.Orchestrator, out io.Writer, run func() error) error {
	stop := make(chan struct{})
	drained := make(chan struct{})
	projection := newTerminalStreamProjection(out)
	go func() {
		defer close(drained)
		for {
			select {
			case ev := <-orch.Stream:
				printStreamEvent(projection, ev)
			case <-stop:
				for {
					select {
					case ev := <-orch.Stream:
						printStreamEvent(projection, ev)
					default:
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	err := run()
	close(stop)
	<-drained
	return err
}

func printStreamEvent(out *terminalStreamProjection, ev agentcore.StreamEvent) {
	switch ev.Type {
	case agentcore.EvTextDelta:
		if td, ok := ev.Content.(agentcore.TextDelta); ok {
			out.WriteString(td.Text)
		}
	case agentcore.EvDone:
		if d, ok := ev.Content.(agentcore.Done); ok && d.Cost != nil {
			out.WriteString(fmt.Sprintf("\n[cost: $%.4f]\n", d.Cost.Total()))
		}
	}
}

func specStore(opts options) *spec.Store {
	return spec.NewStore(filepath.Join(projectDir(opts), "specs"))
}

func runSpec(args []string, opts options, out io.Writer) error {
	if len(args) == 0 {
		return exitf(2, "usage: maestro spec list|show|validate <id>")
	}
	store := specStore(opts)
	ctx := context.Background()
	switch args[0] {
	case "list":
		summaries, err := store.List(ctx)
		if err != nil {
			return fmt.Errorf("list specs: %w", err)
		}
		if len(summaries) == 0 {
			fmt.Fprintln(out, "No specs yet.")
			return nil
		}
		fmt.Fprintf(out, "%-24s %-14s %-8s %s\n", "ID", "STATUS", "KIND", "TITLE")
		for _, s := range summaries {
			fmt.Fprintf(out, "%-24s %-14s %-8s %s\n", s.ID, s.Status, s.Category, s.Title)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return exitf(2, "usage: maestro spec show <id>")
		}
		s, err := store.Load(ctx, args[1])
		if err != nil {
			return err
		}
		data, err := spec.Marshal(s)
		if err != nil {
			return fmt.Errorf("render spec %s: %w", s.ID, err)
		}
		fmt.Fprint(out, string(data))
		return nil
	case "validate":
		if len(args) != 2 {
			return exitf(2, "usage: maestro spec validate <id>")
		}
		s, err := store.Load(ctx, args[1])
		if err != nil {
			return err
		}
		report := s.ValidateReadiness()
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(out, "%s [%s] %s: %s\n", strings.ToUpper(string(diagnostic.Severity)), diagnostic.Code, diagnostic.Path, diagnostic.Message)
		}
		if !report.Ready() {
			return exitf(1, "spec %s is not ready", s.ID)
		}
		fmt.Fprintf(out, "Spec %s is ready.\n", s.ID)
		return nil
	default:
		return exitf(2, "unknown subcommand %q — try 'maestro spec list'", args[0])
	}
}

func runPropose(args []string, opts options, out, errOut io.Writer) error {
	orch, err := newOrchestrator(opts, out, errOut)
	if err != nil {
		return err
	}
	defer func() { _ = orch.Close() }()
	fs := flag.NewFlagSet("propose", flag.ContinueOnError)
	msg := fs.String("m", "", "prompt describing the idea")
	recipe := fs.String("recipe", "", "quick, feature, bug, or architecture")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return exitf(2, "propose: %v", err)
	}
	if *msg == "" {
		return exitf(2, "usage: maestro propose -m \"<prompt>\"")
	}
	if err := orch.Dispatch(context.Background(), orchestrator.Command{
		Cmd:   "propose",
		Args:  []string{*msg},
		Flags: map[string]string{"recipe": *recipe},
	}); err != nil {
		return err
	}
	return nil
}

// runTUI launches the charm.land v2 frontend (§5).
func runTUI(opts options, out, errOut io.Writer) error {
	dir := projectDir(opts)
	cfg, v := loadEnv(dir, errOut)
	st, settingsPath := loadSettings()
	dev := newModelsDev(cfg)
	commandOut := tui.NewCommandOutput()
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir:   dir,
		SessionsDir:  sessionsDir(),
		Config:       cfg,
		Keys:         keyStore{vault: v},
		Settings:     st,
		SettingsPath: settingsPath,
		ModelsDev:    dev,
		Vault:        v,
		In:           os.Stdin,
		Out:          commandOut,
	})
	if err != nil {
		return err
	}
	defer func() { _ = orch.Close() }()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	propDir := filepath.Join(home, ".maestro", "proposals", orch.Session().ID)
	props := proposals.NewWorkspaceProposalStore(propDir, orch.WorkDirDisplay)
	perm := tui.NewPermissionQueue(8)
	orch.SetGate(perm)
	orch.SetDevTools([]agentcore.Tool{proposals.StagingWriteTool(props)})
	m := tui.New(orch, props, perm)
	if draft := tui.LoadDraft(); draft != "" {
		m.InputSet(draft)
	}
	profiledOut := applyColorProfile()
	restoreCursor := setTermCursorColor(tui.ThemeForName(st.Theme).Color(tui.TokenCharple))
	defer restoreCursor()
	programOpts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithFilter(tui.InputFilter()),
		tea.WithReportFocus(),
		tea.WithOutput(profiledOut),
	}
	if mouse := mouseOption(); mouse != nil {
		programOpts = append(programOpts, mouse)
	}
	p := tea.NewProgram(m, programOpts...)
	if _, err := p.Run(); err != nil {
		return err
	}
	tui.SaveDraft(m.InputValue())
	return nil
}

// setTermCursorColor paints the terminal cursor in the theme accent via
// OSC 12 and returns a restore function (OSC 112) for teardown. Terminals
// that ignore the sequence are unaffected.
func setTermCursorColor(c color.Color) func() {
	hex := colorHex(c)
	if len(hex) != 6 {
		return func() {}
	}
	fmt.Fprintf(os.Stderr, "\x1b]12;#%s\x07", hex)
	return func() { fmt.Fprint(os.Stderr, "\x1b]112\x07") }
}

// colorProfile resolves the lipgloss color profile so weak terminals never
// receive truecolor sequences they display literally. MAESTRO_COLOR
// overrides auto-detection (NO_COLOR / CLICOLOR_FORCE / TERM / COLORTERM).
func colorProfile() termenv.Profile {
	switch strings.ToLower(os.Getenv("MAESTRO_COLOR")) {
	case "none", "ascii":
		return termenv.Ascii
	case "16", "ansi":
		return termenv.ANSI
	case "256", "ansi256":
		return termenv.ANSI256
	case "truecolor", "24bit":
		return termenv.TrueColor
	}
	return termenv.EnvColorProfile()
}

// applyColorProfile returns a terminal writer that applies the resolved color
// profile to the complete Bubble Tea frame. lipgloss v2 emits full-fidelity
// ANSI from Style.Render; the writer is therefore the boundary where
// MAESTRO_COLOR and NO_COLOR must be enforced.
func applyColorProfile() *profileOutput {
	return &profileOutput{
		File: os.Stdout,
		writer: &colorprofile.Writer{
			Forward: os.Stdout,
			Profile: outputProfile(colorProfile()),
		},
	}
}

// profileOutput keeps Bubble Tea's terminal detection intact while routing
// writes through the color-profile filter. Embedding *os.File preserves Fd,
// Read, and Close, which Bubble Tea needs for raw-mode and resize handling.
type profileOutput struct {
	*os.File
	writer *colorprofile.Writer
}

func (w *profileOutput) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func outputProfile(p termenv.Profile) colorprofile.Profile {
	switch p {
	case termenv.Ascii:
		return colorprofile.ASCII
	case termenv.ANSI:
		return colorprofile.ANSI
	case termenv.ANSI256:
		return colorprofile.ANSI256
	case termenv.TrueColor:
		return colorprofile.TrueColor
	default:
		return colorprofile.TrueColor
	}
}

// colorHex converts a color.Color to a "#RRGGBB" hex string.
func colorHex(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X", r>>8, g>>8, b>>8)
}

// mouseOption enables cell-motion by default: clicks, wheel and drag are core
// product interactions. The TUI input filter rejects malformed reports that a
// terminal exposes as text. MAESTRO_MOUSE=none remains the emergency opt-out;
// `all` additionally enables hover events without a pressed button.
func mouseOption() tea.ProgramOption {
	switch mouseMode() {
	case "cell":
		return tea.WithMouseCellMotion()
	case "all":
		return tea.WithMouseAllMotion()
	}
	return nil
}

func mouseMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAESTRO_MOUSE"))) {
	case "", "cell":
		return "cell"
	case "none", "off", "0":
		return "none"
	case "all":
		return "all"
	default:
		return "none"
	}
}

func effectiveVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if moduleVersion := strings.TrimSpace(info.Main.Version); moduleVersion != "" && moduleVersion != "(devel)" {
			return moduleVersion
		}
	}
	return "dev"
}

func printUsage(out io.Writer, buildVersion string) {
	fmt.Fprintf(out, `maestro %s — the spec-driven AI orchestra

Usage:
  maestro [--engine native|subscription] [--dir <root>] [command]
  maestro                       launch the premium TUI

Commands:
  maestro chat [-m <msg>]   interactive REPL, or one-shot chat
  maestro tui               premium TUI (charm.land v2)
  /bootstrap                in the TUI: review a new-project questionnaire and MAESTRO.md
  /onboard                  in the TUI: scan an existing repo, then review the same MAESTRO.md
  maestro propose -m <prompt>       draft a spec proposal [--recipe quick|feature|bug|architecture]
  maestro validate                  check proposal readiness
  maestro answer Q-001 <answer>     resolve a blocking clarification
  maestro accept [--branch NAME | --worktree]   accept the proposal
  maestro build [<id>] [--engine] [--agent] [--model]   launch the dev sub-agent
  maestro review [<id>]     run the reviewer
  maestro fix               send review findings back to dev
  maestro docs [<id>]       generate documentation
  maestro archive [<id>] [--yes] [--merge]   commit + archive
  maestro rewind <cp> [--code] [--conv]      restore a checkpoint
  maestro remember <fact>   retain a decision (Hindsight)
  maestro reflect           synthesize the decision memory
  maestro rules import|export   rules from MDC/.clinerules/AGENTS.md
  maestro commit [--yes]    plan/apply atomic spec-mapped commits
  maestro learn guided|challenge|off   set the private Coach mode
  maestro learn status|next|done|later inspect or advance an explicit Coach lesson
  maestro learn <path> [--deep]  explain code and write maestro/learn/*.md
  maestro skills list            discover project and user Agent Skills
  maestro skills show <id>       inspect bounded SKILL.md source
  maestro skills enable|disable <id> [--scope=project|session]
  maestro skills run <id>        explicitly run one enabled skill read-only
  maestro mcp list|status        show configured MCP integrations safely
  maestro mcp tools [server|all] show connected, approval-gated MCP tools
  maestro mcp reconnect <server|all> reconnect and refresh MCP tools
  maestro rename <title>    set the current session title
  maestro resume [id]       show or load a saved session
  maestro git list          list registered Git workspaces
  maestro git create <branch> | select <path>   create or select a workspace
  maestro provider list|add|remove   providers (catalog + env-detected)
  maestro model list [--json]  all known models
  maestro auth login|status|logout   API keys and credential status
  maestro spec list|show    inspect specs
  maestro help              this help
  maestro version           print version
`, buildVersion)
}
