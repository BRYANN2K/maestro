package orchestrator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

const specContractVersion = 1

type specTrioState struct {
	specHash          string
	designHash        string
	tasksTemplateHash string
	taskStates        []bool
}

// ensureSpecContract only validates the baseline persisted atomically by
// Accept. It must never create one from the live filesystem: doing that at the
// first build would bless edits made between /accept and /build.
func (o *Orchestrator) ensureSpecContract() error {
	if o.spec == nil {
		return errors.New("spec contract: no active spec")
	}
	if o.sess.SpecContract == nil {
		return errors.New("spec contract: restored legacy session has no accepted-trio baseline; restart the spec lifecycle")
	}
	return o.validateSpecContract()
}

// validateSpecContract requires the exact accepted spec/design and the exact
// task state last authorized by a successful review. Build and Fix may leave
// monotonic checkbox progress in the worktree, but that progress remains
// pending until Review blesses it.
func (o *Orchestrator) validateSpecContract() error {
	contract, state, err := o.currentSpecContractState()
	if err != nil {
		return err
	}
	if state.specHash != contract.SpecHash {
		return errors.New("spec contract: spec.md was modified; restore the accepted specification")
	}
	if state.designHash != contract.DesignHash {
		return errors.New("spec contract: design.md was modified; restore the accepted design")
	}
	if state.tasksTemplateHash != contract.TasksTemplateHash || len(state.taskStates) != len(contract.TaskStates) {
		return errors.New("spec contract: tasks.md structure or text changed; only [ ] to [x] checkbox transitions are allowed")
	}
	for i := range state.taskStates {
		if state.taskStates[i] != contract.TaskStates[i] {
			return fmt.Errorf("spec contract: tasks.md checkbox %d changed outside a successful review; restore it before continuing", i+1)
		}
	}
	return nil
}

// validatePendingSpecContract permits only unchecked-to-checked progress over
// the durable review-authorized state. It is the entry gate for Review and
// Fix: pending progress can be tested and repaired without being trusted.
func (o *Orchestrator) validatePendingSpecContract() (specTrioState, error) {
	contract, state, err := o.currentSpecContractState()
	if err != nil {
		return specTrioState{}, err
	}
	if state.specHash != contract.SpecHash {
		return specTrioState{}, errors.New("spec contract: spec.md was modified; restore the accepted specification")
	}
	if state.designHash != contract.DesignHash {
		return specTrioState{}, errors.New("spec contract: design.md was modified; restore the accepted design")
	}
	if state.tasksTemplateHash != contract.TasksTemplateHash || len(state.taskStates) != len(contract.TaskStates) {
		return specTrioState{}, errors.New("spec contract: tasks.md structure or text changed; only [ ] to [x] checkbox transitions are allowed")
	}
	for i := range state.taskStates {
		if contract.TaskStates[i] && !state.taskStates[i] {
			return specTrioState{}, fmt.Errorf("spec contract: tasks.md checkbox %d regressed from review-authorized [x] to [ ]", i+1)
		}
	}
	return state, nil
}

// validateDevSpecContractProgress verifies a completed Build/Fix round without
// authorizing it. In addition to the durable contract, floor binds the state
// observed immediately before the runner so a Fix cannot undo checkbox
// progress left pending by a failed review.
func (o *Orchestrator) validateDevSpecContractProgress(floor []bool) (specTrioState, error) {
	contract, state, err := o.currentSpecContractState()
	if err != nil {
		return specTrioState{}, err
	}
	if state.specHash != contract.SpecHash {
		return specTrioState{}, errors.New("spec contract: dev modified spec.md; only implementation files and pending task checkboxes may change")
	}
	if state.designHash != contract.DesignHash {
		return specTrioState{}, errors.New("spec contract: dev modified design.md; only implementation files and pending task checkboxes may change")
	}
	if state.tasksTemplateHash != contract.TasksTemplateHash || len(state.taskStates) != len(contract.TaskStates) {
		return specTrioState{}, errors.New("spec contract: dev rewrote or removed tasks.md content; only [ ] to [x] checkbox transitions may change")
	}
	if len(floor) != len(state.taskStates) {
		return specTrioState{}, errors.New("spec contract: pending task baseline no longer matches tasks.md")
	}
	for i := range state.taskStates {
		if (contract.TaskStates[i] || floor[i]) && !state.taskStates[i] {
			return specTrioState{}, fmt.Errorf("spec contract: dev reverted pending or review-authorized tasks.md checkbox %d from [x] to [ ]", i+1)
		}
	}
	return state, nil
}

// advanceSpecContract prepares review-authorized task progress in memory. The
// caller persists it atomically with the passing ReviewResult. expected is the
// exact pending trio captured before the gates; rereading it here prevents a
// late checkbox edit from being blessed without review.
func (o *Orchestrator) advanceSpecContract(expected specTrioState) ([]bool, error) {
	contract, state, err := o.currentSpecContractState()
	if err != nil {
		return nil, err
	}
	if state.specHash != contract.SpecHash || state.designHash != contract.DesignHash ||
		state.tasksTemplateHash != contract.TasksTemplateHash || len(state.taskStates) != len(contract.TaskStates) {
		return nil, errors.New("spec contract: accepted trio changed while review gates were running")
	}
	if !sameSpecTrioState(state, expected) {
		return nil, errors.New("spec contract: task state changed while review gates were running; rerun /review")
	}
	for i := range state.taskStates {
		if contract.TaskStates[i] && !state.taskStates[i] {
			return nil, fmt.Errorf("spec contract: tasks.md checkbox %d regressed from review-authorized [x] to [ ]", i+1)
		}
	}
	previous := append([]bool(nil), contract.TaskStates...)
	contract.TaskStates = append([]bool(nil), state.taskStates...)
	return previous, nil
}

func (o *Orchestrator) currentSpecContractState() (*session.SpecContract, specTrioState, error) {
	contract := o.sess.SpecContract
	if contract == nil {
		return nil, specTrioState{}, errors.New("spec contract: missing accepted-trio baseline; rerun the spec lifecycle")
	}
	if o.spec == nil || contract.Version != specContractVersion || contract.SpecID != o.spec.ID {
		return nil, specTrioState{}, errors.New("spec contract: persisted contract does not match the active spec")
	}
	state, err := o.readSpecTrioState()
	if err != nil {
		return nil, specTrioState{}, err
	}
	return contract, state, nil
}

func sameSpecTrioState(a, b specTrioState) bool {
	if a.specHash != b.specHash || a.designHash != b.designHash || a.tasksTemplateHash != b.tasksTemplateHash || len(a.taskStates) != len(b.taskStates) {
		return false
	}
	for i := range a.taskStates {
		if a.taskStates[i] != b.taskStates[i] {
			return false
		}
	}
	return true
}

func (o *Orchestrator) readSpecTrioState() (specTrioState, error) {
	if o.spec == nil {
		return specTrioState{}, errors.New("spec contract: no active spec")
	}
	return readSpecTrioState(o.workspaceRoute().store, o.spec.ID)
}

func captureSpecContract(store *spec.Store, specID string) (*session.SpecContract, error) {
	state, err := readSpecTrioState(store, specID)
	if err != nil {
		return nil, err
	}
	return &session.SpecContract{
		Version:           specContractVersion,
		SpecID:            specID,
		SpecHash:          state.specHash,
		DesignHash:        state.designHash,
		TasksTemplateHash: state.tasksTemplateHash,
		TaskStates:        append([]bool(nil), state.taskStates...),
	}, nil
}

func readSpecTrioState(store *spec.Store, specID string) (specTrioState, error) {
	read := func(name string) ([]byte, error) {
		path := store.PathFor(specID, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("spec contract: inspect %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("spec contract: %s must remain a regular file", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("spec contract: read %s: %w", name, err)
		}
		return data, nil
	}

	specData, err := read(spec.FileSpec)
	if err != nil {
		return specTrioState{}, err
	}
	designData, err := read(spec.FileDesign)
	if err != nil {
		return specTrioState{}, err
	}
	tasksData, err := read(spec.FileTasks)
	if err != nil {
		return specTrioState{}, err
	}
	tasksTemplate, states, err := normalizeTaskCheckboxes(tasksData)
	if err != nil {
		return specTrioState{}, fmt.Errorf("spec contract: tasks.md: %w", err)
	}
	return specTrioState{
		specHash:          contentHash(specData),
		designHash:        contentHash(designData),
		tasksTemplateHash: contentHash(tasksTemplate),
		taskStates:        states,
	}, nil
}

// normalizeTaskCheckboxes returns an otherwise byte-identical tasks document
// with each Markdown task marker normalized to [ ]. This makes its hash bind
// line endings, whitespace, ordering, and task text while keeping checkbox
// progress as a separately validated monotonic vector.
func normalizeTaskCheckboxes(data []byte) ([]byte, []bool, error) {
	normalized := append([]byte(nil), data...)
	var states []bool
	for lineStart := 0; lineStart < len(data); {
		lineEnd := bytes.IndexByte(data[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(data)
		} else {
			lineEnd += lineStart
		}
		line := data[lineStart:lineEnd]
		i := 0
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if len(line)-i >= 5 && line[i] == '-' && line[i+1] == ' ' && line[i+2] == '[' && line[i+4] == ']' {
			after := i + 5
			if after == len(line) || line[after] == ' ' || line[after] == '\t' || line[after] == '\r' {
				switch line[i+3] {
				case ' ':
					states = append(states, false)
				case 'x':
					states = append(states, true)
				default:
					return nil, nil, fmt.Errorf("unsupported checkbox marker %q on task %d; use exactly [ ] or [x]", line[i+3], len(states)+1)
				}
				normalized[lineStart+i+3] = ' '
			}
		}
		if lineEnd == len(data) {
			break
		}
		lineStart = lineEnd + 1
	}
	return normalized, states, nil
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
