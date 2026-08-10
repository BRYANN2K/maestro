package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxTitleRunes = 72

// Summary is safe to render in a picker. ID remains the authoritative
// selector; DisplayTitle is disambiguated without changing persisted titles.
type Summary struct {
	ID             string
	Title          string
	DisplayTitle   string
	Phase          Phase
	Updated        string
	WorkspaceRef   string
	Worktree       string
	Disabled       bool
	DisabledReason string
}

// NormalizeTitle turns arbitrary text into one safe, bounded display line.
// It deliberately removes format controls (including bidi overrides) rather
// than allowing invisible direction changes in terminal pickers.
func NormalizeTitle(value string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(value, "") {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			if unicode.IsSpace(r) {
				space = b.Len() > 0
			}
			continue
		}
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return truncateTitle(strings.TrimSpace(b.String()), MaxTitleRunes)
}

// FallbackTitle derives deterministic metadata from the first meaningful
// user turn. The seed hashes the complete normalized input, not the truncated
// display title, so two long prompts with the same prefix do not share a CAS.
func FallbackTitle(value string) (title, seed string, ok bool) {
	normalized := normalizeSeed(value)
	if !meaningful(normalized) {
		return "", "", false
	}
	cleaned := strings.TrimLeft(normalized, "#>*-_`[]()! ")
	if meaningful(cleaned) {
		normalized = cleaned
	}
	for _, delimiter := range []string{". ", "? ", "! ", "; "} {
		if index := strings.Index(normalized, delimiter); index > 0 {
			normalized = normalized[:index+1]
			break
		}
	}
	title = NormalizeTitle(normalized)
	if !meaningful(title) {
		return "", "", false
	}
	sum := sha256.Sum256([]byte(normalizeSeed(value)))
	return title, hex.EncodeToString(sum[:16]), true
}

func normalizeSeed(value string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(value, "") {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func meaningful(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func truncateTitle(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	cut := limit - 1
	if cut <= 0 {
		return ""
	}
	for i := cut; i > limit/2; i-- {
		if unicode.IsSpace(runes[i-1]) {
			cut = i - 1
			break
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

// CompareAndSwapTitle updates a title only while its generation seed and
// provenance still match. It is the durable guard against a late model result
// overwriting a rename or a different generation attempt.
func (s *Store) CompareAndSwapTitle(ctx context.Context, project, id, expectedSeed string, expectedSource TitleSource, title string, source TitleSource) (Session, bool, error) {
	if source != TitleSourceLLM && source != TitleSourceUser && source != TitleSourceFallback {
		return Session{}, false, fmt.Errorf("update session title: invalid source %q", source)
	}
	title = NormalizeTitle(title)
	if !meaningful(title) {
		return Session{}, false, errors.New("update session title: title is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var updated Session
	var swapped bool
	err := s.withRecordLock(ctx, project, id, func() error {
		sess, err := s.loadLocked(ctx, project, id)
		if err != nil {
			return err
		}
		if sess.TitleSeedHash != expectedSeed || sess.TitleSource != expectedSource {
			updated = sess
			return nil
		}
		sess.Title = title
		sess.TitleSource = source
		if source == TitleSourceUser {
			sess.TitleSeedHash = ""
		}
		if _, err := s.saveExactLocked(ctx, sess); err != nil {
			return err
		}
		updated, err = s.loadLocked(ctx, project, id)
		swapped = err == nil
		return err
	})
	return updated, swapped, err
}

// SetUserTitle updates only user-owned title metadata while preserving the
// latest lifecycle state written by another Maestro process.
func (s *Store) SetUserTitle(ctx context.Context, project, id, title string) (Session, error) {
	title = NormalizeTitle(title)
	if !meaningful(title) {
		return Session{}, errors.New("update session title: title is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var updated Session
	err := s.withRecordLock(ctx, project, id, func() error {
		sess, err := s.loadLocked(ctx, project, id)
		if err != nil {
			return err
		}
		sess.Title = title
		sess.TitleSource = TitleSourceUser
		sess.TitleSeedHash = ""
		if _, err := s.saveExactLocked(ctx, sess); err != nil {
			return err
		}
		updated, err = s.loadLocked(ctx, project, id)
		return err
	})
	return updated, err
}

// ListSummaries reads actual session metadata and orders it by Updated. A
// corrupt record is retained as a disabled row so users can diagnose it
// without a terminal picker rendering untrusted bytes.
func (s *Store) ListSummaries(ctx context.Context, project string) ([]Summary, error) {
	if !validComponent(project) {
		return nil, errors.New("list session summaries: invalid project")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dir, project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list session summaries: %w", err)
	}
	type row struct {
		summary Summary
		when    time.Time
	}
	rows := make([]row, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		info, _ := entry.Info()
		when := time.Time{}
		if info != nil {
			when = info.ModTime()
		}
		sess, loadErr := s.loadLocked(ctx, project, id)
		if loadErr != nil {
			reason := NormalizeTitle(loadErr.Error())
			rows = append(rows, row{summary: Summary{
				ID: id, Title: "Unavailable session", DisplayTitle: "Unavailable session",
				Disabled: true, DisabledReason: reason,
			}, when: when})
			continue
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, sess.Updated); parseErr == nil {
			when = parsed
		} else if parsed, parseErr := time.Parse(time.RFC3339, sess.Updated); parseErr == nil {
			when = parsed
		}
		title := NormalizeTitle(sess.Title)
		if title == "" {
			title = "Untitled · " + shortID(sess.ID)
		}
		disabledReason := ""
		if !sess.Phase.Valid() {
			disabledReason = fmt.Sprintf("unknown phase %q", NormalizeTitle(string(sess.Phase)))
		}
		rows = append(rows, row{summary: Summary{
			ID: sess.ID, Title: title, DisplayTitle: title, Phase: sess.Phase,
			Updated: sess.Updated, WorkspaceRef: pickerSafe(sess.WorkspaceRef),
			Worktree: pickerSafe(sess.Worktree), Disabled: disabledReason != "",
			DisabledReason: disabledReason,
		}, when: when})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].when.Equal(rows[j].when) {
			return rows[i].summary.ID > rows[j].summary.ID
		}
		return rows[i].when.After(rows[j].when)
	})
	counts := map[string]int{}
	for _, item := range rows {
		counts[displayKey(item.summary.Title)]++
	}
	out := make([]Summary, len(rows))
	for i, item := range rows {
		out[i] = item.summary
		if counts[displayKey(item.summary.Title)] > 1 {
			out[i].DisplayTitle = item.summary.Title + " · " + shortID(item.summary.ID)
		}
	}
	return out, nil
}

func displayKey(value string) string { return strings.ToLower(NormalizeTitle(value)) }

func shortID(id string) string {
	runes := []rune(NormalizeTitle(id))
	if len(runes) > 6 {
		runes = runes[len(runes)-6:]
	}
	return string(runes)
}

func pickerSafe(value string) string {
	value = strings.ToValidUTF8(value, "")
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SetActive atomically records the session selected for the next process.
func (s *Store) SetActive(ctx context.Context, project, id string) error {
	if !validComponent(project) || !validComponent(id) {
		return errors.New("set active session: invalid project or id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadLocked(ctx, project, id); err != nil {
		return fmt.Errorf("set active session: %w", err)
	}
	dir := filepath.Join(s.dir, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("set active session: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, "active"), []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("set active session: %w", err)
	}
	return nil
}

// Active returns the durable selected session ID.
func (s *Store) Active(ctx context.Context, project string) (string, error) {
	if !validComponent(project) {
		return "", errors.New("active session: invalid project")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, project, "active"))
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(string(data), "\n")
	if !validComponent(id) || strings.Contains(id, "\n") {
		return "", errors.New("active session: invalid pointer")
	}
	return id, nil
}

// Delete removes exactly one session record. It is used only to roll back a
// fresh workspace session that was never published in memory.
func (s *Store) Delete(ctx context.Context, project, id string) error {
	if !validComponent(project) || !validComponent(id) {
		return errors.New("delete session: invalid project or id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.dir, project, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
