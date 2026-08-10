// Package repl provides the interactive frontend: a stdin/stdout
// conversation loop that drives the orchestrator's dispatch surface. Every
// slash command and every chat line flows through orchestrator.Dispatch or
// orchestrator.Chat — the REPL is a thin renderer.
package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/settings"
	"github.com/bryann2k/maestro/internal/spec"
)

// Options configures a REPL session.
type Options struct {
	Dir          string // project root; specs live in Dir/specs
	In           io.Reader
	Out          io.Writer
	Config       *config.Config
	Keys         agentcore.KeyStore
	Model        string // active model ID
	Once         string // one-shot message: send it, print, exit
	SessionsDir  string // override for the session store
	Settings     settings.Settings
	SettingsPath string               // settings file for engine persistence
	ModelsDev    *agentcore.ModelsDev // models.dev catalog (like the TUI)
}

// Run starts the REPL loop and returns when the user quits or stdin closes.
// ctx cancellation exits cleanly.
func Run(ctx context.Context, opts Options) error {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		opts.Dir = cwd
	}
	if opts.SessionsDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			opts.SessionsDir = filepath.Join(home, ".maestro", "sessions")
		}
	}
	st := settings.Defaults()
	if opts.Settings.RoleDefaults != nil {
		st = opts.Settings
	}
	orch, err := orchestrator.New(ctx, orchestrator.Options{
		ProjectDir:   opts.Dir,
		SessionsDir:  opts.SessionsDir,
		Config:       opts.Config,
		Keys:         opts.Keys,
		Settings:     st,
		SettingsPath: opts.SettingsPath,
		ModelsDev:    opts.ModelsDev,
		Model:        opts.Model,
		In:           opts.In,
		Out:          opts.Out,
	})
	if err != nil {
		fmt.Fprintf(opts.Out, "warning: %s\n", terminalSafeError(err))
	}
	if orch != nil {
		defer func() { _ = orch.Close() }()
	}

	store := spec.NewStore(filepath.Join(opts.Dir, "specs"))
	if opts.Once != "" {
		if orch == nil {
			return errors.New("chat unavailable: configure a provider in maestrorc first")
		}
		return withDrain(ctx, orch, opts.Out, func() error { return orch.Chat(ctx, opts.Once) })
	}

	r := bufio.NewReader(opts.In)
	fmt.Fprintln(opts.Out, "Maestro v1 — spec-driven AI orchestra")
	fmt.Fprintln(opts.Out, `Type /help for commands. /quit to exit.`)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		fmt.Fprint(opts.Out, "maestro> ")
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(opts.Out)
				return nil
			}
			return fmt.Errorf("read input: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			var cmdErr error
			if orch == nil {
				cmdErr = runCommand(ctx, orch, store, line)
			} else {
				cmdErr = withDrain(ctx, orch, opts.Out, func() error { return runCommand(ctx, orch, store, line) })
			}
			if cmdErr != nil {
				if errors.Is(cmdErr, errQuit) {
					return nil
				}
				printREPLError(opts.Out, cmdErr)
			}
			continue
		}
		if orch == nil {
			fmt.Fprintln(opts.Out, "Chat is not configured. Add a provider to maestrorc.")
			continue
		}
		if err := withDrain(ctx, orch, opts.Out, func() error { return orch.Chat(ctx, line) }); err != nil {
			printREPLError(opts.Out, err)
		}
	}
}

// withDrain attaches exactly one consumer for one synchronous operation and
// flushes the tail before returning. Interactive turns therefore cannot leave
// immortal competing consumers that scramble later streams.
func withDrain(ctx context.Context, orch *orchestrator.Orchestrator, out io.Writer, run func() error) error {
	stop := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case ev := <-orch.Stream:
				printEvent(out, ev)
			case <-stop:
				for {
					select {
					case ev := <-orch.Stream:
						printEvent(out, ev)
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

func printEvent(out io.Writer, ev agentcore.StreamEvent) {
	switch ev.Type {
	case agentcore.EvTextDelta:
		if td, ok := ev.Content.(agentcore.TextDelta); ok {
			fmt.Fprint(out, terminalSafeStreamText(td.Text))
		}
	case agentcore.EvDone:
		if d, ok := ev.Content.(agentcore.Done); ok && d.Cost != nil {
			fmt.Fprintf(out, "\n[cost: $%.4f]\n", d.Cost.Total())
		}
	}
}

const maxREPLErrorRunes = 1024

func terminalSafeStreamText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' {
			out.WriteRune(r)
			continue
		}
		if replUnsafeControl(r) {
			fmt.Fprintf(&out, "<U+%04X>", r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func replUnsafeControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x061c ||
		r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2066 && r <= 0x2069)
}

func terminalSafeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(terminalSafeStreamText(err.Error())), " ")
	runes := []rune(value)
	if len(runes) > maxREPLErrorRunes {
		value = string(runes[:maxREPLErrorRunes-1]) + "…"
	}
	return value
}

func printREPLError(out io.Writer, err error) {
	cause := terminalSafeError(err)
	lower := strings.ToLower(cause)
	var fix string
	switch {
	case strings.HasPrefix(lower, "learn source:"):
		fix = "Choose a readable, non-sensitive UTF-8 source file inside the active project, then retry /learn <path>."
	case strings.HasPrefix(lower, "learn response:"),
		strings.Contains(lower, "model json"),
		strings.Contains(lower, "explainer did not complete successfully"):
		fix = "Retry /learn once; if it repeats, choose a smaller file or another configured model."
	case strings.Contains(lower, "learn: no explainer configured"):
		fix = "Configure a working model provider, then retry /learn <path>."
	case strings.HasPrefix(lower, "coach: no pending lesson"),
		strings.Contains(lower, "explicitly offered lesson"):
		fix = "Run /learn next, complete the offered exercise, then mark that lesson done."
	}
	if fix != "" {
		fmt.Fprintf(out, "Cause: %s\nFix: %s\n", cause, fix)
		return
	}
	fmt.Fprintf(out, "error: %s\n", cause)
}

var errQuit = errors.New("quit")

func runCommand(ctx context.Context, orch *orchestrator.Orchestrator, store *spec.Store, line string) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	cmd := fields[0]
	cmdName := strings.TrimPrefix(cmd, "/")
	flags := map[string]string{}
	args := make([]string, 0, len(fields)-1)
	for _, a := range fields[1:] {
		if strings.HasPrefix(a, "-") && strings.Contains(a, "=") {
			k, v, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
			flags[k] = v
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			switch name {
			case "deep", "yes", "merge", "worktree", "isolated", "code", "conv", "json":
				flags[name] = "true"
				continue
			}
		}
		args = append(args, a)
	}
	switch cmdName {
	case "quit", "exit":
		return errQuit
	case "help":
		printHelp(outWriter(orch))
		return nil
	case "spec":
		return runSpec(ctx, outWriter(orch), store, fields[1:])
	case "settings":
		if orch == nil {
			return errors.New("no orchestrator")
		}
		fmt.Fprintln(outWriter(orch), "Settings are available in the TUI with /settings.")
		return nil
	case "bootstrap", "boostrap":
		fmt.Fprintln(outWriter(orch), "Bootstrap is a reviewed transcript conversation. Run `maestro tui`, then use /bootstrap.")
		return nil
	case "adopt", "onboard":
		fmt.Fprintln(outWriter(orch), "Adopt combines transcript decisions with static repository analysis. Run `maestro tui`, then use /adopt.")
		return nil
	}
	if orch == nil {
		return errors.New("orchestrator unavailable: configure a provider in maestrorc first")
	}
	return orch.Dispatch(ctx, orchestrator.Command{Cmd: cmdName, Args: args, Flags: flags})
}

func outWriter(orch *orchestrator.Orchestrator) io.Writer {
	if orch == nil {
		return os.Stdout
	}
	return orch.Out()
}

func runSpec(ctx context.Context, out io.Writer, store *spec.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: /spec list | /spec show <id> | /spec validate <id>")
	}
	switch args[0] {
	case "list":
		summaries, err := store.List(ctx)
		if err != nil {
			return err
		}
		if len(summaries) == 0 {
			fmt.Fprintln(out, "No specs yet. `maestro propose -m \"<prompt>\"` drafts the first one.")
			return nil
		}
		fmt.Fprintf(out, "%-24s %-14s %-8s %s\n", "ID", "STATUS", "KIND", "TITLE")
		for _, s := range summaries {
			fmt.Fprintf(out, "%-24s %-14s %-8s %s\n", s.ID, s.Status, s.Category, s.Title)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: /spec show <id>")
		}
		s, err := store.Load(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(out, terminalSafeStreamText(s.Body))
		return nil
	case "validate":
		if len(args) != 2 {
			return errors.New("usage: /spec validate <id>")
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
			return fmt.Errorf("spec %s is not ready", s.ID)
		}
		fmt.Fprintf(out, "Spec %s is ready.\n", s.ID)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q — try /spec list", args[0])
	}
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  /bootstrap            initialize Git and start the project contract conversation")
	fmt.Fprintln(out, "  /adopt                TUI-only existing-repository contract conversation")
	fmt.Fprintln(out, "  /onboard              compatibility alias for /adopt")
	fmt.Fprintln(out, "  /propose              draft a spec proposal")
	fmt.Fprintln(out, "  /validate             check proposal readiness")
	fmt.Fprintln(out, "  /answer Q-… <text>    resolve a spec clarification")
	fmt.Fprintln(out, "  /accept               accept the proposal (branch menu)")
	fmt.Fprintln(out, "  /edit <text>          refine the proposal")
	fmt.Fprintln(out, "  /cancel               drop the proposal")
	fmt.Fprintln(out, "  /build                launch the dev sub-agent")
	fmt.Fprintln(out, "  /review               launch the reviewer")
	fmt.Fprintln(out, "  /fix                  send findings back to dev")
	fmt.Fprintln(out, "  /docs                 generate documentation")
	fmt.Fprintln(out, "  /archive              commit + archive the spec")
	fmt.Fprintln(out, "  /rename <title>       rename the current session")
	fmt.Fprintln(out, "  /resume [id]          show or load a saved session")
	fmt.Fprintln(out, "  /git list|create|select manage registered Git workspaces")
	fmt.Fprintln(out, "  /learn guided|challenge|off set the private Coach mode")
	fmt.Fprintln(out, "  /learn status|next|done|later inspect or advance a Coach lesson")
	fmt.Fprintln(out, "  /learn <path> [--deep] explain code to maestro/learn/*.md")
	fmt.Fprintln(out, "  /skills list|show      discover or inspect Agent Skills")
	fmt.Fprintln(out, "  /skills enable|disable <id> [--scope=project|session]")
	fmt.Fprintln(out, "  /skills run <id>       explicitly run one enabled skill read-only")
	fmt.Fprintln(out, "  /mcp list|status       show configured MCP integrations safely")
	fmt.Fprintln(out, "  /mcp tools [server|all] show connected, approval-gated MCP tools")
	fmt.Fprintln(out, "  /mcp reconnect <server|all> reconnect and refresh MCP tools")
	fmt.Fprintln(out, "  /spec list|show|validate inspect specs")
	fmt.Fprintln(out, "  /model [id]           list or select the active model")
	fmt.Fprintln(out, "  /help                 this help")
	fmt.Fprintln(out, "  /quit, /exit          quit")
	fmt.Fprintln(out, "Anything else is sent to the orchestrator.")
	fmt.Fprintln(out, "Use `maestro tui` for provider, model, and editor settings.")
}
