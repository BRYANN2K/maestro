package editor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCrashPreviewRequiresExplicitAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	crash := NewCrashStore(dir)
	ed := NewEditor(t.TempDir())
	buffer := NewBuffer("notes.txt", []byte("recover me\n"))
	buffer.Dirty = true
	ed.Buffers = []*Buffer{buffer}
	if err := crash.Save(ed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		states, present, err := crash.PreviewRestore()
		if err != nil {
			t.Fatalf("PreviewRestore attempt %d: %v", attempt, err)
		}
		if !present || len(states) != 1 || states[0].Lines[0] != "recover me" {
			t.Fatalf("PreviewRestore attempt %d = present %v, states %+v", attempt, present, states)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "crash.json")); err != nil {
		t.Fatalf("preview consumed recovery file: %v", err)
	}

	crash.AcknowledgeRestore()
	states, present, err := crash.PreviewRestore()
	if err != nil || present || len(states) != 0 {
		t.Fatalf("PreviewRestore after AcknowledgeRestore = present %v, states %+v, err %v", present, states, err)
	}
}

func TestCrashAcknowledgementCannotDeleteANewerSave(t *testing.T) {
	dir := t.TempDir()
	crash := NewCrashStore(dir)
	ed := NewEditor(t.TempDir())
	buffer := NewBuffer("notes.txt", []byte("old recovery\n"))
	buffer.Dirty = true
	ed.Buffers = []*Buffer{buffer}
	if err := crash.Save(ed); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	if _, present, err := crash.PreviewRestore(); err != nil || !present {
		t.Fatalf("PreviewRestore = present %v, err %v", present, err)
	}

	buffer.InsertText("new ")
	if err := crash.Save(ed); err != nil {
		t.Fatalf("replacement Save: %v", err)
	}
	crash.AcknowledgeRestore()
	states, present, err := crash.PreviewRestore()
	if err != nil || !present || len(states) != 1 || states[0].Lines[0] != "new old recovery" {
		t.Fatalf("newer recovery = present %v, states %+v, err %v", present, states, err)
	}
}
