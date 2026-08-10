package learn

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Format renders validated explanation data as bounded, injection-resistant
// Markdown. Code fences grow beyond any backtick run in the source excerpt.
func Format(e Explanation) string {
	var b strings.Builder
	path := safeArtifactPath(e.Path)
	fmt.Fprintf(&b, "# Learn: %s\n\n", escapeMarkdown(path))
	if e.SourceSHA256 != "" {
		fmt.Fprintf(&b, "Source SHA-256: %s\n\n", escapeMarkdown(e.SourceSHA256))
	}
	if e.HighLevel != "" {
		fmt.Fprintf(&b, "## Start here\n\n%s\n\n", escapeMarkdown(e.HighLevel))
	}
	for i, block := range e.Blocks {
		title := block.Code
		if first, _, ok := strings.Cut(title, "\n"); ok {
			title = first
		}
		title = truncateRunes(title, 40)
		fmt.Fprintf(&b, "## %d. Lines %d-%d — %s\n", i+1, block.Start, block.End, escapeMarkdown(title))
		if block.Code != "" {
			fence := safeFence(block.Code)
			language := safeLanguage(e.Language)
			fmt.Fprintf(&b, "%s%s\n%s\n%s\n\n", fence, language, block.Code, fence)
		}
		if block.What != "" {
			fmt.Fprintf(&b, "**Purpose:** %s\n\n", escapeMarkdown(block.What))
		}
		if block.Trap != "" {
			fmt.Fprintf(&b, "**Watch for:** %s\n\n", escapeMarkdown(block.Trap))
		}
		if block.Caution != "" {
			fmt.Fprintf(&b, "**Caution:** %s\n\n", escapeMarkdown(block.Caution))
		}
		if i < len(e.Blocks)-1 {
			b.WriteString("---\n\n")
		}
	}
	if len(e.FollowUps) > 0 {
		b.WriteString("## Next\n\n")
		for _, question := range e.FollowUps {
			fmt.Fprintf(&b, "- %s\n", escapeMarkdown(question))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func safeArtifactPath(path string) string {
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		path = filepath.Base(path)
	}
	path = filepath.ToSlash(path)
	for strings.HasPrefix(path, "../") {
		path = strings.TrimPrefix(path, "../")
	}
	if path == "." || path == "" || !validText(path) {
		return "source"
	}
	return path
}

func safeLanguage(language string) string {
	for _, r := range language {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "text"
		}
	}
	if language == "" {
		return "text"
	}
	return language
}

func safeFence(code string) string {
	longest, current := 0, 0
	for _, r := range code {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func escapeMarkdown(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_",
		"[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;",
		"#", "\\#", "|", "\\|",
	)
	return replacer.Replace(value)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a path into a filesystem-safe name. Long names retain a hash
// suffix so distinct paths cannot silently overwrite each other.
func Slugify(path string) string {
	original := filepath.ToSlash(path)
	slug := slugRe.ReplaceAllString(strings.ToLower(original), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "learn"
	}
	if len(slug) > 60 {
		sum := sha256.Sum256([]byte(original))
		suffix := hex.EncodeToString(sum[:4])
		slug = strings.TrimRight(slug[:51], "-") + "-" + suffix
	}
	return slug
}
