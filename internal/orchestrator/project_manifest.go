package orchestrator

import (
	"context"

	"github.com/bryann2k/maestro/internal/projectprofile"
)

// ProjectBootstrapDefaults returns the shared greenfield profile and answers
// schema used by a future TUI questionnaire. It performs no repository write.
func (o *Orchestrator) ProjectBootstrapDefaults(ctx context.Context) (projectprofile.ProjectProfile, projectprofile.Answers, error) {
	return projectprofile.GreenfieldDefaults(ctx, o.workDir())
}

// ProjectOnboardProfile statically discovers the active repository and
// returns reviewable brownfield answers. It never executes project code.
func (o *Orchestrator) ProjectOnboardProfile(ctx context.Context) (projectprofile.ProjectProfile, projectprofile.Answers, error) {
	profile, err := projectprofile.Discover(ctx, o.workDir(), projectprofile.ModeBrownfield)
	if err != nil {
		return projectprofile.ProjectProfile{}, projectprofile.Answers{}, err
	}
	return profile, projectprofile.AnswersFromProfile(profile), nil
}

// BootstrapManifestDraft renders the greenfield MAESTRO.md and returns it for
// TUI staging. Existing different content is a conflict; exact content is a
// no-op. The method never writes the returned bytes.
func (o *Orchestrator) BootstrapManifestDraft(ctx context.Context, answers projectprofile.Answers) (string, []byte, error) {
	profile, _, err := projectprofile.GreenfieldDefaults(ctx, o.workDir())
	if err != nil {
		return "", nil, err
	}
	return projectprofile.Draft(ctx, profile, answers)
}

// OnboardManifestDraft discovers an existing repository and renders its
// deterministic MAESTRO.md for staging without writing it.
func (o *Orchestrator) OnboardManifestDraft(ctx context.Context) (string, []byte, error) {
	profile, answers, err := o.ProjectOnboardProfile(ctx)
	if err != nil {
		return "", nil, err
	}
	return projectprofile.Draft(ctx, profile, answers)
}

// OnboardManifestDraftWithAnswers is the questionnaire-ready form of
// OnboardManifestDraft: discovered facts remain evidence while confirmed
// answers can replace the reviewable contract fields.
func (o *Orchestrator) OnboardManifestDraftWithAnswers(ctx context.Context, answers projectprofile.Answers) (string, []byte, error) {
	profile, _, err := o.ProjectOnboardProfile(ctx)
	if err != nil {
		return "", nil, err
	}
	return projectprofile.Draft(ctx, profile, answers)
}
