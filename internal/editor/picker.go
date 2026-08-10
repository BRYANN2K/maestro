package editor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Picker is the fuzzy picker sub-mode (files, commands, symbols).
type Picker struct {
	Active   bool
	Title    string
	Items    []string
	Sel      int
	Query    string
	OnSelect func(selected string)
}

// NewPicker returns an idle picker.
func NewPicker() *Picker { return &Picker{} }

// Start opens the picker with the given items.
func (p *Picker) Start(title string, items []string, onSelect func(string)) {
	p.Active = true
	p.Title = title
	p.Items = items
	p.Sel = 0
	p.Query = ""
	p.OnSelect = onSelect
}

// fuzzyScore returns a subsequence match score, -1 when no match. Earlier
// matches and longer needles score higher.
func fuzzyScore(needle, haystack string) int {
	if needle == "" {
		return 0
	}
	ni, hi := 0, 0
	firstMatch := -1
	for ni < len(needle) && hi < len(haystack) {
		if needle[ni] == haystack[hi] {
			if firstMatch < 0 {
				firstMatch = hi
			}
			ni++
		}
		hi++
	}
	if ni != len(needle) {
		return -1
	}
	return 100*len(needle) - hi - firstMatch
}

// Filter returns the items matching the query, best first.
func (p *Picker) Filter() []string {
	type scored struct {
		item  string
		score int
	}
	var out []scored
	for _, item := range p.Items {
		if s := fuzzyScore(p.Query, item); s >= 0 {
			out = append(out, scored{item: item, score: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	items := make([]string, 0, len(out))
	for _, s := range out {
		items = append(items, s.item)
	}
	return items
}

// Update handles picker keys.
func (p *Picker) Update(k Key) {
	switch k.Kind {
	case KeyEsc:
		p.Active = false
	case KeyDown, KeyTab:
		if p.Sel < len(p.Filter())-1 {
			p.Sel++
		}
	case KeyUp:
		if p.Sel > 0 {
			p.Sel--
		}
	case KeyEnter:
		items := p.Filter()
		if p.Sel >= 0 && p.Sel < len(items) && p.OnSelect != nil {
			p.OnSelect(items[p.Sel])
		}
		p.Active = false
	case KeyBackspace:
		if len(p.Query) > 0 {
			p.Query = p.Query[:len(p.Query)-1]
			if p.Sel >= len(p.Filter()) {
				p.Sel = 0
			}
		}
	case KeyRune:
		p.Query += string(k.Runes)
		p.Sel = 0
	case KeySpace:
		p.Query += " "
		p.Sel = 0
	}
}

// View renders the picker.
func (p *Picker) View(width int) string {
	var b strings.Builder
	b.WriteString(safePickerDisplay(p.Title) + "  " + safePickerDisplay(p.Query) + "\n\n")
	items := p.Filter()
	if len(items) == 0 {
		b.WriteString("  no matches\n")
	} else {
		for i, item := range items {
			if i > 12 {
				b.WriteString("  …\n")
				break
			}
			marker := "  "
			if i == p.Sel {
				marker = "▸ "
			}
			b.WriteString(marker + safePickerDisplay(item) + "\n")
		}
	}
	b.WriteString("\n  type to filter · ↑/↓ · enter select · esc close")
	return b.String()
}

// safePickerDisplay projects untrusted filenames and queries onto a single
// terminal-safe line. Picker values themselves remain unchanged, so selecting
// a displayed item still opens the exact filesystem path.
func safePickerDisplay(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || isBidiFormatControl(r) {
			return '�'
		}
		return r
	}, s)
}

// updatePicker routes picker keys.
func (e *Editor) updatePicker(k Key) EditAction {
	e.Picker.Update(k)
	return ActNone
}

const (
	listFilesGitTimeout    = 5 * time.Second
	listFilesGitBufferSize = 64 << 10
	listFilesGitMaxBytes   = 32 << 20
	listFilesGitMaxRecords = 100_000
)

// ListFiles returns project-relative files for the picker. Git is the source
// of truth inside a repository: tracked files stay discoverable regardless of
// ignore rules, while untracked ignored files never leak into the picker. The
// NUL-delimited format preserves every valid Git filename, including spaces,
// Unicode, and newlines. Non-repositories retain a bounded filesystem walk.
func ListFiles(root string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	root = filepath.Clean(root)
	ctx, cancel := context.WithTimeout(context.Background(), listFilesGitTimeout)
	defer cancel()
	if files, ok := listGitFiles(ctx, root, limit); ok {
		return files
	}
	// A failed listing inside a recognized Git repository must fail closed:
	// walking the filesystem here would surface ignored files. The fallback is
	// reserved for directories that are genuinely outside Git.
	if isGitDirectory(ctx, root) || ctx.Err() != nil {
		return nil
	}
	return listWalkedFiles(root, limit)
}

func listGitFiles(ctx context.Context, root string, limit int) ([]string, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	files, stopped, readErr := readGitFileList(stdout, root, limit)
	if stopped {
		// The hard byte/record ceiling bounds adversarial or enormous indexes.
		// Stop the producer once the reader has retained a deterministic,
		// sorted subset within that ceiling.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	waitErr := cmd.Wait()
	if stopped {
		return files, true
	}
	if readErr != nil || waitErr != nil {
		return nil, false
	}
	return files, true
}

func readGitFileList(input io.Reader, root string, limit int) ([]string, bool, error) {
	if limit <= 0 {
		return nil, true, nil
	}
	reader := bufio.NewReaderSize(input, listFilesGitBufferSize)
	seen := make(map[string]struct{})
	files := make([]string, 0, min(limit, 512))
	bytesRead := 0
	recordsRead := 0
	for {
		record, err := reader.ReadSlice(0)
		bytesRead += len(record)
		if errors.Is(err, bufio.ErrBufferFull) || bytesRead > listFilesGitMaxBytes || recordsRead >= listFilesGitMaxRecords {
			return finishFileList(files, limit), true, nil
		}
		if errors.Is(err, io.EOF) {
			if len(record) != 0 {
				return nil, false, errors.New("git returned a non-NUL-terminated filename")
			}
			return finishFileList(files, limit), false, nil
		}
		if err != nil {
			return nil, false, err
		}
		recordsRead++
		raw := string(record[:len(record)-1])
		rel, ok := normalizeRelativeFile(raw)
		if !ok {
			continue
		}
		if _, duplicate := seen[rel]; duplicate {
			continue
		}
		info, statErr := os.Stat(filepath.Join(root, rel))
		if statErr != nil || !info.Mode().IsRegular() {
			// gitlinks/submodules are directory entries in the index, while
			// deleted tracked paths and special files cannot be opened safely.
			continue
		}
		seen[rel] = struct{}{}
		files = append(files, rel)
	}
}

func finishFileList(files []string, limit int) []string {
	sort.Strings(files)
	if len(files) > limit {
		files = files[:limit]
	}
	return files
}

func isGitDirectory(ctx context.Context, root string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree", "--is-inside-git-dir", "--is-bare-repository")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Fields(string(out)) {
		if line == "true" {
			return true
		}
	}
	return false
}

func parseNULFileList(data []byte, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var files []string
	for _, raw := range bytes.Split(data, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		rel, ok := normalizeRelativeFile(string(raw))
		if !ok {
			continue
		}
		if _, duplicate := seen[rel]; duplicate {
			continue
		}
		seen[rel] = struct{}{}
		files = append(files, rel)
	}
	sort.Strings(files)
	if len(files) > limit {
		files = files[:limit]
	}
	return files
}

func normalizeRelativeFile(path string) (string, bool) {
	rel := filepath.Clean(filepath.FromSlash(path))
	return rel, safeRelativeFile(rel)
}

func listWalkedFiles(root string, limit int) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "specs") {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= limit {
			return filepath.SkipAll
		}
		if rel, err := filepath.Rel(root, path); err == nil && safeRelativeFile(rel) {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func safeRelativeFile(path string) bool {
	return path != "" && path != "." && path != ".." && !filepath.IsAbs(path) && filepath.VolumeName(path) == "" &&
		!strings.HasPrefix(path, ".."+string(filepath.Separator))
}

// Symbols extracts function-like declarations for the buffer (best-effort).
func Symbols(b *Buffer) []string {
	var out []string
	for i, line := range b.Lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "func(") || strings.HasPrefix(trimmed, "fn ") || strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "function ") {
			out = append(out, line+"  (line "+itoa(i+1)+")")
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
