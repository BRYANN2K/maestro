package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/projectprofile"
)

type manifestConversationRunner struct {
	summaries []string
	prompts   []string
}

func (runner *manifestConversationRunner) Run(_ context.Context, role agentcore.Role, prompt string) (agentcore.AgentResult, error) {
	runner.prompts = append(runner.prompts, prompt)
	if len(runner.summaries) == 0 {
		return agentcore.AgentResult{}, errors.New("unexpected project contract extraction")
	}
	summary := runner.summaries[0]
	runner.summaries = runner.summaries[1:]
	return agentcore.AgentResult{Role: string(role), OK: true, Summary: summary}, nil
}

func TestProjectBootstrapDefaultsAndDraftDoNotWrite(t *testing.T) {
	root := t.TempDir()
	o := &Orchestrator{baseDir: root, dir: root}

	profile, answers, err := o.ProjectBootstrapDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if profile.Mode != projectprofile.ModeGreenfield || answers.Mode != projectprofile.ModeGreenfield {
		t.Fatalf("modes = %q/%q, want greenfield", profile.Mode, answers.Mode)
	}
	answers.Purpose = "Build a safe project."
	answers.Safety = []string{"Never deploy automatically."}
	path, content, err := o.BootstrapManifestDraft(t.Context(), answers)
	if err != nil {
		t.Fatal(err)
	}
	if path != projectprofile.ManifestPath(profile.Root) {
		t.Fatalf("path = %q", path)
	}
	if !strings.Contains(string(content), `mode: "greenfield"`) || !strings.Contains(string(content), "maestro_schema: 1") || !strings.Contains(string(content), "Never deploy automatically.") {
		t.Fatalf("unexpected contract:\n%s", content)
	}
	assertNotWritten(t, path)
}

func TestOnboardManifestDraftDiscoversAndDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "go.mod", "module example.com/backend\n")
	writeProjectFixture(t, root, "web/package.json", `{"name":"web","scripts":{"test":"vitest"}}`)
	o := &Orchestrator{baseDir: root, dir: root}

	profile, answers, err := o.ProjectOnboardProfile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if profile.Mode != projectprofile.ModeBrownfield || answers.Mode != projectprofile.ModeBrownfield {
		t.Fatalf("modes = %q/%q, want brownfield", profile.Mode, answers.Mode)
	}
	path, content, err := o.OnboardManifestDraft(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if path != projectprofile.ManifestPath(profile.Root) {
		t.Fatalf("path = %q", path)
	}
	for _, want := range []string{`mode: "brownfield"`, `"go"`, `"node"`, `"web"`} {
		if !strings.Contains(string(content), want) {
			t.Errorf("contract missing %q:\n%s", want, content)
		}
	}
	assertNotWritten(t, path)
}

func TestOnboardManifestDraftReconcilesConservatively(t *testing.T) {
	root := t.TempDir()
	writeProjectFixture(t, root, "Cargo.toml", "[package]\nname = \"engine\"\nversion = \"0.1.0\"\n")
	o := &Orchestrator{baseDir: root, dir: root}

	path, content, err := o.OnboardManifestDraft(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	_, same, err := o.OnboardManifestDraft(t.Context())
	if err != nil {
		t.Fatalf("exact no-op rejected: %v", err)
	}
	if string(same) != string(content) {
		t.Fatal("exact no-op produced different bytes")
	}
	if err := os.WriteFile(path, []byte("human contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = o.OnboardManifestDraft(t.Context())
	if !errors.Is(err, projectprofile.ErrManifestConflict) {
		t.Fatalf("different existing contract error = %v, want conflict", err)
	}
}

func TestProjectManifestDraftsRejectAnswersFromStaleQuestionnaires(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func(*Orchestrator) (projectprofile.Answers, error)
		run  func(*Orchestrator, projectprofile.Answers) error
	}{
		{
			name: "bootstrap",
			load: func(o *Orchestrator) (projectprofile.Answers, error) {
				_, answers, err := o.ProjectBootstrapDefaults(t.Context())
				return answers, err
			},
			run: func(o *Orchestrator, answers projectprofile.Answers) error {
				_, _, err := o.BootstrapManifestDraft(t.Context(), answers)
				return err
			},
		},
		{
			name: "onboard",
			load: func(o *Orchestrator) (projectprofile.Answers, error) {
				_, answers, err := o.ProjectOnboardProfile(t.Context())
				return answers, err
			},
			run: func(o *Orchestrator, answers projectprofile.Answers) error {
				_, _, err := o.OnboardManifestDraftWithAnswers(t.Context(), answers)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFixture(t, root, "go.mod", "module example.com/before\n")
			o := &Orchestrator{baseDir: root, dir: root}
			answers, err := tc.load(o)
			if err != nil {
				t.Fatal(err)
			}
			answers.Purpose = "Reject stale project facts."
			writeProjectFixture(t, root, "go.mod", "module example.com/after\n")
			if err := tc.run(o, answers); !errors.Is(err, projectprofile.ErrRepositoryChanged) {
				t.Fatalf("draft error = %v, want repository drift", err)
			}
			assertNotWritten(t, projectprofile.ManifestPath(root))
		})
	}
}

func TestProjectManifestConversationPersistsQuestionsAndNeverWrites(t *testing.T) {
	dir := newTestRepo(t)
	runner := &manifestConversationRunner{summaries: []string{
		`{"ready":false,"question":"Who is this for, and what must success look like?","name":"Port Scout","purpose":"","non_goals":[],"stacks":["Go"],"commands":[],"safety":[],"verification":[],"missing":["purpose","safety","verification"]}`,
		`{"ready":true,"question":"","name":"Port Scout","purpose":"Discover open TCP ports on authorized systems.","non_goals":["No exploitation"],"stacks":["Go"],"commands":[{"name":"test","run":"go test ./...","cwd":"."}],"safety":["Scan only systems the user owns or is authorized to test."],"verification":["Run go test ./..."],"missing":[]}`,
	}}
	orch := newTestOrch(t, dir, runner)
	orch.appendConversation("user", "I want a port discovery CLI.")
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}

	step, err := orch.ProjectManifestConversation(t.Context(), projectprofile.ModeBrownfield, "Build Port Scout as a safe CLI.", "")
	if err != nil {
		t.Fatal(err)
	}
	if step.Ready || !strings.Contains(step.Question, "Who is this for") {
		t.Fatalf("first step = %+v", step)
	}
	conversation := orch.Session().Conversation
	if len(conversation) < 2 || conversation[len(conversation)-1].Role != "assistant" || conversation[len(conversation)-1].Content != step.Question {
		t.Fatalf("follow-up was not persisted: %+v", conversation)
	}

	step, err = orch.ProjectManifestConversation(t.Context(), projectprofile.ModeBrownfield, "", "Python developers; authorized hosts only; go test ./... must pass.")
	if err != nil {
		t.Fatal(err)
	}
	if !step.Ready || step.Answers.Purpose == "" || len(step.Answers.Safety) == 0 {
		t.Fatalf("ready step = %+v", step)
	}
	if len(runner.prompts) != 2 || !strings.Contains(runner.prompts[0], "I want a port discovery CLI") ||
		!strings.Contains(runner.prompts[0], "Build Port Scout as a safe CLI") || !strings.Contains(runner.prompts[1], "authorized hosts only") {
		t.Fatalf("prompts lost the transcript: %#v", runner.prompts)
	}
	assertNotWritten(t, projectprofile.ManifestPath(dir))
}

func TestProjectManifestConversationRunnerIsPrivateReadOnlyAndToolFree(t *testing.T) {
	native := &nativeRunner{}
	privateNative, ok := privateProjectManifestRunner(native).(*nativeRunner)
	if !ok || !privateNative.silent || !privateNative.readOnly || !privateNative.noTools {
		t.Fatalf("private native runner = %+v", privateNative)
	}
	if native.silent || native.readOnly || native.noTools {
		t.Fatal("project setup mutated the shared native runner")
	}

	legacy := &legacyRunner{}
	privateLegacy, ok := privateProjectManifestRunner(legacy).(*legacyRunner)
	if !ok || !privateLegacy.silent || !privateLegacy.readOnly {
		t.Fatalf("private legacy runner = %+v", privateLegacy)
	}
	if legacy.silent || legacy.readOnly {
		t.Fatal("project setup mutated the shared legacy runner")
	}
}

func TestProjectStructuredOutputRequiresEveryReviewedBoundary(t *testing.T) {
	complete := projectConversationOutput{
		Name: "Port Scout", Purpose: "Find open ports.", Stacks: []string{"Python"},
		Safety: []string{"Authorized hosts only."}, Verification: []string{"Run pytest."},
	}
	if !projectStructuredOutputReady(complete) {
		t.Fatal("complete project contract was not ready")
	}
	tests := map[string]func(*projectConversationOutput){
		"name":         func(output *projectConversationOutput) { output.Name = "" },
		"purpose":      func(output *projectConversationOutput) { output.Purpose = "" },
		"stack":        func(output *projectConversationOutput) { output.Stacks = nil },
		"safety":       func(output *projectConversationOutput) { output.Safety = nil },
		"verification": func(output *projectConversationOutput) { output.Verification = nil },
	}
	for name, remove := range tests {
		t.Run(name, func(t *testing.T) {
			output := complete
			remove(&output)
			if projectStructuredOutputReady(output) {
				t.Fatalf("project output without %s was ready", name)
			}
		})
	}
}

func TestProjectConversationOutputIsStrictJSON(t *testing.T) {
	valid := `{"ready":false,"question":"What is the purpose?","name":"","purpose":"","non_goals":[],"stacks":[],"commands":[],"safety":[],"verification":[],"missing":["purpose"]}`
	var output projectConversationOutput
	if err := decodeProjectConversationOutput(valid, &output); err != nil {
		t.Fatalf("valid output: %v", err)
	}
	for name, raw := range map[string]string{
		"prose wrapper":  "result: " + valid,
		"unknown field":  strings.TrimSuffix(valid, "}") + `,"authorization":true}`,
		"second value":   valid + ` {}`,
		"invalid utf8":   string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}),
		"markdown fence": "```json\n" + valid + "\n```",
	} {
		t.Run(name, func(t *testing.T) {
			if err := decodeProjectConversationOutput(raw, &projectConversationOutput{}); err == nil {
				t.Fatalf("accepted non-strict output %q", raw)
			}
		})
	}
}

func TestProjectManifestPresenceAndModeFailClosed(t *testing.T) {
	t.Run("empty repository is greenfield", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		orch := &Orchestrator{baseDir: root, dir: root}
		mode, err := orch.RecommendedProjectMode(t.Context())
		if err != nil || mode != projectprofile.ModeGreenfield {
			t.Fatalf("mode = %q, err = %v", mode, err)
		}
	})

	t.Run("existing material is brownfield", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFixture(t, root, "pyproject.toml", "[project]\nname='port-scout'\n")
		orch := &Orchestrator{baseDir: root, dir: root}
		mode, err := orch.RecommendedProjectMode(t.Context())
		if err != nil || mode != projectprofile.ModeBrownfield {
			t.Fatalf("mode = %q, err = %v", mode, err)
		}
	})

	t.Run("manifest symlink is rejected", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(target, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, projectprofile.ManifestPath(root)); err != nil {
			t.Fatal(err)
		}
		orch := &Orchestrator{baseDir: root, dir: root}
		if present, err := orch.ProjectManifestPresent(t.Context()); err == nil || present {
			t.Fatalf("unsafe manifest = present %v, err %v", present, err)
		}
	})
}

func assertNotWritten(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("draft method wrote %q: %v", path, err)
	}
}

func writeProjectFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
