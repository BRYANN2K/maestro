package spec

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// CurrentSchemaVersion is the newest structured contract version written by
// Maestro. Version zero is intentionally reserved for legacy specs.
const CurrentSchemaVersion = 2

// Recipe controls the amount and shape of planning required for a change.
type Recipe string

const (
	RecipeQuick        Recipe = "quick"
	RecipeFeature      Recipe = "feature"
	RecipeBug          Recipe = "bug"
	RecipeArchitecture Recipe = "architecture"
)

var validRecipes = []Recipe{RecipeQuick, RecipeFeature, RecipeBug, RecipeArchitecture}

// Requirement is one durable, normative product contract.
type Requirement struct {
	ID        string     `yaml:"id" json:"id"`
	Statement string     `yaml:"statement" json:"statement"`
	Priority  string     `yaml:"priority,omitempty" json:"priority,omitempty"`
	Scenarios []Scenario `yaml:"scenarios,omitempty" json:"scenarios,omitempty"`
}

// Scenario describes one observable acceptance case in Given/When/Then form.
type Scenario struct {
	ID       string   `yaml:"id" json:"id"`
	Given    []string `yaml:"given,omitempty" json:"given,omitempty"`
	When     string   `yaml:"when" json:"when"`
	Then     []string `yaml:"then" json:"then"`
	Verifier string   `yaml:"verifier,omitempty" json:"verifier,omitempty"`
}

// Question is a clarification tracked as state rather than free-form prose.
type Question struct {
	ID             string   `yaml:"id" json:"id"`
	Prompt         string   `yaml:"prompt" json:"prompt"`
	Severity       string   `yaml:"severity,omitempty" json:"severity,omitempty"`
	Status         string   `yaml:"status,omitempty" json:"status,omitempty"`
	Blocking       bool     `yaml:"blocking,omitempty" json:"blocking,omitempty"`
	RequirementIDs []string `yaml:"requirements,omitempty" json:"requirements,omitempty"`
	Answer         string   `yaml:"answer,omitempty" json:"answer,omitempty"`
}

// DiagnosticSeverity identifies whether a readiness finding blocks acceptance.
type DiagnosticSeverity string

const (
	SeverityError   DiagnosticSeverity = "error"
	SeverityWarning DiagnosticSeverity = "warning"
)

// Diagnostic is one stable, frontend-independent contract finding.
type Diagnostic struct {
	Code     string
	Severity DiagnosticSeverity
	Path     string
	Message  string
}

// ReadinessReport is the complete result of checking a contract for acceptance.
type ReadinessReport struct {
	Diagnostics []Diagnostic
}

// Ready reports whether the contract can cross the /accept gate.
func (r ReadinessReport) Ready() bool {
	return !slices.ContainsFunc(r.Diagnostics, func(d Diagnostic) bool {
		return d.Severity == SeverityError
	})
}

// Errors returns only blocking findings.
func (r ReadinessReport) Errors() []Diagnostic {
	return slices.DeleteFunc(slices.Clone(r.Diagnostics), func(d Diagnostic) bool {
		return d.Severity != SeverityError
	})
}

// Warnings returns only non-blocking findings.
func (r ReadinessReport) Warnings() []Diagnostic {
	return slices.DeleteFunc(slices.Clone(r.Diagnostics), func(d Diagnostic) bool {
		return d.Severity != SeverityWarning
	})
}

// Error renders blocking diagnostics for headless callers.
func (r ReadinessReport) Error() string {
	issues := r.Errors()
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		where := ""
		if issue.Path != "" {
			where = " at " + issue.Path
		}
		parts = append(parts, fmt.Sprintf("%s%s: %s", issue.Code, where, issue.Message))
	}
	return strings.Join(parts, "; ")
}

var (
	requirementIDRe = regexp.MustCompile(`^REQ-[A-Z0-9][A-Z0-9-]*$`)
	scenarioIDRe    = regexp.MustCompile(`^SCN-[A-Z0-9][A-Z0-9-]*$`)
	questionIDRe    = regexp.MustCompile(`^Q-[A-Z0-9][A-Z0-9-]*$`)
)

// ValidateReadiness checks whether a spec is safe to accept. Legacy specs are
// deliberately allowed through with a warning so existing repositories and
// restored sessions do not require migration.
func (s *Spec) ValidateReadiness() ReadinessReport {
	var out ReadinessReport
	add := func(code string, severity DiagnosticSeverity, path, message string) {
		out.Diagnostics = append(out.Diagnostics, Diagnostic{Code: code, Severity: severity, Path: path, Message: message})
	}
	if err := s.Valid(); err != nil {
		add("SPEC_INVALID", SeverityError, "spec", err.Error())
		return out
	}
	if s.SchemaVersion == 0 {
		add("SPEC_LEGACY", SeverityWarning, "schema_version", "legacy contract; structured readiness checks were skipped")
		return out
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		add("SPEC_SCHEMA_UNSUPPORTED", SeverityError, "schema_version", fmt.Sprintf("got %d, want %d", s.SchemaVersion, CurrentSchemaVersion))
	}
	if !slices.Contains(validRecipes, s.Recipe) {
		add("SPEC_RECIPE_REQUIRED", SeverityError, "recipe", "use quick, feature, bug, or architecture")
	}
	if len(s.Requirements) == 0 {
		add("SPEC_REQUIREMENTS_EMPTY", SeverityError, "requirements", "add at least one testable requirement")
	}

	knownRequirements := make(map[string]struct{}, len(s.Requirements))
	knownScenarios := make(map[string]struct{})
	for i, requirement := range s.Requirements {
		path := fmt.Sprintf("requirements[%d]", i)
		if !requirementIDRe.MatchString(requirement.ID) {
			add("REQ_ID_INVALID", SeverityError, path+".id", "use a durable ID such as REQ-AUTH-001")
		} else if _, exists := knownRequirements[requirement.ID]; exists {
			add("REQ_ID_DUPLICATE", SeverityError, path+".id", "requirement IDs must be unique")
		} else {
			knownRequirements[requirement.ID] = struct{}{}
		}
		if strings.TrimSpace(requirement.Statement) == "" {
			add("REQ_STATEMENT_EMPTY", SeverityError, path+".statement", "write an observable normative statement")
		}
		if !slices.Contains([]string{"must", "should", "could"}, requirement.Priority) {
			add("REQ_PRIORITY_INVALID", SeverityError, path+".priority", "use must, should, or could")
		}
		if len(requirement.Scenarios) == 0 {
			add("REQ_SCENARIO_MISSING", SeverityError, path+".scenarios", "add at least one Given/When/Then scenario")
		}
		for j, scenario := range requirement.Scenarios {
			scenarioPath := fmt.Sprintf("%s.scenarios[%d]", path, j)
			if !scenarioIDRe.MatchString(scenario.ID) {
				add("SCN_ID_INVALID", SeverityError, scenarioPath+".id", "use a durable ID such as SCN-AUTH-001-A")
			} else if _, exists := knownScenarios[scenario.ID]; exists {
				add("SCN_ID_DUPLICATE", SeverityError, scenarioPath+".id", "scenario IDs must be unique")
			} else {
				knownScenarios[scenario.ID] = struct{}{}
			}
			if len(scenario.Given) == 0 || strings.TrimSpace(scenario.When) == "" || len(scenario.Then) == 0 {
				add("SCN_INCOMPLETE", SeverityError, scenarioPath, "given, when, and then must all be present")
			}
		}
	}

	knownQuestions := make(map[string]struct{}, len(s.Questions))
	for i, question := range s.Questions {
		path := fmt.Sprintf("questions[%d]", i)
		if !questionIDRe.MatchString(question.ID) {
			add("QUESTION_ID_INVALID", SeverityError, path+".id", "use a durable ID such as Q-001")
		} else if _, exists := knownQuestions[question.ID]; exists {
			add("QUESTION_ID_DUPLICATE", SeverityError, path+".id", "question IDs must be unique")
		} else {
			knownQuestions[question.ID] = struct{}{}
		}
		if strings.TrimSpace(question.Prompt) == "" {
			add("QUESTION_PROMPT_EMPTY", SeverityError, path+".prompt", "clarification prompt is required")
		}
		if !slices.Contains([]string{"critical", "high", "medium", "low"}, question.Severity) {
			add("QUESTION_SEVERITY_INVALID", SeverityError, path+".severity", "use critical, high, medium, or low")
		}
		if !slices.Contains([]string{"open", "resolved"}, question.Status) {
			add("QUESTION_STATUS_INVALID", SeverityError, path+".status", "use open or resolved")
		}
		if question.Blocking && !questionResolved(question) {
			add("QUESTION_UNRESOLVED", SeverityError, path, fmt.Sprintf("%s must be answered before acceptance", question.ID))
		}
		for _, requirementID := range question.RequirementIDs {
			if _, exists := knownRequirements[requirementID]; !exists {
				add("QUESTION_REQUIREMENT_UNKNOWN", SeverityError, path+".requirements", fmt.Sprintf("%s does not exist", requirementID))
			}
		}
	}

	if s.Recipe != RecipeQuick {
		covered := make(map[string]struct{})
		for i, batch := range s.Batches {
			for _, requirementID := range batch.RequirementIDs {
				if _, exists := knownRequirements[requirementID]; !exists {
					add("BATCH_REQUIREMENT_UNKNOWN", SeverityError, fmt.Sprintf("batches[%d].requirements", i), fmt.Sprintf("%s does not exist", requirementID))
					continue
				}
				covered[requirementID] = struct{}{}
			}
		}
		for _, requirement := range s.Requirements {
			if requirement.Priority == "could" {
				continue
			}
			requirementID := requirement.ID
			if _, exists := covered[requirementID]; !exists {
				add("REQ_BATCH_UNCOVERED", SeverityError, "batches", fmt.Sprintf("%s is not covered by a delivery batch", requirementID))
			}
		}
	}
	if len(s.Success) == 0 {
		add("SPEC_SUCCESS_EMPTY", SeverityWarning, "success_criteria", "add measurable whole-change success criteria")
	}
	sort.SliceStable(out.Diagnostics, func(i, j int) bool {
		if out.Diagnostics[i].Severity != out.Diagnostics[j].Severity {
			return out.Diagnostics[i].Severity == SeverityError
		}
		if out.Diagnostics[i].Path != out.Diagnostics[j].Path {
			return out.Diagnostics[i].Path < out.Diagnostics[j].Path
		}
		return out.Diagnostics[i].Code < out.Diagnostics[j].Code
	})
	return out
}

func questionResolved(question Question) bool {
	return question.Status == "resolved" || strings.TrimSpace(question.Answer) != ""
}

// InferRecipe classifies a request without model access. The explicit category
// wins for fixes/docs, while high-impact vocabulary promotes architectural work.
func InferRecipe(prompt, category string) Recipe {
	p := strings.ToLower(prompt)
	architectureTerms := []string{
		"architecture", "migration", "migrate", "schema", "database", "security",
		"authentication", "authorization", "encryption", "infrastructure", "distributed",
		"breaking change", "rewrite", "rebuild", "refactor entire", "cross-cutting",
	}
	if containsAny(p, architectureTerms...) {
		return RecipeArchitecture
	}
	if category == "fix" || containsAny(p, "bug", "fix", "repair", "regression", "broken", "crash") {
		return RecipeBug
	}
	quickTerms := []string{"typo", "copy change", "rename", "small change", "quick", "readme", "documentation", "docs"}
	if category == "docs" || containsAny(p, quickTerms...) {
		return RecipeQuick
	}
	return RecipeFeature
}

func containsAny(value string, candidates ...string) bool {
	return slices.ContainsFunc(candidates, func(candidate string) bool {
		return strings.Contains(value, candidate)
	})
}
