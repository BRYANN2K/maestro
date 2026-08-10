package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTrustedStateAnchorCanonicalizesOnlyTheAnchor(t *testing.T) {
	base := t.TempDir()
	realAnchor := filepath.Join(base, "real")
	if err := os.Mkdir(realAnchor, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realAnchor, alias); err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(alias, "project", "skills")
	anchor, rel, absolute, ok := resolveTrustedStateAnchor(wanted, []string{alias})
	if !ok {
		t.Fatal("trusted alias was not resolved")
	}
	canonicalRealAnchor, err := filepath.EvalSymlinks(realAnchor)
	if err != nil {
		t.Fatal(err)
	}
	if anchor != canonicalRealAnchor || rel != filepath.Join("project", "skills") || absolute != filepath.Join(canonicalRealAnchor, rel) {
		t.Fatalf("resolved = anchor %q rel %q path %q", anchor, rel, absolute)
	}
}

func TestStateSupportsCanonicalUnixTemporaryAlias(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("Unix temporary alias test")
	}
	root, err := os.MkdirTemp("/tmp", "maestro-state-anchor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	store := NewStateStore(filepath.Join(root, "skills-state"), "repo-123")
	if err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, ""); err != nil {
		t.Fatalf("SetEnabled under /tmp alias: %v", err)
	}
	state, err := store.Load(t.Context())
	if err != nil || state.Project["project:audit"] {
		t.Fatalf("Load under /tmp alias = %+v, %v", state, err)
	}
	canonicalTmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !pathWithin(canonicalTmp, store.StatePath()) {
		t.Fatalf("state path %q is not below canonical temp root %q", store.StatePath(), canonicalTmp)
	}
}

func TestStateRejectsTrustedAnchorAsStateDirectory(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("Unix temporary anchor test")
	}
	store := NewStateStore("/tmp", "repo-123")
	if store.initErr == nil || !strings.Contains(store.initErr.Error(), "below, not equal") {
		t.Fatalf("state store init at trusted anchor = %v", store.initErr)
	}
	if _, err := store.Load(t.Context()); err == nil || !strings.Contains(err.Error(), "below, not equal") {
		t.Fatalf("Load at trusted anchor = %v", err)
	}
	if err := store.SetEnabled(t.Context(), "project:audit", false, EnableProject, ""); err == nil || !strings.Contains(err.Error(), "below, not equal") {
		t.Fatalf("SetEnabled at trusted anchor = %v", err)
	}
}

func TestStateRejectsSymlinkBelowCanonicalTemporaryAlias(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("Unix symlink test")
	}
	root, err := os.MkdirTemp("/tmp", "maestro-state-symlink-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	outside := t.TempDir()
	redirect := filepath.Join(root, "redirect")
	if err := os.Symlink(outside, redirect); err != nil {
		t.Fatal(err)
	}

	store := NewStateStore(filepath.Join(redirect, "private", "skills"), "repo-123")
	err = store.SetEnabled(t.Context(), "project:audit", false, EnableProject, "")
	if err == nil || !strings.Contains(err.Error(), "not symlinks") {
		t.Fatalf("SetEnabled through descendant symlink = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "private")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state escaped through descendant symlink: %v", statErr)
	}
}
