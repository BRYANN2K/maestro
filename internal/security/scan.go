// Package security scans generated code for the 5 OWASP patterns that
// plague AI-written code (F5, §11.1): bundled secrets, missing RLS, auth
// without authz, exposed /debug routes, and wildcard CORS. Findings render
// as cards in /review — not a PDF report.
package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Severity levels.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Finding is one security issue.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Pattern  string `json:"pattern"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

// String renders the finding as a card line.
func (f Finding) String() string {
	return fmt.Sprintf("%s: %s (%s:%d)", f.Severity, f.Message, f.File, f.Line)
}

// ReadFile reads a file's content (injectable for tests).
type ReadFile func(path string) ([]byte, error)

// patterns is the 5-schema OWASP set.
var patterns = []struct {
	Name     string
	Severity string
	Re       *regexp.Regexp
	Check    func(path string, lines []string, i int, line string) *Finding
}{
	{
		Name: "bundled-secret", Severity: High,
		Re: regexp.MustCompile(`(?i)(sk-[a-z0-9]{16,}|ghp_[a-z0-9]{30,}|AKIA[0-9A-Z]{16}|xox[baprs]-[a-z0-9-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`),
	},
	{
		Name: "wildcard-cors", Severity: High,
		Re: regexp.MustCompile(`(?i)(access-control-allow-origin\s*[:=]\s*["']?\*["']?|alloworigins\(["']\*["']\)|allow_origins\s*=\s*["']\*["']|cors\s*\(\s*["']\*["']\s*\))`),
	},
	{
		Name: "debug-route", Severity: Medium,
		Re: regexp.MustCompile(`(?i)("/debug/?["'\s),]|mux\.handle\("/debug|router\.(get|post|route)\("/debug|/debug/pprof)`),
	},
	{
		Name: "auth-without-authz", Severity: Medium,
		Check: func(path string, lines []string, i int, line string) *Finding {
			low := strings.ToLower(line)
			authOnly := strings.Contains(low, "authenticate") || strings.Contains(low, "requireauth") || strings.Contains(low, "isauthenticated")
			authz := strings.Contains(low, "authorize") || strings.Contains(low, "requirerole") || strings.Contains(low, "permission") || strings.Contains(low, "hasrole") || strings.Contains(low, "isadmin")
			if authOnly && !authz {
				return &Finding{
					ID: "auth-without-authz", Severity: Medium, Pattern: "auth≠authz",
					File: path, Line: i + 1,
					Message: "authentication without authorization check on this line",
				}
			}
			return nil
		},
	},
	{
		Name: "missing-rls", Severity: Medium,
		Check: func(path string, lines []string, i int, line string) *Finding {
			// Heuristic: CREATE TABLE in a migration without RLS nearby.
			low := strings.ToLower(line)
			if strings.Contains(low, "create table") {
				// scan the next 15 lines for RLS
				for j := i; j < len(lines) && j < i+15; j++ {
					lj := strings.ToLower(lines[j])
					if strings.Contains(lj, "row level security") || strings.Contains(lj, "enable_row_level_security") || strings.Contains(lj, "rls") {
						return nil
					}
				}
				return &Finding{
					ID: "missing-rls", Severity: Medium, Pattern: "RLS",
					File: path, Line: i + 1,
					Message: "CREATE TABLE without row-level security in the same migration",
				}
			}
			return nil
		},
	},
}

// Scan checks every file in the diff for the 5 patterns.
func Scan(ctx context.Context, files []string, read ReadFile) ([]Finding, error) {
	if read == nil {
		read = func(path string) ([]byte, error) {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	var findings []Finding
	for _, path := range files {
		if skip(path) {
			continue
		}
		data, err := read(path)
		if err != nil {
			continue // deleted or unreadable file
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for i, line := range lines {
			for _, p := range patterns {
				if p.Re != nil && p.Re.MatchString(line) {
					findings = append(findings, Finding{
						ID: p.Name, Severity: p.Severity, Pattern: p.Name,
						File: path, Line: i + 1, Message: p.Name,
					})
					continue
				}
				if p.Check != nil {
					if f := p.Check(path, lines, i, line); f != nil {
						findings = append(findings, *f)
					}
				}
			}
		}
	}
	return findings, nil
}

// skip filters binary and vendored files.
func skip(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return true
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip", ".lock"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return strings.Contains(path, "vendor/") || strings.Contains(path, "node_modules/")
}

// SeverityRank orders severities for sorting.
func SeverityRank(s string) int {
	switch s {
	case High:
		return 0
	case Medium:
		return 1
	default:
		return 2
	}
}
