package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeSkill(t *testing.T, root, name, fm, body string) string {
	t.Helper()
	path := filepath.Join(root, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\n" + fm + "---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestDiscoverMetadataOnlyAndExplicitInspect(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "refactor",
		"name: refactor\ndescription: Refactor code safely\nuser-invocable: true\n", "# Refactor\n\nRefactor safely.\n")
	writeSkill(t, filepath.Join(project, ".claude", "skills"), "testgen",
		"name: testgen\ndescription: Generate tests\nuser-invocable: false\n", "# Testgen\n")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "global-one",
		"name: global-one\ndescription: Global skill\n", "# Global\n")

	catalog := DiscoverCatalog(home, project, nil)
	if len(catalog.Skills) != 3 || len(catalog.Issues) != 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	for _, skill := range catalog.Skills {
		if skill.Content != "" {
			t.Fatalf("discovery eagerly loaded %s", skill.ID)
		}
	}
	if got := UserInvokable(catalog.Skills); len(got) != 2 {
		t.Fatalf("invokable = %d, want 2", len(got))
	}
	inspection, err := catalog.Inspect(t.Context(), "refactor")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(inspection.Content, "Refactor safely") || !strings.HasSuffix(inspection.Path, "SKILL.md") {
		t.Fatalf("inspection = %+v", inspection)
	}
	if strings.Contains(catalog.Skills[0].Prompt(), catalog.Skills[0].Path) {
		t.Fatal("compatibility prompt leaked a host path")
	}
}

func TestDiscoverEmpty(t *testing.T) {
	got := DiscoverCatalog(t.TempDir(), t.TempDir(), nil)
	if len(got.Skills) != 0 || len(got.Issues) != 0 {
		t.Errorf("catalog = %+v", got)
	}
}

func TestDiscoverExtraPath(t *testing.T) {
	extra := t.TempDir()
	writeSkill(t, extra, "custom", "name: custom\ndescription: Custom workflow\n", "# Custom\n")
	got := DiscoverCatalog(t.TempDir(), t.TempDir(), []string{extra})
	if len(got.Skills) != 1 || got.Skills[0].ID != "configured-01:custom" {
		t.Errorf("skills = %+v, issues=%+v", got.Skills, got.Issues)
	}
}

func TestDiscoveryRejectsSymlinksUnicodeControlsAndOversize(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	root := filepath.Join(project, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeSkill(t, outside, "linked", "name: linked\ndescription: Outside\n", "outside")
	if err := os.Symlink(filepath.Join(outside, "linked"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	fileLinkDir := filepath.Join(root, "file-link")
	if err := os.MkdirAll(fileLinkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "linked", "SKILL.md"), filepath.Join(fileLinkDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, root, "café", "name: café\ndescription: Unicode name\n", "body")
	writeSkill(t, root, "bidi", "name: bidi\ndescription: \"hidden\\u202Etext\"\n", "body")
	writeSkill(t, root, "oversize", "name: oversize\ndescription: Large\n", strings.Repeat("x", maxSkillBytes))

	catalog := DiscoverCatalog(home, project, nil)
	if len(catalog.Skills) != 0 {
		t.Fatalf("unsafe skills discovered: %+v", catalog.Skills)
	}
	joined := issuesText(catalog.Issues)
	for _, want := range []string{"skill directory must not be a symlink", "SKILL.md must be a regular file, not a symlink", "lowercase ASCII", "safe Unicode", "exceeds 128 KiB"} {
		if !strings.Contains(joined, want) {
			t.Errorf("issues missing %q: %s", want, joined)
		}
	}
}

func TestDiscoveryRejectsSymlinkedRootComponent(t *testing.T) {
	project := t.TempDir()
	realRoot := t.TempDir()
	writeSkill(t, filepath.Join(realRoot, "skills"), "escape", "name: escape\ndescription: Escaped\n", "body")
	if err := os.Symlink(realRoot, filepath.Join(project, ".agents")); err != nil {
		t.Fatal(err)
	}
	catalog := DiscoverCatalog(t.TempDir(), project, nil)
	if len(catalog.Skills) != 0 || !strings.Contains(issuesText(catalog.Issues), "not symlinks") {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestCollisionsRequireQualifiedID(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "review",
		"name: review\ndescription: Project review\n", "project")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "review",
		"name: review\ndescription: User review\n", "user")
	catalog := DiscoverCatalog(home, project, nil)
	if _, err := catalog.Resolve("review"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Resolve collision = %v", err)
	}
	projectSkill, err := catalog.Resolve("project:review")
	if err != nil || projectSkill.Description != "Project review" {
		t.Fatalf("qualified Resolve = %+v, %v", projectSkill, err)
	}
	manager := NewManager(ManagerOptions{Home: home, ProjectDir: project})
	summaries := manager.Summaries(t.Context())
	if len(summaries) != 2 || !summaries[0].Valid || !summaries[1].Valid || summaries[0].Warning == "" || summaries[1].Warning == "" || summaries[0].Error != "" || summaries[1].Error != "" {
		t.Fatalf("collision summaries = %+v", summaries)
	}
	for _, skill := range manager.SkillList(t.Context()) {
		if skill.Name != skill.ID {
			t.Fatalf("legacy palette name must be qualified on collision: %+v", skill)
		}
	}
}

func TestInspectFailsClosedAfterReplacementAndUnsafeBody(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	path := writeSkill(t, filepath.Join(project, ".agents", "skills"), "audit",
		"name: audit\ndescription: Audit code\n", "safe")
	catalog := DiscoverCatalog(home, project, nil)
	if err := os.WriteFile(path, []byte("---\nname: audit\ndescription: Audit code\n---\nchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Inspect(t.Context(), "audit"); err == nil || !strings.Contains(err.Error(), "changed since discovery") {
		t.Fatalf("Inspect replaced file = %v", err)
	}

	spoofPath := writeSkill(t, filepath.Join(project, ".agents", "skills"), "spoof",
		"name: spoof\ndescription: Same metadata\n", "safe")
	catalog = DiscoverCatalog(home, project, nil)
	before, err := os.Stat(spoofPath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(spoofPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(original), "safe", "evil", 1)
	if len(changed) != len(original) {
		t.Fatal("test rewrite must preserve size")
	}
	if err := os.WriteFile(spoofPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(spoofPath, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Inspect(t.Context(), "spoof"); err == nil || !strings.Contains(err.Error(), "content changed") {
		t.Fatalf("Inspect same-size/mtime spoof = %v", err)
	}

	writeSkill(t, filepath.Join(project, ".agents", "skills"), "unsafe-body",
		"name: unsafe-body\ndescription: Unsafe body\n", "hello\x1b[31m")
	catalog = DiscoverCatalog(home, project, nil)
	if _, err := catalog.Resolve("unsafe-body"); err == nil || !strings.Contains(issuesText(catalog.Issues), "unsafe terminal") {
		t.Fatalf("unsafe body catalog = %+v, resolve=%v", catalog, err)
	}
}

func TestManagerProjectAndSessionEnablement(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "private", "skills")
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "audit",
		"name: audit\ndescription: Audit code\n", "body")
	manager := NewManager(ManagerOptions{
		Home: home, ProjectDir: project, StateDir: stateDir,
		ProjectKey: "repo-123", SessionID: "session-1",
	})
	if got := manager.Summaries(t.Context()); len(got) != 1 || !got[0].Enabled {
		t.Fatalf("default summaries = %+v", got)
	}
	if err := manager.SetEnabled(t.Context(), "audit", false, EnableProject); err != nil {
		t.Fatal(err)
	}
	if got := manager.Summaries(t.Context()); got[0].Enabled {
		t.Fatalf("project disabled summary = %+v", got)
	}
	if _, err := manager.EnabledInspection(t.Context(), "audit"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled run = %v", err)
	}
	if err := manager.SetEnabled(t.Context(), "project:audit", true, EnableSession); err != nil {
		t.Fatal(err)
	}
	if got := manager.Summaries(t.Context()); !got[0].Enabled {
		t.Fatalf("session override summary = %+v", got)
	}
	if _, err := manager.EnabledInspection(t.Context(), "audit"); err != nil {
		t.Fatalf("enabled inspection: %v", err)
	}
	info, err := os.Stat(manager.store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
	if dirInfo, err := os.Stat(stateDir); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir = %v mode=%o", err, dirInfo.Mode().Perm())
	}
}

func TestStateConcurrentManagersDoNotLoseUpdates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	storeA := NewStateStore(dir, "repo-123")
	storeB := NewStateStore(dir, "repo-123")
	var wg sync.WaitGroup
	for i, store := range []*StateStore{storeA, storeB} {
		wg.Add(1)
		go func(i int, store *StateStore) {
			defer wg.Done()
			id := "project:skill-" + string(rune('a'+i))
			if err := store.SetEnabled(context.Background(), id, false, EnableProject, ""); err != nil {
				t.Errorf("SetEnabled: %v", err)
			}
		}(i, store)
	}
	wg.Wait()
	state, err := storeA.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Project) != 2 || state.Project["project:skill-a"] || state.Project["project:skill-b"] {
		t.Fatalf("concurrent state = %+v", state)
	}
}

func TestStateUsesHashedFilenameForUnicodeOrHostileProjectKey(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state"), "café/../../\x1bproject")
	base := filepath.Base(store.StatePath())
	if !strings.HasPrefix(base, "project-") || strings.Contains(base, "café") || strings.Contains(base, "..") {
		t.Fatalf("unsafe state filename = %q", base)
	}
	if err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, ""); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil || state.Project["project:audit"] {
		t.Fatalf("unicode project state = %+v, %v", state, err)
	}
}

func TestStateRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStateStore(dir, "repo-123")
	if err := os.Symlink(outside, store.StatePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context()); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("Load symlink = %v", err)
	}
	if err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, ""); err == nil {
		t.Fatal("SetEnabled replaced symlink")
	}
}

func TestStateRejectsBroadFilePermissions(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state"), "repo-123")
	if err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.StatePath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context()); err == nil || !strings.Contains(err.Error(), "expected 0600") {
		t.Fatalf("Load broad permissions = %v", err)
	}
}

func TestStateRejectsSymlinkedParentBeforeCreatingOutside(t *testing.T) {
	trusted := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(trusted, "redirect")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	store := NewStateStore(filepath.Join(link, "private", "skills"), "repo-123")
	err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, "")
	if err == nil || !strings.Contains(err.Error(), "not symlinks") {
		t.Fatalf("SetEnabled through parent symlink = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "private")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state escaped through parent symlink: %v", statErr)
	}
}

func TestStateQuarantinesStaleLock(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state"), "repo-123")
	root, err := store.openStateRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("repo-123.lock", []byte("abandoned"), 0o600); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	old := time.Now().Add(-stateLockStale - time.Minute)
	if err := root.Chtimes("repo-123.lock", old, old); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	_ = root.Close()
	if err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, ""); err != nil {
		t.Fatalf("SetEnabled with stale lock: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(store.dir, "repo-123.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale/current lock remains: %v", err)
	}
}

func TestParseFrontmatter(t *testing.T) {
	name, desc, inv := parseFrontmatter("---\nname: foo\ndescription: Does foo\nuser-invocable: true\n---\nbody")
	if name != "foo" || desc != "Does foo" || !inv {
		t.Errorf("parsed = %q, %q, %v", name, desc, inv)
	}
	name, desc, inv = parseFrontmatter("no frontmatter")
	if name != "" || desc != "" || inv {
		t.Errorf("no-fm parsed = %q, %q, %v", name, desc, inv)
	}
}

func TestContextCancellation(t *testing.T) {
	manager := NewManager(ManagerOptions{Home: t.TempDir(), ProjectDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh = %v", err)
	}
}

func issuesText(issues []Issue) string {
	var parts []string
	for _, issue := range issues {
		parts = append(parts, issue.Error)
	}
	return strings.Join(parts, " | ")
}
