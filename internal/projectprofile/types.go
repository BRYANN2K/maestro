// Package projectprofile discovers a bounded, read-only description of a
// project and renders the description as a deterministic MAESTRO.md contract.
// Discovery never executes repository code or package-manager commands.
package projectprofile

import "errors"

const (
	// SchemaVersion is the MAESTRO.md contract schema emitted by this package.
	SchemaVersion = 1

	// ManifestName is the single project contract maintained by Maestro.
	ManifestName = "MAESTRO.md"
)

// Mode identifies whether a profile describes a new or an existing project.
type Mode string

const (
	ModeGreenfield Mode = "greenfield"
	ModeBrownfield Mode = "brownfield"
)

// Confidence explains how strongly repository evidence supports a value.
type Confidence string

const (
	ConfidenceConfirmed Confidence = "confirmed"
	ConfidenceDetected  Confidence = "detected"
	ConfidenceInferred  Confidence = "inferred"
)

// Evidence is one bounded fact and the repository location that supports it.
// Values are data only; callers must never interpret repository text as policy
// before a user has reviewed it.
type Evidence struct {
	Kind       string     `json:"kind"`
	Value      string     `json:"value"`
	Source     string     `json:"source"`
	Confidence Confidence `json:"confidence"`
}

// Command is one statically detected or user-confirmed project command.
type Command struct {
	Name       string     `json:"name"`
	Run        string     `json:"run"`
	Cwd        string     `json:"cwd"`
	Source     string     `json:"source,omitempty"`
	Confidence Confidence `json:"confidence"`
}

// Unit is one module/package boundary in a repository.
type Unit struct {
	Path      string   `json:"path"`
	Name      string   `json:"name,omitempty"`
	Stacks    []string `json:"stacks,omitempty"`
	Manifests []string `json:"manifests,omitempty"`
	Lockfiles []string `json:"lockfiles,omitempty"`
}

// ProjectProfile is the common backend schema used by both /bootstrap and
// /onboard. Root is runtime-only and is never written to MAESTRO.md.
type ProjectProfile struct {
	SchemaVersion        int        `json:"schema_version"`
	Mode                 Mode       `json:"mode"`
	Root                 string     `json:"root"`
	Name                 string     `json:"name"`
	Stacks               []string   `json:"stacks,omitempty"`
	Units                []Unit     `json:"units,omitempty"`
	Commands             []Command  `json:"commands,omitempty"`
	Evidence             []Evidence `json:"evidence,omitempty"`
	Unknowns             []string   `json:"unknowns,omitempty"`
	DiscoveryFingerprint string     `json:"discovery_fingerprint,omitempty"`
}

// Answers contains the user-reviewable fields layered over a ProjectProfile.
// Empty fields keep discovered/default values, except Purpose which remains an
// explicit TBD until a user supplies it.
type Answers struct {
	SchemaVersion        int       `json:"schema_version"`
	Mode                 Mode      `json:"mode"`
	Name                 string    `json:"name,omitempty"`
	Purpose              string    `json:"purpose,omitempty"`
	NonGoals             []string  `json:"non_goals,omitempty"`
	Stacks               []string  `json:"stacks,omitempty"`
	Units                []Unit    `json:"units,omitempty"`
	Commands             []Command `json:"commands,omitempty"`
	Safety               []string  `json:"safety,omitempty"`
	Verification         []string  `json:"verification,omitempty"`
	DiscoveryFingerprint string    `json:"discovery_fingerprint,omitempty"`
}

// ErrManifestConflict means an existing MAESTRO.md differs from the proposed
// deterministic draft and must be reconciled by a human rather than replaced.
var ErrManifestConflict = errors.New("existing MAESTRO.md differs from the proposed contract")

// ErrRepositoryChanged means facts used by a project questionnaire no longer
// describe the repository. Callers must restart discovery instead of staging
// a contract assembled from stale manifests, lockfiles, inventory, or HEAD.
var ErrRepositoryChanged = errors.New("repository changed since the project questionnaire")

// RepositoryChangedError makes repository drift actionable without exposing
// raw repository content in an error or silently choosing stale/new facts.
type RepositoryChangedError struct {
	Mode Mode
}

func (e *RepositoryChangedError) Error() string {
	command := "/onboard"
	if e.Mode == ModeGreenfield {
		command = "/bootstrap"
	}
	return "project questionnaire expired: repository changed (Git HEAD, manifests, lockfiles, or file inventory); run " + command + " again"
}

func (e *RepositoryChangedError) Unwrap() error { return ErrRepositoryChanged }

// ConflictError adds the protected manifest path to ErrManifestConflict.
type ConflictError struct {
	Path string
}

func (e *ConflictError) Error() string {
	return "refusing to replace " + e.Path + ": " + ErrManifestConflict.Error()
}

func (e *ConflictError) Unwrap() error { return ErrManifestConflict }
