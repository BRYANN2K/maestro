package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
)

// workspaceTool roots path-bearing tool arguments in the active worktree and
// rejects lexical and symlink escapes before the underlying tool sees them.
// For bash this constrains only the initial working directory; the approval
// gate remains the security boundary for arbitrary shell commands.
func (o *Orchestrator) workspaceTool(tool agentcore.Tool) agentcore.Tool {
	spec := tool.Spec()
	return agentcore.NewToolFunc(spec, func(ctx context.Context, args map[string]any) (string, error) {
		clean := make(map[string]any, len(args)+1)
		for key, value := range args {
			clean[key] = value
		}
		switch spec.Name {
		case "read", "write":
			path, _ := clean["path"].(string)
			resolved, err := resolveWorkspacePath(o.workDir(), path)
			if err != nil {
				return "", fmt.Errorf("%s: %w", spec.Name, err)
			}
			clean["path"] = resolved
		case "grep":
			path, _ := clean["path"].(string)
			if path == "" {
				path = "."
			}
			resolved, err := resolveWorkspacePath(o.workDir(), path)
			if err != nil {
				return "", fmt.Errorf("grep: %w", err)
			}
			clean["path"] = resolved
		case "bash":
			path, _ := clean["workdir"].(string)
			if path == "" {
				path = "."
			}
			resolved, err := resolveWorkspacePath(o.workDir(), path)
			if err != nil {
				return "", fmt.Errorf("bash: %w", err)
			}
			clean["workdir"] = resolved
		}
		return tool.Run(ctx, clean)
	})
}

func resolveWorkspacePath(root, requested string) (string, error) {
	if root == "" {
		return "", errors.New("workspace root is empty")
	}
	if requested == "" {
		return "", errors.New("path is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if !withinWorkspace(rootAbs, target) {
		return "", fmt.Errorf("path %s escapes workspace %s", requested, rootAbs)
	}
	ancestor := target
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("path %s has no existing parent", requested)
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if !withinWorkspace(rootReal, realAncestor) {
		return "", fmt.Errorf("path %s escapes workspace through symlink", requested)
	}
	return target, nil
}

func withinWorkspace(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
