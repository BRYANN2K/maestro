package tui

import (
	"strings"
	"unicode/utf8"
)

// safeIDEPlainText projects untrusted, unstyled IDE labels to terminal-safe
// Unicode. It is deliberately not a generic output sanitizer: applying it to
// strings already styled by lipgloss would destroy Maestro's own ANSI.
//
// POSIX permits control bytes and malformed UTF-8 in file names. C0 controls
// are shown with Unicode Control Pictures, DEL with its control picture, and
// C1/malformed input with U+FFFD. The projection is one rune per source rune
// (or malformed byte), preserving editor selection and cursor coordinates.
func safeIDEPlainText(s string) string {
	if s == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			r = utf8.RuneError
		}
		s = s[size:]
		switch {
		case r < 0x20:
			r = 0x2400 + r
		case r == 0x7f:
			r = 0x2421
		case r >= 0x80 && r <= 0x9f:
			r = utf8.RuneError
		case isIDEBidiFormatControl(r):
			r = utf8.RuneError
		}
		out.WriteRune(r)
	}
	return out.String()
}

func isIDEBidiFormatControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

func truncateIDEPlainText(s string, width int) string {
	return truncateRunes(safeIDEPlainText(s), width)
}

// safeIDEPlainMultilineText preserves structural newlines while applying the
// plain-text projection to every rendered line. Markdown previews use this
// before glamour so raw file bytes can never be mistaken for renderer ANSI.
func safeIDEPlainMultilineText(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = safeIDEPlainText(lines[i])
	}
	return strings.Join(lines, "\n")
}
