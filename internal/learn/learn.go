// Package learn implements the read-only source explanation pipeline used by
// /learn. Source bytes and model output both cross explicit validation
// boundaries before an explanation can be rendered or written.
package learn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxSourceBytes is deliberately smaller than the editor limit: learn
	// content is sent to a model and must remain bounded as prompt data.
	MaxSourceBytes int64 = 256 << 10
	// MaxArtifactBytes bounds both staged and headless learn output.
	MaxArtifactBytes = 512 << 10
)

// Block is one explained code region, anchored to exact source lines.
type Block struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Code    string `json:"code"`
	What    string `json:"what"`
	Trap    string `json:"trap"`
	Caution string `json:"caution"`
}

// Explanation is the structured output of one /learn run. Path is always
// project-relative and SourceSHA256 fingerprints the exact on-disk bytes read.
type Explanation struct {
	Path         string   `json:"path"`
	SourceSHA256 string   `json:"source_sha256"`
	Language     string   `json:"language,omitempty"`
	HighLevel    string   `json:"high_level"`
	Blocks       []Block  `json:"blocks"`
	FollowUps    []string `json:"follow_ups,omitempty"`
}

// ExplainFunc generates an explanation. path is a safe project-relative path
// and content is bounded, validated UTF-8 source data.
type ExplainFunc func(ctx context.Context, path string, content []byte, deep bool) (Explanation, error)

// Generator produces and writes learn artifacts for one active project root.
type Generator struct {
	ProjectDir string
	Explain    ExplainFunc
}

// New builds a generator rooted at the active project or worktree.
func New(projectDir string, fn ExplainFunc) *Generator {
	return &Generator{ProjectDir: projectDir, Explain: fn}
}

// Generate validates and reads source, invokes the explainer, validates every
// response anchor against that source, and returns bounded safe Markdown.
func (g *Generator) Generate(ctx context.Context, path string, deep bool) (Explanation, string, error) {
	if g.Explain == nil {
		return Explanation{}, "", errors.New("learn: no explainer configured")
	}
	snapshot, err := ReadSource(ctx, g.ProjectDir, path)
	if err != nil {
		return Explanation{}, "", err
	}
	if err := ctx.Err(); err != nil {
		return Explanation{}, "", err
	}
	exp, err := g.Explain(ctx, snapshot.RelativePath, snapshot.Content, deep)
	if err != nil {
		return Explanation{}, "", err
	}
	if err := ctx.Err(); err != nil {
		return Explanation{}, "", err
	}
	exp.Path = snapshot.RelativePath
	exp.SourceSHA256 = snapshot.SHA256
	exp.Language = snapshot.Language
	if err := ValidateExplanationForDepth(snapshot, &exp, deep); err != nil {
		return Explanation{}, "", fmt.Errorf("learn response: %w", err)
	}
	formatted := Format(exp)
	if len(formatted) > MaxArtifactBytes {
		return Explanation{}, "", fmt.Errorf("learn response: formatted artifact exceeds %d bytes", MaxArtifactBytes)
	}
	return exp, formatted, nil
}

// Write atomically persists already formatted output. Interactive frontends
// may continue to stage the same bytes in the proposal store before calling
// any write path.
func (g *Generator) Write(path, formatted string) (string, error) {
	return g.WriteArtifact(g.OutputPath(path), formatted)
}

// WriteArtifact atomically persists a staged learn artifact at its exact
// precomputed destination. The destination is restricted to the generator's
// maestro/learn directory, which keeps headless and staged writes identical.
func (g *Generator) WriteArtifact(out, formatted string) (string, error) {
	if len(formatted) > MaxArtifactBytes {
		return "", fmt.Errorf("learn write: artifact exceeds %d bytes", MaxArtifactBytes)
	}
	if !validText(formatted) {
		return "", errors.New("learn write: artifact contains unsafe text")
	}
	root, err := canonicalRoot(g.ProjectDir)
	if err != nil {
		return "", err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("learn write: resolve output: %w", err)
	}
	out = filepath.Clean(out)
	dir := filepath.Dir(out)
	expectedDir := filepath.Join(root, "maestro", "learn")
	if dir != expectedDir || filepath.Ext(out) != ".md" {
		return "", errors.New("learn write: output is outside the learn artifact directory")
	}
	if err := prepareArtifactDir(root, dir); err != nil {
		return "", err
	}
	if info, err := os.Lstat(out); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("learn write: output is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("learn write: inspect output: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".maestro-learn-*")
	if err != nil {
		return "", fmt.Errorf("learn write: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(formatted); err != nil {
		tmp.Close()
		return "", fmt.Errorf("learn write: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", fmt.Errorf("learn write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("learn write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("learn write: %w", err)
	}
	if err := os.Rename(tmpPath, out); err != nil {
		return "", fmt.Errorf("learn write: %w", err)
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return out, nil
}

// OutputPath returns the deterministic in-project artifact path without
// touching disk. Generate is still the authority for validating the source.
func (g *Generator) OutputPath(path string) string {
	root := filepath.Clean(g.ProjectDir)
	if canonical, err := canonicalRoot(g.ProjectDir); err == nil {
		root = canonical
	}
	rel := filepath.Clean(path)
	if filepath.IsAbs(rel) && root != "" {
		if candidate, err := filepath.Rel(root, rel); err == nil && pathWithin(root, rel) {
			rel = candidate
		}
	}
	return filepath.Join(root, "maestro", "learn", Slugify(rel)+".md")
}

func prepareArtifactDir(root, dir string) error {
	if !pathWithin(root, dir) {
		return errors.New("learn write: output escapes project root")
	}
	if err := rejectExistingSymlinks(root, dir); err != nil {
		return fmt.Errorf("learn write: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("learn write: %w", err)
	}
	if err := rejectExistingSymlinks(root, dir); err != nil {
		return fmt.Errorf("learn write: %w", err)
	}
	return nil
}

func validText(value string) bool {
	if !validUTF8String(value) {
		return false
	}
	for _, r := range value {
		if unsafeControl(r) {
			return false
		}
	}
	return !strings.ContainsRune(value, '\x00')
}
