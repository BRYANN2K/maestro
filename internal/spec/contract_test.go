package spec

import (
	"strings"
	"testing"
)

func readyContract() *Spec {
	return &Spec{
		SchemaVersion: CurrentSchemaVersion,
		Recipe:        RecipeFeature,
		ID:            "structured-contract",
		Title:         "Structured contract",
		Status:        StatusProposal,
		Category:      "feat",
		Success:       []string{"acceptance passes"},
		Requirements: []Requirement{{
			ID:        "REQ-API-001",
			Statement: "The system shall expose a health endpoint.",
			Priority:  "must",
			Scenarios: []Scenario{{
				ID: "SCN-API-001-A", Given: []string{"a running server"},
				When: "the health endpoint is requested", Then: []string{"HTTP 200 is returned"},
			}},
		}, {
			ID:        "REQ-API-002",
			Statement: "The system could expose build metadata.",
			Priority:  "could",
			Scenarios: []Scenario{{
				ID: "SCN-API-002-A", Given: []string{"build metadata exists"},
				When: "health is requested", Then: []string{"metadata may be returned"},
			}},
		}},
		Batches: []Batch{{ID: "B1", Name: "Health", RequirementIDs: []string{"REQ-API-001"}}},
	}
}

func TestValidateReadinessReadyContract(t *testing.T) {
	report := readyContract().ValidateReadiness()
	if !report.Ready() {
		t.Fatalf("ready contract rejected: %s", report.Error())
	}
	if len(report.Errors()) != 0 {
		t.Fatalf("unexpected errors: %+v", report.Errors())
	}
}

func TestValidateReadinessLegacyCompatibility(t *testing.T) {
	legacy := testSpec()
	report := legacy.ValidateReadiness()
	if !report.Ready() {
		t.Fatalf("legacy spec must stay acceptable: %s", report.Error())
	}
	if len(report.Warnings()) != 1 || report.Warnings()[0].Code != "SPEC_LEGACY" {
		t.Fatalf("legacy warnings = %+v", report.Warnings())
	}
}

func TestValidateReadinessFindings(t *testing.T) {
	contract := readyContract()
	contract.Requirements[0].Scenarios = nil
	contract.Questions = []Question{{
		ID: "Q-001", Prompt: "Which datastore?", Severity: "high", Status: "open",
		Blocking: true, RequirementIDs: []string{"REQ-API-001"},
	}}
	report := contract.ValidateReadiness()
	if report.Ready() {
		t.Fatal("invalid contract reported ready")
	}
	got := report.Error()
	for _, code := range []string{"REQ_SCENARIO_MISSING", "QUESTION_UNRESOLVED"} {
		if !strings.Contains(got, code) {
			t.Errorf("report %q missing %s", got, code)
		}
	}
}

func TestStructuredContractRoundTrip(t *testing.T) {
	want := readyContract()
	want.Questions = []Question{{
		ID: "Q-001", Prompt: "Use JSON?", Severity: "low", Status: "resolved", Answer: "yes",
	}}
	data, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != CurrentSchemaVersion || got.Recipe != RecipeFeature {
		t.Fatalf("contract metadata = version %d recipe %q", got.SchemaVersion, got.Recipe)
	}
	if len(got.Requirements) != 2 || got.Requirements[0].Scenarios[0].ID != "SCN-API-001-A" {
		t.Fatalf("requirements did not round-trip: %+v", got.Requirements)
	}
	if len(got.Batches) != 1 || len(got.Batches[0].RequirementIDs) != 1 {
		t.Fatalf("batch coverage did not round-trip: %+v", got.Batches)
	}
}

func TestInferRecipe(t *testing.T) {
	tests := []struct {
		prompt, category string
		want             Recipe
	}{
		{"Fix the crash in login", "fix", RecipeBug},
		{"Correct a README typo", "docs", RecipeQuick},
		{"Add team invitations", "feat", RecipeFeature},
		{"Migrate the database schema", "feat", RecipeArchitecture},
		{"Add authentication", "feat", RecipeArchitecture},
	}
	for _, test := range tests {
		if got := InferRecipe(test.prompt, test.category); got != test.want {
			t.Errorf("InferRecipe(%q, %q) = %q, want %q", test.prompt, test.category, got, test.want)
		}
	}
}
