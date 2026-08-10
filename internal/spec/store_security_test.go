package spec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRejectsSymlinkedRootForEveryMutation(t *testing.T) {
	operations := map[string]func(*Store) error{
		"save": func(st *Store) error {
			return st.Save(t.Context(), testSpec())
		},
		"write-trio": func(st *Store) error {
			return st.WriteTrio(t.Context(), testSpec(), "design", "tasks")
		},
		"append-idea": func(st *Store) error {
			return st.AppendIdea(t.Context(), testSpec().ID, "outside")
		},
		"archive": func(st *Store) error {
			return st.Archive(t.Context(), testSpec().ID)
		},
		"restore": func(st *Store) error {
			return st.RestoreArchive(t.Context(), testSpec().ID)
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			outside := t.TempDir()
			writeSentinel(t, outside, "sentinel", "unchanged")
			if err := os.MkdirAll(filepath.Join(outside, testSpec().ID), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(outside, ArchiveDir, testSpec().ID), 0o755); err != nil {
				t.Fatal(err)
			}
			storePath := filepath.Join(parent, "specs")
			if err := os.Symlink(outside, storePath); err != nil {
				t.Fatal(err)
			}
			err := operation(NewStore(storePath))
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("operation through symlinked root error = %v, want symlink refusal", err)
			}
			assertSentinel(t, outside, "sentinel", "unchanged")
			if _, err := os.Stat(filepath.Join(outside, testSpec().ID, FileSpec)); !os.IsNotExist(err) {
				t.Fatalf("operation wrote outside store: %v", err)
			}
		})
	}
}

func TestStoreRejectsSymlinkedSpecDirectoryForEveryMutation(t *testing.T) {
	operations := map[string]func(*Store) error{
		"save": func(st *Store) error {
			return st.Save(t.Context(), testSpec())
		},
		"write-trio": func(st *Store) error {
			return st.WriteTrio(t.Context(), testSpec(), "design", "tasks")
		},
		"append-idea": func(st *Store) error {
			return st.AppendIdea(t.Context(), testSpec().ID, "outside")
		},
		"archive": func(st *Store) error {
			return st.Archive(t.Context(), testSpec().ID)
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			storePath := filepath.Join(base, "specs")
			if err := os.Mkdir(storePath, 0o755); err != nil {
				t.Fatal(err)
			}
			writeSentinel(t, outside, "sentinel", "unchanged")
			if err := os.Symlink(outside, filepath.Join(storePath, testSpec().ID)); err != nil {
				t.Fatal(err)
			}
			err := operation(NewStore(storePath))
			if err == nil {
				t.Fatal("operation through symlinked spec directory unexpectedly succeeded")
			}
			assertSentinel(t, outside, "sentinel", "unchanged")
			if _, err := os.Stat(filepath.Join(outside, FileSpec)); !os.IsNotExist(err) {
				t.Fatalf("operation wrote through spec symlink: %v", err)
			}
		})
	}
}

func TestStoreRejectsSymlinkedArchiveDirectory(t *testing.T) {
	for _, operation := range []string{"archive", "restore"} {
		t.Run(operation, func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			storePath := filepath.Join(base, "specs")
			st := NewStore(storePath)
			if err := st.Save(t.Context(), testSpec()); err != nil {
				t.Fatal(err)
			}
			writeSentinel(t, outside, "sentinel", "unchanged")
			if err := os.MkdirAll(filepath.Join(outside, testSpec().ID), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(storePath, ArchiveDir)); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "archive" {
				err = st.Archive(t.Context(), testSpec().ID)
			} else {
				err = st.RestoreArchive(t.Context(), testSpec().ID)
			}
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("%s error = %v, want symlink refusal", operation, err)
			}
			assertSentinel(t, outside, "sentinel", "unchanged")
			if _, err := os.Stat(filepath.Join(storePath, testSpec().ID, FileSpec)); err != nil {
				t.Fatalf("active spec changed: %v", err)
			}
		})
	}
}

func TestStoreArchiveAndRestoreRejectSymlinkedDestinations(t *testing.T) {
	t.Run("archive destination", func(t *testing.T) {
		st := NewStore(filepath.Join(t.TempDir(), "specs"))
		if err := st.Save(t.Context(), testSpec()); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		writeSentinel(t, outside, "sentinel", "unchanged")
		if err := os.MkdirAll(filepath.Join(st.Dir(), ArchiveDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(st.Dir(), ArchiveDir, testSpec().ID)); err != nil {
			t.Fatal(err)
		}
		if err := st.Archive(t.Context(), testSpec().ID); err == nil {
			t.Fatal("Archive replaced a symlink destination")
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
		if _, err := os.Stat(st.PathFor(testSpec().ID, FileSpec)); err != nil {
			t.Fatalf("active source changed: %v", err)
		}
	})

	t.Run("restore destination", func(t *testing.T) {
		st := NewStore(filepath.Join(t.TempDir(), "specs"))
		if err := st.Save(t.Context(), testSpec()); err != nil {
			t.Fatal(err)
		}
		if err := st.Archive(t.Context(), testSpec().ID); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		writeSentinel(t, outside, "sentinel", "unchanged")
		if err := os.Symlink(outside, st.Path(testSpec().ID)); err != nil {
			t.Fatal(err)
		}
		if err := st.RestoreArchive(t.Context(), testSpec().ID); err == nil {
			t.Fatal("RestoreArchive replaced a symlink destination")
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
		if _, err := os.Stat(filepath.Join(st.Dir(), ArchiveDir, testSpec().ID, FileSpec)); err != nil {
			t.Fatalf("archived source changed: %v", err)
		}
	})
}

func TestStoreRejectsSymlinkedDocumentTargets(t *testing.T) {
	t.Run("save spec.md", func(t *testing.T) {
		st, outside := storeWithSpecAndOutside(t)
		writeSentinel(t, outside, "sentinel", "unchanged")
		if err := os.Symlink(filepath.Join(outside, "sentinel"), st.PathFor(testSpec().ID, FileSpec)); err != nil {
			t.Fatal(err)
		}
		if err := st.Save(t.Context(), testSpec()); err == nil {
			t.Fatal("Save through spec.md symlink unexpectedly succeeded")
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
	})

	t.Run("append spec-idea.md", func(t *testing.T) {
		st, outside := storeWithSpecAndOutside(t)
		writeSentinel(t, outside, "sentinel", "unchanged")
		if err := os.Symlink(filepath.Join(outside, "sentinel"), st.PathFor(testSpec().ID, FileIdeas)); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendIdea(t.Context(), testSpec().ID, "outside"); err == nil {
			t.Fatal("AppendIdea through symlink unexpectedly succeeded")
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
	})
}

func TestStoreRejectsSymlinkedTemporaryEntriesWithoutPartialWrite(t *testing.T) {
	t.Run("save", func(t *testing.T) {
		st, outside := storeWithSpecAndOutside(t)
		writeSentinel(t, outside, "sentinel", "unchanged")
		original := testSpec()
		original.Title = "original"
		if err := st.Save(t.Context(), original); err != nil {
			t.Fatal(err)
		}
		temp := ".forced-save-temp"
		st.tempName = func(string) (string, error) { return temp, nil }
		if err := os.Symlink(filepath.Join(outside, "sentinel"), st.PathFor(testSpec().ID, temp)); err != nil {
			t.Fatal(err)
		}
		updated := testSpec()
		updated.Title = "updated"
		if err := st.Save(t.Context(), updated); err == nil || !strings.Contains(err.Error(), "temporary symlink") {
			t.Fatalf("Save error = %v, want temporary symlink refusal", err)
		}
		got, err := st.Load(t.Context(), testSpec().ID)
		if err != nil || got.Title != "original" {
			t.Fatalf("failed Save changed original: title=%q err=%v", got.Title, err)
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
	})

	t.Run("write trio", func(t *testing.T) {
		base := t.TempDir()
		outside := t.TempDir()
		storePath := filepath.Join(base, "specs")
		if err := os.Mkdir(storePath, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSentinel(t, outside, "sentinel", "unchanged")
		st := NewStore(storePath)
		temp := ".forced-trio-temp"
		st.tempName = func(string) (string, error) { return temp, nil }
		if err := os.Symlink(outside, filepath.Join(storePath, temp)); err != nil {
			t.Fatal(err)
		}
		if err := st.WriteTrio(t.Context(), testSpec(), "design", "tasks"); err == nil || !strings.Contains(err.Error(), "temporary symlink") {
			t.Fatalf("WriteTrio error = %v, want temporary symlink refusal", err)
		}
		if _, err := os.Lstat(st.Path(testSpec().ID)); !os.IsNotExist(err) {
			t.Fatalf("failed WriteTrio published partial target: %v", err)
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
	})
}

func TestStoreOutputModes(t *testing.T) {
	base := t.TempDir()
	st := NewStore(filepath.Join(base, "specs"))
	if err := st.Save(t.Context(), testSpec()); err != nil {
		t.Fatal(err)
	}
	assertMode(t, st.Path(testSpec().ID), 0o755)
	assertMode(t, st.PathFor(testSpec().ID, FileSpec), 0o644)

	other := testSpec()
	other.ID = "atomic-trio"
	if err := st.WriteTrio(t.Context(), other, "design", "tasks"); err != nil {
		t.Fatal(err)
	}
	assertMode(t, st.Path(other.ID), 0o700)
	for _, name := range []string{FileSpec, FileDesign, FileTasks} {
		assertMode(t, st.PathFor(other.ID, name), 0o644)
	}
}

func TestStoreMutationsRejectReservedArchiveID(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "specs"))
	s := testSpec()
	s.ID = ArchiveDir
	if err := s.Valid(); err == nil {
		t.Fatal("Spec.Valid accepted reserved archive ID")
	}
	if got := Slugify("archive"); got == ArchiveDir {
		t.Fatalf("Slugify produced reserved ID %q", got)
	}
	for name, operation := range map[string]func() error{
		"save":        func() error { return st.Save(t.Context(), s) },
		"write-trio":  func() error { return st.WriteTrio(t.Context(), s, "design", "tasks") },
		"append-idea": func() error { return st.AppendIdea(t.Context(), ArchiveDir, "idea") },
		"archive":     func() error { return st.Archive(t.Context(), ArchiveDir) },
		"restore":     func() error { return st.RestoreArchive(t.Context(), ArchiveDir) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil {
				t.Fatalf("%s accepted reserved archive ID", name)
			}
		})
	}
}

func TestRollbackTrioRemovesOnlyExactMaterialization(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "specs"))
	materialization, err := st.WriteTrioTracked(t.Context(), testSpec(), "design", "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RollbackTrio(t.Context(), materialization); err != nil {
		t.Fatalf("RollbackTrio: %v", err)
	}
	if _, err := os.Lstat(st.Path(testSpec().ID)); !os.IsNotExist(err) {
		t.Fatalf("exact trio remains after rollback: %v", err)
	}
}

func TestRollbackTrioPreservesConcurrentChanges(t *testing.T) {
	tests := map[string]func(*testing.T, *Store){
		"added file": func(t *testing.T, st *Store) {
			if err := os.WriteFile(st.PathFor(testSpec().ID, "concurrent.txt"), []byte("mine"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"replaced file": func(t *testing.T, st *Store) {
			path := st.PathFor(testSpec().ID, FileDesign)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("concurrent replacement"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"edited file": func(t *testing.T, st *Store) {
			if err := os.WriteFile(st.PathFor(testSpec().ID, FileTasks), []byte("concurrent edit"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			st := NewStore(filepath.Join(t.TempDir(), "specs"))
			materialization, err := st.WriteTrioTracked(t.Context(), testSpec(), "design", "tasks")
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, st)
			if err := st.RollbackTrio(t.Context(), materialization); err == nil {
				t.Fatal("RollbackTrio deleted a concurrently changed trio")
			}
			if _, err := os.Stat(st.Path(testSpec().ID)); err != nil {
				t.Fatalf("concurrently changed directory was not preserved: %v", err)
			}
			if name == "added file" {
				assertSentinel(t, st.Path(testSpec().ID), "concurrent.txt", "mine")
			}
			if name == "replaced file" {
				assertSentinel(t, st.Path(testSpec().ID), FileDesign, "concurrent replacement")
			}
			if name == "edited file" {
				assertSentinel(t, st.Path(testSpec().ID), FileTasks, "concurrent edit")
			}
		})
	}
}

func TestRollbackTrioRejectsSymlinkReplacementAndPreservesOutside(t *testing.T) {
	t.Run("document", func(t *testing.T) {
		st := NewStore(filepath.Join(t.TempDir(), "specs"))
		materialization, err := st.WriteTrioTracked(t.Context(), testSpec(), "design", "tasks")
		if err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		writeSentinel(t, outside, "sentinel", "unchanged")
		path := st.PathFor(testSpec().ID, FileDesign)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "sentinel"), path); err != nil {
			t.Fatal(err)
		}
		if err := st.RollbackTrio(t.Context(), materialization); err == nil {
			t.Fatal("RollbackTrio followed replacement symlink")
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
		if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("replacement symlink was not preserved: %v, %v", info, err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		st := NewStore(filepath.Join(t.TempDir(), "specs"))
		materialization, err := st.WriteTrioTracked(t.Context(), testSpec(), "design", "tasks")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(st.Path(testSpec().ID)); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		writeSentinel(t, outside, "sentinel", "unchanged")
		if err := os.Symlink(outside, st.Path(testSpec().ID)); err != nil {
			t.Fatal(err)
		}
		if err := st.RollbackTrio(t.Context(), materialization); err == nil {
			t.Fatal("RollbackTrio followed replacement directory symlink")
		}
		assertSentinel(t, outside, "sentinel", "unchanged")
	})
}

func storeWithSpecAndOutside(t *testing.T) (*Store, string) {
	t.Helper()
	base := t.TempDir()
	outside := t.TempDir()
	storePath := filepath.Join(base, "specs")
	st := NewStore(storePath)
	if err := os.MkdirAll(st.Path(testSpec().ID), 0o755); err != nil {
		t.Fatal(err)
	}
	return st, outside
}

func writeSentinel(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSentinel(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != want {
		t.Fatalf("sentinel = %q, %v; want %q", got, err, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

func TestStoreHonorsCanceledContextBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := NewStore(filepath.Join(t.TempDir(), "specs"))
	if err := st.Save(ctx, testSpec()); err == nil {
		t.Fatal("Save with canceled context succeeded")
	}
	if _, err := os.Lstat(st.Dir()); !os.IsNotExist(err) {
		t.Fatalf("canceled Save created store: %v", err)
	}
}
