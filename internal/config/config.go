// Package config parses the bash-like maestrorc configuration files:
// builtins such as provider add, model add, mcp add, permissions, lsp add,
// and option, plus the modelRoles block. Configuration is merged from the
// user config dir and the project root, with the project winning.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Builtin command names.
const (
	CmdProvider    = "provider"
	CmdModel       = "model"
	CmdModelRoles  = "modelRoles:"
	CmdMcp         = "mcp"
	CmdPermissions = "permissions"
	CmdLsp         = "lsp"
	CmdOption      = "option"
)

// File names, in rising priority.
const (
	FileGlobal  = "maestrorc"
	FileProject = "maestrorc"
	FileHidden  = ".maestrorc"
)

// Provider describes one configured LLM provider.
type Provider struct {
	Name               string
	Type               string // openai | openai-compat | anthropic | ollama | ...
	BaseURL            string
	APIKey             string
	Disabled           bool
	FlatRate           bool
	DiscoverModels     bool
	SystemPromptPrefix string
	ExtraHeaders       []string // "K V" pairs
	ExtraBody          string
	ProviderOptions    string
}

// Model describes one model with metadata and pricing.
type Model struct {
	ID               string
	Name             string
	ContextWindow    int
	DefaultMaxTokens int
	CanReason        bool
	SupportsImages   bool
	PriceInput       float64
	PriceOutput      float64
	PriceCacheCreate float64
	PriceCacheHit    float64
	ReasoningEffort  string
}

// Sampling carries per-model sampling options.
type Sampling struct {
	Think            bool
	ReasoningEffort  string
	MaxTokens        int
	Temperature      *float64
	TopP             *float64
	TopK             int
	FrequencyPenalty *float64
	PresencePenalty  *float64
	ProviderOptions  string
}

// Slot binds a model to a sampling profile (large / small).
type Slot struct {
	Model    string
	Sampling Sampling
}

// Mcp describes one MCP server.
type Mcp struct {
	Name    string
	Type    string // stdio | http | sse
	URL     string
	Command string   // stdio only
	Headers []string // "K V" pairs
}

// PermissionRule is one allow/deny rule for tools.
type PermissionRule struct {
	Action string // allow | deny
	Tools  []string
}

// Lsp describes one LSP server.
type Lsp struct {
	Name    string
	Command string
}

// Config is the merged result of all parsed maestrorc files.
type Config struct {
	Providers   []Provider
	Models      []Model
	Slots       map[string]Slot // "large" | "small"
	ModelRoles  map[string]Slot // default | smol | slow | plan | commit
	Mcp         []Mcp
	Permissions []PermissionRule
	LSP         []Lsp
	Options     map[string]string
}

// New returns an empty Config.
func New() *Config {
	return &Config{
		Slots:      map[string]Slot{},
		ModelRoles: map[string]Slot{},
		Options:    map[string]string{},
	}
}

// GlobalPath returns the user-level config file path honoring
// XDG_CONFIG_HOME when set (all platforms), falling back to the platform
// user config dir, e.g. ~/.config/maestro/maestrorc.
func GlobalPath() (string, error) {
	base, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(base, "maestro", FileGlobal), nil
}

// userConfigDir returns $XDG_CONFIG_HOME when set, os.UserConfigDir
// otherwise. os.UserConfigDir only honors XDG on Unix; macOS and Windows
// users with XDG set get the documented behavior instead.
func userConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	return os.UserConfigDir()
}

// Load parses and merges the global and project config files. Priority is
// ./.maestrorc > ./maestrorc > ~/.config/maestro/maestrorc: later files
// override earlier ones for maps and append for lists.
func Load(ctx context.Context, projectDir string) (*Config, error) {
	global, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	merged := New()
	paths := []string{global}
	if projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, FileProject), filepath.Join(projectDir, FileHidden))
	}
	var errs []error
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, fmt.Errorf("read %s: %w", p, err))
			continue
		}
		cfg, err := Parse(p, string(data))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		Merge(merged, cfg)
	}
	return merged, errors.Join(errs...)
}

// Merge overlays src onto dst: lists append, maps override by key.
func Merge(dst, src *Config) {
	dst.Providers = append(dst.Providers, src.Providers...)
	dst.Models = append(dst.Models, src.Models...)
	for k, v := range src.Slots {
		dst.Slots[k] = v
	}
	for k, v := range src.ModelRoles {
		dst.ModelRoles[k] = v
	}
	dst.Mcp = append(dst.Mcp, src.Mcp...)
	dst.Permissions = append(dst.Permissions, src.Permissions...)
	dst.LSP = append(dst.LSP, src.LSP...)
	for k, v := range src.Options {
		dst.Options[k] = v
	}
}

// Parse parses one config file's source. Parse errors are collected and
// joined, each prefixed with the file name and line number.
func Parse(name, src string) (*Config, error) {
	lines, err := logicalLines(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	cfg := New()
	var errs []error
	inRoles := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			inRoles = false
			continue
		}
		indented := line != raw
		if indented && inRoles {
			if err := parseRoleLine(cfg, line); err != nil {
				errs = append(errs, fmt.Errorf("%s:%d: %w", name, i+1, err))
			}
			continue
		}
		inRoles = false
		toks, err := tokenize(line)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s:%d: %w", name, i+1, err))
			continue
		}
		if len(toks) == 0 {
			continue
		}
		if toks[0] == CmdModelRoles {
			inRoles = true
			continue
		}
		if err := parseLine(cfg, toks); err != nil {
			errs = append(errs, fmt.Errorf("%s:%d: %w", name, i+1, err))
		}
	}
	return cfg, errors.Join(errs...)
}

// logicalLines splits src into lines, joining backslash continuations and
// stripping comments (a # outside quotes).
func logicalLines(src string) ([]string, error) {
	phys := strings.Split(src, "\n")
	var out []string
	var cur strings.Builder
	for i, line := range phys {
		stripped, cont, err := stripComment(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		cur.WriteString(stripped)
		if cont {
			cur.WriteString(" ")
			continue
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

// stripComment removes everything from an unquoted # to the end of the line
// and reports whether the line ends with a backslash continuation.
func stripComment(line string) (string, bool, error) {
	var b strings.Builder
	inS, inD := false, false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case ch == '\'' && !inD:
			inS = !inS
			b.WriteByte(ch)
		case ch == '"' && !inS:
			inD = !inD
			b.WriteByte(ch)
		case ch == '#' && !inS && !inD:
			return b.String(), false, nil
		default:
			b.WriteByte(ch)
		}
	}
	if inS || inD {
		return "", false, errors.New("unterminated quote")
	}
	s := b.String()
	if strings.HasSuffix(s, `\`) {
		return s[:len(s)-1], true, nil
	}
	return s, false, nil
}

// tokenize splits a line into tokens, respecting single and double quotes.
// Command substitutions like $(op read ...) are kept literal.
func tokenize(line string) ([]string, error) {
	var toks []string
	var cur strings.Builder
	inS, inD := false, false
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case ch == '\'' && !inD:
			inS = !inS
		case ch == '"' && !inS:
			inD = !inD
		case (ch == ' ' || ch == '\t') && !inS && !inD:
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	if inS || inD {
		return nil, errors.New("unterminated quote")
	}
	flush()
	return toks, nil
}

func parseLine(cfg *Config, toks []string) error {
	switch toks[0] {
	case CmdProvider:
		return parseProvider(cfg, toks[1:])
	case CmdModel:
		return parseModel(cfg, toks[1:])
	case CmdMcp:
		return parseMcp(cfg, toks[1:])
	case CmdPermissions:
		return parsePermissions(cfg, toks[1:])
	case CmdLsp:
		return parseLsp(cfg, toks[1:])
	case CmdOption:
		return parseOption(cfg, toks[1:])
	default:
		return fmt.Errorf("unknown command %q", toks[0])
	}
}

func parseProvider(cfg *Config, args []string) error {
	if len(args) < 2 || args[0] != "add" {
		return errors.New("usage: provider add NAME [--type TYPE] [--base-url URL] [--api-key KEY] etc")
	}
	p := Provider{Name: args[1]}
	if err := ValidateProviderID(p.Name); err != nil {
		return err
	}
	flags, err := flagPairs(args[2:])
	if err != nil {
		return err
	}
	for _, f := range flags {
		switch f.name {
		case "--type":
			p.Type = f.value
		case "--base-url":
			p.BaseURL = f.value
		case "--api-key":
			p.APIKey = f.value
		case "--disable":
			p.Disabled = true
		case "--flat-rate":
			p.FlatRate = true
		case "--discover-models":
			p.DiscoverModels = true
		case "--system-prompt-prefix":
			p.SystemPromptPrefix = f.value
		case "--extra-header":
			if f.rest == "" {
				return errors.New("--extra-header needs a value pair: --extra-header KEY VALUE")
			}
			p.ExtraHeaders = append(p.ExtraHeaders, f.value+" "+f.rest)
		case "--extra-body":
			p.ExtraBody = f.value
		case "--provider-options":
			p.ProviderOptions = f.value
		default:
			return fmt.Errorf("unknown provider flag %s", f.name)
		}
	}
	cfg.Providers = append(cfg.Providers, p)
	return nil
}

func parseModel(cfg *Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: model add NAME [flags] | model SLOT NAME [sampling flags]")
	}
	if args[0] == "add" {
		return parseModelAdd(cfg, args[1:])
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: model %s MODEL [sampling flags]", args[0])
	}
	slotName, modelRef := args[0], args[1]
	slot, err := parseSampling(args[2:])
	if err != nil {
		return err
	}
	cfg.Slots[slotName] = Slot{Model: modelRef, Sampling: slot}
	return nil
}

func parseModelAdd(cfg *Config, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: model add NAME [--name N] [--context-window N] [--price-input F] etc")
	}
	m := Model{ID: args[0]}
	flags, err := flagPairs(args[1:])
	if err != nil {
		return err
	}
	for _, f := range flags {
		switch f.name {
		case "--name":
			m.Name = f.value
		case "--context-window":
			m.ContextWindow, err = strconv.Atoi(f.value)
		case "--default-max-tokens":
			m.DefaultMaxTokens, err = strconv.Atoi(f.value)
		case "--can-reason":
			m.CanReason = true
		case "--supports-images":
			m.SupportsImages = true
		case "--price-input":
			m.PriceInput, err = strconv.ParseFloat(f.value, 64)
		case "--price-output":
			m.PriceOutput, err = strconv.ParseFloat(f.value, 64)
		case "--price-cache-create":
			m.PriceCacheCreate, err = strconv.ParseFloat(f.value, 64)
		case "--price-cache-hit":
			m.PriceCacheHit, err = strconv.ParseFloat(f.value, 64)
		case "--reasoning-effort":
			m.ReasoningEffort = f.value
		default:
			return fmt.Errorf("unknown model flag %s", f.name)
		}
		if err != nil {
			return fmt.Errorf("flag %s: %w", f.name, err)
		}
	}
	cfg.Models = append(cfg.Models, m)
	return nil
}

// parseSampling consumes sampling flags and returns the Sampling profile.
func parseSampling(args []string) (Sampling, error) {
	var s Sampling
	flags, err := flagPairs(args)
	if err != nil {
		return s, err
	}
	for _, f := range flags {
		switch f.name {
		case "--think":
			s.Think = true
		case "--reasoning-effort":
			s.ReasoningEffort = f.value
		case "--max-tokens":
			s.MaxTokens, err = strconv.Atoi(f.value)
		case "--temperature":
			var v float64
			v, err = strconv.ParseFloat(f.value, 64)
			s.Temperature = &v
		case "--top-p":
			var v float64
			v, err = strconv.ParseFloat(f.value, 64)
			s.TopP = &v
		case "--top-k":
			s.TopK, err = strconv.Atoi(f.value)
		case "--frequency-penalty":
			var v float64
			v, err = strconv.ParseFloat(f.value, 64)
			s.FrequencyPenalty = &v
		case "--presence-penalty":
			var v float64
			v, err = strconv.ParseFloat(f.value, 64)
			s.PresencePenalty = &v
		case "--provider-options":
			s.ProviderOptions = f.value
		default:
			return s, fmt.Errorf("unknown sampling flag %s", f.name)
		}
		if err != nil {
			return s, fmt.Errorf("flag %s: %w", f.name, err)
		}
	}
	return s, nil
}

// parseRoleLine parses one "role: model [sampling flags]" line of the
// modelRoles block.
func parseRoleLine(cfg *Config, line string) error {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return fmt.Errorf("modelRoles entry %q: want role: MODEL [flags]", line)
	}
	role := strings.TrimSpace(line[:idx])
	rest := strings.TrimSpace(line[idx+1:])
	if rest == "" {
		return fmt.Errorf("modelRoles entry %q: model is required", line)
	}
	toks, err := tokenize(rest)
	if err != nil {
		return err
	}
	slot, err := parseSampling(toks[1:])
	if err != nil {
		return err
	}
	cfg.ModelRoles[role] = Slot{Model: toks[0], Sampling: slot}
	return nil
}

func parseMcp(cfg *Config, args []string) error {
	if len(args) < 2 || args[0] != "add" {
		return errors.New("usage: mcp add NAME --type stdio --command COMMAND | --type http|sse --url URL [--header K V]")
	}
	m := Mcp{Name: args[1]}
	if strings.TrimSpace(m.Name) == "" || strings.HasPrefix(m.Name, "-") {
		return errors.New("mcp server name is required")
	}
	flags, err := flagPairs(args[2:])
	if err != nil {
		return err
	}
	for _, f := range flags {
		switch f.name {
		case "--type":
			m.Type = f.value
		case "--url":
			m.URL = f.value
		case "--command":
			m.Command = f.value
		case "--header":
			if f.rest == "" {
				return errors.New("--header needs a value pair: --header KEY VALUE")
			}
			m.Headers = append(m.Headers, f.value+" "+f.rest)
		default:
			return fmt.Errorf("unknown mcp flag %s", f.name)
		}
	}
	switch m.Type {
	case "stdio":
		if strings.TrimSpace(m.Command) == "" {
			return errors.New("stdio mcp requires --command")
		}
		if m.URL != "" || len(m.Headers) > 0 {
			return errors.New("stdio mcp does not accept --url or --header")
		}
	case "http", "sse":
		if strings.TrimSpace(m.URL) == "" {
			return fmt.Errorf("%s mcp requires --url", m.Type)
		}
		if m.Command != "" {
			return fmt.Errorf("%s mcp does not accept --command", m.Type)
		}
	case "":
		return errors.New("mcp requires --type")
	default:
		return fmt.Errorf("unsupported mcp type %q: want stdio, http, or sse", m.Type)
	}
	cfg.Mcp = append(cfg.Mcp, m)
	return nil
}

func parsePermissions(cfg *Config, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: permissions allow|deny TOOLS")
	}
	action := args[0]
	if action != "allow" && action != "deny" {
		return fmt.Errorf("permissions action %q invalid: want allow or deny", action)
	}
	if len(args) < 2 {
		return errors.New("permissions requires at least one tool")
	}
	cfg.Permissions = append(cfg.Permissions, PermissionRule{Action: action, Tools: args[1:]})
	return nil
}

func parseLsp(cfg *Config, args []string) error {
	if len(args) < 2 || args[0] != "add" {
		return errors.New("usage: lsp add NAME --command CMD")
	}
	l := Lsp{Name: args[1]}
	flags, err := flagPairs(args[2:])
	if err != nil {
		return err
	}
	for _, f := range flags {
		if f.name == "--command" {
			l.Command = f.value
			continue
		}
		return fmt.Errorf("unknown lsp flag %s", f.name)
	}
	if l.Command == "" {
		return errors.New("lsp requires --command")
	}
	cfg.LSP = append(cfg.LSP, l)
	return nil
}

func parseOption(cfg *Config, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: option KEY VALUE")
	}
	cfg.Options[args[0]] = args[1]
	return nil
}

// flagPair is one parsed flag: name plus its value tokens.
type flagPair struct {
	name  string
	value string
	rest  string // second value token, for K V flags
}

// flagPairs walks raw tokens and pairs flags with their values. Flags are
// "--name value"; "--extra-header KEY VALUE" consumes two value tokens;
// boolean flags (--disable, --think, ...) consume none.
func flagPairs(args []string) ([]flagPair, error) {
	var out []flagPair
	boolFlags := map[string]bool{
		"--disable": true, "--flat-rate": true, "--discover-models": true,
		"--can-reason": true, "--supports-images": true, "--think": true,
	}
	multi := map[string]int{"--extra-header": 2, "--header": 2}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "--") {
			return nil, fmt.Errorf("unexpected positional %q", tok)
		}
		if boolFlags[tok] {
			out = append(out, flagPair{name: tok})
			continue
		}
		arity, isMulti := multi[tok]
		if !isMulti {
			arity = 1
		}
		if i+arity >= len(args) {
			return nil, fmt.Errorf("flag %s requires a value", tok)
		}
		fp := flagPair{name: tok}
		if arity >= 1 {
			fp.value = args[i+1]
		}
		if arity >= 2 {
			fp.rest = args[i+2]
		}
		out = append(out, fp)
		i += arity
	}
	return out, nil
}
