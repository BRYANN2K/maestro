package projectprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDiscoverMixedMonorepo(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/maestro\n\ngo 1.26.5\n")
	writeFixture(t, root, "go.sum", "example.com/dependency v1.0.0 h1:sum\n")
	writeFixture(t, root, "Makefile", ".PHONY: build test check\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\ncheck: test\n")
	writeFixture(t, root, "web/package.json", `{"name":"web-ui","scripts":{"build":"vite build","test":"vitest","lint":"eslint .","postinstall":"evil","test:unit;touch":"never"},"workspaces":["packages/*"]}`)
	writeFixture(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeFixture(t, root, "python/pyproject.toml", "[project]\nname = \"worker\"\n[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n")
	writeFixture(t, root, "python/uv.lock", "version = 1\n")
	writeFixture(t, root, "rust/Cargo.toml", "[package]\nname = \"engine\"\nversion = \"0.1.0\"\n")
	writeFixture(t, root, "rust/Cargo.lock", "version = 4\n")
	writeFixture(t, root, ".github/workflows/ci.yml", "name: CI\njobs:\n  test:\n    steps:\n      - run: make check\n")

	profile, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	for _, stack := range []string{"go", "node", "python", "rust"} {
		if !contains(profile.Stacks, stack) {
			t.Errorf("stacks %v missing %q", profile.Stacks, stack)
		}
	}
	for _, path := range []string{".", "python", "rust", "web"} {
		if !hasUnit(profile, path) {
			t.Errorf("units %+v missing %q", profile.Units, path)
		}
	}
	for _, command := range []struct{ name, run, cwd string }{
		{"check", "make check", "."},
		{"test", "pnpm run test", "web"},
		{"test", "python -m pytest", "python"},
		{"build", "cargo build", "rust"},
	} {
		if !hasCommand(profile, command.name, command.run, command.cwd) {
			t.Errorf("commands %+v missing %+v", profile.Commands, command)
		}
	}
	if !hasEvidence(profile, "ci", ".github/workflows/ci.yml") {
		t.Errorf("evidence %+v missing CI", profile.Evidence)
	}
	if serialized := profileString(t, profile); strings.Contains(serialized, "postinstall") || strings.Contains(serialized, "test:unit;touch") || strings.Contains(serialized, "vite build") {
		t.Errorf("unsafe or implementation script details leaked into profile: %s", serialized)
	}
	for _, lock := range []string{"go.sum", "pnpm-lock.yaml", "uv.lock", "Cargo.lock"} {
		if !hasLockfile(profile, lock) {
			t.Errorf("units %+v missing lockfile %q", profile.Units, lock)
		}
	}
}

func TestGitInventoryUsesCanonicalRootAndIgnoresIgnoredFiles(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runFixture(t, root, gitPath, "init", "-q")
	writeFixture(t, root, ".gitignore", "ignored/\n")
	writeFixture(t, root, "go.mod", "module example.com/root\n")
	writeFixture(t, root, "web/package.json", `{"name":"web","scripts":{"test":"vitest"}}`)
	writeFixture(t, root, "ignored/pyproject.toml", "[project]\nname = \"ignored-secret\"\n")
	runFixture(t, root, gitPath, "add", ".gitignore", "go.mod")

	profile, err := Discover(t.Context(), filepath.Join(root, "web"), ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Root != wantRoot {
		t.Fatalf("root = %q, want %q", profile.Root, wantRoot)
	}
	if !contains(profile.Stacks, "go") || !contains(profile.Stacks, "node") {
		t.Fatalf("stacks = %v, want tracked Go and untracked Node", profile.Stacks)
	}
	if contains(profile.Stacks, "python") || strings.Contains(profileString(t, profile), "ignored-secret") {
		t.Fatalf("ignored project leaked into profile: %+v", profile)
	}
}

func TestDiscoveryRefusesSecretsSymlinksBinaryAndOversize(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/safe\n")
	writeFixture(t, root, ".env", "TOPSECRET=never-leak\n")
	writeFixture(t, root, "secrets/package.json", `{"name":"never-leak","scripts":{"test":"steal"}}`)
	writeFixture(t, root, "binary/Cargo.toml", "[package]\x00\nname = \"bad\"\n")
	writeFixture(t, root, "large/pyproject.toml", strings.Repeat("x", maxCandidateBytes+1))
	writeFixture(t, root, "large/package-lock.json", strings.Repeat("x", maxCandidateBytes+1))
	writeFixture(t, root, "binary/yarn.lock", "version 1\x00binary")
	writeFixture(t, root, "web/package.json", `{"name":"safe-node","scripts":{"test":"vitest"}}`)
	writeFixture(t, root, "evil\nunit/package.json", `{"name":"adversarial","scripts":{"test":"node test.js"}}`)
	outside := filepath.Join(t.TempDir(), "Cargo.toml")
	if err := os.WriteFile(outside, []byte("[package]\nname = \"linked\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Join(root, "linked"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "linked", "Cargo.toml")); err != nil {
			t.Fatal(err)
		}
	}

	profile, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(profile.Stacks, "go") || !contains(profile.Stacks, "node") {
		t.Fatalf("safe stacks not detected: %v", profile.Stacks)
	}
	if contains(profile.Stacks, "python") || contains(profile.Stacks, "rust") {
		t.Fatalf("unsafe candidates affected stacks: %v", profile.Stacks)
	}
	// Lockfiles are identity signals, not configuration input. Discovery must
	// recognize regular lockfiles by no-follow metadata without reading or
	// rejecting them because a real-world lock graph is large/binary.
	if !hasLockfile(profile, "package-lock.json") {
		t.Fatalf("oversize lockfile was not recognized: %+v", profile.Units)
	}
	if !hasLockfile(profile, "yarn.lock") {
		t.Fatalf("binary lockfile was not recognized: %+v", profile.Units)
	}
	serialized := profileString(t, profile)
	for _, forbidden := range []string{"TOPSECRET", "never-leak", "secrets/package.json", "linked\""} {
		if strings.Contains(serialized, forbidden) {
			t.Errorf("profile leaked %q: %s", forbidden, serialized)
		}
	}
	for _, reason := range []string{"binary file", "oversize file"} {
		if !strings.Contains(serialized, reason) {
			t.Errorf("profile did not preserve %q unknown: %s", reason, serialized)
		}
	}
	if runtime.GOOS != "windows" && !strings.Contains(serialized, "symlink") {
		t.Errorf("profile did not preserve symlink unknown: %s", serialized)
	}

	content, err := Render(profile, AnswersFromProfile(profile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "evil") || strings.Contains(string(content), "adversarial") {
		t.Fatalf("adversarial path was not excluded from manifest:\n%s", content)
	}
	if !strings.Contains(string(content), "unsafe path name") {
		t.Fatalf("adversarial path exclusion was not preserved as an unknown:\n%s", content)
	}
}

func TestDiscoveryNeverExecutesProjectCommands(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	writeFixture(t, root, "package.json", `{"name":"safe","scripts":{"test":"touch executed","build":"touch executed"}}`)
	writeFixture(t, root, "Makefile", "test:\n\ttouch executed\n")
	writeFixture(t, root, "Cargo.toml", "[package]\nname = \"safe\"\nversion = \"0.1.0\"\n")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	for _, name := range []string{"go", "npm", "pnpm", "yarn", "bun", "make", "python", "cargo", "sh", "bash"} {
		writeExecutable(t, fakeBin, name, "#!/bin/sh\ntouch "+shellSingleQuote(marker)+"\n")
	}
	t.Setenv("PATH", fakeBin)

	if _, err := Discover(t.Context(), root, ModeBrownfield); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository command executed during discovery: %v", err)
	}
}

func TestLargeUnreadableLockfileSelectsExactNodeManager(t *testing.T) {
	for _, tc := range []struct {
		lockfile string
		manager  string
		run      string
	}{
		{lockfile: "pnpm-lock.yaml", manager: "pnpm", run: "pnpm run test"},
		{lockfile: "yarn.lock", manager: "yarn", run: "yarn test"},
	} {
		t.Run(tc.manager, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "package.json", `{"name":"web","scripts":{"test":"vitest"}}`)
			lockPath := filepath.Join(root, tc.lockfile)
			writeFixture(t, root, tc.lockfile, strings.Repeat("x", maxCandidateBytes+64<<10))
			if err := os.Chmod(lockPath, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })

			profile, err := Discover(t.Context(), root, ModeBrownfield)
			if err != nil {
				t.Fatal(err)
			}
			if !hasLockfile(profile, tc.lockfile) {
				t.Fatalf("lockfiles = %+v, missing %s", profile.Units, tc.lockfile)
			}
			if !hasCommand(profile, "test", tc.run, ".") {
				t.Fatalf("commands = %+v, want exact manager command %q", profile.Commands, tc.run)
			}
		})
	}
}

func TestLockfileSymlinkIsRejectedWithoutFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires Unix-like permissions")
	}
	root := t.TempDir()
	writeFixture(t, root, "package.json", `{"name":"web","scripts":{"test":"vitest"}}`)
	outside := filepath.Join(t.TempDir(), "pnpm-lock.yaml")
	if err := os.WriteFile(outside, []byte("outside-secret-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "pnpm-lock.yaml")); err != nil {
		t.Fatal(err)
	}

	profile, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	if hasLockfile(profile, "pnpm-lock.yaml") {
		t.Fatalf("symlink lockfile affected discovery: %+v", profile.Units)
	}
	if !hasCommand(profile, "test", "npm run test", ".") {
		t.Fatalf("symlink lockfile selected a package manager: %+v", profile.Commands)
	}
	serialized := profileString(t, profile)
	if !strings.Contains(serialized, "symlink") || strings.Contains(serialized, "outside-secret-marker") {
		t.Fatalf("symlink handling leaked content or lost reason: %s", serialized)
	}
}

func TestDraftRejectsStaleDiscoveryWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(t *testing.T, root string)
		drift  func(t *testing.T, root string)
	}{
		{
			name: "go manifest",
			before: func(t *testing.T, root string) {
				writeFixture(t, root, "go.mod", "module example.com/old\n")
			},
			drift: func(t *testing.T, root string) {
				writeFixture(t, root, "go.mod", "module example.com/new\n")
			},
		},
		{
			name: "node manifest",
			before: func(t *testing.T, root string) {
				writeFixture(t, root, "package.json", `{"name":"old","scripts":{"test":"vitest"}}`)
			},
			drift: func(t *testing.T, root string) {
				writeFixture(t, root, "package.json", `{"name":"new","scripts":{"test":"vitest"}}`)
			},
		},
		{
			name: "lockfile metadata",
			before: func(t *testing.T, root string) {
				writeFixture(t, root, "package.json", `{"name":"web","scripts":{"test":"vitest"}}`)
				writeFixture(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
			},
			drift: func(t *testing.T, root string) {
				writeFixture(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\npackages: {}\n")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.before(t, root)
			profile, err := Discover(t.Context(), root, ModeBrownfield)
			if err != nil {
				t.Fatal(err)
			}
			answers := AnswersFromProfile(profile)
			answers.Purpose = "Keep discovery and the reviewed contract coherent."
			tc.drift(t, root)

			path, _, err := Draft(t.Context(), profile, answers)
			if !errors.Is(err, ErrRepositoryChanged) {
				t.Fatalf("Draft error = %v, want repository drift", err)
			}
			if path != "" {
				t.Fatalf("stale Draft path = %q, want no staged target", path)
			}
			if _, statErr := os.Lstat(ManifestPath(root)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("stale Draft wrote MAESTRO.md: %v", statErr)
			}
		})
	}
}

func TestDraftRejectsGitHEADDriftWithStableInventory(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runFixture(t, root, gitPath, "init", "-q")
	runFixture(t, root, gitPath, "config", "user.email", "maestro@example.test")
	runFixture(t, root, gitPath, "config", "user.name", "Maestro Test")
	writeFixture(t, root, "go.mod", "module example.com/head\n")
	writeFixture(t, root, "README.md", "one\n")
	runFixture(t, root, gitPath, "add", "go.mod", "README.md")
	runFixture(t, root, gitPath, "commit", "-q", "-m", "first")

	profile, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	answers := AnswersFromProfile(profile)
	answers.Purpose = "Track the exact reviewed revision."
	writeFixture(t, root, "README.md", "two\n")
	runFixture(t, root, gitPath, "add", "README.md")
	runFixture(t, root, gitPath, "commit", "-q", "-m", "second")

	if _, _, err := Draft(t.Context(), profile, answers); !errors.Is(err, ErrRepositoryChanged) {
		t.Fatalf("Draft error = %v, want HEAD drift", err)
	}
}

func TestRenderIsDeterministicAndSharesSchemaAcrossModes(t *testing.T) {
	greenProfile, greenAnswers, err := GreenfieldDefaults(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	greenAnswers.Name = "sample"
	greenAnswers.Purpose = "Provide a deterministic sample."
	first, err := Render(greenProfile, greenAnswers)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(greenProfile, greenAnswers)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("greenfield render is not byte-idempotent:\n%s\n---\n%s", first, second)
	}

	brownRoot := t.TempDir()
	writeFixture(t, brownRoot, "go.mod", "module example.com/brown\n")
	brownProfile, err := Discover(t.Context(), brownRoot, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	brown, err := Render(brownProfile, AnswersFromProfile(brownProfile))
	if err != nil {
		t.Fatal(err)
	}
	brownProfileAgain, err := Discover(t.Context(), brownRoot, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	brownAgain, err := Render(brownProfileAgain, AnswersFromProfile(brownProfileAgain))
	if err != nil {
		t.Fatal(err)
	}
	if string(brown) != string(brownAgain) {
		t.Fatalf("brownfield discovery/render is not byte-idempotent:\n%s\n---\n%s", brown, brownAgain)
	}
	for name, content := range map[string][]byte{"green": first, "brown": brown} {
		for _, want := range []string{"maestro_schema: 1", "evidence_fingerprint: \"sha256:", "# Maestro Project Contract"} {
			if !strings.Contains(string(content), want) {
				t.Errorf("%s manifest missing %q:\n%s", name, want, content)
			}
		}
		if strings.Contains(string(content), "generated_at") || strings.Contains(string(content), "last_updated") {
			t.Errorf("%s manifest contains volatile metadata:\n%s", name, content)
		}
	}
	if !strings.Contains(string(first), `mode: "greenfield"`) || !strings.Contains(string(brown), `mode: "brownfield"`) {
		t.Fatalf("mode fields differ from contract: green=%s brown=%s", first, brown)
	}
}

func TestValidateManagedManifestRejectsMalformedFingerprintAndShape(t *testing.T) {
	root := t.TempDir()
	content, err := Render(ProjectProfile{
		SchemaVersion: SchemaVersion,
		Mode:          ModeBrownfield,
		Root:          root,
		Name:          "validated",
	}, Answers{
		SchemaVersion: SchemaVersion,
		Mode:          ModeBrownfield,
		Name:          "validated",
		Purpose:       "Keep the project contract exact.\nmode: \"brownfield\"\nevidence_fingerprint: \"sha256:" + contentHashZeros + "\"",
		Safety:        []string{"Never expose secrets."},
		Verification:  []string{"go test ./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedManifest(content); err != nil {
		t.Fatalf("valid generated manifest rejected: %v", err)
	}
	for _, malformed := range [][]byte{
		[]byte("# Maestro Project Contract\n"),
		bytes.Replace(content, []byte("evidence_fingerprint: \"sha256:"), []byte("evidence_fingerprint: \"sha256:xyz"), 1),
		bytes.Replace(content, []byte("## Safety boundaries"), []byte("## Hidden boundaries"), 1),
	} {
		if err := ValidateManagedManifest(malformed); err == nil {
			t.Fatalf("malformed manifest accepted:\n%s", malformed)
		}
	}
}

func TestReconcileExistingAllowsManagedDriftAndRejectsUnmanagedContent(t *testing.T) {
	root := t.TempDir()
	profile, answers, err := GreenfieldDefaults(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := Render(profile, answers)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileExisting(t.Context(), root, draft); err != nil {
		t.Fatalf("missing manifest rejected: %v", err)
	}
	writeFixture(t, root, ManifestName, string(draft))
	if err := ReconcileExisting(t.Context(), root, draft); err != nil {
		t.Fatalf("exact manifest rejected: %v", err)
	}
	answers.Purpose = "A repository whose reviewed contract evolved."
	updated, err := Render(profile, answers)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileExisting(t.Context(), root, updated); err != nil {
		t.Fatalf("managed manifest drift rejected: %v", err)
	}
	writeFixture(t, root, ManifestName, "# Human project notes\n\nDo not replace this file.\n")
	if err := ReconcileExisting(t.Context(), root, updated); !errors.Is(err, ErrManifestConflict) {
		t.Fatalf("unmanaged manifest error = %v, want conflict", err)
	}
	writeFixture(t, root, ManifestName, string(draft)+"\x1b[31m")
	if err := ReconcileExisting(t.Context(), root, updated); !errors.Is(err, ErrManifestConflict) {
		t.Fatalf("control-tainted managed manifest error = %v, want conflict", err)
	}
}

func TestCancellationIsHonored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, t.TempDir(), ModeBrownfield); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover error = %v, want context cancellation", err)
	}
	if _, _, err := GreenfieldDefaults(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("GreenfieldDefaults error = %v, want context cancellation", err)
	}
}

func TestInventoryLimitIsExplicit(t *testing.T) {
	var inventory strings.Builder
	for i := 0; i <= maxInventoryFiles; i++ {
		inventory.WriteString("unit/file-")
		inventory.WriteString(strconv.Itoa(i))
		inventory.WriteByte(0)
	}
	view, err := parseInventory([]byte(inventory.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !view.DisplayTruncated || view.ScanTruncated || len(view.Paths) != maxInventoryFiles || view.Total != maxInventoryFiles+1 {
		t.Fatalf("inventory view = %+v", view)
	}
}

func TestInventoryKeepsDiscoveryCandidatesAfterDisplayCap(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < maxInventoryFiles+32; i++ {
		fmt.Fprintf(&raw, "a/source-%05d.txt%c", i, byte(0))
	}
	for _, path := range []string{
		"zzzz/service/package.json",
		"zzzz/pnpm-lock.yaml",
		".github/workflows/late.yml",
	} {
		raw.WriteString(path)
		raw.WriteByte(0)
	}
	view, err := parseInventory([]byte(raw.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !view.DisplayTruncated || view.ScanTruncated || len(view.Paths) != maxInventoryFiles {
		t.Fatalf("inventory limits = paths:%d display:%v scan:%v", len(view.Paths), view.DisplayTruncated, view.ScanTruncated)
	}
	for _, candidate := range []string{"zzzz/service/package.json", "zzzz/pnpm-lock.yaml", ".github/workflows/late.yml"} {
		if !contains(view.Candidates, candidate) {
			t.Errorf("candidates %v missing %q", view.Candidates, candidate)
		}
	}

	root := t.TempDir()
	writeFixture(t, root, "zzzz/service/package.json", `{"name":"late-web","scripts":{"test":"vitest"}}`)
	writeFixture(t, root, "zzzz/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeFixture(t, root, ".github/workflows/late.yml", "steps:\n  - run: pnpm test\n")
	profile := ProjectProfile{SchemaVersion: SchemaVersion, Mode: ModeBrownfield, Root: root, Name: "late"}
	discovery := discovery{
		profile: &profile, root: root, units: map[string]*unitAccumulator{},
		stackSet: map[string]bool{}, commandSet: map[string]bool{},
	}
	discovery.scan(t.Context(), view.Candidates)
	discovery.finish()
	profile.normalize()
	if !contains(profile.Stacks, "node") || !hasLockfile(profile, "pnpm-lock.yaml") || !hasCommand(profile, "test", "pnpm run test", "zzzz/service") {
		t.Fatalf("late discovery candidates were not analyzed: %+v", profile)
	}
	if !hasEvidence(profile, "ci", ".github/workflows/late.yml") {
		t.Fatalf("late CI candidate was not analyzed: %+v", profile.Evidence)
	}
}

func TestInventoryDigestCoversPathsBeyondDisplayCap(t *testing.T) {
	makeInventory := func(last string) inventoryView {
		var raw strings.Builder
		for i := 0; i < maxInventoryFiles; i++ {
			fmt.Fprintf(&raw, "a/source-%05d.txt%c", i, byte(0))
		}
		raw.WriteString(last)
		raw.WriteByte(0)
		view, err := parseInventory([]byte(raw.String()))
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	before := makeInventory("zzzz/beyond-a.txt")
	after := makeInventory("zzzz/beyond-b.txt")
	if strings.Join(before.Paths, "\x00") != strings.Join(after.Paths, "\x00") {
		t.Fatal("display-sized inventory unexpectedly changed")
	}
	if before.Digest == after.Digest {
		t.Fatal("inventory digest ignored a path beyond the display cap")
	}
}

func TestInventoryScanHasIndependentHardBound(t *testing.T) {
	var raw strings.Builder
	for i := 0; i <= maxInventoryScanFiles; i++ {
		fmt.Fprintf(&raw, "bounded/file-%05d.txt%c", i, byte(0))
	}
	view, err := parseInventory([]byte(raw.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !view.ScanTruncated || view.Scanned != maxInventoryScanFiles || view.Total != maxInventoryScanFiles {
		t.Fatalf("scan boundary = truncated:%v scanned:%d total:%d", view.ScanTruncated, view.Scanned, view.Total)
	}
}

func TestRenderRejectsPathsOutsideRepository(t *testing.T) {
	profile, answers, err := GreenfieldDefaults(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	answers.Units = []Unit{{Path: "../outside"}}
	if _, err := Render(profile, answers); err == nil || !strings.Contains(err.Error(), "safe repository-relative path") {
		t.Fatalf("unsafe unit path error = %v", err)
	}
	answers.Units = nil
	answers.Commands = []Command{{Name: "test", Run: "go test ./...", Cwd: "/tmp"}}
	if _, err := Render(profile, answers); err == nil || !strings.Contains(err.Error(), "safe repository-relative path") {
		t.Fatalf("unsafe command cwd error = %v", err)
	}
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, root, name, content string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".bat"
		content = "@echo off\r\nexit /b 99\r\n"
	}
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runFixture(t *testing.T, dir, binary string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, args, err, output)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func hasUnit(profile ProjectProfile, path string) bool {
	for _, unit := range profile.Units {
		if unit.Path == path {
			return true
		}
	}
	return false
}

func hasCommand(profile ProjectProfile, name, run, cwd string) bool {
	for _, command := range profile.Commands {
		if command.Name == name && command.Run == run && command.Cwd == cwd {
			return true
		}
	}
	return false
}

func hasEvidence(profile ProjectProfile, kind, source string) bool {
	for _, evidence := range profile.Evidence {
		if evidence.Kind == kind && evidence.Source == source {
			return true
		}
	}
	return false
}

func hasLockfile(profile ProjectProfile, name string) bool {
	for _, unit := range profile.Units {
		if contains(unit.Lockfiles, name) {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func profileString(t *testing.T, profile ProjectProfile) string {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
