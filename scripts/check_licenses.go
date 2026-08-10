//go:build ignore

// check_licenses verifies Maestro's distributable third-party notices.
//
// Run from any directory inside the repository with:
//
//	go run ./scripts/check_licenses.go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

const manifestPath = "LICENSES/manifest.json"

var releaseTargets = []string{
	"darwin/amd64",
	"darwin/arm64",
	"linux/amd64",
	"linux/arm64",
	"windows/amd64",
	"windows/arm64",
}

var requiredSupplementalFiles = []string{
	"LICENSES/data/Liberation-Mono-OFL-1.1",
	"LICENSES/data/Unicode-LICENSE-v3",
	"LICENSES/data/github-gemoji-LICENSE",
	"LICENSES/data/models.dev-LICENSE",
	"LICENSES/provenance/Crush-FSL-1.1-MIT",
}

var historicalCrushPorts = []string{
	"Archive/internal/tui/glamour.go",
	"Archive/internal/tui/diff.go",
	"Archive/internal/tui/list.go",
	"Archive/internal/tui/pills.go",
}

var historicalCrushMarkers = []string{
	"type diffSignal struct {",
	"func inspectDiff(content string) diffSignal {",
	"func looksLikeDiff(content string) bool {",
	"type parsedDiffFile struct {",
	"func parseUnifiedDiff(content string) []parsedDiffFile {",
	"type Versioned struct {",
	"func renderTodoPill(m Model) string {",
	"func renderQueuePill(m Model) string {",
}

type manifest struct {
	Schema       int            `json:"schema"`
	Command      string         `json:"command"`
	Targets      []string       `json:"targets"`
	Modules      []moduleNotice `json:"modules"`
	Supplemental []supplemental `json:"supplemental"`
}

type moduleNotice struct {
	Path          string       `json:"path"`
	Version       string       `json:"version"`
	Targets       []string     `json:"targets"`
	Files         []noticeFile `json:"files"`
	LicenseSource string       `json:"license_source,omitempty"`
	Note          string       `json:"note,omitempty"`
}

type supplemental struct {
	Name             string       `json:"name"`
	Source           string       `json:"source"`
	AppliesToModules []string     `json:"applies_to_modules,omitempty"`
	CurrentFiles     []string     `json:"current_files,omitempty"`
	ArchivedPorts    []string     `json:"archived_ports,omitempty"`
	ReleaseBlocker   bool         `json:"release_blocker,omitempty"`
	Files            []noticeFile `json:"files"`
}

type noticeFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type listedPackage struct {
	Module *listedModule `json:"Module"`
}

type listedModule struct {
	Path    string        `json:"Path"`
	Version string        `json:"Version"`
	Main    bool          `json:"Main"`
	Replace *listedModule `json:"Replace"`
}

type actualModule struct {
	Version string
	Targets map[string]bool
}

func main() {
	root, err := repositoryRoot()
	if err != nil {
		fatal(err)
	}

	m, rawManifest, err := loadManifest(root)
	if err != nil {
		fatal(err)
	}
	if err := validateManifest(root, m, rawManifest); err != nil {
		fatal(err)
	}
	if err := verifyLinkedGraph(root, m); err != nil {
		fatal(err)
	}
	if err := verifyNotice(root, m); err != nil {
		fatal(err)
	}
	if err := verifyNPMMirror(root); err != nil {
		fatal(err)
	}
	if err := verifyPackagingConfig(root); err != nil {
		fatal(err)
	}

	fmt.Printf("license audit passed: %d linked modules, %d supplemental notices, %d release targets\n", len(m.Modules), len(m.Supplemental), len(m.Targets))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "license audit failed:", err)
	os.Exit(1)
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && info.Mode().IsRegular() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func loadManifest(root string) (manifest, []byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifestPath)))
	if err != nil {
		return manifest{}, nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return manifest{}, nil, fmt.Errorf("decode %s: %w", manifestPath, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return manifest{}, nil, fmt.Errorf("decode %s: %w", manifestPath, err)
	}
	return m, data, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func validateManifest(root string, m manifest, rawManifest []byte) error {
	if m.Schema != 1 {
		return fmt.Errorf("%s schema = %d, want 1", manifestPath, m.Schema)
	}
	if m.Command != "./cmd/maestro" {
		return fmt.Errorf("manifest command = %q, want ./cmd/maestro", m.Command)
	}
	if !equalStrings(m.Targets, releaseTargets) {
		return fmt.Errorf("manifest targets = %v, want %v in this order", m.Targets, releaseTargets)
	}
	if len(m.Modules) == 0 {
		return errors.New("manifest has no linked modules")
	}

	moduleSeen := make(map[string]bool, len(m.Modules))
	expectedFiles := make(map[string]string)
	lastModule := ""
	for _, module := range m.Modules {
		if module.Path == "" || module.Version == "" {
			return fmt.Errorf("module has empty path or version: %+v", module)
		}
		if moduleSeen[module.Path] {
			return fmt.Errorf("duplicate module mapping %q", module.Path)
		}
		moduleSeen[module.Path] = true
		if lastModule != "" && module.Path <= lastModule {
			return errors.New("module mappings must be strictly sorted by path")
		}
		lastModule = module.Path
		if len(module.Targets) == 0 || !sortedUnique(module.Targets) {
			return fmt.Errorf("module %s targets must be non-empty, sorted, and unique", module.Path)
		}
		for _, target := range module.Targets {
			if !contains(releaseTargets, target) {
				return fmt.Errorf("module %s has unknown target %q", module.Path, target)
			}
		}
		if len(module.Files) == 0 {
			return fmt.Errorf("module %s has no license file", module.Path)
		}
		for _, file := range module.Files {
			if err := addExpectedFile(expectedFiles, file); err != nil {
				return fmt.Errorf("module %s: %w", module.Path, err)
			}
		}
	}

	requiredSupplemental := make(map[string]bool, len(requiredSupplementalFiles))
	for _, name := range requiredSupplementalFiles {
		requiredSupplemental[name] = false
	}
	crushFound := false
	for _, item := range m.Supplemental {
		if item.Name == "" || item.Source == "" || len(item.Files) == 0 {
			return fmt.Errorf("incomplete supplemental mapping: %+v", item)
		}
		for _, file := range item.Files {
			if _, ok := requiredSupplemental[file.Path]; ok {
				requiredSupplemental[file.Path] = true
			}
			if err := addExpectedFile(expectedFiles, file); err != nil {
				return fmt.Errorf("supplemental %s: %w", item.Name, err)
			}
		}
		if strings.Contains(strings.ToLower(item.Name), "crush") {
			crushFound = true
			if item.ReleaseBlocker {
				return errors.New("deleted historical Crush ports must not be mapped as an active release blocker")
			}
			if len(item.CurrentFiles) != 0 {
				return errors.New("historical Crush provenance must not map unrelated active files")
			}
			for _, name := range historicalCrushPorts {
				if !contains(item.ArchivedPorts, name) {
					return fmt.Errorf("Crush provenance is missing historical port %s", name)
				}
			}
		}
	}
	if !crushFound {
		return errors.New("historical Crush provenance mapping is missing")
	}
	for name, found := range requiredSupplemental {
		if !found {
			return fmt.Errorf("required supplemental notice %s is not mapped", name)
		}
	}

	for rel, wantHash := range expectedFiles {
		full, err := safeRepositoryFile(root, rel)
		if err != nil {
			return err
		}
		gotHash, err := hashRegularFile(full)
		if err != nil {
			return fmt.Errorf("verify %s: %w", rel, err)
		}
		if gotHash != wantHash {
			return fmt.Errorf("%s SHA-256 = %s, want %s", rel, gotHash, wantHash)
		}
	}

	actualTree, err := collectTree(root, "LICENSES")
	if err != nil {
		return err
	}
	expectedFiles[manifestPath] = hashBytes(rawManifest)
	if err := compareExpectedTree(actualTree, expectedFiles); err != nil {
		return err
	}
	if err := verifyHistoricalCrushBoundary(root); err != nil {
		return err
	}
	return nil
}

func verifyHistoricalCrushBoundary(root string) error {
	for _, rel := range historicalCrushPorts {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		switch {
		case err == nil:
			return fmt.Errorf("historical Crush port %s is present in the release tree", rel)
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("inspect historical Crush port %s: %w", rel, err)
		}
	}

	internalRoot := filepath.Join(root, "internal")
	err := filepath.WalkDir(internalRoot, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(name) != ".go" {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		for _, marker := range historicalCrushMarkers {
			if bytes.Contains(data, []byte(marker)) {
				rel, relErr := filepath.Rel(root, name)
				if relErr != nil {
					return relErr
				}
				return fmt.Errorf("active source %s contains historical Crush port marker %q; manual provenance audit required", filepath.ToSlash(rel), marker)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify historical Crush boundary: %w", err)
	}
	return nil
}

func addExpectedFile(expected map[string]string, file noticeFile) error {
	if _, err := validateRelativePath(file.Path, "LICENSES"); err != nil {
		return err
	}
	if len(file.SHA256) != sha256.Size*2 {
		return fmt.Errorf("%s has invalid SHA-256 %q", file.Path, file.SHA256)
	}
	if _, err := hex.DecodeString(file.SHA256); err != nil {
		return fmt.Errorf("%s has invalid SHA-256 %q", file.Path, file.SHA256)
	}
	if old, ok := expected[file.Path]; ok && old != file.SHA256 {
		return fmt.Errorf("%s is mapped with conflicting hashes", file.Path)
	}
	expected[file.Path] = file.SHA256
	return nil
}

func verifyLinkedGraph(root string, m manifest) error {
	actual := make(map[string]actualModule)
	for _, target := range releaseTargets {
		parts := strings.Split(target, "/")
		if len(parts) != 2 {
			return fmt.Errorf("invalid release target %q", target)
		}
		modules, err := listModules(root, m.Command, parts[0], parts[1])
		if err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
		for modulePath, version := range modules {
			record := actual[modulePath]
			if record.Version != "" && record.Version != version {
				return fmt.Errorf("module %s resolves to both %s and %s", modulePath, record.Version, version)
			}
			if record.Targets == nil {
				record.Targets = make(map[string]bool)
			}
			record.Version = version
			record.Targets[target] = true
			actual[modulePath] = record
		}
	}

	declared := make(map[string]moduleNotice, len(m.Modules))
	for _, module := range m.Modules {
		declared[module.Path] = module
	}
	for modulePath, record := range actual {
		mapping, ok := declared[modulePath]
		if !ok {
			return fmt.Errorf("linked module %s@%s has no license mapping", modulePath, record.Version)
		}
		if mapping.Version != record.Version {
			return fmt.Errorf("module %s version drift: linked %s, mapped %s", modulePath, record.Version, mapping.Version)
		}
		actualTargets := mapKeys(record.Targets)
		if !equalStrings(mapping.Targets, actualTargets) {
			return fmt.Errorf("module %s target drift: linked %v, mapped %v", modulePath, actualTargets, mapping.Targets)
		}
	}
	for modulePath, mapping := range declared {
		if _, ok := actual[modulePath]; !ok {
			return fmt.Errorf("mapped module %s@%s is not linked by any release target", modulePath, mapping.Version)
		}
	}
	return nil
}

func listModules(root, command, goos, goarch string) (map[string]string, error) {
	cmd := exec.Command("go", "list", "-mod=readonly", "-deps", "-json", command)
	cmd.Dir = root
	cmd.Env = withEnvironment(os.Environ(), map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      goarch,
		"GOOS":        goos,
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	modules := make(map[string]string)
	dec := json.NewDecoder(&stdout)
	for {
		var pkg listedPackage
		err := dec.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if pkg.Module == nil || pkg.Module.Main {
			continue
		}
		if pkg.Module.Replace != nil {
			return nil, fmt.Errorf("module replacement for %s requires a manual license audit", pkg.Module.Path)
		}
		if pkg.Module.Path == "" || pkg.Module.Version == "" {
			return nil, fmt.Errorf("linked module has incomplete identity: %+v", pkg.Module)
		}
		if old, ok := modules[pkg.Module.Path]; ok && old != pkg.Module.Version {
			return nil, fmt.Errorf("module %s has conflicting versions %s and %s", pkg.Module.Path, old, pkg.Module.Version)
		}
		modules[pkg.Module.Path] = pkg.Module.Version
	}
	return modules, nil
}

func withEnvironment(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := overrides[strings.ToUpper(key)]; replace {
				continue
			}
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

func verifyNotice(root string, m manifest) error {
	data, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES.md"))
	if err != nil {
		return fmt.Errorf("read THIRD_PARTY_NOTICES.md: %w", err)
	}
	text := string(data)
	for _, module := range m.Modules {
		row := "| `" + module.Path + "` | `" + module.Version + "` |"
		if !strings.Contains(text, row) {
			return fmt.Errorf("THIRD_PARTY_NOTICES.md has no exact row for %s@%s", module.Path, module.Version)
		}
		for _, file := range module.Files {
			if !strings.Contains(text, "("+file.Path+")") {
				return fmt.Errorf("THIRD_PARTY_NOTICES.md does not link %s", file.Path)
			}
		}
	}
	for _, item := range m.Supplemental {
		for _, file := range item.Files {
			if !strings.Contains(text, "("+file.Path+")") {
				return fmt.Errorf("THIRD_PARTY_NOTICES.md does not link %s", file.Path)
			}
		}
		for _, name := range item.CurrentFiles {
			if !strings.Contains(text, "`"+name+"`") {
				return fmt.Errorf("THIRD_PARTY_NOTICES.md does not map current provenance file %s", name)
			}
		}
	}
	for _, statement := range []string{
		"Because the FSL-covered ports are deleted, unshipped, and have no active-code lineage, they are not mapped as a Maestro v1 release blocker.",
		"2028-07-20T20:08:59Z",
	} {
		if !strings.Contains(text, statement) {
			return fmt.Errorf("THIRD_PARTY_NOTICES.md is missing the audited Crush boundary statement %q", statement)
		}
	}
	return nil
}

func verifyNPMMirror(root string) error {
	rootNotice, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES.md"))
	if err != nil {
		return err
	}
	npmNotice, err := os.ReadFile(filepath.Join(root, "npm", "THIRD_PARTY_NOTICES.md"))
	if err != nil {
		return fmt.Errorf("read npm/THIRD_PARTY_NOTICES.md: %w", err)
	}
	if !bytes.Equal(rootNotice, npmNotice) {
		return errors.New("npm/THIRD_PARTY_NOTICES.md is not an exact mirror of the root notice")
	}

	rootTree, err := collectTree(root, "LICENSES")
	if err != nil {
		return err
	}
	npmTree, err := collectTree(root, "npm/LICENSES")
	if err != nil {
		return err
	}
	normalizedNPM := make(map[string]string, len(npmTree))
	for name, hash := range npmTree {
		normalizedNPM[strings.TrimPrefix(name, "npm/")] = hash
	}
	if err := compareExpectedTree(normalizedNPM, rootTree); err != nil {
		return fmt.Errorf("npm license mirror: %w", err)
	}
	return nil
}

func verifyPackagingConfig(root string) error {
	packageData, err := os.ReadFile(filepath.Join(root, "npm", "package.json"))
	if err != nil {
		return fmt.Errorf("read npm/package.json: %w", err)
	}
	var pkg struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(packageData, &pkg); err != nil {
		return fmt.Errorf("decode npm/package.json: %w", err)
	}
	for _, want := range []string{"THIRD_PARTY_NOTICES.md", "LICENSES"} {
		count := 0
		for _, name := range pkg.Files {
			normalized := strings.TrimPrefix(pathpkg.Clean(strings.ReplaceAll(name, "\\", "/")), "./")
			if normalized == want {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("npm/package.json files must include %q exactly once; got %d", want, count)
		}
	}

	releaseData, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		return fmt.Errorf("read .goreleaser.yaml: %w", err)
	}
	for _, want := range []string{"THIRD_PARTY_NOTICES.md", "LICENSES/**"} {
		if count := countYAMLListItem(string(releaseData), want); count != 1 {
			return fmt.Errorf(".goreleaser.yaml must include %q exactly once; got %d", want, count)
		}
	}
	return nil
}

func countYAMLListItem(data, want string) int {
	count := 0
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if before, _, found := strings.Cut(line, " #"); found {
			line = strings.TrimSpace(before)
		}
		line = strings.Trim(line, "\"'")
		if line == want {
			count++
		}
	}
	return count
}

func safeRepositoryFile(root, rel string) (string, error) {
	clean, err := validateRelativePath(rel, "LICENSES")
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	current := root
	for _, part := range strings.Split(clean, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("lstat %s: %w", clean, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("license path %s contains a symlink", clean)
		}
	}
	return full, nil
}

func validateRelativePath(rel, requiredRoot string) (string, error) {
	if rel == "" || strings.Contains(rel, "\\") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("unsafe license path %q", rel)
	}
	clean := pathpkg.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("non-canonical license path %q", rel)
	}
	if clean != requiredRoot && !strings.HasPrefix(clean, requiredRoot+"/") {
		return "", fmt.Errorf("license path %q is outside %s", rel, requiredRoot)
	}
	return clean, nil
}

func hashRegularFile(name string) (string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file (mode %s)", info.Mode())
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func collectTree(root, relRoot string) (map[string]string, error) {
	clean, err := validateRelativePath(relRoot, strings.Split(relRoot, "/")[0])
	if err != nil {
		return nil, err
	}
	fullRoot := filepath.Join(root, filepath.FromSlash(clean))
	files := make(map[string]string)
	err = filepath.WalkDir(fullRoot, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s contains symlink %s", clean, name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s contains non-regular file %s", clean, name)
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		hash, err := hashRegularFile(name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = hash
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", clean, err)
	}
	return files, nil
}

func compareExpectedTree(actual, expected map[string]string) error {
	for name, wantHash := range expected {
		gotHash, ok := actual[name]
		if !ok {
			return fmt.Errorf("missing file %s", name)
		}
		if gotHash != wantHash {
			return fmt.Errorf("file %s has hash %s, want %s", name, gotHash, wantHash)
		}
	}
	for name := range actual {
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("unmapped file %s", name)
		}
	}
	return nil
}

func sortedUnique(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] >= values[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mapKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
