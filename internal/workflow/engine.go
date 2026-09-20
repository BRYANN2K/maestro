// Package workflow embeds Stipulate's contract and evidence engine.
package workflow

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets
var assets embed.FS

// Materialize uses a private temporary directory, never executable code from a
// project's .workflow directory. Call cleanup after the runtime has exited.
func Materialize() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "maestro-workflow-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	err = fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel("assets", path)
		dest := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		data, e := assets.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(dest, data, 0600)
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(dir, "workflow.py"), cleanup, nil
}

func Initialized(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".workflow", "config.json"))
	return err == nil && info.Mode().IsRegular()
}

// Run exposes the pinned engine verbatim, with the project root fixed by the
// host. Approval must come through this explicit user command, never a tool.
func Run(ctx context.Context, root string, args []string, in io.Reader) ([]byte, error) {
	for _, arg := range args {
		if arg == "--root" || strings.HasPrefix(arg, "--root=") {
			return nil, errors.New("workflow root is owned by Maestro")
		}
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := Materialize()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	argv := append([]string{path, "--root", root}, args...)
	if len(args) > 0 && args[0] == "install-extensions" {
		argv = []string{filepath.Join(filepath.Dir(path), "install_extensions.py"), "--project", root}
		for _, id := range args[1:] {
			if strings.HasPrefix(id, "-") {
				return nil, errors.New("expected extension identifier")
			}
			argv = append(argv, "--extension", id)
		}
	}
	cmd := exec.CommandContext(ctx, "python3", append([]string{"-B"}, argv...)...)
	cmd.Dir = root
	cmd.Stdin = in
	cmd.WaitDelay = time.Second
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("workflow: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("workflow output limit exceeded")
	}
	return b.Buffer.Write(p)
}
