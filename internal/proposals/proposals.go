package proposals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
)

const (
	maxProposalFileBytes   int64 = 8 << 20
	maxProposalRecordBytes int64 = 4*maxProposalFileBytes + 1<<20
)

// Hunk is one contiguous block of line replacements anchored by content
// hash: OldLines must still be present verbatim in the target file for the
// hunk to apply (stale rejection).
type Hunk struct {
	Start    int      `json:"start"`         // 1-based line of the first old line in the original file
	OldLines []string `json:"old,omitempty"` // lines to remove (may be empty: pure insertion)
	NewLines []string `json:"new,omitempty"` // lines to insert (may be empty: pure deletion)
}

// Proposal is a staged write: the full set of hunks between the file's
// current content and the agent's proposed content.
type Proposal struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Anchor      string   `json:"anchor"`                 // sha256 of the base file content at staging time
	ContentHash string   `json:"content_hash,omitempty"` // sha256 of the complete staged target
	BaseLines   []string `json:"base,omitempty"`
	Hunks       []Hunk   `json:"hunks"`
	EndsNL      bool     `json:"ends_nl"` // proposed content ends with a newline
}

// Store stages agent writes in an isolated directory. Nothing touches the
// user's files until Accept.
type Store struct {
	dir  string
	root func() string
}

// NewProposalStore returns a Store rooted at dir.
func NewProposalStore(dir string) *Store { return &Store{dir: dir} }

// NewWorkspaceProposalStore returns a proposal store whose target paths are
// jailed to the current workspace root. root is evaluated for every operation
// so a session can move into a worktree without rebuilding the store.
func NewWorkspaceProposalStore(dir string, root func() string) *Store {
	return &Store{dir: dir, root: root}
}

// Stage writes the proposal JSON for path+content into the store.
func (s *Store) Stage(path, content string) (Proposal, error) {
	path, err := s.resolvePath(path)
	if err != nil {
		return Proposal{}, err
	}
	if int64(len(content)) > maxProposalFileBytes {
		return Proposal{}, fmt.Errorf("stage %s: proposed content is %d bytes; limit is %d", path, len(content), maxProposalFileBytes)
	}
	base, err := readBoundedFile(path, maxProposalFileBytes)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Proposal{}, fmt.Errorf("stage %s: %w", path, err)
		}
		base = nil
	}
	baseLines := splitLines(string(base))
	prop := Proposal{
		ID:          "p" + sha256Sum(path + content)[:8],
		Path:        path,
		Anchor:      sha256Sum(string(base)),
		ContentHash: sha256Sum(content),
		BaseLines:   baseLines,
		Hunks:       lineDiff(baseLines, splitLines(content)),
		EndsNL:      strings.HasSuffix(content, "\n"),
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Proposal{}, fmt.Errorf("stage %s: %w", path, err)
	}
	data, err := json.Marshal(prop)
	if err != nil {
		return Proposal{}, fmt.Errorf("stage %s: %w", path, err)
	}
	if int64(len(data)) > maxProposalRecordBytes {
		return Proposal{}, fmt.Errorf("stage %s: proposal record is %d bytes; limit is %d", path, len(data), maxProposalRecordBytes)
	}
	if err := os.WriteFile(filepath.Join(s.dir, prop.ID+".json"), data, 0o600); err != nil {
		return Proposal{}, fmt.Errorf("stage %s: %w", path, err)
	}
	return prop, nil
}

// Load reads a staged proposal by ID.
func (s *Store) Load(id string) (Proposal, error) {
	data, err := readBoundedFile(filepath.Join(s.dir, id+".json"), maxProposalRecordBytes)
	if err != nil {
		return Proposal{}, fmt.Errorf("load proposal %s: %w", id, err)
	}
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return Proposal{}, fmt.Errorf("load proposal %s: %w", id, err)
	}
	return p, nil
}

// Accept applies every hunk to the real file. A stale base (content hash
// diverged from the anchor) aborts before touching anything.
func (s *Store) Accept(p Proposal) error {
	return s.accept(p, nil)
}

// AcceptVerified applies a proposal atomically only after the exact complete
// staged content passes an additional domain validator. It is used for files
// such as MAESTRO.md that must never be composed from partial hunk decisions.
func (s *Store) AcceptVerified(p Proposal, validate func([]byte) error) error {
	if validate == nil {
		return errors.New("accept verified: validator is required")
	}
	return s.accept(p, validate)
}

func (s *Store) accept(p Proposal, validate func([]byte) error) error {
	path, err := s.resolvePath(p.Path)
	if err != nil {
		return err
	}
	p.Path = path
	out, err := s.applyHunks(p, p.Hunks)
	if err != nil {
		return err
	}
	if validate != nil {
		content := []byte(joinLines(out, p.EndsNL))
		if len(p.ContentHash) != 64 || sha256Sum(string(content)) != p.ContentHash {
			return fmt.Errorf("accept %s: staged content integrity mismatch; restage the proposal", p.Path)
		}
		if err := validate(content); err != nil {
			return fmt.Errorf("accept %s: %w", p.Path, err)
		}
	}
	if err := s.writeFile(p, out); err != nil {
		return err
	}
	s.Discard(p)
	return nil
}

// applyHunks checks the anchor and produces the merged lines in one pass over
// the base. Rebuilding head+tail for every hunk made scattered edits O(H*N).
func (s *Store) applyHunks(p Proposal, hunks []Hunk) ([]string, error) {
	current, err := readBoundedFile(p.Path, maxProposalFileBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("accept %s: %w", p.Path, err)
	}
	if sha256Sum(string(current)) != p.Anchor {
		return nil, fmt.Errorf("accept %s: stale — file changed since staging (anchor mismatch)", p.Path)
	}
	capacity := len(p.BaseLines)
	for _, h := range hunks {
		capacity += len(h.NewLines) - len(h.OldLines)
	}
	if capacity < 0 {
		capacity = 0
	}
	out := make([]string, 0, capacity)
	cursor := 0
	for _, h := range hunks {
		start := h.Start - 1
		if start < cursor {
			return nil, fmt.Errorf("accept %s: hunk at line %d overlaps or is out of order", p.Path, h.Start)
		}
		if !linesMatch(p.BaseLines, start, h.OldLines) {
			return nil, fmt.Errorf("accept %s: hunk at line %d does not match base", p.Path, h.Start)
		}
		out = append(out, p.BaseLines[cursor:start]...)
		out = append(out, h.NewLines...)
		cursor = start + len(h.OldLines)
	}
	out = append(out, p.BaseLines[cursor:]...)
	return out, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file is %d bytes; limit is %d", info.Size(), limit)
	}
	f, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed type while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file is %d+ bytes; limit is %d", limit, limit)
	}
	return data, nil
}

// writeFile persists merged lines to the proposal's path.
func (s *Store) writeFile(p Proposal, out []string) error {
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("accept %s: %w", p.Path, err)
	}
	data := joinLines(out, p.EndsNL)
	tmp, err := os.CreateTemp(dir, ".maestro-accept-*")
	if err != nil {
		return fmt.Errorf("accept %s: %w", p.Path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		return fmt.Errorf("accept %s: %w", p.Path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("accept %s: %w", p.Path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("accept %s: %w", p.Path, err)
	}
	if err := os.Rename(tmpPath, p.Path); err != nil {
		return fmt.Errorf("accept %s: %w", p.Path, err)
	}
	return nil
}

// AcceptHunk applies a single hunk (index into p.Hunks) to the real file,
// then persists the proposal without it. Accepting every hunk removes the
// proposal file.
func (s *Store) AcceptHunk(p *Proposal, idx int) error {
	if p == nil {
		return errors.New("accept hunk: proposal is nil")
	}
	if filepath.Base(filepath.Clean(p.Path)) == "MAESTRO.md" {
		return errors.New("accept hunk: MAESTRO.md is an atomic contract; accept or decline the complete proposal")
	}
	if idx < 0 || idx >= len(p.Hunks) {
		return fmt.Errorf("accept hunk: index %d out of range", idx)
	}
	path, err := s.resolvePath(p.Path)
	if err != nil {
		return err
	}
	p.Path = path
	h := p.Hunks[idx]
	out, err := s.applyHunks(*p, []Hunk{h})
	if err != nil {
		return err
	}
	if err := s.writeFile(*p, out); err != nil {
		return err
	}
	// Remaining hunks were anchored to the original base. Rebase their line
	// positions and anchor onto the file we just wrote so sequential hunk
	// acceptance remains possible without weakening stale-file protection.
	delta := len(h.NewLines) - len(h.OldLines)
	for i := range p.Hunks {
		if i != idx && p.Hunks[i].Start > h.Start {
			p.Hunks[i].Start += delta
		}
	}
	p.Hunks = append(p.Hunks[:idx], p.Hunks[idx+1:]...)
	p.ContentHash = ""
	if len(p.Hunks) == 0 {
		s.Discard(*p)
		return nil
	}
	p.BaseLines = append([]string(nil), out...)
	p.Anchor = sha256Sum(joinLines(out, p.EndsNL))
	return s.persist(p)
}

// RejectHunk drops a single hunk from the proposal and persists the rest.
func (s *Store) RejectHunk(p *Proposal, idx int) error {
	if p == nil {
		return errors.New("reject hunk: proposal is nil")
	}
	if filepath.Base(filepath.Clean(p.Path)) == "MAESTRO.md" {
		return errors.New("reject hunk: MAESTRO.md is an atomic contract; accept or decline the complete proposal")
	}
	if idx < 0 || idx >= len(p.Hunks) {
		return fmt.Errorf("reject hunk: index %d out of range", idx)
	}
	p.Hunks = append(p.Hunks[:idx], p.Hunks[idx+1:]...)
	p.ContentHash = ""
	if len(p.Hunks) == 0 {
		s.Discard(*p)
		return nil
	}
	return s.persist(p)
}

// persist rewrites the proposal file.
func (s *Store) persist(p *Proposal) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxProposalRecordBytes {
		return fmt.Errorf("persist proposal %s: record is %d bytes; limit is %d", p.ID, len(data), maxProposalRecordBytes)
	}
	return os.WriteFile(filepath.Join(s.dir, p.ID+".json"), data, 0o600)
}

// Discard drops the staged proposal.
func (s *Store) Discard(p Proposal) {
	_ = os.Remove(filepath.Join(s.dir, p.ID+".json"))
}

// Pending lists the staged proposal IDs (for crash recovery).
func (s *Store) Pending() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return out, nil
}

// String renders the proposal for the card preview.
func (p Proposal) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Proposal %s → %s (%d hunk(s), anchor %s)\n", p.ID, p.Path, len(p.Hunks), p.Anchor[:12])
	for _, h := range p.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.Start, len(h.OldLines), h.Start, len(h.NewLines))
		for _, l := range h.OldLines {
			fmt.Fprintf(&b, "-%s\n", l)
		}
		for _, l := range h.NewLines {
			fmt.Fprintf(&b, "+%s\n", l)
		}
	}
	return b.String()
}

// maxLineDiffCells bounds the LCS workspace. Common prefixes and suffixes are
// removed first, so localized edits in very large files still get precise
// hunks. A wholesale rewrite falls back to one safe replacement hunk rather
// than allocating O(len(base)*len(target)) memory.
const maxLineDiffCells = 4 << 20

// lineDiff computes a minimal-ish line diff between base and target as
// hunks. Its auxiliary memory is bounded even for generated or minified files.
func lineDiff(base, target []string) []Hunk {
	prefix := 0
	for prefix < len(base) && prefix < len(target) && base[prefix] == target[prefix] {
		prefix++
	}
	if prefix == len(base) && prefix == len(target) {
		return nil
	}

	suffix := 0
	for suffix < len(base)-prefix && suffix < len(target)-prefix &&
		base[len(base)-1-suffix] == target[len(target)-1-suffix] {
		suffix++
	}
	base = base[prefix : len(base)-suffix]
	target = target[prefix : len(target)-suffix]
	n, m := len(base), len(target)
	rows, cols := n+1, m+1
	if cols != 0 && rows > maxLineDiffCells/cols {
		return []Hunk{{
			Start:    prefix + 1,
			OldLines: append([]string(nil), base...),
			NewLines: append([]string(nil), target...),
		}}
	}

	// A flat int32 table avoids one allocation per source line and halves the
	// space of []int on 64-bit systems. The configured cell cap keeps lengths
	// comfortably within int32.
	dp := make([]int32, rows*cols)
	for i := n - 1; i >= 0; i-- {
		row, next := i*cols, (i+1)*cols
		for j := m - 1; j >= 0; j-- {
			if base[i] == target[j] {
				dp[row+j] = dp[next+j+1] + 1
			} else if dp[next+j] >= dp[row+j+1] {
				dp[row+j] = dp[next+j]
			} else {
				dp[row+j] = dp[row+j+1]
			}
		}
	}
	var hunks []Hunk
	var cur *Hunk
	flush := func() {
		if cur != nil && (len(cur.OldLines) > 0 || len(cur.NewLines) > 0) {
			hunks = append(hunks, *cur)
		}
		cur = nil
	}
	i, j := 0, 0
	for i < n && j < m {
		if base[i] == target[j] {
			flush()
			i++
			j++
			continue
		}
		if cur == nil {
			cur = &Hunk{Start: prefix + i + 1}
		}
		if dp[(i+1)*cols+j] >= dp[i*cols+j+1] {
			cur.OldLines = append(cur.OldLines, base[i])
			i++
		} else {
			cur.NewLines = append(cur.NewLines, target[j])
			j++
		}
	}
	for ; i < n; i++ {
		if cur == nil {
			cur = &Hunk{Start: prefix + i + 1}
		}
		cur.OldLines = append(cur.OldLines, base[i])
	}
	for ; j < m; j++ {
		if cur == nil {
			cur = &Hunk{Start: prefix + n + 1}
		}
		cur.NewLines = append(cur.NewLines, target[j])
	}
	flush()
	return hunks
}

func linesMatch(lines []string, start int, want []string) bool {
	if start < 0 || start+len(want) > len(lines) {
		return false
	}
	for k := range want {
		if lines[start+k] != want[k] {
			return false
		}
	}
	return true
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinLines(lines []string, endsNL bool) string {
	out := strings.Join(lines, "\n")
	if endsNL {
		out += "\n"
	}
	return out
}

func (s *Store) resolvePath(path string) (string, error) {
	if s.root == nil {
		return filepath.Clean(path), nil
	}
	root := s.root()
	if root == "" {
		return "", errors.New("proposal workspace root is empty")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("proposal root: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("proposal root %s: %w", rootAbs, err)
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("proposal path %s: %w", path, err)
	}
	if !pathWithin(rootAbs, target) {
		return "", fmt.Errorf("proposal path %s escapes workspace %s", path, rootAbs)
	}
	ancestor := target
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("proposal path %s: %w", path, statErr)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("proposal path %s has no existing parent", path)
		}
		ancestor = parent
	}
	ancestorReal, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("proposal path %s: %w", path, err)
	}
	if !pathWithin(rootReal, ancestorReal) {
		return "", fmt.Errorf("proposal path %s escapes workspace through symlink", path)
	}
	return target, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sha256Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// StagingWriteTool is the write tool installed by the TUI: instead of
// touching the workspace, it stages a proposal that the human accepts or
// discards hunk-by-hunk (preview-then-accept, §5.1.1).
func StagingWriteTool(store *Store) agentcore.Tool {
	return agentcore.NewToolFunc(agentcore.ToolSpec{
		Name:          "write",
		Description:   "Write content to a file. The write is staged and must be accepted by the human.",
		InputSchema:   map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}},
		NeedsApproval: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if path == "" {
			return "", errors.New("write: path is required")
		}
		prop, err := store.Stage(path, content)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("staged %s → %s (%d hunk(s)) — accept or discard", prop.ID, path, len(prop.Hunks)), nil
	})
}
