package tui

import (
	"path/filepath"

	"github.com/bryann2k/maestro/internal/projectprofile"
	"github.com/bryann2k/maestro/internal/proposals"
)

// proposalRequiresAtomicDecision identifies project contracts whose meaning
// spans the complete document. A fingerprint or safety hunk must never be
// accepted independently from the rest of the reviewed contract.
func proposalRequiresAtomicDecision(proposal *proposals.Proposal) bool {
	return proposal != nil && filepath.Base(filepath.Clean(proposal.Path)) == projectprofile.ManifestName
}
