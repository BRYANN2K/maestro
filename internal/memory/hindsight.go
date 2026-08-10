// Package memory implements Hindsight (F7, §11.1): decision memory written
// during the work (retain), re-read when relevant (recall), and synthesized
// at spec end (reflect). Scoped per project; complements RAG with the
// memory of choices.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Fact is one retained decision or observation.
type Fact struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Tags    []string `json:"tags,omitempty"`
	Created string   `json:"created"`
}

// Memory persists facts per project.
type Memory struct {
	dir string
}

// New builds a project-scoped memory at dir.
func New(dir string) *Memory { return &Memory{dir: dir} }

// Retain records a fact (append-only).
func (m *Memory) Retain(ctx context.Context, text string, tags []string) (Fact, error) {
	f := Fact{
		ID:      fmt.Sprintf("f%d", time.Now().UnixNano()),
		Text:    strings.TrimSpace(text),
		Tags:    tags,
		Created: time.Now().Format(time.RFC3339),
	}
	if f.Text == "" {
		return Fact{}, errors.New("retain: fact is required")
	}
	facts, err := m.load()
	if err != nil {
		return Fact{}, err
	}
	facts = append(facts, f)
	if err := m.save(facts); err != nil {
		return Fact{}, err
	}
	if err := m.appendMarkdown(f); err != nil {
		return Fact{}, err
	}
	return f, nil
}

// Recall returns the facts matching the query, best first (keyword match +
// recency).
func (m *Memory) Recall(ctx context.Context, query string, limit int) []Fact {
	facts, err := m.load()
	if err != nil {
		return nil
	}
	type scored struct {
		f     Fact
		score int
	}
	var out []scored
	q := strings.ToLower(query)
	for _, f := range facts {
		score := 0
		lower := strings.ToLower(f.Text)
		for _, word := range strings.Fields(q) {
			if strings.Contains(lower, word) {
				score += 10
			}
		}
		for _, tag := range f.Tags {
			if strings.Contains(q, strings.ToLower(tag)) {
				score += 5
			}
		}
		if score == 0 && q != "" {
			continue // recency alone never pulls in non-matches
		}
		// recency bonus: newer facts score higher
		if t, err := time.Parse(time.RFC3339, f.Created); err == nil {
			score += max(0, 3-int(time.Since(t).Hours()/24))
		}
		out = append(out, scored{f: f, score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].f.Created != out[j].f.Created {
			return out[i].f.Created > out[j].f.Created
		}
		return out[i].f.ID > out[j].f.ID
	})
	limit = min(limit, len(out))
	if limit < 0 {
		limit = 10
	}
	results := make([]Fact, 0, limit)
	for _, s := range out[:limit] {
		results = append(results, s.f)
	}
	return results
}

// Reflect synthesizes the facts into a reflection document, grouped by tag.
func (m *Memory) Reflect(ctx context.Context) (string, error) {
	facts, err := m.load()
	if err != nil {
		return "", err
	}
	if len(facts) == 0 {
		return "", errors.New("reflect: no facts retained yet")
	}
	groups := map[string][]Fact{}
	for _, f := range facts {
		tag := "general"
		if len(f.Tags) > 0 {
			tag = f.Tags[0]
		}
		groups[tag] = append(groups[tag], f)
	}
	var b strings.Builder
	b.WriteString("# Hindsight Reflection\n\n")
	b.WriteString(fmt.Sprintf("> Synthesized %s — %d facts\n\n", time.Now().Format("2006-01-02"), len(facts)))
	tags := make([]string, 0, len(groups))
	for t := range groups {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		fmt.Fprintf(&b, "## %s\n\n", tag)
		for _, f := range groups[tag] {
			fmt.Fprintf(&b, "- %s\n", f.Text)
		}
		b.WriteString("\n")
	}
	reflections := filepath.Join(m.dir, "reflections.md")
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(reflections, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return b.String(), nil
}

// All returns every fact, newest first.
func (m *Memory) All(ctx context.Context) []Fact {
	facts, err := m.load()
	if err != nil {
		return nil
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Created > facts[j].Created })
	return facts
}

func (m *Memory) load() ([]Fact, error) {
	data, err := os.ReadFile(filepath.Join(m.dir, "facts.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var facts []Fact
	if err := json.Unmarshal(data, &facts); err != nil {
		return nil, err
	}
	return facts, nil
}

func (m *Memory) save(facts []Fact) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dir, "facts.json"), data, 0o600)
}

func (m *Memory) appendMarkdown(f Fact) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	fh, err := os.OpenFile(filepath.Join(m.dir, "facts.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	tags := ""
	if len(f.Tags) > 0 {
		tags = " `[" + strings.Join(f.Tags, ",") + "]`"
	}
	_, err = fmt.Fprintf(fh, "- %s%s\n", f.Text, tags)
	return err
}
