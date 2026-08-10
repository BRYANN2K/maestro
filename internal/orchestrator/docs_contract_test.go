package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

type captureDocsLegacyAgent struct {
	prompt  string
	opts    agent.Options
	summary string
}

func (a *captureDocsLegacyAgent) Name() string     { return "capture-docs" }
func (a *captureDocsLegacyAgent) Models() []string { return []string{"gpt-test"} }

func (a *captureDocsLegacyAgent) Execute(_ context.Context, task string, opts agent.Options) (<-chan agentcore.StreamEvent, error) {
	a.prompt = task
	a.opts = opts
	ch := make(chan agentcore.StreamEvent, 2)
	ch <- agentcore.NewEvent(nil, agentcore.RoleDocs, agentcore.EvTextDelta, agentcore.TextDelta{Text: a.summary})
	ch <- agentcore.NewEvent(nil, agentcore.RoleDocs, agentcore.EvDone, agentcore.Done{})
	close(ch)
	return ch, nil
}

func TestLegacyDocsReceivesFullContractAndRejectsInventedObligations(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})

	requirement := "GREETING_NAME is optional; when present, preserve its value byte-for-byte (for example, `  Ada  ` stays `  Ada  `); when absent, fall back to the exact value `World`."
	decision := "Use GREETING_NAME only as an optional greeting override."
	success := "An unset GREETING_NAME prints Hello, World!."
	active := &spec.Spec{
		SchemaVersion: spec.CurrentSchemaVersion,
		Recipe:        spec.RecipeFeature,
		ID:            "optional-greeting-name",
		Title:         "Optional exact greeting name",
		Status:        spec.StatusImplemented,
		Category:      "feat",
		Requirements: []spec.Requirement{{
			ID: "REQ-GREETING-001", Statement: requirement, Priority: "must",
		}},
		Decisions: []string{decision},
		Success:   []string{success},
		Body: `# Goal

Print a configurable greeting without changing the supplied name.
`,
	}
	design := "# Design\n\nUse os.LookupEnv and return the raw value without trimming.\n"
	tasks := "# Tasks\n\n- [x] Add the optional environment lookup.\n- [x] Cover unset and space-preserving cases.\n"
	if err := orch.store.WriteTrio(t.Context(), active, design, tasks); err != nil {
		t.Fatal(err)
	}
	implementation := `package main

import "os"

func greetingName() string {
	if value, ok := os.LookupEnv("GREETING_NAME"); ok {
		return value
	}
	return "World"
}

func main() {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(implementation), 0o644); err != nil {
		t.Fatal(err)
	}

	orch.spec = active
	orch.sess.SpecID = active.ID
	contract, err := captureSpecContract(orch.store, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	orch.sess.SpecContract = contract
	orch.sess.Phase = session.PhaseReview
	identity, err := readGitWorkspaceIdentity(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	orch.sess.WorkspaceRef = identity.ref
	fingerprint, err := orch.worktreeFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	orch.sess.Review = &session.ReviewResult{
		Level: "pass", Summary: "Review: pass — optional fallback and exact spaces covered",
		Findings: "none", Fingerprint: fingerprint, GitRef: identity.ref, GitHead: identity.head,
	}

	contradictory := fmt.Sprintf(`# ADR — contradictory greeting

Status: accepted
Date: 2026-08-08
Spec: specs/%s/spec.md

## Context

%s

## Decision

- %s
- %s
- %s
- At startup, GREETING_NAME must be present; validate and escape it before use.

## Alternatives

- None.

## Consequences

- Extra safety.

## Operational notes

- Provision the variable before boot.
`, active.ID, active.GoalLine(), requirement, decision, success)
	capture := &captureDocsLegacyAgent{summary: contradictory}
	orch.runner = &legacyRunner{agent: capture, model: "gpt-5.6-luna", o: orch}

	path, adr, err := orch.DocsDraft(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("DocsDraft wrote before acceptance: %v", err)
	}
	for _, requiredContext := range []string{
		"MAESTRO_OPERATION: DOCS_CONTRACT",
		requirement,
		decision,
		success,
		"Use os.LookupEnv and return the raw value without trimming.",
		"Cover unset and space-preserving cases.",
		"Review: pass — optional fallback and exact spaces covered",
		`os.LookupEnv("GREETING_NAME")`,
		"Do not invent validation, sanitization, escaping, secrets, credential provisioning, or startup checks.",
		"Preserve optionality, fallback behavior, exact literal values, and whitespace semantics exactly as written.",
	} {
		if !strings.Contains(capture.prompt, requiredContext) {
			t.Errorf("legacy docs prompt missing %q", requiredContext)
		}
	}
	wantWorkDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if capture.opts.Model != "gpt-5.6-luna" || capture.opts.WorkDir != wantWorkDir {
		t.Fatalf("legacy docs options = %+v", capture.opts)
	}
	if !strings.Contains(adr, requirement) || !strings.Contains(adr, decision) || !strings.Contains(adr, success) {
		t.Fatalf("safe ADR lost normative contract:\n%s", adr)
	}
	lowerADR := strings.ToLower(adr)
	for _, contradiction := range []string{"must be present", "validate", "escape it", "startup", "provision the variable"} {
		if strings.Contains(lowerADR, contradiction) {
			t.Errorf("safe ADR retained invented obligation %q:\n%s", contradiction, adr)
		}
	}
}

func TestValidateDocsADRAcceptsContractFaithfulCandidate(t *testing.T) {
	claim := "The optional value is preserved byte-for-byte."
	contract := docsContract{
		normative:  []string{claim},
		specSource: claim,
		date:       "2026-08-08",
		specPath:   "specs/faithful/spec.md",
	}
	candidate := `# ADR — faithful

Status: accepted
Date: 2026-08-08
Spec: specs/faithful/spec.md

## Context

The greeting is configurable.

## Decision

- The optional value is preserved byte-for-byte.

## Alternatives

- None recorded.

## Consequences

- Existing fallback behavior remains.

## Operational notes

- No additional operation is introduced.
`
	if err := validateDocsADR(candidate, contract); err != nil {
		t.Fatalf("faithful ADR rejected: %v", err)
	}
}

func TestFallbackADRPassesStructuredContractValidation(t *testing.T) {
	orch := &Orchestrator{
		spec: &spec.Spec{
			ID:    "safe-fallback",
			Title: "Safe fallback",
			Requirements: []spec.Requirement{{
				ID: "REQ-001", Statement: "The optional value is preserved byte-for-byte.", Priority: "must",
			}},
			Decisions: []string{"Use the configured fallback when the value is absent."},
			Success:   []string{"An absent value produces the documented fallback."},
		},
		sess: session.Session{Review: &session.ReviewResult{Level: "pass"}},
	}
	contract := docsContract{
		normative:  orch.docsNormativeClaims(),
		specSource: "accepted specification",
		date:       "2026-08-08",
		specPath:   "specs/safe-fallback/spec.md",
	}
	if err := validateDocsADR(orch.fallbackADR("2026-08-08"), contract); err != nil {
		t.Fatalf("fallback ADR violates its structured contract: %v", err)
	}
}

func TestValidateDocsADRRejectsClaimOnlyInAlternatives(t *testing.T) {
	claim := "The optional value is preserved byte-for-byte."
	contract := docsContract{normative: []string{claim}, specSource: claim, date: "2026-08-08", specPath: "specs/optional/spec.md"}
	candidate := `# ADR — misplaced claim

Status: accepted
Date: 2026-08-08
Spec: specs/optional/spec.md

## Context

The greeting is configurable.

## Decision

## Alternatives

- The optional value is preserved byte-for-byte.

## Consequences

- Existing fallback behavior remains.

## Operational notes

- No additional operation is introduced.
`
	if err := validateDocsADR(candidate, contract); err == nil || !strings.Contains(err.Error(), "decision must contain normative claim exactly once") {
		t.Fatalf("validateDocsADR error = %v, want misplaced-claim refusal", err)
	}
}

func TestValidateDocsADRRejectsNegatedNormativeClaim(t *testing.T) {
	claim := "The optional value is preserved byte-for-byte."
	contract := docsContract{normative: []string{claim}, specSource: claim, date: "2026-08-08", specPath: "specs/optional/spec.md"}
	candidate := `# ADR — negated claim

Status: accepted
Date: 2026-08-08
Spec: specs/optional/spec.md

## Context

The greeting is configurable.

## Decision

- It is not true that The optional value is preserved byte-for-byte.

## Alternatives

- None.

## Consequences

- Existing fallback behavior remains.

## Operational notes

- No additional operation is introduced.
`
	if err := validateDocsADR(candidate, contract); err == nil || !strings.Contains(err.Error(), "introduced or altered a normative claim") {
		t.Fatalf("validateDocsADR error = %v, want negation refusal", err)
	}
}

func TestValidateDocsADRDoesNotGloballyWhitelistRequired(t *testing.T) {
	claim := "Authentication is required."
	contract := docsContract{normative: []string{claim}, specSource: claim, date: "2026-08-08", specPath: "specs/auth/spec.md"}
	candidate := `# ADR — invented obligation

Status: accepted
Date: 2026-08-08
Spec: specs/auth/spec.md

## Context

Authentication protects the service.

## Decision

- Authentication is required.
- GREETING_NAME is required.

## Alternatives

- None.

## Consequences

- Existing behavior remains.

## Operational notes

- No additional operation is introduced.
`
	if err := validateDocsADR(candidate, contract); err == nil || !strings.Contains(err.Error(), "introduced or altered a normative claim") {
		t.Fatalf("validateDocsADR error = %v, want invented-obligation refusal", err)
	}
}

func TestValidateDocsADRRejectsObligationInUnknownSection(t *testing.T) {
	claim := "Authentication is required."
	contract := docsContract{normative: []string{claim}, specSource: claim, date: "2026-08-08", specPath: "specs/auth/spec.md"}
	candidate := `# ADR — hidden obligation

Status: accepted
Date: 2026-08-08
Spec: specs/auth/spec.md

## Context

Authentication protects the service.

## Decision

- Authentication is required.

## Alternatives

- None.

## Consequences

- Existing behavior remains.

## Operational notes

- No additional operation is introduced.

## Deployment

- GREETING_NAME must be provisioned at startup.
`
	if err := validateDocsADR(candidate, contract); err == nil || !strings.Contains(err.Error(), "unsupported ADR section") {
		t.Fatalf("validateDocsADR error = %v, want hidden-section refusal", err)
	}
}
