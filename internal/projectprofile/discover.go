package projectprofile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxInventoryFiles     = 4096
	maxInventoryScanFiles = 32768
	maxInventoryScanBytes = 16 << 20
	maxGitOutputBytes     = maxInventoryScanBytes
	maxCandidateBytes     = 256 << 10
	maxDiscoveryBytes     = 8 << 20
	maxEvidence           = 512
	maxUnknowns           = 256
)

var (
	errCandidateSymlink    = errors.New("candidate is a symlink")
	errCandidateNonRegular = errors.New("candidate is not a regular file")
	errCandidateOversize   = errors.New("candidate exceeds the read limit")
	errCandidateBinary     = errors.New("candidate is binary or invalid UTF-8")
	errCandidateChanged    = errors.New("candidate changed while it was read")
	errDiscoveryReadBudget = errors.New("aggregate discovery read budget was exhausted")
)

// GreenfieldDefaults returns a minimal profile and reviewable defaults for a
// project that has not been implemented yet. It intentionally does not infer
// a stack from neighboring files or execute a scaffolder.
func GreenfieldDefaults(ctx context.Context, start string) (ProjectProfile, Answers, error) {
	if err := ctx.Err(); err != nil {
		return ProjectProfile{}, Answers{}, err
	}
	root, err := canonicalDirectory(start)
	if err != nil {
		return ProjectProfile{}, Answers{}, err
	}
	fingerprint, err := workspaceFingerprint(ctx, root)
	if err != nil {
		return ProjectProfile{}, Answers{}, err
	}
	profile := ProjectProfile{
		SchemaVersion:        SchemaVersion,
		Mode:                 ModeGreenfield,
		Root:                 root,
		Name:                 safeFact(filepath.Base(root)),
		Units:                []Unit{{Path: "."}},
		DiscoveryFingerprint: fingerprint,
		Unknowns: []string{
			"Project purpose has not been confirmed.",
			"Project stack has not been selected.",
			"Project commands have not been confirmed.",
		},
	}
	return profile, AnswersFromProfile(profile), nil
}

// Discover performs bounded, deterministic, static discovery. Git is used
// only for repository-root and file-inventory plumbing. No project binary,
// manifest command, hook, package manager, network client, MCP server, or
// shell is executed.
func Discover(ctx context.Context, start string, mode Mode) (ProjectProfile, error) {
	if mode == ModeGreenfield {
		profile, _, err := GreenfieldDefaults(ctx, start)
		return profile, err
	}
	if mode != ModeBrownfield {
		return ProjectProfile{}, fmt.Errorf("project profile: unsupported mode %q", mode)
	}
	if err := ctx.Err(); err != nil {
		return ProjectProfile{}, err
	}
	root, err := canonicalDirectory(start)
	if err != nil {
		return ProjectProfile{}, err
	}

	profile := ProjectProfile{
		SchemaVersion: SchemaVersion,
		Mode:          mode,
		Root:          root,
		Name:          safeFact(filepath.Base(root)),
	}
	gitRoot, gitErr := repositoryRoot(ctx, root)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ProjectProfile{}, ctxErr
	}
	if gitErr == nil {
		profile.Root = gitRoot
		profile.Name = safeFact(filepath.Base(gitRoot))
		root = gitRoot
	}
	beforeFingerprint, err := workspaceFingerprint(ctx, root)
	if err != nil {
		return ProjectProfile{}, err
	}

	view, usedGitInventory, inventoryErr := inventory(ctx, root, gitErr == nil)
	if inventoryErr != nil {
		return ProjectProfile{}, inventoryErr
	}
	if !usedGitInventory {
		profile.addUnknown("Git inventory was unavailable; used a bounded filesystem inventory.")
	}
	if view.DisplayTruncated {
		profile.addUnknown(fmt.Sprintf("File inventory summary retained %d of %d discovered path(s); all %d project-discovery candidate(s) in the bounded scan were still inspected.", maxInventoryFiles, view.Total, len(view.Candidates)))
	}
	if view.ScanTruncated {
		profile.addUnknown(fmt.Sprintf("Repository inventory scan reached its safety boundary after %d path(s) and %d path byte(s); candidates beyond that boundary were not inspected.", view.Scanned, view.ScannedBytes))
	}
	if view.Excluded > 0 {
		profile.addUnknown(fmt.Sprintf("%d secret-like path(s) were excluded without being read.", view.Excluded))
	}
	if view.UnsafePaths > 0 {
		profile.addUnknown(fmt.Sprintf("%d unsafe path name(s) were excluded without being read.", view.UnsafePaths))
	}
	if view.Total == 0 {
		profile.addUnknown("No readable project files were discovered.")
	}

	d := discovery{
		profile:    &profile,
		root:       root,
		units:      map[string]*unitAccumulator{},
		stackSet:   map[string]bool{},
		commandSet: map[string]bool{},
	}
	d.scan(ctx, view.Candidates)
	if err := ctx.Err(); err != nil {
		return ProjectProfile{}, err
	}
	d.finish()
	profile.addUnknown("Project purpose requires human confirmation.")
	profile.normalize()
	profile.DiscoveryFingerprint, err = workspaceFingerprint(ctx, root)
	if err != nil {
		return ProjectProfile{}, err
	}
	if profile.DiscoveryFingerprint != beforeFingerprint {
		return ProjectProfile{}, &RepositoryChangedError{Mode: mode}
	}
	return profile, nil
}

// AnswersFromProfile creates the common review schema for either mode.
func AnswersFromProfile(profile ProjectProfile) Answers {
	return Answers{
		SchemaVersion:        SchemaVersion,
		Mode:                 profile.Mode,
		Name:                 profile.Name,
		Stacks:               append([]string(nil), profile.Stacks...),
		Units:                cloneUnits(profile.Units),
		Commands:             append([]Command(nil), profile.Commands...),
		DiscoveryFingerprint: profile.DiscoveryFingerprint,
		Safety: []string{
			"Never read, print, or modify secrets and local credential files.",
			"Preserve unrelated user changes and repository state.",
			"Require explicit approval for dependencies, migrations, deployments, and releases.",
		},
		Verification: defaultVerification(profile.Commands),
	}
}

func defaultVerification(commands []Command) []string {
	verification := []string{"Do not report completion without verification evidence."}
	var tests, quality bool
	for _, command := range commands {
		name := strings.ToLower(command.Name)
		tests = tests || strings.Contains(name, "test") || strings.Contains(name, "e2e")
		quality = quality || strings.Contains(name, "check") || strings.Contains(name, "lint") || strings.Contains(name, "vet")
	}
	if tests {
		verification = append(verification, "Run the relevant detected test command before completion.")
	}
	if quality {
		verification = append(verification, "Run the detected quality checks before completion.")
	}
	return verification
}

type discovery struct {
	profile    *ProjectProfile
	root       string
	units      map[string]*unitAccumulator
	stackSet   map[string]bool
	commandSet map[string]bool
	readBytes  int64
}

type unitAccumulator struct {
	unit      Unit
	stacks    map[string]bool
	manifests map[string]bool
	lockfiles map[string]bool
}

func (d *discovery) scan(ctx context.Context, paths []string) {
	// Lockfiles are collected first so package-script commands can use the
	// repository's actual package manager without running it.
	for _, relative := range paths {
		if ctx.Err() != nil {
			return
		}
		manager, ok := lockfileManager(filepath.Base(relative))
		if !ok {
			continue
		}
		if _, err := regularMetadataNoFollow(d.root, relative, false); err != nil {
			d.profile.addUnknown(skippedCandidate(relative, err))
			continue
		}
		unit := d.unit(filepath.ToSlash(filepath.Dir(relative)))
		unit.lockfiles[filepath.Base(relative)] = true
		d.profile.addEvidence(Evidence{Kind: "lockfile", Value: manager, Source: relative, Confidence: ConfidenceDetected})
	}

	for _, relative := range paths {
		if ctx.Err() != nil {
			return
		}
		base := filepath.Base(relative)
		lower := strings.ToLower(base)
		switch {
		case lower == "go.mod" || lower == "go.work":
			d.scanGo(relative)
		case lower == "package.json":
			d.scanNode(relative)
		case lower == "pyproject.toml":
			d.scanPython(relative)
		case lower == "cargo.toml":
			d.scanRust(relative)
		case lower == "makefile" || lower == "gnumakefile":
			d.scanMakefile(relative)
		case isCIPath(relative):
			d.scanCI(relative)
		}
	}
}

func (d *discovery) scanGo(relative string) {
	data, err := d.readCandidate(relative)
	if err != nil {
		d.profile.addUnknown(skippedCandidate(relative, err))
		return
	}
	unit := d.addManifest(relative, "go")
	if filepath.Base(relative) == "go.mod" {
		if name := prefixedLine(data, "module "); name != "" {
			unit.unit.Name = safeFact(name)
			d.profile.addEvidence(Evidence{Kind: "module", Value: safeFact(name), Source: relative, Confidence: ConfidenceDetected})
		}
	}
	d.profile.addEvidence(Evidence{Kind: "manifest", Value: "go", Source: relative, Confidence: ConfidenceDetected})
	d.addCommand(Command{Name: "test", Run: "go test ./...", Cwd: unit.unit.Path, Source: relative, Confidence: ConfidenceInferred})
	d.addCommand(Command{Name: "build", Run: "go build ./...", Cwd: unit.unit.Path, Source: relative, Confidence: ConfidenceInferred})
}

func (d *discovery) scanNode(relative string) {
	data, err := d.readCandidate(relative)
	if err != nil {
		d.profile.addUnknown(skippedCandidate(relative, err))
		return
	}
	var manifest struct {
		Name       string            `json:"name"`
		Scripts    map[string]string `json:"scripts"`
		Workspaces json.RawMessage   `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		d.profile.addUnknown(fmt.Sprintf("Could not parse candidate %q as JSON.", relative))
		return
	}
	unit := d.addManifest(relative, "node")
	if name := safeFact(manifest.Name); name != "" {
		unit.unit.Name = name
		d.profile.addEvidence(Evidence{Kind: "package", Value: name, Source: relative, Confidence: ConfidenceDetected})
	}
	if len(bytes.TrimSpace(manifest.Workspaces)) > 0 && string(bytes.TrimSpace(manifest.Workspaces)) != "null" {
		d.profile.addEvidence(Evidence{Kind: "workspace", Value: "node", Source: relative, Confidence: ConfidenceDetected})
	}
	d.profile.addEvidence(Evidence{Kind: "manifest", Value: "node", Source: relative, Confidence: ConfidenceDetected})
	manager := d.nodeManager(unit.unit.Path)
	names := make([]string, 0, len(manifest.Scripts))
	for name := range manifest.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !validScriptName(name) || !usefulCommandName(name) {
			continue
		}
		run := manager + " run " + name
		if manager == "yarn" {
			run = "yarn " + name
		}
		d.addCommand(Command{Name: name, Run: run, Cwd: unit.unit.Path, Source: relative, Confidence: ConfidenceDetected})
		d.profile.addEvidence(Evidence{Kind: "script", Value: safeFact(name), Source: relative, Confidence: ConfidenceDetected})
	}
}

func (d *discovery) scanPython(relative string) {
	data, err := d.readCandidate(relative)
	if err != nil {
		d.profile.addUnknown(skippedCandidate(relative, err))
		return
	}
	unit := d.addManifest(relative, "python")
	if name := tomlName(data, "project"); name != "" {
		unit.unit.Name = name
		d.profile.addEvidence(Evidence{Kind: "package", Value: name, Source: relative, Confidence: ConfidenceDetected})
	}
	d.profile.addEvidence(Evidence{Kind: "manifest", Value: "python", Source: relative, Confidence: ConfidenceDetected})
	lower := strings.ToLower(string(data))
	if strings.Contains(lower, "[tool.pytest") || strings.Contains(lower, "pytest") {
		d.addCommand(Command{Name: "test", Run: "python -m pytest", Cwd: unit.unit.Path, Source: relative, Confidence: ConfidenceInferred})
	}
}

func (d *discovery) scanRust(relative string) {
	data, err := d.readCandidate(relative)
	if err != nil {
		d.profile.addUnknown(skippedCandidate(relative, err))
		return
	}
	unit := d.addManifest(relative, "rust")
	if name := tomlName(data, "package"); name != "" {
		unit.unit.Name = name
		d.profile.addEvidence(Evidence{Kind: "package", Value: name, Source: relative, Confidence: ConfidenceDetected})
	}
	d.profile.addEvidence(Evidence{Kind: "manifest", Value: "rust", Source: relative, Confidence: ConfidenceDetected})
	d.addCommand(Command{Name: "test", Run: "cargo test", Cwd: unit.unit.Path, Source: relative, Confidence: ConfidenceInferred})
	d.addCommand(Command{Name: "build", Run: "cargo build", Cwd: unit.unit.Path, Source: relative, Confidence: ConfidenceInferred})
}

func (d *discovery) scanMakefile(relative string) {
	data, err := d.readCandidate(relative)
	if err != nil {
		d.profile.addUnknown(skippedCandidate(relative, err))
		return
	}
	d.profile.addEvidence(Evidence{Kind: "task-runner", Value: "make", Source: relative, Confidence: ConfidenceDetected})
	cwd := cleanUnitPath(filepath.ToSlash(filepath.Dir(relative)))
	for _, target := range makeTargets(data) {
		if !usefulCommandName(target) {
			continue
		}
		d.addCommand(Command{Name: target, Run: "make " + target, Cwd: cwd, Source: relative, Confidence: ConfidenceDetected})
		d.profile.addEvidence(Evidence{Kind: "target", Value: safeFact(target), Source: relative, Confidence: ConfidenceDetected})
	}
}

func (d *discovery) scanCI(relative string) {
	data, err := d.readCandidate(relative)
	if err != nil {
		d.profile.addUnknown(skippedCandidate(relative, err))
		return
	}
	// Do not preserve arbitrary run payloads: a workflow may contain literal
	// credentials or adversarial instructions. Count static run keys only.
	runs := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if strings.HasPrefix(line, "run:") {
			runs++
		}
	}
	value := "workflow"
	if runs > 0 {
		value = fmt.Sprintf("workflow with %d run step(s)", runs)
	}
	d.profile.addEvidence(Evidence{Kind: "ci", Value: value, Source: relative, Confidence: ConfidenceDetected})
}

func (d *discovery) addManifest(relative, stack string) *unitAccumulator {
	path := cleanUnitPath(filepath.ToSlash(filepath.Dir(relative)))
	unit := d.unit(path)
	unit.stacks[stack] = true
	unit.manifests[filepath.Base(relative)] = true
	d.stackSet[stack] = true
	return unit
}

func (d *discovery) readCandidate(relative string) ([]byte, error) {
	if err := safeMetadata(d.root, relative); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(d.root, filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(maxDiscoveryBytes)-d.readBytes {
		return nil, errDiscoveryReadBudget
	}
	data, err := readCandidate(d.root, relative)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > int64(maxDiscoveryBytes)-d.readBytes {
		return nil, errDiscoveryReadBudget
	}
	d.readBytes += int64(len(data))
	return data, nil
}

func (d *discovery) unit(path string) *unitAccumulator {
	path = cleanUnitPath(path)
	if found := d.units[path]; found != nil {
		return found
	}
	unit := &unitAccumulator{
		unit:      Unit{Path: path},
		stacks:    map[string]bool{},
		manifests: map[string]bool{},
		lockfiles: map[string]bool{},
	}
	d.units[path] = unit
	return unit
}

func (d *discovery) nodeManager(path string) string {
	for current := cleanUnitPath(path); ; current = cleanUnitPath(filepath.ToSlash(filepath.Dir(current))) {
		if unit := d.units[current]; unit != nil {
			for _, pair := range []struct{ file, manager string }{
				{"pnpm-lock.yaml", "pnpm"},
				{"yarn.lock", "yarn"},
				{"bun.lock", "bun"},
				{"bun.lockb", "bun"},
				{"package-lock.json", "npm"},
				{"npm-shrinkwrap.json", "npm"},
			} {
				if unit.lockfiles[pair.file] {
					return pair.manager
				}
			}
		}
		if current == "." {
			break
		}
	}
	return "npm"
}

func (d *discovery) addCommand(command Command) {
	command.Name = safeFact(command.Name)
	command.Run = safeFact(command.Run)
	command.Cwd = cleanUnitPath(command.Cwd)
	command.Source = safeLocator(command.Source)
	if command.Name == "" || command.Run == "" || len(d.profile.Commands) >= maxEvidence {
		return
	}
	key := strings.Join([]string{command.Name, command.Run, command.Cwd, command.Source, string(command.Confidence)}, "\x00")
	if d.commandSet[key] {
		return
	}
	d.commandSet[key] = true
	d.profile.Commands = append(d.profile.Commands, command)
}

func (d *discovery) finish() {
	for stack := range d.stackSet {
		d.profile.Stacks = append(d.profile.Stacks, stack)
	}
	for _, accumulator := range d.units {
		for value := range accumulator.stacks {
			accumulator.unit.Stacks = append(accumulator.unit.Stacks, value)
		}
		for value := range accumulator.manifests {
			accumulator.unit.Manifests = append(accumulator.unit.Manifests, value)
		}
		for value := range accumulator.lockfiles {
			accumulator.unit.Lockfiles = append(accumulator.unit.Lockfiles, value)
		}
		sort.Strings(accumulator.unit.Stacks)
		sort.Strings(accumulator.unit.Manifests)
		sort.Strings(accumulator.unit.Lockfiles)
		d.profile.Units = append(d.profile.Units, accumulator.unit)
	}
	if root := d.units["."]; root != nil && root.unit.Name != "" {
		d.profile.Name = root.unit.Name
	}
}

func (p *ProjectProfile) addEvidence(evidence Evidence) {
	if len(p.Evidence) >= maxEvidence {
		p.addUnknown("Evidence was truncated at the deterministic safety limit.")
		return
	}
	evidence.Kind = safeFact(evidence.Kind)
	evidence.Value = safeFact(evidence.Value)
	evidence.Source = safeLocator(evidence.Source)
	if evidence.Kind == "" || evidence.Value == "" {
		return
	}
	p.Evidence = append(p.Evidence, evidence)
}

func (p *ProjectProfile) addUnknown(value string) {
	if len(p.Unknowns) >= maxUnknowns {
		return
	}
	value = strings.TrimSpace(value)
	if value != "" {
		p.Unknowns = append(p.Unknowns, value)
	}
}

func (p *ProjectProfile) normalize() {
	sort.Strings(p.Stacks)
	sort.Slice(p.Units, func(i, j int) bool { return p.Units[i].Path < p.Units[j].Path })
	sort.Slice(p.Commands, func(i, j int) bool {
		a, b := p.Commands[i], p.Commands[j]
		return strings.Join([]string{a.Name, a.Cwd, a.Run, a.Source}, "\x00") < strings.Join([]string{b.Name, b.Cwd, b.Run, b.Source}, "\x00")
	})
	sort.Slice(p.Evidence, func(i, j int) bool {
		a, b := p.Evidence[i], p.Evidence[j]
		return strings.Join([]string{a.Kind, a.Source, a.Value, string(a.Confidence)}, "\x00") < strings.Join([]string{b.Kind, b.Source, b.Value, string(b.Confidence)}, "\x00")
	})
	p.Stacks = uniqueSorted(p.Stacks)
	p.Unknowns = uniqueSorted(p.Unknowns)
}

func canonicalDirectory(start string) (string, error) {
	if start == "" {
		return "", errors.New("project profile: project directory is required")
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("project profile: resolve directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("project profile: resolve directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("project profile: inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project profile: %q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func repositoryRoot(ctx context.Context, start string) (string, error) {
	out, err := runGit(ctx, start, 32<<10, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r")
	if root == "" {
		return "", errors.New("git returned an empty repository root")
	}
	return canonicalDirectory(root)
}

type inventoryView struct {
	// Paths is the deterministic, display-sized inventory summary. Candidates
	// are tracked independently so a manifest after this cap is never skipped.
	Paths            []string
	Candidates       []string
	Total            int
	Scanned          int
	ScannedBytes     int
	Excluded         int
	UnsafePaths      int
	DisplayTruncated bool
	ScanTruncated    bool
	Digest           string
}

type inventoryCollector struct {
	paths        map[string]struct{}
	scanned      int
	scannedBytes int
	excluded     int
	unsafePaths  int
	truncated    bool
}

func newInventoryCollector() *inventoryCollector {
	return &inventoryCollector{paths: make(map[string]struct{}, maxInventoryFiles)}
}

// add accounts for one raw inventory path. False means the deterministic scan
// budget has been exhausted and the caller must stop traversing. Every safe,
// non-secret path inside the accepted boundary is retained until finalize, so
// discovery candidates cannot disappear merely because the display cap was
// reached first.
func (collector *inventoryCollector) add(relative string, retain bool) bool {
	if relative == "" {
		return true
	}
	if collector.scanned >= maxInventoryScanFiles || len(relative) > maxInventoryScanBytes-collector.scannedBytes {
		collector.truncated = true
		return false
	}
	collector.scanned++
	collector.scannedBytes += len(relative)
	relative = filepath.ToSlash(relative)
	if !safeRelativePath(relative) {
		collector.unsafePaths++
		return true
	}
	if secretLikePath(relative) {
		collector.excluded++
		return true
	}
	if retain {
		collector.paths[relative] = struct{}{}
	}
	return true
}

func (collector *inventoryCollector) finalize() inventoryView {
	all := make([]string, 0, len(collector.paths))
	for relative := range collector.paths {
		all = append(all, relative)
	}
	sort.Strings(all)

	displayCount := min(len(all), maxInventoryFiles)
	paths := append([]string(nil), all[:displayCount]...)
	candidates := make([]string, 0, min(len(all), maxEvidence))
	digest := sha256.New()
	for _, relative := range all {
		writeFingerprintField(digest, "path", relative)
		if inventoryDiscoveryCandidate(relative) {
			candidates = append(candidates, relative)
		}
	}
	return inventoryView{
		Paths:            paths,
		Candidates:       candidates,
		Total:            len(all),
		Scanned:          collector.scanned,
		ScannedBytes:     collector.scannedBytes,
		Excluded:         collector.excluded,
		UnsafePaths:      collector.unsafePaths,
		DisplayTruncated: len(all) > maxInventoryFiles,
		ScanTruncated:    collector.truncated,
		Digest:           fmt.Sprintf("%x", digest.Sum(nil)),
	}
}

func inventoryDiscoveryCandidate(relative string) bool {
	if _, ok := lockfileManager(filepath.Base(relative)); ok {
		return true
	}
	return contentDiscoveryCandidate(relative)
}

func inventory(ctx context.Context, root string, useGit bool) (inventoryView, bool, error) {
	if useGit {
		out, err := runGit(ctx, root, maxGitOutputBytes, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--")
		if err == nil {
			view, parseErr := parseInventory(out)
			return view, true, parseErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventoryView{}, false, ctxErr
		}
	}
	view, err := walkInventory(ctx, root)
	return view, false, err
}

func parseInventory(out []byte) (inventoryView, error) {
	if len(out) == 0 {
		return newInventoryCollector().finalize(), nil
	}
	if out[len(out)-1] != 0 {
		return inventoryView{}, errors.New("project profile: malformed Git inventory")
	}
	collector := newInventoryCollector()
	payload := out[:len(out)-1]
	for len(payload) > 0 {
		end := bytes.IndexByte(payload, 0)
		if end < 0 {
			end = len(payload)
		}
		if !collector.add(string(payload[:end]), true) {
			break
		}
		if end == len(payload) {
			break
		}
		payload = payload[end+1:]
	}
	return collector.finalize(), nil
}

func walkInventory(ctx context.Context, root string) (inventoryView, error) {
	collector := newInventoryCollector()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if !collector.add(relative, !entry.IsDir()) {
			return fs.SkipAll
		}
		if !safeRelativePath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if skippedDirectory(relative) || secretLikePath(relative+"/placeholder") {
				return filepath.SkipDir
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return inventoryView{}, err
	}
	return collector.finalize(), nil
}

func runGit(ctx context.Context, dir string, maxBytes int, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	gitArgs := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}
	gitArgs = append(gitArgs, args...)
	cmd := exec.CommandContext(ctx, gitPath, gitArgs...)
	cmd.Dir = dir
	cmd.Env = safeGitEnvironment()
	out := &boundedBuffer{max: maxBytes}
	errOut := &boundedBuffer{max: 32 << 10}
	cmd.Stdout = out
	cmd.Stderr = errOut
	runErr := cmd.Run()
	if out.exceeded || errOut.exceeded {
		return nil, errors.New("project profile: Git output exceeded the safety limit")
	}
	if runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("project profile: Git metadata unavailable: %w", runErr)
	}
	return append([]byte(nil), out.Bytes()...), nil
}

type boundedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		remaining := b.max - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		b.exceeded = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func safeGitEnvironment() []string {
	const optionalLocks = "GIT_OPTIONAL_LOCKS"
	const terminalPrompt = "GIT_TERMINAL_PROMPT"
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if name != optionalLocks && name != terminalPrompt {
			env = append(env, item)
		}
	}
	return append(env, optionalLocks+"=0", terminalPrompt+"=0")
}

func safeMetadata(root, relative string) error {
	_, err := regularMetadataNoFollow(root, relative, true)
	return err
}

// regularMetadataNoFollow validates every path component with Lstat. The
// final size limit is optional so lockfiles can be recognized exclusively by
// metadata even when a package manager has produced a very large file.
func regularMetadataNoFollow(root, relative string, enforceSizeLimit bool) (os.FileInfo, error) {
	if !safeRelativePath(relative) {
		return nil, errCandidateNonRegular
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))), "/")
	current := root
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errCandidateNonRegular
		}
		current = filepath.Join(current, filepath.FromSlash(part))
		if !withinRoot(root, current) {
			return nil, errCandidateNonRegular
		}
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errCandidateSymlink
		}
		if index < len(parts)-1 {
			if !info.IsDir() {
				return nil, errCandidateNonRegular
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, errCandidateNonRegular
		}
		if enforceSizeLimit && info.Size() > maxCandidateBytes {
			return nil, errCandidateOversize
		}
		return info, nil
	}
	return nil, errCandidateNonRegular
}

func readCandidate(root, relative string) ([]byte, error) {
	if err := safeMetadata(root, relative); err != nil {
		return nil, err
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	before, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if before.Size() > maxCandidateBytes {
		return nil, errCandidateOversize
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameFileSnapshot(before, opened) {
		return nil, errCandidateChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCandidateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCandidateBytes {
		return nil, errCandidateOversize
	}
	after, err := os.Lstat(full)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !sameFileSnapshot(before, after) {
		return nil, errCandidateChanged
	}
	if binaryContent(data) {
		return nil, errCandidateBinary
	}
	return data, nil
}

func sameFileSnapshot(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func binaryContent(data []byte) bool {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	if len(data) == 0 {
		return false
	}
	controls := 0
	for _, value := range data {
		if value < 0x20 && value != '\n' && value != '\r' && value != '\t' && value != '\f' {
			controls++
		}
	}
	return controls*100 > len(data)*2
}

func safeRelativePath(relative string) bool {
	if relative == "" || strings.ContainsAny(relative, "\x00\r\n\t") || filepath.IsAbs(relative) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func withinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func skippedDirectory(relative string) bool {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(relative)))
	switch base {
	case ".git", "node_modules", "vendor", "dist", "build", "out", "target", "coverage", "__pycache__", ".cache", ".next", ".turbo", ".gradle", ".idea", ".vscode", ".terraform", ".venv", "venv":
		return true
	default:
		return false
	}
}

func secretLikePath(relative string) bool {
	lower := strings.ToLower(filepath.ToSlash(relative))
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		switch part {
		case ".ssh", ".aws", ".kube", ".gnupg", ".azure", ".gcloud", "credentials", "secrets", "secret", "vault":
			return true
		}
	}
	base := parts[len(parts)-1]
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasPrefix(base, ".env-") {
		return true
	}
	switch base {
	case ".npmrc", ".pypirc", ".netrc", ".yarnrc", ".yarnrc.yml", "pip.conf", "credentials.json", "service-account.json":
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".tfstate", ".tfstate.backup"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.Contains(base, "credential") || strings.Contains(base, "secret") || strings.Contains(base, "private-key")
}

func lockfileManager(base string) (string, bool) {
	switch strings.ToLower(base) {
	case "go.sum":
		return "go", true
	case "package-lock.json", "npm-shrinkwrap.json":
		return "npm", true
	case "pnpm-lock.yaml":
		return "pnpm", true
	case "yarn.lock":
		return "yarn", true
	case "bun.lock", "bun.lockb":
		return "bun", true
	case "uv.lock":
		return "uv", true
	case "poetry.lock":
		return "poetry", true
	case "pdm.lock":
		return "pdm", true
	case "pipfile.lock":
		return "pipenv", true
	case "cargo.lock":
		return "cargo", true
	default:
		return "", false
	}
}

func isCIPath(relative string) bool {
	lower := strings.ToLower(filepath.ToSlash(relative))
	if lower == ".gitlab-ci.yml" || lower == ".gitlab-ci.yaml" {
		return true
	}
	if !strings.HasPrefix(lower, ".github/workflows/") {
		return false
	}
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

func usefulCommandName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, token := range []string{"build", "test", "check", "lint", "fmt", "format", "vet", "typecheck", "type-check", "dev", "start", "run", "e2e", "audit", "vuln", "security", "release", "verify"} {
		if name == token || strings.HasPrefix(name, token+":") || strings.HasPrefix(name, token+"-") || strings.HasPrefix(name, token+"_") {
			return true
		}
	}
	return false
}

func validScriptName(name string) bool {
	if name == "" || len(name) > 80 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("_.:-", r) {
			return false
		}
	}
	return true
}

func makeTargets(data []byte) []string {
	seen := map[string]bool{}
	var targets []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' || strings.HasPrefix(line, "#") {
			continue
		}
		left, _, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(left, "=%$(){}[]*?\\") {
			continue
		}
		for _, target := range strings.Fields(left) {
			if validTarget(target) && !seen[target] {
				seen[target] = true
				targets = append(targets, target)
			}
		}
	}
	sort.Strings(targets)
	return targets
}

func validTarget(value string) bool {
	if value == "" || value[0] == '.' || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("_.-", r) {
			return false
		}
	}
	return true
}

func prefixedLine(data []byte, prefix string) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func tomlName(data []byte, wantedSection string) string {
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		if section != wantedSection {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "name" {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			return safeFact(unquoted)
		}
		return safeFact(strings.Trim(value, "'\""))
	}
	return ""
}

func safeFact(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 240 {
		value = value[:240]
	}
	var out strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			out.WriteRune(' ')
		} else {
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func cleanUnitPath(path string) string {
	path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if path == "" || path == "/" || path == "." {
		return "."
	}
	return strings.TrimPrefix(path, "./")
}

func skippedCandidate(relative string, err error) string {
	reason := "unreadable"
	switch {
	case errors.Is(err, errCandidateSymlink):
		reason = "symlink"
	case errors.Is(err, errCandidateNonRegular):
		reason = "non-regular file"
	case errors.Is(err, errCandidateOversize):
		reason = "oversize file"
	case errors.Is(err, errCandidateBinary):
		reason = "binary file"
	case errors.Is(err, errCandidateChanged):
		reason = "file changed during discovery"
	case errors.Is(err, errDiscoveryReadBudget):
		reason = "aggregate read-budget"
	}
	return fmt.Sprintf("Skipped %s candidate %q.", reason, relative)
}

func cloneUnits(units []Unit) []Unit {
	out := make([]Unit, len(units))
	for i, unit := range units {
		out[i] = unit
		out[i].Stacks = append([]string(nil), unit.Stacks...)
		out[i].Manifests = append([]string(nil), unit.Manifests...)
		out[i].Lockfiles = append([]string(nil), unit.Lockfiles...)
	}
	return out
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if value == "" || len(out) > 0 && out[len(out)-1] == value {
			continue
		}
		out = append(out, value)
	}
	return out
}
