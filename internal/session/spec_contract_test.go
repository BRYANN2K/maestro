package session

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSpecContractPersists(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	sess := New("project")
	sess.SpecID = "health-helper"
	sess.Phase = PhaseBuild
	sess.SpecContract = &SpecContract{
		Version:           1,
		SpecID:            sess.SpecID,
		SpecHash:          "spec-hash",
		DesignHash:        "design-hash",
		TasksTemplateHash: "tasks-hash",
		TaskStates:        []bool{true, false, true},
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), sess.Project, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.SpecContract, sess.SpecContract) {
		t.Fatalf("loaded spec contract = %+v, want %+v", loaded.SpecContract, sess.SpecContract)
	}
}
