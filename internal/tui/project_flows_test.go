package tui

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
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

func TestProjectQuestionnairesStageOneSharedManifest(t *testing.T) {
	for _, tc := range []struct {
		command string
		mode    string
	}{
		{command: "/bootstrap", mode: "greenfield"},
		{command: "/onboard", mode: "brownfield"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m, dir := newTestModel(t)
			if tc.command == "/onboard" {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/onboard\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			m.input.Set(tc.command)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			msg := primaryBatchMessage(t, cmd)
			if _, ok := msg.(projectFormMsg); !ok {
				t.Fatalf("questionnaire command returned %T", msg)
			}
			m.Update(msg)
			form, ok := m.overlayM.(*formOverlay)
			if !ok || m.overlay != overlayForm {
				t.Fatalf("questionnaire overlay = %T/%v", m.overlayM, m.overlay)
			}
			values := map[string]string{
				"name":         "Premium API",
				"purpose":      "Help teams ship a verified API.",
				"stack":        "Go, PostgreSQL",
				"non_goals":    "Mobile client",
				"verification": "go test ./..., go vet ./...",
				"safety":       "Never commit secrets, preserve public APIs",
			}
			for i := range form.fields {
				form.fields[i].Value = values[form.fields[i].Key]
			}
			form.active = len(form.fields) - 1
			_, cmd = m.updateOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
			msg = primaryBatchMessage(t, cmd)
			if _, ok := msg.(uiOperationDoneMsg); !ok {
				t.Fatalf("manifest command returned %T", msg)
			}
			m.Update(msg)
			if len(m.pending) != 1 || m.pending[0].Proposal == nil {
				t.Fatalf("pending manifest cards = %+v", m.pending)
			}
			proposal := m.pending[0].Proposal
			if filepath.Base(proposal.Path) != "MAESTRO.md" {
				t.Fatalf("proposal path = %q", proposal.Path)
			}
			preview := proposal.String()
			for _, want := range []string{"maestro_schema: 1", `mode: "` + tc.mode + `"`, "Help teams ship a verified API.", "Never commit secrets"} {
				if !strings.Contains(preview, want) {
					t.Fatalf("manifest preview missing %q:\n%s", want, preview)
				}
			}
			if _, err := os.Stat(proposal.Path); !os.IsNotExist(err) {
				t.Fatalf("MAESTRO.md was written before approval: %v", err)
			}
			m.acceptProposalCard(m.pending[0])
			if m.pending[0].Status != "done" {
				t.Fatalf("manifest acceptance status = %q (%s)", m.pending[0].Status, m.pending[0].Detail)
			}
			accepted, err := os.ReadFile(proposal.Path)
			if err != nil {
				t.Fatalf("accepted MAESTRO.md: %v", err)
			}
			for _, want := range []string{"# Maestro Project Contract", `mode: "` + tc.mode + `"`, "Help teams ship a verified API."} {
				if !strings.Contains(string(accepted), want) {
					t.Fatalf("accepted manifest missing %q:\n%s", want, accepted)
				}
			}
		})
	}
}

func TestProjectRuleChipsPreserveCommaProseAndUneditedRender(t *testing.T) {
	m, _ := newTestModel(t)
	profile, answers, err := m.orch.ProjectBootstrapDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	answers.Name = "lossless-contract"
	answers.Purpose = "Keep reviewed prose intact."
	answers.Stacks = []string{"Go", "PostgreSQL"}
	answers.NonGoals = []string{"Do not build mobile, desktop, or embedded clients."}
	answers.Verification = []string{"Run unit, integration, and race tests before completion."}
	answers.Safety = []string{
		"Never read, print, or modify secrets and local credential files.",
		"Preserve APIs, migrations, and unrelated user changes.",
	}
	want, err := projectprofile.Render(profile, answers)
	if err != nil {
		t.Fatal(err)
	}

	m.openProjectForm(formActionBootstrap, profile, answers, m.orch.SnapshotWorkspace())
	form, ok := m.overlayM.(*formOverlay)
	if !ok {
		t.Fatalf("project form = %T", m.overlayM)
	}
	values := form.values()
	roundTrip := answers
	roundTrip.Stacks = splitStackFormList(values["stack"])
	roundTrip.NonGoals = collectProjectRuleFields(values, "non_goals")
	roundTrip.Verification = collectProjectRuleFields(values, "verification")
	roundTrip.Safety = collectProjectRuleFields(values, "safety")
	if !reflect.DeepEqual(roundTrip.NonGoals, answers.NonGoals) ||
		!reflect.DeepEqual(roundTrip.Verification, answers.Verification) ||
		!reflect.DeepEqual(roundTrip.Safety, answers.Safety) {
		t.Fatalf("rule chip round-trip changed semantics:\n before=%+v\n after=%+v", answers, roundTrip)
	}
	got, err := projectprofile.Render(profile, roundTrip)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("unedited form changed manifest bytes:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestOnboardRefusesRepositoryDriftBeforeStaging(t *testing.T) {
	m, dir := newTestModel(t)
	manifest := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(manifest, []byte("module example.com/before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.input.Set("/onboard")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	formMsg, ok := primaryBatchMessage(t, cmd).(projectFormMsg)
	if !ok {
		t.Fatal("/onboard did not return a project questionnaire")
	}
	m.Update(formMsg)
	form, ok := m.overlayM.(*formOverlay)
	if !ok {
		t.Fatalf("project form = %T", m.overlayM)
	}
	for index := range form.fields {
		if form.fields[index].Key == "purpose" {
			form.fields[index].Value = "Build against reviewed repository facts."
		}
	}
	if err := os.WriteFile(manifest, []byte("module example.com/after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd = m.submitForm(formActionOnboard, form.values())
	result, ok := primaryBatchMessage(t, cmd).(uiOperationDoneMsg)
	if !ok {
		t.Fatalf("stale questionnaire returned %T", result)
	}
	if !errors.Is(result.err, projectprofile.ErrRepositoryChanged) {
		t.Fatalf("stale questionnaire error = %v", result.err)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "run /onboard again") {
		t.Fatalf("stale questionnaire message is not actionable: %v", result.err)
	}
	m.Update(result)
	if len(m.pending) != 0 {
		t.Fatalf("repository drift staged proposal(s): %+v", m.pending)
	}
	if _, err := os.Lstat(filepath.Join(dir, projectprofile.ManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository drift wrote MAESTRO.md: %v", err)
	}
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Text, "run /onboard again") {
		t.Fatalf("repository drift was not visible in the transcript: %+v", m.messages)
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
