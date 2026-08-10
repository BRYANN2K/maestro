//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/mcp"
	"github.com/bryann2k/maestro/internal/session"
)

// TestMCPWorkspaceStdioHelper is re-executed as a real stdio MCP server. Its
// touch tool records the process cwd, making workspace confinement observable.
func TestMCPWorkspaceStdioHelper(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_WORKSPACE_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("MCP_WORKSPACE_PID_FILE"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write PID: %v", err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == 0 {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcp.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "touch", "description": "record the server workspace",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			}}}
		case "tools/call":
			cwd, err := os.Getwd()
			if err != nil {
				os.Exit(2)
			}
			if err := os.WriteFile(filepath.Join(cwd, "mcp-workspace.marker"), []byte(cwd+"\n"), 0o600); err != nil {
				os.Exit(2)
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": cwd}}}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func newWorkspaceMCPOrchestrator(t *testing.T, dir, pidFile string, runner Runner) *Orchestrator {
	t.Helper()
	t.Setenv("GO_WANT_MCP_WORKSPACE_HELPER", "1")
	t.Setenv("MCP_WORKSPACE_PID_FILE", pidFile)
	command := fmt.Sprintf("%q -test.run=^TestMCPWorkspaceStdioHelper$", os.Args[0])
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{Mcp: []config.Mcp{{Name: "workspace", Type: "stdio", Command: command}}},
		Runner: runner, In: strings.NewReader(""), Out: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake, ok := runner.(*fakeRunner); ok {
		fake.Wd = func() string { return orch.WorkDirDisplay() }
	}
	return orch
}

func runWorkspaceMCPTool(t *testing.T, orch *Orchestrator) string {
	t.Helper()
	if err := orch.MCPReconnect(t.Context(), "workspace"); err != nil {
		t.Fatalf("MCPReconnect: %v", err)
	}
	tool, ok := orch.scopedTools(agentcore.RoleDev)["mcp__workspace__touch"]
	if !ok {
		t.Fatal("workspace MCP tool was not exposed")
	}
	output, err := tool.Run(t.Context(), map[string]any{})
	if err != nil {
		t.Fatalf("MCP touch: %v", err)
	}
	return output
}

func readWorkspaceMCPPID(t *testing.T, path string, previous int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 1 && pid != previous {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP PID file %q did not publish a new process", path)
	return 0
}

func assertWorkspaceMCPProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("old MCP stdio process %d survived workspace transition", pid)
}

func writeWorkspaceSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("---\nname: %s\ndescription: %s workspace skill\nuser-invocable: true\n---\n# %s\n", name, name, name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasWorkspaceSkill(orch *Orchestrator, name string) bool {
	for _, summary := range orch.SkillSummaries(context.Background()) {
		if summary.Name == name && summary.Valid {
			return true
		}
	}
	return false
}

func TestAcceptWorktreeRetargetsMCPAndClosesOldProcess(t *testing.T) {
	base := newTestRepo(t)
	pidFile := filepath.Join(t.TempDir(), "mcp.pid")
	orch := newWorkspaceMCPOrchestrator(t, base, pidFile, &fakeRunner{})
	if err := orch.MCPReconnect(t.Context(), "workspace"); err != nil {
		t.Fatal(err)
	}
	oldPID := readWorkspaceMCPPID(t, pidFile, 0)
	t.Cleanup(func() { _ = syscall.Kill(oldPID, syscall.SIGKILL) })

	if _, err := orch.Propose(t.Context(), "Add a workspace-aware feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "worktree", Name: "feature-mcp-workspace"}); err != nil {
		t.Fatal(err)
	}
	target := orch.WorkDirDisplay()
	if target == base {
		t.Fatal("accept did not activate a worktree")
	}
	assertWorkspaceMCPProcessGone(t, oldPID)

	output := runWorkspaceMCPTool(t, orch)
	newPID := readWorkspaceMCPPID(t, pidFile, oldPID)
	t.Cleanup(func() { _ = syscall.Kill(newPID, syscall.SIGKILL) })
	if !strings.Contains(output, target) {
		t.Fatalf("MCP output = %q, want worktree %q", output, target)
	}
	if _, err := os.Stat(filepath.Join(target, "mcp-workspace.marker")); err != nil {
		t.Fatalf("worktree marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "mcp-workspace.marker")); !os.IsNotExist(err) {
		t.Fatalf("base checkout was mutated by worktree MCP: %v", err)
	}
}

func TestOrchestratorCloseKillsMCPStdioAndIsIdempotent(t *testing.T) {
	base := newTestRepo(t)
	pidFile := filepath.Join(t.TempDir(), "mcp.pid")
	orch := newWorkspaceMCPOrchestrator(t, base, pidFile, &fakeRunner{})
	if err := orch.MCPReconnect(t.Context(), "workspace"); err != nil {
		t.Fatal(err)
	}
	pid := readWorkspaceMCPPID(t, pidFile, 0)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := orch.Close(); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceMCPProcessGone(t, pid)
	if err := orch.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestIsolatedBuildConfinesMCPThenClosesTemporaryProcess(t *testing.T) {
	base := newTestRepo(t)
	pidFile := filepath.Join(t.TempDir(), "mcp.pid")
	var orch *Orchestrator
	var isolatedDir string
	var isolatedPID int
	inner := runnerFunc(func(ctx context.Context, role agentcore.Role, prompt string) (agentcore.AgentResult, error) {
		output := runWorkspaceMCPTool(t, orch)
		isolatedDir = orch.WorkDirDisplay()
		isolatedPID = readWorkspaceMCPPID(t, pidFile, 0)
		if !strings.Contains(output, isolatedDir) {
			return agentcore.AgentResult{}, fmt.Errorf("MCP output %q does not contain isolated dir %q", output, isolatedDir)
		}
		fake := &fakeRunner{Wd: func() string { return orch.WorkDirDisplay() }}
		return fake.Run(ctx, role, prompt)
	})
	orch = newWorkspaceMCPOrchestrator(t, base, pidFile, inner)
	if _, err := orch.Propose(t.Context(), "Add an isolated MCP feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(t.Context(), BuildOptions{Isolated: true}); err != nil {
		t.Fatal(err)
	}
	if isolatedDir == "" || isolatedDir == base || !strings.Contains(isolatedDir, "maestro-isolated-dev-") {
		t.Fatalf("isolated MCP cwd = %q", isolatedDir)
	}
	assertWorkspaceMCPProcessGone(t, isolatedPID)
	if filepathKey(orch.WorkDirDisplay()) != filepathKey(base) {
		t.Fatalf("workspace restored to %q, want %q", orch.WorkDirDisplay(), base)
	}
	if got := orch.MCPServerSummaries(t.Context()); len(got) != 1 || got[0].Status != "disconnected" {
		t.Fatalf("restored MCP registry = %+v, want fresh disconnected client", got)
	}
	marker, err := os.ReadFile(filepath.Join(base, "mcp-workspace.marker"))
	if err != nil || !strings.Contains(string(marker), isolatedDir) {
		t.Fatalf("isolated delta marker = %q, %v", marker, err)
	}
}

func TestArchiveRebindsMCPAndSkillsToBaseWorkspace(t *testing.T) {
	base := newTestRepo(t)
	pidFile := filepath.Join(t.TempDir(), "mcp.pid")
	runner := &fakeRunner{}
	orch := newWorkspaceMCPOrchestrator(t, base, pidFile, runner)
	if _, err := orch.Propose(t.Context(), "Archive a workspace-aware feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "worktree", Name: "feature-mcp-archive"}); err != nil {
		t.Fatal(err)
	}
	target := orch.WorkDirDisplay()
	writeWorkspaceSkill(t, target, "worktree-only")
	writeWorkspaceSkill(t, base, "base-only")
	if err := orch.RefreshSkills(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !hasWorkspaceSkill(orch, "worktree-only") || hasWorkspaceSkill(orch, "base-only") {
		t.Fatalf("accepted worktree SkillMgr is rooted incorrectly: %+v", orch.SkillSummaries(t.Context()))
	}

	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := orch.Docs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := orch.MCPReconnect(t.Context(), "workspace"); err != nil {
		t.Fatal(err)
	}
	oldPID := readWorkspaceMCPPID(t, pidFile, 0)
	t.Cleanup(func() { _ = syscall.Kill(oldPID, syscall.SIGKILL) })
	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true, Merge: false}); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceMCPProcessGone(t, oldPID)
	if filepathKey(orch.WorkDirDisplay()) != filepathKey(base) {
		t.Fatalf("archive restored %q, want base %q", orch.WorkDirDisplay(), base)
	}
	if got := orch.MCPServerSummaries(t.Context()); len(got) != 1 || got[0].Status != "disconnected" {
		t.Fatalf("archive MCP registry = %+v", got)
	}
	if !hasWorkspaceSkill(orch, "base-only") || hasWorkspaceSkill(orch, "worktree-only") {
		t.Fatalf("archived base SkillMgr is rooted incorrectly: %+v", orch.SkillSummaries(t.Context()))
	}
}

func TestArchiveMergeClosesMCPBeforeManagedWorktreeRemoval(t *testing.T) {
	base := newTestRepo(t)
	pidFile := filepath.Join(t.TempDir(), "mcp.pid")
	orch := newWorkspaceMCPOrchestrator(t, base, pidFile, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Merge a workspace-aware feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "worktree", Name: "feature-mcp-merge"}); err != nil {
		t.Fatal(err)
	}
	target := orch.WorkDirDisplay()
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := orch.Docs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := orch.MCPReconnect(t.Context(), "workspace"); err != nil {
		t.Fatal(err)
	}
	pid := readWorkspaceMCPPID(t, pidFile, 0)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true, Merge: true}); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceMCPProcessGone(t, pid)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("managed worktree still exists after merged archive: %v", err)
	}
}

func TestArchiveRecoveryFinalizerClosesMCPAndRebindsSkills(t *testing.T) {
	base := newTestRepo(t)
	pidFile := filepath.Join(t.TempDir(), "mcp.pid")
	orch := newWorkspaceMCPOrchestrator(t, base, pidFile, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Recover a workspace-aware archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "worktree", Name: "feature-mcp-recovery"}); err != nil {
		t.Fatal(err)
	}
	target := orch.WorkDirDisplay()
	writeWorkspaceSkill(t, target, "recovery-worktree")
	writeWorkspaceSkill(t, base, "recovery-base")
	if err := orch.RefreshSkills(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !hasWorkspaceSkill(orch, "recovery-worktree") || hasWorkspaceSkill(orch, "recovery-base") {
		t.Fatalf("pre-recovery SkillMgr = %+v", orch.SkillSummaries(t.Context()))
	}
	if err := orch.MCPReconnect(t.Context(), "workspace"); err != nil {
		t.Fatal(err)
	}
	oldPID := readWorkspaceMCPPID(t, pidFile, 0)
	t.Cleanup(func() { _ = syscall.Kill(oldPID, syscall.SIGKILL) })

	id := orch.sess.SpecID
	orch.sess.Phase = session.PhaseArchive
	if _, err := orch.finalizeRecoveredArchive(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceMCPProcessGone(t, oldPID)
	if filepathKey(orch.WorkDirDisplay()) != filepathKey(base) {
		t.Fatalf("recovery restored %q, want base %q", orch.WorkDirDisplay(), base)
	}
	if orch.sess.ManagedWorktree {
		t.Fatal("recovery retained the stale managed-worktree marker")
	}
	if got := orch.MCPServerSummaries(t.Context()); len(got) != 1 || got[0].Status != "disconnected" {
		t.Fatalf("recovery MCP registry = %+v", got)
	}
	if !hasWorkspaceSkill(orch, "recovery-base") || hasWorkspaceSkill(orch, "recovery-worktree") {
		t.Fatalf("post-recovery SkillMgr = %+v", orch.SkillSummaries(t.Context()))
	}
}
