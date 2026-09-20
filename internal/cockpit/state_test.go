package cockpit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/runtimebridge"
	"github.com/bryann2k/maestro/internal/workflow"
)

func TestCockpitStateReadsRealContractWithoutModel(t *testing.T) {
	if !runtimebridge.Available() {
		t.Skip("make ui-test builds the bundled runtime")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, args := range [][]string{{"bootstrap"}, {"explore", "sample"}} {
		if _, err := workflow.Run(t.Context(), root, args, nil); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, ".workflow", "changes", "sample", "spec.md"), []byte("# Contract\n\n- AC-1: Read the saved specification.\n"), 0600)
	if _, err := workflow.Run(t.Context(), root, []string{"validate", "sample"}, nil); err != nil {
		t.Fatal(err)
	}
	h := New()
	o, err := orchestrator.New(t.Context(), orchestrator.Options{ProjectDir: root, SessionsDir: t.TempDir(), RequireHarness: true, In: h, Out: h, Gate: h})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	h.Bind(o)
	got, err := h.state(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	var state struct {
		Workflow struct {
			Change string `json:"changeID"`
		}
		Documents map[string]string
		Contract  struct {
			Digest string `json:"contract_digest"`
		}
		WorkflowError string
	}
	json.Unmarshal(b, &state)
	if state.WorkflowError != "" || state.Workflow.Change != "sample" || state.Documents["spec"] == "" || state.Contract.Digest == "" {
		t.Fatalf("invalid interface state: %s", b)
	}
}
