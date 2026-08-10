package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type gitWorkspaceIdentity struct {
	ref  string
	head string
}

func readGitWorkspaceIdentity(ctx context.Context, dir string) (gitWorkspaceIdentity, error) {
	rootOutput, err := runIsolatedGit(ctx, dir, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitWorkspaceIdentity{}, fmt.Errorf("resolve repository root: %w", err)
	}
	root, err := canonicalExistingPath(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return gitWorkspaceIdentity{}, fmt.Errorf("resolve repository root: %w", err)
	}
	workspace, err := canonicalExistingPath(dir)
	if err != nil {
		return gitWorkspaceIdentity{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	if root != workspace {
		return gitWorkspaceIdentity{}, fmt.Errorf("workspace %q is not repository root %q", workspace, root)
	}
	refOutput, err := runIsolatedGit(ctx, dir, nil, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return gitWorkspaceIdentity{}, fmt.Errorf("resolve symbolic HEAD: %w", err)
	}
	ref := strings.TrimSpace(string(refOutput))
	if !strings.HasPrefix(ref, "refs/heads/") || strings.ContainsAny(ref, "\r\n") {
		return gitWorkspaceIdentity{}, fmt.Errorf("unsafe symbolic HEAD %q", ref)
	}
	headOutput, err := runIsolatedGit(ctx, dir, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return gitWorkspaceIdentity{}, fmt.Errorf("resolve HEAD commit: %w", err)
	}
	head := strings.TrimSpace(string(headOutput))
	if head == "" || strings.ContainsAny(head, "\r\n") {
		return gitWorkspaceIdentity{}, errors.New("git returned an invalid HEAD commit")
	}
	return gitWorkspaceIdentity{ref: ref, head: head}, nil
}

func (o *Orchestrator) validateSessionWorkspaceIdentity(ctx context.Context, operation string) error {
	if strings.TrimSpace(o.sess.WorkspaceRef) == "" {
		return fmt.Errorf("%s: session has no accepted workspace identity; restart the spec lifecycle", operation)
	}
	identity, err := readGitWorkspaceIdentity(ctx, o.workDir())
	if err != nil {
		return fmt.Errorf("%s: verify workspace identity: %w", operation, err)
	}
	if identity.ref != o.sess.WorkspaceRef {
		return fmt.Errorf("%s: active branch is %q, but this spec was accepted on %q; switch back before continuing", operation, identity.ref, o.sess.WorkspaceRef)
	}
	if o.sess.Worktree != "" {
		expected, err := canonicalExistingPath(o.sess.Worktree)
		if err != nil {
			return fmt.Errorf("%s: resolve recorded worktree: %w", operation, err)
		}
		actual, err := canonicalExistingPath(o.workDir())
		if err != nil {
			return fmt.Errorf("%s: resolve active worktree: %w", operation, err)
		}
		if actual != expected {
			return fmt.Errorf("%s: active worktree is %q, want %q", operation, actual, expected)
		}
	}
	return nil
}

func (o *Orchestrator) requireReviewedGitIdentity(ctx context.Context, operation string) error {
	if o.sess.Review == nil || strings.TrimSpace(o.sess.Review.GitRef) == "" || strings.TrimSpace(o.sess.Review.GitHead) == "" {
		return fmt.Errorf("%s: the passing review has no Git ref/HEAD identity; rerun /review", operation)
	}
	identity, err := readGitWorkspaceIdentity(ctx, o.workDir())
	if err != nil {
		return fmt.Errorf("%s: verify reviewed Git identity: %w", operation, err)
	}
	if identity.ref != o.sess.Review.GitRef || identity.head != o.sess.Review.GitHead {
		return fmt.Errorf("%s: Git ref or HEAD changed after review; rerun /review", operation)
	}
	return nil
}
