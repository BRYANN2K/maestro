// Package skills discovers and manages standard Agent Skills without granting
// repository-controlled instructions any implicit authority. Discovery keeps
// metadata only; complete bounded files are hashed for integrity but their
// bodies are neither retained nor injected before an explicit inspect or run.
package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxDiscoveryRoots    = 16
	maxEntriesPerRoot    = 512
	maxSkills            = 256
	maxFrontmatterBytes  = 16 << 10
	maxSkillBytes        = 128 << 10
	maxDescriptionRunes  = 1024
	maxCompatibilityRune = 500
)

// Scope identifies who supplied a skill. It is display metadata, never an
// authority or permission level.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
	ScopeConfig  Scope = "configured"
)

// Skill is metadata for one discovered Agent Skill. Content is retained only
// for source compatibility with older callers; discovery deliberately leaves
// it empty so instructions are not injected or retained before explicit use.
type Skill struct {
	ID            string
	Name          string
	Description   string
	Compatibility string
	Metadata      map[string]string
	UserInvokable bool
	Path          string // absolute directory containing SKILL.md
	Content       string // populated only on an Inspection copy
	Source        string
	Scope         Scope

	anchor   string
	rootRel  string
	dirName  string
	dirInfo  os.FileInfo
	fileInfo os.FileInfo
	digest   [sha256.Size]byte
}

// Issue is a bounded discovery diagnostic for an unsafe or malformed entry.
// Invalid entries can be shown in settings but can never be enabled or run.
type Issue struct {
	ID     string
	Name   string
	Source string
	Scope  Scope
	Path   string
	Error  string
}

// Catalog is one deterministic metadata snapshot.
type Catalog struct {
	Skills []Skill
	Issues []Issue
}

// Inspection is the full, explicitly requested SKILL.md source plus metadata.
type Inspection struct {
	Skill   Skill
	Path    string
	Content string
}

type discoveryRoot struct {
	anchor   string
	rel      string
	path     string
	source   string
	scope    Scope
	idPrefix string
}

var validSkillName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// Discover is the compatibility metadata-only discovery surface. New callers
// should keep the Catalog so collisions and invalid entries remain visible.
func Discover(home, projectDir string, extraPaths []string) []Skill {
	return DiscoverCatalog(home, projectDir, extraPaths).Skills
}

// DiscoverCatalog scans bounded immediate children of approved roots. It does
// not recurse, follow symlinks, read resources, or retain SKILL.md bodies.
func DiscoverCatalog(home, projectDir string, extraPaths []string) Catalog {
	roots := []discoveryRoot{
		anchoredRoot(projectDir, filepath.Join(".agents", "skills"), "project/.agents", ScopeProject, "project"),
		anchoredRoot(projectDir, filepath.Join(".claude", "skills"), "project/.claude", ScopeProject, "project-claude"),
		anchoredRoot(projectDir, filepath.Join(".cursor", "skills"), "project/.cursor", ScopeProject, "project-cursor"),
		anchoredRoot(home, filepath.Join(".agents", "skills"), "user/.agents", ScopeUser, "user"),
		anchoredRoot(home, filepath.Join(".config", "agents", "skills"), "user/config", ScopeUser, "user-config"),
	}
	for i, path := range extraPaths {
		if len(roots) >= maxDiscoveryRoots {
			break
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		roots = append(roots, discoveryRoot{
			anchor: filepath.Dir(abs), rel: filepath.Base(abs), path: abs,
			source: fmt.Sprintf("configured/%d", i+1), scope: ScopeConfig,
			idPrefix: fmt.Sprintf("configured-%02d", i+1),
		})
	}

	var catalog Catalog
	for _, root := range roots {
		if len(catalog.Skills) >= maxSkills {
			catalog.Issues = append(catalog.Issues, rootIssue(root, "skill catalog exceeds 256 entries"))
			break
		}
		scanDiscoveryRoot(root, &catalog)
	}
	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].ID < catalog.Skills[j].ID })
	sort.Slice(catalog.Issues, func(i, j int) bool { return catalog.Issues[i].ID < catalog.Issues[j].ID })
	return catalog
}

func anchoredRoot(anchor, rel, source string, scope Scope, idPrefix string) discoveryRoot {
	return discoveryRoot{anchor: anchor, rel: rel, path: filepath.Join(anchor, rel), source: source, scope: scope, idPrefix: idPrefix}
}

func scanDiscoveryRoot(spec discoveryRoot, catalog *Catalog) {
	root, err := openAnchoredRoot(spec.anchor, spec.rel)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			catalog.Issues = append(catalog.Issues, rootIssue(spec, safeError(err)))
		}
		return
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		catalog.Issues = append(catalog.Issues, rootIssue(spec, safeError(err)))
		return
	}
	entries, readErr := dir.ReadDir(maxEntriesPerRoot + 1)
	_ = dir.Close()
	if readErr != nil {
		catalog.Issues = append(catalog.Issues, rootIssue(spec, safeError(readErr)))
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if len(entries) > maxEntriesPerRoot {
		catalog.Issues = append(catalog.Issues, rootIssue(spec, "discovery root exceeds 512 entries"))
		entries = entries[:maxEntriesPerRoot]
	}
	for _, entry := range entries {
		if len(catalog.Skills) >= maxSkills {
			catalog.Issues = append(catalog.Issues, rootIssue(spec, "skill catalog exceeds 256 entries"))
			return
		}
		name := entry.Name()
		info, err := root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if info != nil && info.Mode()&os.ModeSymlink != 0 {
				catalog.Issues = append(catalog.Issues, entryIssue(spec, name, "skill directory must not be a symlink"))
			}
			continue
		}
		if err := validateName(name); err != nil {
			catalog.Issues = append(catalog.Issues, entryIssue(spec, name, err.Error()))
			continue
		}
		skillRoot, err := openSkillDirectory(root, name, info)
		if err != nil {
			catalog.Issues = append(catalog.Issues, entryIssue(spec, name, safeError(err)))
			continue
		}
		metadata, fileInfo, err := readSkillMetadata(skillRoot)
		_ = skillRoot.Close()
		if err != nil {
			catalog.Issues = append(catalog.Issues, entryIssue(spec, name, safeError(err)))
			continue
		}
		if metadata.Name != name {
			catalog.Issues = append(catalog.Issues, entryIssue(spec, name, "frontmatter name must exactly match the skill directory"))
			continue
		}
		metadata.ID = spec.idPrefix + ":" + metadata.Name
		metadata.Path = filepath.Join(spec.path, name)
		metadata.Source = spec.source
		metadata.Scope = spec.scope
		metadata.anchor = spec.anchor
		metadata.rootRel = spec.rel
		metadata.dirName = name
		metadata.dirInfo = info
		metadata.fileInfo = fileInfo
		catalog.Skills = append(catalog.Skills, metadata)
	}
}

// openAnchoredRoot validates every repository/user-controlled relative path
// component before opening it. os.Root then confines later operations even if
// a directory is concurrently renamed.
func openAnchoredRoot(anchor, rel string) (*os.Root, error) {
	if strings.TrimSpace(anchor) == "" {
		return nil, os.ErrNotExist
	}
	base, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("unsafe discovery root")
	}
	current := ""
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return nil, errors.New("unsafe discovery root component")
		}
		current = filepath.Join(current, component)
		info, err := base.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("discovery root components must be real directories, not symlinks")
		}
	}
	return base.OpenRoot(clean)
}

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  any               `yaml:"allowed-tools"`
	UserInvokable *bool             `yaml:"user-invocable"`
}

func readSkillMetadata(root *os.Root) (Skill, os.FileInfo, error) {
	// Hash the complete bounded file so an in-place rewrite that restores size
	// and mtime cannot swap reviewed metadata for different instructions. The
	// body bytes are discarded immediately and never injected at discovery.
	data, info, err := readSkillFile(root, "SKILL.md", maxSkillBytes, false)
	if err != nil {
		return Skill{}, nil, err
	}
	fm, err := decodeFrontmatter(data)
	if err != nil {
		return Skill{}, nil, err
	}
	if !safeDocumentText(string(data)) {
		return Skill{}, nil, errors.New("SKILL.md body contains unsafe terminal or format controls")
	}
	invokable := true
	if fm.UserInvokable != nil {
		invokable = *fm.UserInvokable
	}
	return Skill{
		Name: fm.Name, Description: fm.Description, Compatibility: fm.Compatibility,
		Metadata: cloneMetadata(fm.Metadata), UserInvokable: invokable, digest: sha256.Sum256(data),
	}, info, nil
}

func openSkillDirectory(parent *os.Root, name string, expected os.FileInfo) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errors.New("skill directory must be a real directory, not a symlink")
	}
	if expected != nil && !os.SameFile(expected, before) {
		return nil, errors.New("skill directory changed during discovery")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, openErr := child.Stat(".")
	after, afterErr := parent.Lstat(name)
	if openErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = child.Close()
		return nil, errors.New("skill directory changed while opening")
	}
	return child, nil
}

func decodeFrontmatter(data []byte) (frontmatter, error) {
	section, err := frontmatterSection(data)
	if err != nil {
		return frontmatter{}, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(section, &document); err != nil {
		return frontmatter{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	if err := validateYAMLTree(&document, 0, new(int)); err != nil {
		return frontmatter{}, err
	}
	var fm frontmatter
	if err := document.Decode(&fm); err != nil {
		return frontmatter{}, fmt.Errorf("invalid frontmatter fields: %w", err)
	}
	if err := validateName(fm.Name); err != nil {
		return frontmatter{}, fmt.Errorf("invalid name: %w", err)
	}
	if fm.Description == "" || utf8.RuneCountInString(fm.Description) > maxDescriptionRunes || !safeText(fm.Description, false) {
		return frontmatter{}, errors.New("description must contain 1-1024 safe Unicode characters")
	}
	if utf8.RuneCountInString(fm.Compatibility) > maxCompatibilityRune || !safeText(fm.Compatibility, true) {
		return frontmatter{}, errors.New("compatibility must contain at most 500 safe Unicode characters")
	}
	for key, value := range fm.Metadata {
		if key == "" || !safeText(key, false) || !safeText(value, true) {
			return frontmatter{}, errors.New("metadata keys and values must be safe text")
		}
	}
	return fm, nil
}

func validateYAMLTree(node *yaml.Node, depth int, count *int) error {
	if node == nil || depth > 16 {
		return errors.New("frontmatter YAML is too deeply nested")
	}
	*count = *count + 1
	if *count > 256 {
		return errors.New("frontmatter YAML has too many nodes")
	}
	if node.Kind == yaml.AliasNode {
		return errors.New("frontmatter YAML aliases are not supported")
	}
	if node.Anchor != "" {
		return errors.New("frontmatter YAML anchors are not supported")
	}
	if node.Tag != "" && strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		return errors.New("frontmatter YAML custom tags are not supported")
	}
	for _, child := range node.Content {
		if err := validateYAMLTree(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func frontmatterSection(data []byte) ([]byte, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("SKILL.md must be valid UTF-8 text")
	}
	if len(data) > maxFrontmatterBytes+1 {
		data = data[:maxFrontmatterBytes+1]
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return nil, errors.New("SKILL.md requires YAML frontmatter")
	}
	start := strings.IndexByte(text, '\n') + 1
	offset := start
	for offset <= len(text) {
		next := strings.IndexByte(text[offset:], '\n')
		end := len(text)
		if next >= 0 {
			end = offset + next
		}
		line := strings.TrimSuffix(text[offset:end], "\r")
		if line == "---" {
			if end > maxFrontmatterBytes {
				return nil, errors.New("SKILL.md frontmatter exceeds 16 KiB")
			}
			return []byte(text[start:offset]), nil
		}
		if next < 0 || end >= maxFrontmatterBytes {
			break
		}
		offset = end + 1
	}
	return nil, errors.New("SKILL.md frontmatter is unterminated or exceeds 16 KiB")
}

func readSkillFile(root *os.Root, rel string, limit int, prefixOnly bool) ([]byte, os.FileInfo, error) {
	before, err := root.Lstat(rel)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("SKILL.md must be a regular file, not a symlink")
	}
	if before.Size() > maxSkillBytes {
		return nil, nil, fmt.Errorf("SKILL.md exceeds %d KiB", maxSkillBytes>>10)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, nil, errors.New("SKILL.md changed while opening")
	}
	readLimit := int64(limit + 1)
	if !prefixOnly {
		readLimit = int64(maxSkillBytes + 1)
	}
	data, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return nil, nil, err
	}
	if !prefixOnly && len(data) > maxSkillBytes {
		return nil, nil, fmt.Errorf("SKILL.md exceeds %d KiB", maxSkillBytes>>10)
	}
	after, err := root.Lstat(rel)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, nil, errors.New("SKILL.md changed while reading")
	}
	return data, after, nil
}

// Resolve accepts a stable qualified ID, or an unqualified name only when it
// is unique. Ambiguity is always an error instead of a precedence surprise.
func (c Catalog) Resolve(ref string) (Skill, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Skill{}, errors.New("skill reference is required")
	}
	for _, skill := range c.Skills {
		if skill.ID == ref {
			return skill, nil
		}
	}
	var matches []Skill
	for _, skill := range c.Skills {
		if skill.Name == ref {
			matches = append(matches, skill)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, skill := range matches {
			ids = append(ids, skill.ID)
		}
		sort.Strings(ids)
		return Skill{}, fmt.Errorf("skill %q is ambiguous; use one of: %s", ref, strings.Join(ids, ", "))
	}
	return Skill{}, fmt.Errorf("skill %q not found", ref)
}

// Inspect opens the exact metadata snapshot selected by ref. A file modified
// since discovery must be refreshed and reviewed before it can run.
func (c Catalog) Inspect(ctx context.Context, ref string) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	skill, err := c.Resolve(ref)
	if err != nil {
		return Inspection{}, err
	}
	root, err := openAnchoredRoot(skill.anchor, skill.rootRel)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect %s: %w", skill.ID, err)
	}
	defer root.Close()
	skillRoot, err := openSkillDirectory(root, skill.dirName, skill.dirInfo)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect %s: %w", skill.ID, err)
	}
	defer skillRoot.Close()
	data, info, err := readSkillFile(skillRoot, "SKILL.md", maxSkillBytes, false)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect %s: %w", skill.ID, err)
	}
	if skill.fileInfo == nil || !os.SameFile(skill.fileInfo, info) || skill.fileInfo.Size() != info.Size() || !skill.fileInfo.ModTime().Equal(info.ModTime()) {
		return Inspection{}, fmt.Errorf("inspect %s: SKILL.md changed since discovery; refresh skills first", skill.ID)
	}
	if sha256.Sum256(data) != skill.digest {
		return Inspection{}, fmt.Errorf("inspect %s: SKILL.md content changed since discovery; refresh skills first", skill.ID)
	}
	fm, err := decodeFrontmatter(data)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect %s: %w", skill.ID, err)
	}
	if !safeDocumentText(string(data)) {
		return Inspection{}, fmt.Errorf("inspect %s: SKILL.md body contains unsafe terminal or format controls", skill.ID)
	}
	if fm.Name != skill.Name || fm.Description != skill.Description {
		return Inspection{}, fmt.Errorf("inspect %s: metadata changed since discovery; refresh skills first", skill.ID)
	}
	copySkill := skill
	copySkill.Content = string(data)
	return Inspection{Skill: copySkill, Path: filepath.Join(skill.Path, "SKILL.md"), Content: string(data)}, nil
}

// UserInvokable filters skills that may be explicitly launched. No caller
// should interpret this list as permission to auto-trigger a skill.
func UserInvokable(all []Skill) []Skill {
	out := make([]Skill, 0, len(all))
	for _, skill := range all {
		if skill.UserInvokable {
			out = append(out, skill)
		}
	}
	return out
}

// Prompt is a compatibility formatter for an already inspected Skill. It
// intentionally contains no filesystem path and grants no tool capability.
func (s Skill) Prompt() string {
	return "You are running the explicitly selected skill \"" + s.Name + "\".\n\n" + s.Content
}

func validateName(name string) error {
	if len(name) < 1 || len(name) > 64 || !validSkillName.MatchString(name) || strings.Contains(name, "--") {
		return errors.New("name must be 1-64 lowercase ASCII letters, digits, or single hyphens, without leading/trailing hyphens")
	}
	return nil
}

func safeText(value string, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func safeDocumentText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		switch r {
		case '\n', '\r', '\t':
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func cloneMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func rootIssue(root discoveryRoot, message string) Issue {
	return Issue{ID: "invalid:" + root.idPrefix + ":root", Name: "discovery root", Source: root.source, Scope: root.scope, Path: root.path, Error: message}
}

func entryIssue(root discoveryRoot, name, message string) Issue {
	sum := sha256.Sum256([]byte(name))
	return Issue{
		ID: "invalid:" + root.idPrefix + ":" + fmt.Sprintf("%x", sum[:6]), Name: safeDisplayName(name),
		Source: root.source, Scope: root.scope, Path: filepath.Join(root.path, name), Error: message,
	}
}

func safeDisplayName(name string) string {
	name = strings.ToValidUTF8(name, "")
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 128 {
			break
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "invalid entry"
	}
	return b.String()
}

func safeError(err error) string {
	if err == nil {
		return "unknown error"
	}
	return safeDisplayName(err.Error())
}

// parseFrontmatter is retained for package-level compatibility tests. Strict
// discovery uses decodeFrontmatter and reports malformed metadata as issues.
func parseFrontmatter(content string) (name, desc string, invokable bool) {
	fm, err := decodeFrontmatter([]byte(content))
	if err != nil {
		return "", "", false
	}
	invokable = true
	if fm.UserInvokable != nil {
		invokable = *fm.UserInvokable
	}
	return fm.Name, fm.Description, invokable
}
