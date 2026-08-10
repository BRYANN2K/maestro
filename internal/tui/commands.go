package tui

import (
	"fmt"
	"strings"

	"github.com/bryann2k/maestro/internal/orchestrator"
)

type slashSuggestion struct {
	Command     string
	Description string
	Aliases     []string
}

// slashCatalog is the single presentation catalog used by inline `/`
// completion and the command palette. Dispatch remains authoritative in the
// orchestrator; this list only describes user-facing commands.
var slashCatalog = []slashSuggestion{
	{Command: "/bootstrap", Description: "shape a new project and propose MAESTRO.md", Aliases: []string{"/boostrap"}},
	{Command: "/onboard", Description: "analyse an existing repository and propose MAESTRO.md", Aliases: []string{"/adopt"}},
	{Command: "/propose", Description: "draft from this discussion or an explicit request"},
	{Command: "/validate", Description: "check proposal readiness"},
	{Command: "/answer", Description: "resolve a spec clarification"},
	{Command: "/accept", Description: "accept the current proposal"},
	{Command: "/edit", Description: "refine the current proposal"},
	{Command: "/cancel", Description: "drop the current proposal"},
	{Command: "/build", Description: "launch the development agent"},
	{Command: "/review", Description: "review the current implementation"},
	{Command: "/fix", Description: "send review findings back to development"},
	{Command: "/docs", Description: "generate project documentation"},
	{Command: "/archive", Description: "commit and archive the active spec"},
	{Command: "/resume", Description: "browse and restore saved sessions"},
	{Command: "/rename", Description: "rename the current session"},
	{Command: "/learn", Description: "open Coach mode or explain a file"},
	{Command: "/remember", Description: "save a project decision"},
	{Command: "/reflect", Description: "summarize remembered decisions"},
	{Command: "/rules", Description: "import or export project rules"},
	{Command: "/commit", Description: "plan spec-mapped commits"},
	{Command: "/git", Description: "select or create a Git worktree"},
	{Command: "/model", Description: "choose the active model", Aliases: []string{"/models"}},
	{Command: "/providers", Description: "connect subscriptions and API providers", Aliases: []string{"/provider"}},
	{Command: "/settings", Description: "open Maestro settings"},
	{Command: "/mcp", Description: "inspect MCP integrations · native engine only"},
	{Command: "/skills", Description: "inspect enabled skills and their sources"},
	{Command: "/usage", Description: "show context, cost, and tool usage", Aliases: []string{"/usages"}},
	{Command: "/ide", Description: "toggle the code workspace"},
	{Command: "/follow", Description: "toggle live Agent → IDE navigation"},
	{Command: "/checkpoints", Description: "browse restorable code checkpoints"},
	{Command: "/rewind", Description: "restore a checkpoint (requires an id)"},
	{Command: "/help", Description: "list slash commands"},
	{Command: "/quit", Description: "close Maestro", Aliases: []string{"/exit"}},
}

// canonicalSlashCommand folds compatibility aliases before routing. Aliases
// remain accepted, but only the canonical spelling appears in completion,
// the command palette, and /help, so singular/plural variants cannot drift
// into duplicate user-facing commands.
func canonicalSlashCommand(name string) string {
	command := "/" + strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	for _, suggestion := range slashCatalog {
		if command == suggestion.Command {
			return strings.TrimPrefix(suggestion.Command, "/")
		}
		for _, alias := range suggestion.Aliases {
			if command == alias {
				return strings.TrimPrefix(suggestion.Command, "/")
			}
		}
	}
	return strings.TrimPrefix(command, "/")
}

func matchingSlashSuggestions(input string) []slashSuggestion {
	raw := input
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	if strings.ContainsAny(input, " \t\n") || len(raw) != len(strings.TrimRight(raw, " \t\n")) {
		return nil
	}
	query := input
	query = strings.ToLower(query)
	var out []slashSuggestion
	for _, suggestion := range slashCatalog {
		matches := strings.HasPrefix(strings.ToLower(suggestion.Command), query)
		for _, alias := range suggestion.Aliases {
			matches = matches || strings.HasPrefix(strings.ToLower(alias), query)
		}
		if matches {
			out = append(out, suggestion)
		}
	}
	return out
}

func slashHelpText() string {
	var out strings.Builder
	out.WriteString("commands:\n")
	for _, suggestion := range slashCatalog {
		fmt.Fprintf(&out, "  %-14s %s\n", suggestion.Command, suggestion.Description)
	}
	return strings.TrimSpace(out.String())
}

// splitSlashFields tokenizes a slash command without invoking a shell. It
// supports the quoting users naturally reach for in commands such as
// `/edit "keep the public API"`, and rejects incomplete input instead of
// silently dispatching a truncated mutation.
func splitSlashFields(line string) ([]string, error) {
	var (
		fields  []string
		field   strings.Builder
		quote   rune
		started bool
	)
	flush := func() {
		if !started {
			return
		}
		fields = append(fields, field.String())
		field.Reset()
		started = false
	}
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
				started = true
			} else if r == '\\' && quote == '"' && i+1 < len(runes) && runes[i+1] == '"' {
				i++
				field.WriteRune(runes[i])
				started = true
			} else {
				field.WriteRune(r)
				started = true
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			started = true
		case '\\':
			nextEscaped := false
			if i+1 < len(runes) {
				switch runes[i+1] {
				case ' ', '\t', '\n', '\r', '"', '\'':
					nextEscaped = true
				}
			}
			if nextEscaped {
				i++
				field.WriteRune(runes[i])
			} else {
				// Preserve ordinary backslashes so Windows paths do not turn
				// C:\\Users into C:Users.
				field.WriteRune(r)
			}
			started = true
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			field.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return fields, nil
}

// parseSlash converts "/build --engine subscription --agent codex" into a
// dispatchable Command. Flags accept both "--k=v" and "--k v" forms.
// Positional arguments stay positional; their meaning belongs to the
// orchestrator command rather than this syntax parser.
func parseSlash(line string) (orchestrator.Command, error) {
	fields, err := splitSlashFields(line)
	if err != nil {
		return orchestrator.Command{}, err
	}
	if len(fields) == 0 {
		return orchestrator.Command{}, fmt.Errorf("empty command")
	}
	if !strings.HasPrefix(fields[0], "/") {
		return orchestrator.Command{}, fmt.Errorf("slash command must start with /")
	}
	cmd := orchestrator.Command{Cmd: canonicalSlashCommand(fields[0]), Flags: map[string]string{}}
	if cmd.Cmd == "" {
		return orchestrator.Command{}, fmt.Errorf("empty command")
	}
	args := fields[1:]
	var positional []string
	flagsDone := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" && !flagsDone {
			flagsDone = true
			continue
		}
		if flagsDone || !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if name == "" {
			positional = append(positional, a)
			continue
		}
		if k, v, ok := strings.Cut(name, "="); ok {
			cmd.Flags[k] = v
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
			cmd.Flags[name] = "true"
			continue
		}
		cmd.Flags[name] = args[i+1]
		i++
	}
	cmd.Args = positional
	return cmd, nil
}
