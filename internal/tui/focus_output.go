package tui

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// focusOutputMarkdown gives predictable outcome/state labels visual weight
// without changing the persisted transcript. Code fences are byte-preserved;
// only literal labels at the start of a prose line are decorated.
func focusOutputMarkdown(value string) string {
	lines := strings.Split(value, "\n")
	fenceMarker := byte(0)
	fenceWidth := 0
	for i, line := range lines {
		if marker, width, rest, ok := markdownFenceRun(line); ok {
			if fenceMarker == 0 {
				// CommonMark forbids backticks in the info string of a
				// backtick fence. Treating such a line as prose keeps our
				// protection aligned with the renderer.
				if marker != '`' || !strings.Contains(rest, "`") {
					fenceMarker, fenceWidth = marker, width
				}
			} else if marker == fenceMarker && width >= fenceWidth && strings.TrimSpace(rest) == "" {
				fenceMarker, fenceWidth = 0, 0
			}
			continue
		}
		if fenceMarker != 0 || line != strings.TrimLeft(line, " \t") {
			continue
		}
		for _, label := range []string{"Done:", "State:", "Blocked:", "Cause:", "Fix:", "Next:"} {
			if strings.HasPrefix(line, label) {
				lines[i] = "**" + strings.TrimSuffix(label, ":") + ":**" + strings.TrimPrefix(line, label)
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// terminalSafeMarkdownText projects untrusted model text before it reaches a
// Markdown/ANSI renderer. Newlines and tabs remain structural; terminal and
// bidi controls become visible inert Unicode. The stored Message.Text is not
// changed.
func terminalSafeMarkdownText(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			r = utf8.RuneError
		}
		value = value[size:]
		switch {
		case r == '\n' || r == '\t':
			out.WriteRune(r)
		case r < 0x20:
			out.WriteRune(0x2400 + r)
		case r == 0x7f:
			out.WriteRune(0x2421)
		case r >= 0x80 && r <= 0x9f:
			out.WriteRune(utf8.RuneError)
		case isIDEBidiFormatControl(r):
			out.WriteRune(utf8.RuneError)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// markdownFenceRun recognizes a CommonMark fence delimiter with zero to
// three leading spaces. It returns the complete delimiter width so a shorter
// run inside a longer fence cannot close the block during streaming renders.
func markdownFenceRun(line string) (marker byte, width int, rest string, ok bool) {
	start := 0
	for start < len(line) && start < 3 && line[start] == ' ' {
		start++
	}
	if start >= len(line) || (line[start] != '`' && line[start] != '~') {
		return 0, 0, "", false
	}
	marker = line[start]
	end := start
	for end < len(line) && line[end] == marker {
		end++
	}
	if end-start < 3 {
		return 0, 0, "", false
	}
	return marker, end - start, line[end:], true
}

// focusLearningError formats only failures whose cause implies a concrete,
// safe recovery action. Unknown provider or system failures stay untouched so
// the UI never invents a fix. This is a presentation helper, not an error or
// protocol transformation.
func focusLearningError(err error) (string, bool) {
	if err == nil || errors.Is(err, context.Canceled) {
		return "", false
	}
	cause := safeIDEPlainText(strings.TrimSpace(err.Error()))
	lower := strings.ToLower(cause)
	var fix string
	switch {
	case strings.HasPrefix(lower, "learn source:"),
		strings.Contains(lower, "a source file path is required"),
		strings.Contains(lower, "path escapes active project root"):
		fix = "Choose a readable, non-sensitive UTF-8 source file inside the active project, then retry /learn <path>."
	case strings.HasPrefix(lower, "learn response:"),
		strings.Contains(lower, "model json"),
		strings.Contains(lower, "explainer did not complete successfully"):
		fix = "Retry /learn once; if it repeats, choose a smaller file or another configured model."
	case strings.Contains(lower, "learn: no explainer configured"):
		fix = "Configure a working model provider, then retry /learn <path>."
	case strings.HasPrefix(lower, "learn runner:") && strings.Contains(lower, "cannot confine embedded source access"):
		fix = "Choose a native/API model for Maestro, then retry /learn <path>."
	case strings.HasPrefix(lower, "learn runner:"):
		fix = "Configure an available native/API model for Maestro, then retry /learn <path>."
	case strings.HasPrefix(lower, "learn proposal:"):
		fix = "Verify the project and Maestro proposal directories are writable, then retry /learn <path>."
	case strings.HasPrefix(lower, "coach: no pending lesson"),
		strings.Contains(lower, "explicitly offered lesson"):
		fix = "Run /learn next, complete the offered exercise, then mark that lesson done."
	case strings.HasPrefix(lower, "coach: invalid mode"),
		strings.HasPrefix(lower, "unknown coach action"):
		fix = "Open /learn and choose Guided, Challenge, or Off."
	default:
		return "", false
	}
	return "Cause: " + cause + "\nFix: " + fix, true
}
