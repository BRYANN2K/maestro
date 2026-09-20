//go:build unix

package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestReviewReusesChangeInventoryAndGitIdentity(t *testing.T) {
	orch, _ := newAcceptedContractPipeline(t)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		return successfulDevResult(role), nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	countPath := filepath.Join(t.TempDir(), "review-git-calls")
	shim := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MAESTRO_REVIEW_GIT_CALLS\"\nexec \"$MAESTRO_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAESTRO_REVIEW_GIT_CALLS", countPath)
	t.Setenv("MAESTRO_REAL_GIT", realGit)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	// Keep the compiler from adding unrelated VCS-stamping Git calls to this
	// review-orchestration process count.
	t.Setenv("GOFLAGS", "-buildvcs=false")

	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSpace(string(data)), "\n")
	// One validated initial identity (3), two integrity snapshots (5 each),
	// one shared tracked+untracked inventory (2), and one final identity (3).
	// Before the shared inventory and identity snapshot this path used 31 Git
	// subprocesses in a repository without submodules.
	if got := len(commands); got != 18 {
		t.Fatalf("review Git processes = %d, want 18; commands:\n%s", got, data)
	}
}
