package workflow

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalRejectsChangedDisplayedContract(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		b, err := Run(t.Context(), root, args, nil)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	run("bootstrap")
	run("explore", "reviewed")
	spec := filepath.Join(root, ".workflow", "changes", "reviewed", "spec.md")
	os.WriteFile(spec, []byte("# Review\n\n- AC-1: Display the reviewed contract.\n"), 0600)
	run("validate", "reviewed")
	var state struct {
		Digest string `json:"contract_digest"`
	}
	json.Unmarshal(run("status", "reviewed"), &state)
	if state.Digest == "" {
		t.Fatal("missing digest")
	}
	os.WriteFile(spec, []byte("# Changed\n\n- AC-1: Different contract.\n"), 0600)
	_, err := Run(t.Context(), root, []string{"approve", "reviewed", "--by", "reviewer", "--ack-user-approval", "--expected-digest", state.Digest}, nil)
	if err == nil || !strings.Contains(err.Error(), "Contract changed") {
		t.Fatalf("stale approval: %v", err)
	}
	var current struct {
		Digest   string `json:"contract_digest"`
		Approved bool   `json:"approval_current"`
	}
	json.Unmarshal(run("status", "reviewed"), &current)
	if current.Approved {
		t.Fatal("stale contract approved")
	}
	run("approve", "reviewed", "--by", "reviewer", "--ack-user-approval", "--expected-digest", current.Digest)
	json.Unmarshal(run("status", "reviewed"), &current)
	if !current.Approved {
		t.Fatal("current contract not approved")
	}
}
