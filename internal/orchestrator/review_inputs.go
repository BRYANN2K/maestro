package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	gitpkg "github.com/bryann2k/maestro/internal/git"
)

const (
	maxReviewChangedFiles    = 8192
	maxReviewSourceFileBytes = int64(4 << 20)
	maxReviewSourceBytes     = int64(32 << 20)
)

func boundReviewChangeInventory(changes []gitpkg.FileChange, err error) ([]gitpkg.FileChange, error) {
	if err != nil {
		return nil, err
	}
	if len(changes) > maxReviewChangedFiles {
		return nil, fmt.Errorf("change inventory contains %d files; review limit is %d", len(changes), maxReviewChangedFiles)
	}
	return changes, nil
}

type reviewCachedFile struct {
	data []byte
	err  error
}

// reviewFileCache reads each changed source at most once for the security and
// comprehension gates. os.Root confines Git-provided paths to the checkout;
// per-file and aggregate ceilings turn generated/hostile inputs into an
// explicit failing review rather than an unbounded heap allocation.
type reviewFileCache struct {
	ctx        context.Context
	root       *os.Root
	files      map[string]reviewCachedFile
	retained   int64
	firstFail  error
	afterLstat func(string) // deterministic TOCTOU regression hook; nil in production
}

func newReviewFileCache(ctx context.Context, dir string) (*reviewFileCache, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open review workspace: %w", err)
	}
	return &reviewFileCache{ctx: ctx, root: root, files: make(map[string]reviewCachedFile)}, nil
}

func (c *reviewFileCache) Close() error {
	if c == nil || c.root == nil {
		return nil
	}
	return c.root.Close()
}

func (c *reviewFileCache) Err() error {
	if c == nil {
		return nil
	}
	return c.firstFail
}

func (c *reviewFileCache) Read(path string) ([]byte, error) {
	if cached, ok := c.files[path]; ok {
		return cached.data, cached.err
	}
	data, err := c.read(path)
	c.files[path] = reviewCachedFile{data: data, err: err}
	return data, err
}

func (c *reviewFileCache) read(path string) ([]byte, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, c.fail(err)
	}
	rootPath := filepath.FromSlash(path)
	pathInfo, err := c.root.Lstat(rootPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, c.fail(fmt.Errorf("inspect changed file %q: %w", path, err))
	}
	// Git patches review the symlink itself, not its target. Likewise, sockets,
	// devices and directories have no source bytes for these gates.
	if !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("changed path %q is not a regular source file", path)
	}
	if pathInfo.Size() > maxReviewSourceFileBytes {
		return nil, c.fail(fmt.Errorf("changed source %q is %d bytes; per-file review limit is %d bytes", path, pathInfo.Size(), maxReviewSourceFileBytes))
	}
	if pathInfo.Size() > maxReviewSourceBytes-c.retained {
		return nil, c.fail(fmt.Errorf("changed source set exceeds aggregate review limit of %d bytes", maxReviewSourceBytes))
	}

	if c.afterLstat != nil {
		c.afterLstat(path)
	}
	// O_NONBLOCK on Unix closes the Lstat→open FIFO replacement window. The
	// post-open identity/type check below then refuses the substituted object.
	file, err := openRootReadOnly(c.root, rootPath)
	if err != nil {
		return nil, c.fail(fmt.Errorf("open changed file %q: %w", path, err))
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, c.fail(fmt.Errorf("inspect opened changed file %q: %w", path, statErr))
	}
	currentInfo, lstatErr := c.root.Lstat(rootPath)
	if lstatErr != nil || !currentInfo.Mode().IsRegular() || !openedInfo.Mode().IsRegular() || !os.SameFile(openedInfo, currentInfo) {
		_ = file.Close()
		if lstatErr != nil {
			return nil, c.fail(fmt.Errorf("reinspect changed file %q: %w", path, lstatErr))
		}
		return nil, c.fail(fmt.Errorf("changed file %q changed identity while review inputs were captured", path))
	}
	remaining := min(maxReviewSourceFileBytes, maxReviewSourceBytes-c.retained)
	data, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, c.fail(fmt.Errorf("read changed file %q: %w", path, errors.Join(readErr, closeErr)))
	}
	if int64(len(data)) > remaining {
		if remaining < maxReviewSourceFileBytes {
			return nil, c.fail(fmt.Errorf("changed source set exceeds aggregate review limit of %d bytes", maxReviewSourceBytes))
		}
		return nil, c.fail(fmt.Errorf("changed source %q exceeds per-file review limit of %d bytes", path, maxReviewSourceFileBytes))
	}
	c.retained += int64(len(data))
	return data, nil
}

func (c *reviewFileCache) fail(err error) error {
	if c.firstFail == nil {
		c.firstFail = err
	}
	return err
}

// validateReviewRegularPath refuses special, oversized, or identity-swapped
// Go inputs before an external formatter is allowed to reopen their paths.
// O_NONBLOCK closes the regular-file-to-FIFO race during this validation; the
// formatter itself is additionally governed by a hard aggregate deadline.
func validateReviewRegularPath(root *os.Root, path string) error {
	rootPath := filepath.FromSlash(path)
	before, err := root.Lstat(rootPath)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular source file", path)
	}
	if before.Size() > maxReviewSourceFileBytes {
		return fmt.Errorf("%q is %d bytes; formatter input limit is %d bytes", path, before.Size(), maxReviewSourceFileBytes)
	}
	file, err := openRootReadOnly(root, rootPath)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	opened, statErr := file.Stat()
	current, lstatErr := root.Lstat(rootPath)
	closeErr := file.Close()
	if err := errors.Join(statErr, lstatErr, closeErr); err != nil {
		return fmt.Errorf("validate %q: %w", path, err)
	}
	if !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fmt.Errorf("%q changed identity before formatting", path)
	}
	return nil
}
