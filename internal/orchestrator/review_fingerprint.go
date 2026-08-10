package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// worktreeFingerprint returns the exact Git-visible filesystem state without
// reading or modifying the user's index. A Git tree covers tracked and
// untracked paths, binary contents, executable modes, symlinks, and deletes.
func (o *Orchestrator) worktreeFingerprint(ctx context.Context) (string, error) {
	workspace := o.workspaceRoute()
	if err := workspace.git.CheckSubmodulesClean(ctx); err != nil {
		return "", fmt.Errorf("verify submodules: %w", err)
	}
	tempRoot, err := os.MkdirTemp("", "maestro-review-fingerprint-")
	if err != nil {
		return "", fmt.Errorf("create private index: %w", err)
	}
	defer os.RemoveAll(tempRoot)

	fingerprint, err := snapshotWorktree(ctx, workspace.dir, filepath.Join(tempRoot, "index"), "HEAD")
	if err != nil {
		return "", fmt.Errorf("snapshot Git-visible worktree: %w", err)
	}
	return fingerprint, nil
}

// requireCurrentReview rejects stale and legacy review results. Callers use
// this before any worktree or index mutation so a failed integrity check is a
// side-effect-free release-gate refusal.
func (o *Orchestrator) requireCurrentReview(ctx context.Context, operation string) error {
	if err := o.validateSessionWorkspaceIdentity(ctx, operation); err != nil {
		return err
	}
	if err := o.validateSpecContract(); err != nil {
		return fmt.Errorf("%s: %w; rerun /review after restoring the accepted spec trio", operation, err)
	}
	if o.sess.Review == nil || o.sess.Review.Level == "fail" {
		return fmt.Errorf("%s: a passing review is required", operation)
	}
	if strings.TrimSpace(o.sess.Review.Fingerprint) == "" {
		return fmt.Errorf("%s: the passing review has no worktree fingerprint; rerun /review", operation)
	}
	if err := o.requireReviewedGitIdentity(ctx, operation); err != nil {
		return err
	}
	current, err := o.worktreeFingerprint(ctx)
	if err != nil {
		return fmt.Errorf("%s: cannot verify the reviewed worktree: %v; rerun /review", operation, err)
	}
	if current != o.sess.Review.Fingerprint {
		return fmt.Errorf("%s: the worktree changed after review; rerun /review", operation)
	}
	return nil
}

// validateAcceptedADRPath binds CompleteDocs to the single artifact class it
// is allowed to bless: a direct ADR for the active spec. Lexical confinement
// is not enough because a symlinked docs directory or file could redirect an
// apparently valid path to source code or outside the checkout.
func (o *Orchestrator) validateAcceptedADRPath(path string) (string, error) {
	if o.spec == nil || strings.TrimSpace(o.spec.ID) == "" {
		return "", errors.New("accepted ADR requires an active spec")
	}
	root, err := filepath.Abs(o.workDir())
	if err != nil {
		return "", fmt.Errorf("resolve active worktree: %w", err)
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve accepted ADR path: %w", err)
	}
	target = filepath.Clean(target)
	adrDir := filepath.Join(root, "docs-archive", "adr")
	if filepath.Dir(target) != adrDir {
		return "", errors.New("accepted artifact must be a direct file in docs-archive/adr")
	}

	base := filepath.Base(target)
	suffix := "-" + o.spec.ID + ".md"
	if !strings.HasSuffix(base, suffix) {
		return "", fmt.Errorf("accepted ADR filename must end with %q", suffix)
	}
	date := strings.TrimSuffix(base, suffix)
	if len(date) != len("YYYY-MM-DD") {
		return "", errors.New("accepted ADR filename must start with YYYY-MM-DD")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", errors.New("accepted ADR filename must start with a valid YYYY-MM-DD date")
	}

	for _, component := range []struct {
		path string
		dir  bool
	}{
		{path: filepath.Join(root, "docs-archive"), dir: true},
		{path: adrDir, dir: true},
		{path: target},
	} {
		info, err := os.Lstat(component.path)
		if err != nil {
			return "", fmt.Errorf("inspect accepted ADR path %q: %w", component.path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("accepted ADR path component %q must not be a symlink", component.path)
		}
		if component.dir && !info.IsDir() {
			return "", fmt.Errorf("accepted ADR path component %q must be a directory", component.path)
		}
		if !component.dir && !info.Mode().IsRegular() {
			return "", fmt.Errorf("accepted ADR %q must be a regular file", component.path)
		}
	}
	return target, nil
}

// fingerprintWithAcceptedPath applies exactly the validated ADR bytes over the
// reviewed tree using a private index. It deliberately does not reread path:
// CompleteDocs compares this immutable expectation with a fresh whole-worktree
// snapshot, so a concurrent edit cannot replace the bytes that were validated.
func (o *Orchestrator) fingerprintWithAcceptedPath(ctx context.Context, reviewed, path string, content []byte) (string, error) {
	rel, err := filepath.Rel(o.workDir(), path)
	if err != nil {
		return "", fmt.Errorf("resolve accepted ADR path: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("accepted ADR path is outside the active worktree")
	}

	tempRoot, err := os.MkdirTemp("", "maestro-docs-fingerprint-")
	if err != nil {
		return "", fmt.Errorf("create private index: %w", err)
	}
	defer os.RemoveAll(tempRoot)

	env := map[string]string{"GIT_INDEX_FILE": filepath.Join(tempRoot, "index")}
	if _, err := runIsolatedGit(ctx, o.workDir(), nil, env, "read-tree", reviewed); err != nil {
		return "", fmt.Errorf("load reviewed tree: %w", err)
	}
	rel = filepath.ToSlash(rel)
	blobOutput, err := runIsolatedGit(ctx, o.workDir(), strings.NewReader(string(content)), nil, "hash-object", "-w", "--path="+rel, "--stdin")
	if err != nil {
		return "", fmt.Errorf("hash accepted ADR: %w", err)
	}
	blob := strings.TrimSpace(string(blobOutput))
	if blob == "" || strings.ContainsAny(blob, "\r\n") {
		return "", errors.New("git hash-object returned an invalid object ID")
	}
	if _, err := runIsolatedGit(ctx, o.workDir(), nil, env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+rel); err != nil {
		return "", fmt.Errorf("snapshot accepted ADR: %w", err)
	}
	out, err := runIsolatedGit(ctx, o.workDir(), nil, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write expected docs tree: %w", err)
	}
	fingerprint := strings.TrimSpace(string(out))
	if fingerprint == "" {
		return "", errors.New("git write-tree returned an empty object ID")
	}
	return fingerprint, nil
}
