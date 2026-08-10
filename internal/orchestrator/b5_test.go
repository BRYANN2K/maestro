package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	legacyagent "github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/proposals"
	"github.com/bryann2k/maestro/internal/settings"
)

type runnerFunc func(context.Context, agentcore.Role, string) (agentcore.AgentResult, error)

func (runnerFunc) maestroReadOnlySkillRunner() {}

func (f runnerFunc) Run(ctx context.Context, role agentcore.Role, prompt string) (agentcore.AgentResult, error) {
	return f(ctx, role, prompt)
}

func TestEngineChoices(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	choices := orch.EngineChoices("dev")
	if len(choices) < 2 {
		t.Fatalf("choices = %+v", choices)
	}
	if choices[0].Engine != "native" {
		t.Errorf("first choice = %+v, want native", choices[0])
	}
	if got := choices[0].Label(); !strings.Contains(got, "Maestro agent") || !strings.Contains(got, "local model") {
		t.Errorf("native product label = %q", got)
	}
	legacy := map[string]bool{}
	for _, c := range choices[1:] {
		if c.Engine != "legacy" || c.Agent == "" {
			t.Errorf("choice = %+v", c)
		}
		if got := c.Label(); !strings.HasPrefix(got, "subscription · ") || strings.Contains(got, "legacy") {
			t.Errorf("subscription product label = %q", got)
		}
		legacy[c.Agent] = true
	}
	if !legacy["codex"] || !legacy["claude"] || !legacy["opencode"] {
		t.Errorf("legacy agents = %v", legacy)
	}
}

func TestRememberEnginePersists(t *testing.T) {
	dir := newTestRepo(t)
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir:   dir,
		SessionsDir:  filepath.Join(t.TempDir(), "s"),
		In:           strings.NewReader(""),
		Out:          &out,
		SettingsPath: settingsPath,
		Runner:       &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.rememberEngine("dev", "legacy", "codex")
	loaded, err := settings.Load(context.Background(), settingsPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rd := loaded.RoleDefaults["dev"]
	if rd.Engine != "legacy" || rd.Agent != "codex" {
		t.Errorf("persisted = %+v", rd)
	}
}

func TestBuildRememberedEnginePreSelected(t *testing.T) {
	// A legacy default in settings must be picked by buildRunner.
	o := &Orchestrator{settings: settings.Defaults()}
	o.settings.RoleDefaults["dev"] = settings.RoleDefaults{Engine: "legacy", Agent: "claude"}
	runner, err := o.buildRunner(BuildOptions{})
	if err != nil {
		t.Fatalf("buildRunner: %v", err)
	}
	lr, ok := runner.(*legacyRunner)
	if !ok {
		t.Fatalf("runner = %T, want legacyRunner", runner)
	}
	if lr.agent.Name() != "Claude Code" {
		t.Errorf("agent = %s", lr.agent.Name())
	}
}

func TestNewDoesNotMaskExplicitEngineChoice(t *testing.T) {
	dir := newTestRepo(t)
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Settings:    settings.Defaults(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runner, err := orch.buildRunner(BuildOptions{Engine: "legacy", Agent: "claude"})
	if err != nil {
		t.Fatalf("buildRunner: %v", err)
	}
	if _, ok := runner.(*legacyRunner); !ok {
		t.Fatalf("runner = %T, want *legacyRunner", runner)
	}
}

func TestDevToolOverridesPreserveStandardTools(t *testing.T) {
	root := t.TempDir()
	store := proposals.NewProposalStore(filepath.Join(t.TempDir(), "proposals"))
	orch := &Orchestrator{dir: root, devTools: []agentcore.Tool{proposals.StagingWriteTool(store)}}
	got := orch.scopedTools(agentcore.RoleDev)
	for _, name := range []string{"read", "grep", "write", "bash", "ask"} {
		if _, ok := got[name]; !ok {
			t.Errorf("dev tools missing %q: %v", name, got)
		}
	}
}

func TestDevToolsAreRootedToWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	orch := &Orchestrator{dir: root}
	toolset := orch.scopedTools(agentcore.RoleDev)
	out, err := toolset["read"].Run(t.Context(), map[string]any{"path": "inside.txt"})
	if err != nil || out != "inside" {
		t.Fatalf("read inside = %q, %v", out, err)
	}
	if _, err := toolset["read"].Run(t.Context(), map[string]any{"path": "../outside.txt"}); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("read escape error = %v", err)
	}
}

func TestCancelRun(t *testing.T) {
	orch := newTestOrch(t, newTestRepo(t), &fakeRunner{})
	ctx, cancel := orch.withRunContext(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()
	orch.CancelRun()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run context not cancelled")
	}
}

func TestGofmtCheckUsesActiveWorktreeAndLiteralPaths(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	name := "-format me.go"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package main\nfunc  unformatted( ){ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := orch.gofmtCheck(context.Background())
	if len(items) != 1 || items[0].Level != "fail" || !strings.Contains(items[0].Message, name) {
		t.Fatalf("gofmt findings = %+v", items)
	}
}

func TestGofmtCheckFailsClosedWhenToolIsUnavailable(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	t.Setenv("PATH", t.TempDir())

	items := orch.gofmtCheck(context.Background())
	if len(items) != 1 || items[0].Level != "fail" || !strings.Contains(items[0].Message, "gofmt unavailable") {
		t.Fatalf("gofmt findings = %+v, want unavailable fail", items)
	}
}

func TestGofmtCheckFailsClosedWhenToolExecutionFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX executable shim")
	}
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	toolDir := t.TempDir()
	gofmtPath := filepath.Join(toolDir, "gofmt")
	if err := os.WriteFile(gofmtPath, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(toolDir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolDir)

	items := orch.gofmtCheck(context.Background())
	if len(items) != 1 || items[0].Level != "fail" || !strings.Contains(items[0].Message, "gofmt broken.go failed") {
		t.Fatalf("gofmt findings = %+v, want execution fail", items)
	}
}

func TestReviewBlocksOnUnformattedGoFile(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{Role: string(role), OK: true, Summary: "build complete"}, nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unformatted.go"), []byte("package main\nfunc  unformatted( ){ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	verdict, err := orch.Review(t.Context())
	var reviewFailed *ReviewFailedError
	if !errors.As(err, &reviewFailed) {
		t.Fatalf("Review error = %v, want ReviewFailedError", err)
	}
	if verdict.VerdictLevel() != "fail" || orch.Session().Review == nil || orch.Session().Review.Level != "fail" {
		t.Fatalf("review verdict/session = %+v / %+v, want fail", verdict, orch.Session().Review)
	}
	if orch.Session().Review.Fingerprint != "" {
		t.Fatalf("failed review retained archivable fingerprint %q", orch.Session().Review.Fingerprint)
	}
}

func TestIsolatedWorktreeRunReturnsUncommittedDeltaForReview(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\nprior round\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prior.bin"), []byte{0x00, 0x01, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	indexBefore := gitRun(t, dir, "diff", "--cached", "--binary")
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		if role != agentcore.RoleDev {
			return agentcore.AgentResult{Role: string(role), OK: true}, nil
		}
		workDir := orch.WorkDirDisplay()
		readme, err := os.ReadFile(filepath.Join(workDir, "README.md"))
		if err != nil || !strings.Contains(string(readme), "prior round") {
			return agentcore.AgentResult{}, fmt.Errorf("isolated runner did not receive prior changes: %w", err)
		}
		if _, err := os.Stat(filepath.Join(workDir, "specs", orch.spec.ID, "spec.md")); err != nil {
			return agentcore.AgentResult{}, fmt.Errorf("isolated runner did not receive active spec: %w", err)
		}
		mainPath := filepath.Join(workDir, "main.go")
		main, err := os.ReadFile(mainPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(mainPath, append(main, []byte("\n// isolated review marker\n")...), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(workDir, "asset.bin"), []byte{0x00, 0xfe, 0xff, 0x7f}, 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(workDir, "tool.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			return agentcore.AgentResult{}, err
		}
		if runtime.GOOS != "windows" {
			if err := os.Symlink("README.md", filepath.Join(workDir, "readme-link")); err != nil {
				return agentcore.AgentResult{}, err
			}
		}
		return agentcore.AgentResult{Role: string(role), OK: true, Summary: "isolated delta"}, nil
	})
	if err := orch.Build(ctx, BuildOptions{Isolated: true}); err != nil {
		t.Fatalf("Build isolated: %v", err)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("isolated build advanced HEAD to %s, want %s", got, headBefore)
	}
	if got := gitRun(t, dir, "diff", "--cached", "--binary"); got != indexBefore {
		t.Fatalf("isolated build changed the active index:\n%s", got)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || !strings.Contains(string(got), "prior round") {
		t.Fatalf("prior tracked change was lost: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "prior.bin")); err != nil || string(got) != string([]byte{0x00, 0x01, 0xff}) {
		t.Fatalf("prior untracked binary was lost: %v, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "asset.bin")); err != nil || string(got) != string([]byte{0x00, 0xfe, 0xff, 0x7f}) {
		t.Fatalf("isolated binary = %v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "tool.sh"))
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("isolated executable mode = %v, %v", info, err)
		}
		if target, err := os.Readlink(filepath.Join(dir, "readme-link")); err != nil || target != "README.md" {
			t.Fatalf("isolated symlink = %q, %v", target, err)
		}
	}
	diff, err := orch.git.DiffUnified(ctx, "HEAD")
	if err != nil || !strings.Contains(diff, "isolated review marker") {
		t.Fatalf("review diff does not contain isolated change: %v\n%s", err, diff)
	}
	changes, err := orch.git.AllChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundBinary := false
	for _, change := range changes {
		foundBinary = foundBinary || change.Path == "asset.bin"
	}
	if !foundBinary {
		t.Fatalf("security/review change set does not contain asset.bin: %+v", changes)
	}
	if _, err := orch.Review(ctx); err != nil {
		t.Fatalf("Review isolated delta: %v", err)
	}
	worktrees := gitRun(t, dir, "worktree", "list", "--porcelain")
	if got := strings.Count(worktrees, "worktree "); got != 1 {
		t.Fatalf("temporary worktree leaked; list has %d entries:\n%s", got, worktrees)
	}
	if branches := strings.TrimSpace(gitRun(t, dir, "branch", "--list", "maestro-isolated-*")); branches != "" {
		t.Fatalf("temporary branches leaked: %s", branches)
	}
}

func TestIsolatedBuildUsesAcceptedSessionWorktree(t *testing.T) {
	dir := newTestRepo(t)
	runner := &fakeRunner{}
	orch := newTestOrch(t, dir, runner)
	ctx := context.Background()
	if _, err := orch.Propose(ctx, "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "worktree", Name: "feat-isolated-session"}); err != nil {
		t.Fatal(err)
	}
	sessionWorktree := orch.Session().Worktree
	baseHead := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	worktreeHead := strings.TrimSpace(gitRun(t, sessionWorktree, "rev-parse", "HEAD"))
	if err := orch.Build(ctx, BuildOptions{Isolated: true}); err != nil {
		t.Fatalf("Build isolated in session worktree: %v", err)
	}
	if len(runner.files) != 1 {
		t.Fatalf("dev files = %v", runner.files)
	}
	isolatedFile := runner.files[0]
	if _, err := os.Stat(filepath.Join(sessionWorktree, isolatedFile)); err != nil {
		t.Fatalf("session worktree missing isolated delta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, isolatedFile)); !os.IsNotExist(err) {
		t.Fatalf("base checkout received isolated delta: %v", err)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); got != baseHead {
		t.Fatalf("base HEAD = %s, want %s", got, baseHead)
	}
	if got := strings.TrimSpace(gitRun(t, sessionWorktree, "rev-parse", "HEAD")); got != worktreeHead {
		t.Fatalf("session worktree HEAD = %s, want %s", got, worktreeHead)
	}
	if got := strings.Count(gitRun(t, dir, "worktree", "list", "--porcelain"), "worktree "); got != 2 {
		t.Fatalf("temporary worktree leaked; worktree count = %d", got)
	}
}

func TestIsolatedRunCleansTemporaryWorktreeAfterCancellation(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx, cancel := context.WithCancel(context.Background())
	runner := runnerFunc(func(runCtx context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		cancel()
		<-runCtx.Done()
		return agentcore.AgentResult{Role: string(role)}, runCtx.Err()
	})
	isolated, err := orch.isolatedRunner(runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Run(ctx, agentcore.RoleDev, "cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("isolated.Run error = %v, want context cancellation", err)
	}
	worktrees := gitRun(t, dir, "worktree", "list", "--porcelain")
	if got := strings.Count(worktrees, "worktree "); got != 1 {
		t.Fatalf("cancelled run leaked a worktree; list has %d entries:\n%s", got, worktrees)
	}
}

func TestIsolatedLegacyRunUsesWorktreeAndLeavesCheckoutUntouchedOnFailure(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	cwdLog := filepath.Join(t.TempDir(), "cwd.log")
	t.Setenv("MAESTRO_CWD_LOG", cwdLog)
	gitCommon, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatalf("resolve test repository git dir: %v", err)
	}
	t.Setenv("MAESTRO_EXPECTED_GIT_COMMON", gitCommon)

	binDir := t.TempDir()
	shim := filepath.Join(binDir, "opencode")
	script := `#!/bin/sh
set -eu
pwd -P > "$MAESTRO_CWD_LOG"
git rev-parse --show-toplevel >> "$MAESTRO_CWD_LOG"
common_dir=$(git rev-parse --git-common-dir 2>/dev/null || true)
if [ -n "$common_dir" ]; then
  case "$common_dir" in
    /*) ;;
    *) common_dir="$PWD/$common_dir" ;;
  esac
  common_dir=$(cd "$common_dir" && pwd -P)
  if [ "$common_dir" = "$MAESTRO_EXPECTED_GIT_COMMON" ]; then
    touch legacy-agent-marker.txt
  fi
fi
exit 9
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write opencode shim: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	legacy := &legacyRunner{agent: legacyagent.NewOpenCodeAgent(), o: orch, silent: true}
	runner, err := orch.isolatedRunner(legacy)
	if err != nil {
		t.Fatalf("isolatedRunner: %v", err)
	}
	result, err := runner.Run(t.Context(), agentcore.RoleDev, "mutate the workspace")
	if err == nil || !strings.Contains(err.Error(), "opencode exited") {
		t.Fatalf("Run error = %v, want legacy subprocess failure", err)
	}
	var streamErr agentcore.StreamError
	if !errors.As(err, &streamErr) {
		t.Fatalf("Run error type = %T, want agentcore.StreamError in chain", err)
	}
	if result.OK {
		t.Fatal("Run result OK = true, want failed legacy subprocess")
	}

	data, err := os.ReadFile(cwdLog)
	if err != nil {
		t.Fatalf("read cwd log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("cwd log = %q, want cwd and git root", data)
	}
	worktreeDir, gitRoot := filepath.Clean(lines[0]), filepath.Clean(lines[1])
	if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
		t.Fatalf("isolated worktree cleanup state for %q: %v", worktreeDir, err)
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("canonicalize original checkout: %v", err)
	}
	if worktreeDir == canonicalDir {
		t.Fatalf("legacy subprocess ran in original checkout %q", dir)
	}
	if gitRoot != worktreeDir {
		t.Fatalf("active git worktree root = %q, subprocess cwd = %q", gitRoot, worktreeDir)
	}
	if filepath.Base(worktreeDir) != "worktree" || !strings.HasPrefix(filepath.Base(filepath.Dir(worktreeDir)), "maestro-isolated-dev-") {
		t.Fatalf("legacy subprocess cwd = %q, want a Maestro isolated worktree", worktreeDir)
	}
	if _, err := os.Stat(filepath.Join(dir, "legacy-agent-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("original checkout was mutated: %v", err)
	}
	if status := strings.TrimSpace(gitRun(t, dir, "status", "--porcelain", "--untracked-files=all")); status != "" {
		t.Fatalf("original checkout is dirty after failed isolated run:\n%s", status)
	}
}
