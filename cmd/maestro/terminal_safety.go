package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxCLIDiagnosticRunes = 4096
	maxCLIStreamRunes     = 8 << 20
	maxCLIStreamLineRunes = 64 << 10
)

// terminalSafeDiagnostic projects an untrusted error or warning onto one
// bounded terminal line. It preserves useful Unicode, replaces malformed
// UTF-8 and terminal controls, and prevents multiline diagnostic spoofing.
func terminalSafeDiagnostic(value string) string {
	const truncated = "… [diagnostic truncated]"
	var out strings.Builder
	out.Grow(min(len(value), maxCLIDiagnosticRunes))
	written := 0
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			r = utf8.RuneError
		}
		value = value[size:]
		switch r {
		case '\r', '\n', '\t':
			r = ' '
		default:
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				r = utf8.RuneError
			}
		}
		if written >= maxCLIDiagnosticRunes-len([]rune(truncated)) {
			out.WriteString(truncated)
			return strings.TrimSpace(out.String())
		}
		out.WriteRune(r)
		written++
	}
	return strings.TrimSpace(out.String())
}

func writeTerminalDiagnostic(out io.Writer, prefix, value string) {
	if out == nil {
		return
	}
	fmt.Fprintln(out, prefix+terminalSafeDiagnostic(value))
}

// terminalStreamProjection is a display-only boundary for untrusted model
// deltas. Canonical StreamEvents remain untouched. LF and TAB retain prose
// and code layout; all other terminal controls and malformed UTF-8 become
// replacement runes. Per-line and cumulative bounds cap hostile output even
// if a provider bypasses the upstream response budget.
type terminalStreamProjection struct {
	out             io.Writer
	totalRunes      int
	lineRunes       int
	lineTruncated   bool
	streamTruncated bool
}

func newTerminalStreamProjection(out io.Writer) *terminalStreamProjection {
	return &terminalStreamProjection{out: out}
}

func (p *terminalStreamProjection) WriteString(value string) {
	if p == nil || p.out == nil || p.streamTruncated {
		return
	}
	const (
		lineMarker   = "… [line truncated]"
		streamMarker = "\n… [stream output truncated]\n"
	)
	lineReserve := len([]rune(lineMarker))
	streamReserve := len([]rune(streamMarker))
	var projected strings.Builder
	projected.Grow(min(len(value), maxCLIStreamLineRunes))
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			r = utf8.RuneError
		}
		value = value[size:]

		if p.totalRunes >= maxCLIStreamRunes-streamReserve {
			projected.WriteString(streamMarker)
			p.streamTruncated = true
			break
		}
		if r == '\n' {
			projected.WriteByte('\n')
			p.totalRunes++
			p.lineRunes = 0
			p.lineTruncated = false
			continue
		}
		if p.lineTruncated {
			continue
		}
		if p.lineRunes >= maxCLIStreamLineRunes-lineReserve {
			projected.WriteString(lineMarker)
			p.totalRunes += lineReserve
			p.lineRunes += lineReserve
			p.lineTruncated = true
			continue
		}
		if r != '\t' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf)) {
			r = utf8.RuneError
		}
		projected.WriteRune(r)
		p.totalRunes++
		p.lineRunes++
	}
	_, _ = io.WriteString(p.out, projected.String())
}
