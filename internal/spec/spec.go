// Package spec implements the spec pipeline: Spec and Batch models with YAML
// frontmatter, and a Store for loading, saving, listing, and archiving spec
// folders under the project's specs directory.
package spec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Spec status values.
const (
	StatusProposal    = "proposal"
	StatusInProgress  = "in_progress"
	StatusImplemented = "implemented"
	StatusArchived    = "archived"
)

// Valid statuses for validation.
var validStatuses = []string{StatusProposal, StatusInProgress, StatusImplemented, StatusArchived}

// Slugify's input scrubber.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// File names inside a spec folder.
const (
	FileSpec      = "spec.md"
	FileDesign    = "design.md"
	FileTasks     = "tasks.md"
	FileIdeas     = "spec-idea.md"
	ArchiveDir    = "archive"
	frontmatter   = "---"
	frontmatterRe = "^---[ \t]*$"
)

var (
	idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	fmLineRe = regexp.MustCompile(frontmatterRe)
)

func validSpecID(id string) bool {
	return idRe.MatchString(id) && id != ArchiveDir
}

// Spec is the change proposal: goal, scope, decisions, and risks, broken
// down into ordered batches.
type Spec struct {
	SchemaVersion int           `yaml:"schema_version,omitempty"`
	Recipe        Recipe        `yaml:"recipe,omitempty"`
	ID            string        `yaml:"id"`
	Title         string        `yaml:"title"`
	Status        string        `yaml:"status"`
	Category      string        `yaml:"category,omitempty"` // feat | fix | chore | docs
	Tags          []string      `yaml:"tags,omitempty"`
	Batches       []Batch       `yaml:"batches,omitempty"`
	Success       []string      `yaml:"success_criteria,omitempty"`
	Decisions     []string      `yaml:"decisions,omitempty"`
	Ideas         []string      `yaml:"ideas,omitempty"` // spec-idea.md backlog
	Created       string        `yaml:"created,omitempty"`
	DependsOn     []string      `yaml:"depends_on,omitempty"`
	Requirements  []Requirement `yaml:"requirements,omitempty"`
	Questions     []Question    `yaml:"questions,omitempty"`
	Body          string        `yaml:"-"` // markdown body after the frontmatter
	Frontmatter   string        `yaml:"-"` // raw frontmatter block, for round-trip fidelity
}

// Batch is one ordered delivery unit of a spec.
type Batch struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Files          []string `yaml:"files,omitempty"`
	Tasks          []string `yaml:"tasks,omitempty"`
	Accept         []string `yaml:"acceptance,omitempty"`
	RequirementIDs []string `yaml:"requirements,omitempty"`
}

// Summary is the lightweight listing form of a Spec.
type Summary struct {
	ID       string
	Title    string
	Status   string
	Category string
	Created  string
}

// Valid reports whether the spec satisfies the structural rules of the
// pipeline: a valid ID, title, status, and category.
func (s *Spec) Valid() error {
	var errs []error
	if s.ID == ArchiveDir {
		errs = append(errs, fmt.Errorf("spec ID %q is reserved for the archive directory", s.ID))
	} else if !idRe.MatchString(s.ID) {
		errs = append(errs, fmt.Errorf("spec ID %q invalid: want lowercase alphanumerics and dashes", s.ID))
	}
	if strings.TrimSpace(s.Title) == "" {
		errs = append(errs, errors.New("spec title is required"))
	}
	if !slices.Contains(validStatuses, s.Status) {
		errs = append(errs, fmt.Errorf("spec status %q invalid: want one of %v", s.Status, validStatuses))
	}
	if s.Category != "" && !slices.Contains([]string{"feat", "fix", "chore", "docs"}, s.Category) {
		errs = append(errs, fmt.Errorf("spec category %q invalid: want feat, fix, chore, or docs", s.Category))
	}
	return errors.Join(errs...)
}

// Store persists spec folders under a root directory, one folder per spec:
// specs/<id>/{spec.md,design.md,tasks.md,spec-idea.md}.
type Store struct {
	dir      string
	tempName func(prefix string) (string, error)
}

// TrioMaterialization is an opaque identity for the exact directory and three
// regular files created by WriteTrioTracked. It lets /accept roll back only
// Maestro's own materialization, while preserving any replacement or
// concurrent addition.
type TrioMaterialization struct {
	storeDir string
	id       string
	dir      fs.FileInfo
	files    map[string]materializedFile
}

type materializedFile struct {
	info   fs.FileInfo
	mode   fs.FileMode
	size   int64
	digest [sha256.Size]byte
}

// NewStore returns a Store rooted at dir. The directory is created on first
// use, not here, so read-only callers can construct a Store freely.
func NewStore(dir string) *Store {
	return &Store{dir: dir, tempName: randomTempName}
}

// Dir returns the store root.
func (st *Store) Dir() string { return st.dir }

// Path returns the absolute path of the spec folder for id.
func (st *Store) Path(id string) string { return filepath.Join(st.dir, id) }

// Load reads the spec with id from specs/<id>/spec.md.
func (st *Store) Load(ctx context.Context, id string) (*Spec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSpecID(id) {
		return nil, fmt.Errorf("load spec: invalid id %q", id)
	}
	root, err := st.openRoot(false)
	if err != nil {
		return nil, fmt.Errorf("load spec %s: %w", id, err)
	}
	defer root.Close()
	specRoot, _, err := openStableDir(root, id, false, 0)
	if err != nil {
		return nil, fmt.Errorf("load spec %s: %w", id, err)
	}
	defer specRoot.Close()
	data, err := readRegularFile(specRoot, FileSpec)
	if err != nil {
		return nil, fmt.Errorf("load spec %s: %w", id, err)
	}
	s, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("load spec %s: %w", id, err)
	}
	if s.ID != id {
		return nil, fmt.Errorf("load spec %s: frontmatter id %q does not match folder", id, s.ID)
	}
	return s, nil
}

// Save writes the spec's spec.md file (frontmatter + body), creating the
// folder as needed.
func (st *Store) Save(ctx context.Context, s *Spec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Valid(); err != nil {
		return fmt.Errorf("save spec: %w", err)
	}
	data, err := Marshal(s)
	if err != nil {
		return fmt.Errorf("save spec %s: %w", s.ID, err)
	}
	root, err := st.openRoot(true)
	if err != nil {
		return fmt.Errorf("save spec %s: %w", s.ID, err)
	}
	defer root.Close()
	specRoot, _, err := openStableDir(root, s.ID, true, 0o755)
	if err != nil {
		return fmt.Errorf("save spec %s: %w", s.ID, err)
	}
	defer specRoot.Close()
	if err := atomicWriteRegular(specRoot, FileSpec, data, 0o644, st.nextTempName); err != nil {
		return fmt.Errorf("save spec %s: %w", s.ID, err)
	}
	return nil
}

// WriteTrio saves the spec and its design and tasks documents — the full
// /accept output. design and tasks are plain markdown files.
func (st *Store) WriteTrio(ctx context.Context, s *Spec, design, tasks string) (retErr error) {
	_, err := st.WriteTrioTracked(ctx, s, design, tasks)
	return err
}

// WriteTrioTracked atomically publishes the accepted trio and returns its
// exact filesystem/content identity for non-destructive rollback.
func (st *Store) WriteTrioTracked(ctx context.Context, s *Spec, design, tasks string) (materialization *TrioMaterialization, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.Valid(); err != nil {
		return nil, fmt.Errorf("write spec trio: %w", err)
	}
	data, err := Marshal(s)
	if err != nil {
		return nil, err
	}
	root, err := st.openRoot(true)
	if err != nil {
		return nil, fmt.Errorf("write spec trio %s: %w", s.ID, err)
	}
	defer root.Close()
	if _, err := root.Lstat(s.ID); err == nil {
		return nil, fmt.Errorf("write spec trio %s: destination already exists", s.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("write spec trio %s: %w", s.ID, err)
	}
	tmpName, tmpRoot, tmpInfo, err := st.createTempDir(root, "."+s.ID+"-")
	if err != nil {
		return nil, fmt.Errorf("write spec trio %s: %w", s.ID, err)
	}
	published := false
	defer func() {
		closeErr := tmpRoot.Close()
		if !published {
			retErr = errors.Join(retErr, removeCreated(root, tmpName, tmpInfo))
		}
		retErr = errors.Join(retErr, closeErr)
	}()
	files := map[string][]byte{
		FileSpec: data, FileDesign: []byte(design), FileTasks: []byte(tasks),
	}
	identities := make(map[string]materializedFile, len(files))
	for name, content := range files {
		info, err := writeNewRegular(tmpRoot, name, content, 0o644)
		if err != nil {
			return nil, fmt.Errorf("write spec trio %s: %w", s.ID, err)
		}
		identities[name] = materializedFile{
			info: info, mode: info.Mode(), size: int64(len(content)), digest: sha256.Sum256(content),
		}
	}
	if err := verifyNamedIdentity(root, tmpName, tmpInfo); err != nil {
		return nil, fmt.Errorf("write spec trio %s: temporary directory changed: %w", s.ID, err)
	}
	if _, err := root.Lstat(s.ID); err == nil {
		return nil, fmt.Errorf("write spec trio %s: destination already exists", s.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("write spec trio %s: %w", s.ID, err)
	}
	if err := root.Rename(tmpName, s.ID); err != nil {
		return nil, fmt.Errorf("write spec trio %s: %w", s.ID, err)
	}
	published = true
	materialization = &TrioMaterialization{
		storeDir: cleanAbsolute(st.dir), id: s.ID, dir: tmpInfo, files: identities,
	}
	checkRoot, err := verifyTrioMaterialization(root, materialization)
	if err != nil {
		return materialization, fmt.Errorf("write spec trio %s: verify publication: %w", s.ID, err)
	}
	checkRoot.Close()
	return materialization, nil
}

// List returns summaries of all non-archived specs, ordered by ID.
func (st *Store) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := st.openRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list specs: %w", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("list specs: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil || closeErr != nil {
		return nil, fmt.Errorf("list specs: %w", errors.Join(err, closeErr))
	}
	var out []Summary
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ArchiveDir || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		s, err := st.Load(ctx, e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, Summary{ID: s.ID, Title: s.Title, Status: s.Status, Category: s.Category, Created: s.Created})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, errors.Join(errs...)
}

// Archive moves the spec folder to specs/archive/<id>. The move fails if the
// destination already exists.
func (st *Store) Archive(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSpecID(id) {
		return fmt.Errorf("archive spec: invalid id %q", id)
	}
	root, err := st.openRoot(false)
	if err != nil {
		return fmt.Errorf("archive spec %s: %w", id, err)
	}
	defer root.Close()
	srcRoot, srcInfo, err := openStableDir(root, id, false, 0)
	if err != nil {
		return fmt.Errorf("archive spec %s: %w", id, err)
	}
	defer srcRoot.Close()
	archiveRoot, archiveInfo, err := openStableDir(root, ArchiveDir, true, 0o755)
	if err != nil {
		return fmt.Errorf("archive spec %s: %w", id, err)
	}
	defer archiveRoot.Close()
	if _, err := archiveRoot.Lstat(id); err == nil {
		return fmt.Errorf("archive spec %s: destination already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("archive spec %s: %w", id, err)
	}
	if err := verifyNamedIdentity(root, id, srcInfo); err != nil {
		return fmt.Errorf("archive spec %s: source changed: %w", id, err)
	}
	if err := verifyNamedIdentity(root, ArchiveDir, archiveInfo); err != nil {
		return fmt.Errorf("archive spec %s: archive directory changed: %w", id, err)
	}
	if err := root.Rename(id, filepath.Join(ArchiveDir, id)); err != nil {
		return fmt.Errorf("archive spec %s: %w", id, err)
	}
	return nil
}

// RestoreArchive moves an archived spec back to the active store. It is used
// to roll back a failed archive commit so filesystem and session state remain
// consistent.
func (st *Store) RestoreArchive(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSpecID(id) {
		return fmt.Errorf("restore archived spec: invalid id %q", id)
	}
	root, err := st.openRoot(false)
	if err != nil {
		return fmt.Errorf("restore archived spec %s: %w", id, err)
	}
	defer root.Close()
	archiveRoot, archiveInfo, err := openStableDir(root, ArchiveDir, false, 0)
	if err != nil {
		return fmt.Errorf("restore archived spec %s: %w", id, err)
	}
	defer archiveRoot.Close()
	srcRoot, srcInfo, err := openStableDir(archiveRoot, id, false, 0)
	if err != nil {
		return fmt.Errorf("restore archived spec %s: %w", id, err)
	}
	defer srcRoot.Close()
	if _, err := root.Lstat(id); err == nil {
		return fmt.Errorf("restore archived spec %s: destination already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restore archived spec %s: %w", id, err)
	}
	if err := verifyNamedIdentity(root, ArchiveDir, archiveInfo); err != nil {
		return fmt.Errorf("restore archived spec %s: archive directory changed: %w", id, err)
	}
	if err := verifyNamedIdentity(archiveRoot, id, srcInfo); err != nil {
		return fmt.Errorf("restore archived spec %s: source changed: %w", id, err)
	}
	if err := root.Rename(filepath.Join(ArchiveDir, id), id); err != nil {
		return fmt.Errorf("restore archived spec %s: %w", id, err)
	}
	return nil
}

// Ideas returns the spec-idea.md backlog for id.
func (st *Store) Ideas(ctx context.Context, id string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSpecID(id) {
		return nil, fmt.Errorf("read ideas: invalid id %q", id)
	}
	root, err := st.openRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ideas for %s: %w", id, err)
	}
	defer root.Close()
	specRoot, _, err := openStableDir(root, id, false, 0)
	if err != nil {
		return nil, fmt.Errorf("read ideas for %s: %w", id, err)
	}
	defer specRoot.Close()
	data, err := readRegularFile(specRoot, FileIdeas)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ideas for %s: %w", id, err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n"), nil
}

// AppendIdea appends one idea with a timestamp to the spec's spec-idea.md.
// The file is append-only: existing content is never rewritten.
func (st *Store) AppendIdea(ctx context.Context, id, idea string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSpecID(id) {
		return fmt.Errorf("append idea: invalid id %q", id)
	}
	line := fmt.Sprintf("- %s — %s\n", time.Now().Format(time.RFC3339), strings.TrimSpace(idea))
	root, err := st.openRoot(false)
	if err != nil {
		return fmt.Errorf("append idea to %s: %w", id, err)
	}
	defer root.Close()
	specRoot, _, err := openStableDir(root, id, false, 0)
	if err != nil {
		return fmt.Errorf("append idea to %s: %w", id, err)
	}
	defer specRoot.Close()
	if info, err := specRoot.Lstat(FileIdeas); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("append idea to %s: %s is not a regular file", id, FileIdeas)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("append idea to %s: %w", id, err)
	}
	f, err := specRoot.OpenFile(FileIdeas, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append idea to %s: %w", id, err)
	}
	opened, statErr := f.Stat()
	named, namedErr := specRoot.Lstat(FileIdeas)
	if statErr != nil || namedErr != nil || named.Mode()&os.ModeSymlink != 0 || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		_ = f.Close()
		return fmt.Errorf("append idea to %s: target changed while opening: %w", id, errors.Join(statErr, namedErr))
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("append idea to %s: %w", id, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("append idea to %s: sync: %w", id, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("append idea to %s: close: %w", id, err)
	}
	return nil
}

// openRoot anchors all Store operations to the parent of the configured
// specs directory. Opening the final component through os.Root is important:
// os.OpenRoot(st.dir) alone would follow a malicious "specs" symlink before
// establishing its confinement boundary.
func (st *Store) openRoot(create bool) (*os.Root, error) {
	abs, err := filepath.Abs(st.dir)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	parentPath, name := filepath.Dir(abs), filepath.Base(abs)
	if name == "." || name == string(filepath.Separator) {
		return nil, errors.New("invalid spec store root")
	}
	if create {
		if err := os.MkdirAll(parentPath, 0o755); err != nil {
			return nil, err
		}
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	root, _, err := openStableDir(parent, name, create, 0o755)
	return root, err
}

// openStableDir opens a single directory component without accepting a
// symlink and verifies that the opened descriptor is still the named entry.
// Once returned, later operations stay anchored even if an attacker renames
// an ancestor concurrently.
func openStableDir(parent *os.Root, name string, create bool, perm fs.FileMode) (*os.Root, fs.FileInfo, error) {
	if name == "" || name == "." || filepath.Base(name) != name {
		return nil, nil, fmt.Errorf("unsafe directory name %q", name)
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := parent.Mkdir(name, perm); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("refusing symlink directory %q", name)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%q is not a directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := child.Stat(".")
	if err != nil {
		child.Close()
		return nil, nil, err
	}
	current, err := parent.Lstat(name)
	if err != nil {
		child.Close()
		return nil, nil, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(opened, current) {
		child.Close()
		return nil, nil, fmt.Errorf("directory %q changed while opening", name)
	}
	return child, opened, nil
}

func verifyNamedIdentity(root *os.Root, name string, want fs.FileInfo) error {
	got, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if got.Mode()&os.ModeSymlink != 0 || !os.SameFile(got, want) {
		return fmt.Errorf("%q was replaced", name)
	}
	return nil
}

func readRegularFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", name)
	}
	return root.ReadFile(name)
}

func randomTempName(prefix string) (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(entropy[:]), nil
}

func (st *Store) nextTempName(prefix string) (string, error) {
	if st.tempName == nil {
		return randomTempName(prefix)
	}
	return st.tempName(prefix)
}

func (st *Store) createTempDir(root *os.Root, prefix string) (string, *os.Root, fs.FileInfo, error) {
	for range 128 {
		name, err := st.nextTempName(prefix)
		if err != nil {
			return "", nil, nil, err
		}
		if filepath.Base(name) != name || name == "." {
			return "", nil, nil, fmt.Errorf("unsafe temporary name %q", name)
		}
		if err := root.Mkdir(name, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				if info, statErr := root.Lstat(name); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
					return "", nil, nil, fmt.Errorf("refusing temporary symlink %q", name)
				}
				continue
			}
			return "", nil, nil, err
		}
		child, info, err := openStableDir(root, name, false, 0)
		if err != nil {
			return "", nil, nil, errors.Join(err, root.Remove(name))
		}
		return name, child, info, nil
	}
	return "", nil, nil, errors.New("could not allocate a unique temporary directory")
}

func writeNewRegular(root *os.Root, name string, data []byte, perm fs.FileMode) (fs.FileInfo, error) {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	info, err := f.Stat()
	return info, errors.Join(err, f.Close())
}

func atomicWriteRegular(root *os.Root, name string, data []byte, defaultPerm fs.FileMode, nextName func(string) (string, error)) (retErr error) {
	perm := defaultPerm
	if current, err := root.Lstat(name); err == nil {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular target %q", name)
		}
		perm = current.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var tmpName string
	var tmpInfo fs.FileInfo
	for range 128 {
		candidate, err := nextName("." + name + "-")
		if err != nil {
			return err
		}
		if filepath.Base(candidate) != candidate || candidate == "." {
			return fmt.Errorf("unsafe temporary name %q", candidate)
		}
		f, err := root.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				if info, statErr := root.Lstat(candidate); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("refusing temporary symlink %q", candidate)
				}
				continue
			}
			return err
		}
		tmpName = candidate
		tmpInfo, err = f.Stat()
		if err == nil {
			_, err = f.Write(data)
		}
		if err == nil {
			err = f.Chmod(perm)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr, removeCreated(root, tmpName, tmpInfo))
		}
		break
	}
	if tmpName == "" {
		return errors.New("could not allocate a unique temporary file")
	}
	defer func() {
		if tmpName != "" {
			retErr = errors.Join(retErr, removeCreated(root, tmpName, tmpInfo))
		}
	}()
	if err := verifyNamedIdentity(root, tmpName, tmpInfo); err != nil {
		return fmt.Errorf("temporary file changed: %w", err)
	}
	if current, err := root.Lstat(name); err == nil {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular target %q", name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// removeCreated is deliberately identity-checked: rollback/cleanup never
// removes a path that another actor replaced after Maestro created it.
func removeCreated(root *os.Root, name string, want fs.FileInfo) error {
	if want == nil {
		return errors.New("refusing cleanup without file identity")
	}
	got, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if got.Mode()&os.ModeSymlink != 0 || !os.SameFile(got, want) {
		return fmt.Errorf("refusing to remove replaced temporary path %q", name)
	}
	return root.RemoveAll(name)
}

// RollbackTrio removes only the exact trio represented by materialization.
// It first quarantines the directory by atomic rename, revalidates every
// inode, mode and byte, and refuses deletion if a file was replaced, edited,
// or added. On refusal it restores the directory whenever the original name
// is still free; otherwise the quarantined data is deliberately preserved.
func (st *Store) RollbackTrio(ctx context.Context, materialization *TrioMaterialization) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if materialization == nil || materialization.id == "" || materialization.dir == nil {
		return errors.New("rollback spec trio: missing materialization identity")
	}
	if materialization.storeDir != cleanAbsolute(st.dir) {
		return errors.New("rollback spec trio: materialization belongs to another store")
	}
	if !validSpecID(materialization.id) {
		return fmt.Errorf("rollback spec trio: invalid id %q", materialization.id)
	}
	root, err := st.openRoot(false)
	if err != nil {
		return fmt.Errorf("rollback spec trio %s: %w", materialization.id, err)
	}
	defer root.Close()
	verified, err := verifyTrioMaterialization(root, materialization)
	if err != nil {
		return fmt.Errorf("rollback spec trio %s: %w", materialization.id, err)
	}
	verified.Close()

	quarantine, err := st.unusedName(root, ".rollback-"+materialization.id+"-")
	if err != nil {
		return fmt.Errorf("rollback spec trio %s: %w", materialization.id, err)
	}
	if err := root.Rename(materialization.id, quarantine); err != nil {
		return fmt.Errorf("rollback spec trio %s: quarantine: %w", materialization.id, err)
	}
	restore := func(cause error) error {
		if _, statErr := root.Lstat(materialization.id); statErr == nil {
			return errors.Join(cause, fmt.Errorf("rollback spec trio %s: original path was concurrently recreated; preserved materialization at %s", materialization.id, quarantine))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(cause, fmt.Errorf("rollback spec trio %s: inspect restore destination: %w; preserved materialization at %s", materialization.id, statErr, quarantine))
		}
		if renameErr := root.Rename(quarantine, materialization.id); renameErr != nil {
			return errors.Join(cause, fmt.Errorf("rollback spec trio %s: restore quarantined materialization: %w; preserved at %s", materialization.id, renameErr, quarantine))
		}
		return cause
	}

	quarantineRoot, err := verifyTrioAt(root, quarantine, materialization)
	if err != nil {
		return restore(fmt.Errorf("rollback spec trio %s: changed while quarantining: %w", materialization.id, err))
	}
	for _, name := range []string{FileSpec, FileDesign, FileTasks} {
		if err := quarantineRoot.Remove(name); err != nil {
			quarantineRoot.Close()
			return restore(fmt.Errorf("rollback spec trio %s: remove %s: %w", materialization.id, name, err))
		}
	}
	remaining, readErr := readDirNames(quarantineRoot)
	closeErr := quarantineRoot.Close()
	if readErr != nil || closeErr != nil {
		return restore(fmt.Errorf("rollback spec trio %s: verify empty quarantine: %w", materialization.id, errors.Join(readErr, closeErr)))
	}
	if len(remaining) != 0 {
		return restore(fmt.Errorf("rollback spec trio %s: concurrent entries preserved: %v", materialization.id, remaining))
	}
	if err := root.Remove(quarantine); err != nil {
		return restore(fmt.Errorf("rollback spec trio %s: remove empty directory: %w", materialization.id, err))
	}
	return nil
}

func (st *Store) unusedName(root *os.Root, prefix string) (string, error) {
	for range 128 {
		name, err := st.nextTempName(prefix)
		if err != nil {
			return "", err
		}
		if filepath.Base(name) != name || name == "." {
			return "", fmt.Errorf("unsafe temporary name %q", name)
		}
		if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique rollback name")
}

func verifyTrioMaterialization(storeRoot *os.Root, materialization *TrioMaterialization) (*os.Root, error) {
	return verifyTrioAt(storeRoot, materialization.id, materialization)
}

func verifyTrioAt(parent *os.Root, name string, materialization *TrioMaterialization) (*os.Root, error) {
	dirRoot, dirInfo, err := openStableDir(parent, name, false, 0)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(dirInfo, materialization.dir) {
		dirRoot.Close()
		return nil, errors.New("spec directory was replaced")
	}
	names, err := readDirNames(dirRoot)
	if err != nil {
		dirRoot.Close()
		return nil, err
	}
	wantNames := []string{FileDesign, FileSpec, FileTasks}
	sort.Strings(names)
	if !slices.Equal(names, wantNames) {
		dirRoot.Close()
		return nil, fmt.Errorf("spec directory entries changed: got %v, want %v", names, wantNames)
	}
	for _, fileName := range wantNames {
		want, ok := materialization.files[fileName]
		if !ok || want.info == nil {
			dirRoot.Close()
			return nil, fmt.Errorf("missing identity for %s", fileName)
		}
		if err := verifyMaterializedFile(dirRoot, fileName, want); err != nil {
			dirRoot.Close()
			return nil, err
		}
	}
	return dirRoot, nil
}

func verifyMaterializedFile(root *os.Root, name string, want materializedFile) error {
	entry, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return fmt.Errorf("%s is not the created regular file", name)
	}
	if !os.SameFile(entry, want.info) || entry.Mode() != want.mode || entry.Size() != want.size {
		return fmt.Errorf("%s was replaced or modified", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(opened, want.info) {
		f.Close()
		return errors.Join(err, fmt.Errorf("%s changed while opening", name))
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	final, statErr := f.Stat()
	closeErr := f.Close()
	if copyErr != nil || statErr != nil || closeErr != nil {
		return errors.Join(copyErr, statErr, closeErr)
	}
	if !os.SameFile(final, want.info) || final.Mode() != want.mode || final.Size() != want.size {
		return fmt.Errorf("%s changed while reading", name)
	}
	if got := hash.Sum(nil); !slices.Equal(got, want.digest[:]) {
		return fmt.Errorf("%s content changed", name)
	}
	return nil
}

func readDirNames(root *os.Root) ([]string, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func cleanAbsolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// GoalLine returns the first line of the spec's Goal section, or the
// title as a fallback — used for commit messages and task summaries.
func (s *Spec) GoalLine() string {
	for _, line := range strings.Split(s.Body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "**") && !strings.HasPrefix(line, ">") {
			return line
		}
	}
	return s.Title
}

// Slugify turns a prompt into a filesystem-safe spec ID: lowercase,
// non-alphanumerics become dashes, truncated to 40 characters.
func Slugify(prompt string) string {
	slug := slugRe.ReplaceAllString(strings.ToLower(prompt), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
		slug = strings.TrimRight(slug, "-")
	}
	if slug == "" {
		slug = "proposal"
	}
	if slug == ArchiveDir {
		slug = "archive-spec"
	}
	return slug
}

// Parse decodes a spec.md file: YAML frontmatter between --- lines, body after.
func Parse(data []byte) (*Spec, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var s Spec
	if err := yaml.Unmarshal(fm, &s); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	s.Frontmatter = string(fm)
	s.Body = string(body)
	return &s, nil
}

// Marshal encodes a spec.md file: frontmatter + body.
func Marshal(s *Spec) ([]byte, error) {
	fm, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}
	var b strings.Builder
	b.WriteString(frontmatter + "\n")
	b.Write(fm)
	b.WriteString(frontmatter + "\n")
	if strings.TrimSpace(s.Body) != "" {
		b.WriteString("\n")
		b.WriteString(s.Body)
		if !strings.HasSuffix(s.Body, "\n") {
			b.WriteString("\n")
		}
	}
	return []byte(b.String()), nil
}

// splitFrontmatter splits raw spec.md content into frontmatter YAML and body.
func splitFrontmatter(data []byte) (fm, body []byte, err error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 || !frontmatterLine(lines[0]) {
		return nil, nil, errors.New("spec file is missing YAML frontmatter (--- ... ---)")
	}
	var i int
	for i = 1; i < len(lines); i++ {
		if frontmatterLine(lines[i]) {
			break
		}
	}
	if i == len(lines) {
		return nil, nil, errors.New("spec file frontmatter is never closed (missing trailing ---)")
	}
	fm = []byte(strings.Join(lines[1:i], "\n"))
	body = []byte(strings.TrimPrefix(strings.Join(lines[i+1:], "\n"), "\n"))
	return fm, body, nil
}

func frontmatterLine(line string) bool {
	return fmLineRe.MatchString(line)
}

// PathFor returns the path of one of the spec's documents
// (spec.md, design.md, tasks.md, spec-idea.md) without touching the disk.
func (st *Store) PathFor(id, file string) string {
	return filepath.Join(st.Path(id), file)
}
