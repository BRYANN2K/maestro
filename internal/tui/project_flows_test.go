package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/projectprofile"
)

func primaryBatchMessage(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		return msg
	}
	return batch[0]()
}

type projectFlowRunner struct {
	summaries []string
	prompts   []string
}

func (runner *projectFlowRunner) Run(_ context.Context, role agentcore.Role, prompt string) (agentcore.AgentResult, error) {
	runner.prompts = append(runner.prompts, prompt)
	if len(runner.summaries) == 0 {
		return agentcore.AgentResult{}, errors.New("unexpected project flow call")
	}
	summary := runner.summaries[0]
	runner.summaries = runner.summaries[1:]
	return agentcore.AgentResult{Role: string(role), OK: true, Summary: summary}, nil
}

func projectReadyJSON() string {
	return `{"ready":true,"question":"","name":"Port Scout","purpose":"Help developers discover open TCP ports on systems they own.","non_goals":["No vulnerability exploitation"],"stacks":["Python"],"commands":[{"name":"test","run":"python -m pytest","cwd":"."}],"safety":["Scan only systems the user owns or is authorized to test."],"verification":["Run python -m pytest."],"missing":[]}`
}

func projectStageResult(t *testing.T, cmd tea.Cmd) uiOperationDoneMsg {
	t.Helper()
	raw := cmd()
	outer, ok := raw.(tea.BatchMsg)
	if !ok || len(outer) < 2 {
		t.Fatalf("ready project step returned %T, want outer batch", raw)
	}
	innerRaw := outer[len(outer)-1]()
	if msg, ok := innerRaw.(uiOperationDoneMsg); ok {
		return msg
	}
	inner, ok := innerRaw.(tea.BatchMsg)
	if !ok || len(inner) == 0 {
		t.Fatalf("project staging command returned %T, want batch", innerRaw)
	}
	msg, ok := inner[0]().(uiOperationDoneMsg)
	if !ok {
		t.Fatalf("project staging returned %T", inner[0]())
	}
	return msg
}

func TestBootstrapUsesTranscriptQuestionsAndStagesOneAtomicManifest(t *testing.T) {
	m, dir := newTestModel(t)
	runner := &projectFlowRunner{summaries: []string{
		`{"ready":false,"question":"Who will use this tool, and what outcome should it create?","name":"Port Scout","purpose":"","non_goals":[],"stacks":[],"commands":[],"safety":[],"verification":[],"missing":["purpose","stack"]}`,
		projectReadyJSON(),
	}}
	m.orch.SetRunner(runner)

	m.input.Set("/bootstrap Build Port Scout for Python developers")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if progress := m.lastAssistant(); progress == nil || !progress.busy || progress.think == nil || progress.think.Role != "project setup" {
		t.Fatalf("bootstrap did not expose visible progress: %+v", progress)
	}
	step, ok := primaryBatchMessage(t, cmd).(projectConversationMsg)
	if !ok {
		t.Fatalf("bootstrap returned %T", primaryBatchMessage(t, cmd))
	}
	if !step.repositoryInitialized {
		t.Fatal("bootstrap did not report Git initialization")
	}
	insideOutput, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect bootstrap repository: %v\n%s", err, insideOutput)
	}
	if inside := strings.TrimSpace(string(insideOutput)); inside != "true" {
		t.Fatalf("bootstrap repository state = %q, want true", inside)
	}
	head := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD")
	if err := head.Run(); err == nil {
		t.Fatal("bootstrap created a commit before /accept")
	}
	m.Update(step)
	if m.overlay != overlayNone || m.projectFlow == nil {
		t.Fatalf("bootstrap opened a modal or lost its transcript flow: overlay=%v flow=%+v", m.overlay, m.projectFlow)
	}
	if got := m.LastAssistantText(); !strings.Contains(got, "Who will use") {
		t.Fatalf("follow-up question = %q", got)
	}

	m.input.Set("Python developers; a safe CLI for hosts they are authorized to test.")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	step, ok = primaryBatchMessage(t, cmd).(projectConversationMsg)
	if !ok || !step.step.Ready {
		t.Fatalf("reply returned %#v", step)
	}
	_, stageCmd := m.Update(step)
	result := projectStageResult(t, stageCmd)
	m.Update(result)
	if len(m.pending) != 1 || m.pending[0].Lifecycle != "project-manifest" {
		t.Fatalf("pending project contract = %+v", m.pending)
	}
	proposal := m.pending[0].Proposal
	if proposal == nil || filepath.Base(proposal.Path) != projectprofile.ManifestName {
		t.Fatalf("manifest proposal = %+v", proposal)
	}
	if _, err := os.Lstat(proposal.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MAESTRO.md was written before acceptance: %v", err)
	}
	if preview := proposal.String(); !strings.Contains(preview, "Port Scout") || !strings.Contains(preview, "python -m pytest") {
		t.Fatalf("manifest preview lost conversational answers:\n%s", preview)
	}
	m.acceptProposalCard(m.pending[0])
	if _, err := os.Stat(proposal.Path); err != nil {
		t.Fatalf("accepted MAESTRO.md: %v", err)
	}
	if len(runner.prompts) != 2 || !strings.Contains(runner.prompts[0], "Build Port Scout") || !strings.Contains(runner.prompts[1], "Python developers") {
		t.Fatalf("conversation was not carried into extraction: %#v", runner.prompts)
	}
}

func TestAdoptIsCanonicalAndUsesStaticRepositoryEvidence(t *testing.T) {
	m, dir := newTestModel(t)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/adopt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &projectFlowRunner{summaries: []string{projectReadyJSON()}}
	m.orch.SetRunner(runner)
	m.input.Set("/adopt")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	step, ok := primaryBatchMessage(t, cmd).(projectConversationMsg)
	if !ok || step.step.Mode != projectprofile.ModeBrownfield || !step.step.Ready {
		t.Fatalf("adopt step = %#v", step)
	}
	if len(runner.prompts) != 1 || !strings.Contains(runner.prompts[0], `"go"`) {
		t.Fatalf("adopt prompt omitted static Go evidence: %q", runner.prompts)
	}
	if m.overlay != overlayNone {
		t.Fatalf("adopt opened overlay %v", m.overlay)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adopt unexpectedly initialized Git: %v", err)
	}
}

func TestProposeWithoutManifestBuildsContractThenResumesAfterAcceptance(t *testing.T) {
	m, dir := newTestModel(t)
	m.orch.SetRunner(&projectFlowRunner{summaries: []string{projectReadyJSON()}})
	m.input.Set("/propose Build a Python tool that discovers open ports")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	step, ok := primaryBatchMessage(t, cmd).(projectConversationMsg)
	if !ok || !step.step.Ready || step.state.resumeCmd == nil {
		t.Fatalf("propose prerequisite step = %#v", step)
	}
	_, stageCmd := m.Update(step)
	m.Update(projectStageResult(t, stageCmd))
	if len(m.pending) != 1 {
		t.Fatalf("contract was not staged: %+v", m.pending)
	}
	m.acceptLatestPending()
	if m.postAcceptCmd == nil {
		t.Fatal("accepted contract did not queue the original /propose")
	}
	resume := m.takePostAcceptCommand()
	done, ok := primaryBatchMessage(t, resume).(chatDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("resumed propose = %#v", done)
	}
	if m.orch.Session().Draft == nil || !strings.Contains(m.orch.Session().DraftPrompt, "Python tool") {
		t.Fatalf("original proposal request was not resumed: %+v", m.orch.Session())
	}
	if _, err := os.Stat(filepath.Join(dir, projectprofile.ManifestName)); err != nil {
		t.Fatalf("resumed before MAESTRO.md acceptance: %v", err)
	}
}

func TestProjectConversationRefusesRepositoryDriftBeforeStaging(t *testing.T) {
	m, dir := newTestModel(t)
	manifest := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(manifest, []byte("module example.com/before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.orch.SetRunner(&projectFlowRunner{summaries: []string{projectReadyJSON()}})
	step, err := m.orch.ProjectManifestConversation(t.Context(), projectprofile.ModeBrownfield, "", "")
	if err != nil || !step.Ready {
		t.Fatalf("project conversation = %+v, %v", step, err)
	}
	if err := os.WriteFile(manifest, []byte("module example.com/after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := primaryBatchMessage(t, m.runProjectManifest(step.Mode, step.Profile, step.Answers)).(uiOperationDoneMsg)
	if !errors.Is(result.err, projectprofile.ErrRepositoryChanged) {
		t.Fatalf("stale project contract error = %v", result.err)
	}
	if _, err := os.Lstat(filepath.Join(dir, projectprofile.ManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository drift wrote MAESTRO.md: %v", err)
	}
}

func TestCancelledProjectConversationCannotStageALateResult(t *testing.T) {
	m, _ := newTestModel(t)
	m.projectRequest = 7
	m.projectActive = true
	m.busy = true
	m.cancelRun = func() {}
	m.cancelActiveTask()
	if !m.cancelling || m.projectRequest != 8 {
		t.Fatalf("cancellation did not invalidate project request: cancelling=%v request=%d", m.cancelling, m.projectRequest)
	}

	late := projectConversationMsg{
		state: projectConversationState{request: 7},
		step:  orchestrator.ProjectManifestStep{Ready: true},
	}
	m.Update(late)
	if m.busy || m.cancelling || m.projectActive || len(m.pending) != 0 {
		t.Fatalf("late result mutated UI: busy=%v cancelling=%v active=%v pending=%d", m.busy, m.cancelling, m.projectActive, len(m.pending))
	}
}

func TestUnrelatedAcceptanceDoesNotCancelProjectConversation(t *testing.T) {
	m, _ := newTestModel(t)
	m.projectFlow = &projectConversationState{mode: projectprofile.ModeGreenfield}
	if cmd := m.takePostAcceptCommand(); cmd != nil {
		t.Fatal("unrelated acceptance unexpectedly scheduled a project command")
	}
	if m.projectFlow == nil {
		t.Fatal("unrelated acceptance cancelled project setup")
	}
}

func TestRenameSessionUpdatesResumePicker(t *testing.T) {
	m, _ := newTestModel(t)
	msg := primaryBatchMessage(t, m.renameSession("Payments safety review"))
	m.Update(msg)
	if got := m.orch.Session().Title; got != "Payments safety review" {
		t.Fatalf("session title = %q", got)
	}
	picker := newSessionPickerOverlay(m.orch)
	if len(picker.items) == 0 || !strings.Contains(picker.items[0], "Payments safety review") {
		t.Fatalf("resume picker items = %#v", picker.items)
	}
	if got := picker.selectedValue(); got != m.orch.Session().ID {
		t.Fatalf("resume picker value = %q, want session id", got)
	}
}

func TestLateSessionPickerResultIsIgnoredAfterClose(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := m.openSessionPicker()
	request := m.sessionRequest
	if cmd == nil || m.overlay != overlaySessionPicker {
		t.Fatal("session picker did not start")
	}
	m.overlay = overlayNone
	m.overlayM = nil
	m.Update(sessionListMsg{request: request})
	if m.overlay != overlayNone || m.overlayM != nil {
		t.Fatal("late session list reopened a dismissed picker")
	}
}

func TestGitPickerSelectsExactRegisteredWorkspaceAndRebindsIDE(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, dir := newTestModel(t)
	runGitFixture(t, dir, "init", "-b", "main")
	runGitFixture(t, dir, "config", "user.email", "maestro@example.test")
	runGitFixture(t, dir, "config", "user.name", "Maestro Test")
	runGitFixture(t, dir, "add", "-A")
	runGitFixture(t, dir, "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "feature workspace")
	runGitFixture(t, dir, "worktree", "add", "-b", "feature/tui-workspace", linked)

	m.switchTab(TabIDE)
	oldIDE := m.ide
	cmd := m.openWorkspacePicker()
	m.Update(cmd())
	picker, ok := m.overlayM.(*listOverlay)
	if !ok || m.overlay != overlayGit {
		t.Fatalf("workspace overlay = %T/%v", m.overlayM, m.overlay)
	}
	for i, item := range picker.Filter() {
		if sameFilesystemPath(picker.valueOf(item), linked) {
			picker.selected = i
			break
		}
	}
	cmd = m.selectOverlay(picker)
	msg := primaryBatchMessage(t, cmd)
	if _, ok := msg.(uiOperationDoneMsg); !ok {
		t.Fatalf("workspace selection returned %T", msg)
	}
	m.Update(msg)
	if !sameFilesystemPath(m.orch.WorkDirDisplay(), linked) {
		t.Fatalf("active workspace = %q, want %q", m.orch.WorkDirDisplay(), linked)
	}
	if m.ide == nil || m.ide == oldIDE || !sameFilesystemPath(m.ide.project, linked) {
		t.Fatalf("IDE was not rebound to selected workspace: %+v", m.ide)
	}
	if m.orch.Session().ManagedWorktree {
		t.Fatal("/git workspace was incorrectly marked as archive-managed")
	}
}

func TestWorkspaceSwitchRejectsDirtyBackgroundBuffer(t *testing.T) {
	m, _ := newTestModel(t)
	m.switchTab(TabIDE)
	background := editor.NewBuffer("background.go", []byte("package background\n"))
	m.ide.Ed.Buffers = append(m.ide.Ed.Buffers, background)
	background.Dirty = true
	m.ide.Ed.CurBuf = 0
	if cmd := m.openWorkspacePicker(); cmd != nil {
		t.Fatal("workspace listing started with a dirty background buffer")
	}
	if m.overlay != overlayNone {
		t.Fatalf("workspace overlay opened despite dirty buffer: %v", m.overlay)
	}
}

func runGitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
