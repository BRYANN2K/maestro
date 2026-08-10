package orchestrator

import (
	"fmt"

	"github.com/bryann2k/maestro/internal/session"
)

// phases is the pipeline state machine. Every transition is explicit; an
// invalid transition is an error, never a silent no-op.
//
//	CHAT → /propose → PROPOSE → /accept → SPEC + BRANCH MENU
//	     → /build → BUILD (dev) → done
//	     → REVIEW → /review | /accept (manual)
//	     → /docs | /archive
//	     → ARCHIVE → back to CHAT
var phases = &phaseMachine{
	transitions: map[session.Phase]map[session.Phase]bool{
		session.PhaseChat: {
			session.PhasePropose: true,
		},
		session.PhasePropose: {
			session.PhaseSpec: true, // /accept
			session.PhaseChat: true, // /cancel
		},
		session.PhaseSpec: {
			session.PhaseBuild: true, // /build
			session.PhaseChat:  true, // abort
		},
		session.PhaseBuild: {
			session.PhaseReview: true, // /review (or manual /accept)
			session.PhaseChat:   true,
		},
		session.PhaseReview: {
			session.PhaseBuild:   true, // /fix
			session.PhaseDocs:    true, // /docs
			session.PhaseArchive: true, // /archive
			session.PhaseChat:    true,
		},
		session.PhaseDocs: {
			session.PhaseBuild:   true, // failed /review; enables /fix
			session.PhaseArchive: true,
			session.PhaseChat:    true,
		},
		session.PhaseArchive: {
			session.PhaseChat: true,
		},
	},
}

type phaseMachine struct {
	transitions map[session.Phase]map[session.Phase]bool
}

// Transition validates and returns the new phase, or an error when the move
// is not allowed.
func (m *phaseMachine) Transition(from, to session.Phase) error {
	if from == to {
		return nil
	}
	if m.transitions[from][to] {
		return nil
	}
	return fmt.Errorf("cannot move from phase %q to %q", from, to)
}
