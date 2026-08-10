package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/projectprofile"
)

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
