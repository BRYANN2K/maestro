package learn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SourceSnapshot is the only source representation passed beyond the read
// boundary. Content is normalized to LF; SHA256 fingerprints original bytes.
type SourceSnapshot struct {
	RelativePath string
	SHA256       string
	Language     string
	Content      []byte
	Lines        []string
}

var sensitiveContentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(?:sk|ghp|github_pat|xox[baprs])[-_][a-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
}

var deniedPathSegments = map[string]struct{}{
	".git": {}, "vendor": {}, "node_modules": {}, "__pycache__": {},
	".cache": {}, "cache": {}, ".gradle": {}, ".mypy_cache": {},
	".pytest_cache": {}, ".ruff_cache": {}, ".parcel-cache": {}, ".tox": {},
	".turbo": {}, "dist": {}, "build": {}, "out": {}, "bin": {}, "obj": {},
	"target": {}, "coverage": {}, ".next": {}, ".venv": {}, "venv": {},
	"generated": {}, "gen": {}, "tmp": {}, "temp": {}, ".terraform": {},
	".ssh": {}, ".aws": {}, ".gnupg": {}, ".kube": {}, ".docker": {},
}

var deniedExactNames = map[string]struct{}{
	".env": {}, ".netrc": {}, ".npmrc": {}, ".pypirc": {},
	".git-credentials": {}, ".htpasswd": {}, ".credentials": {},
	"id_rsa": {}, "id_dsa": {}, "id_ecdsa": {}, "id_ed25519": {},
	"kubeconfig": {}, "terraform.tfstate": {}, "credentials": {},
	"auth.json": {}, "credentials.json": {}, "service-account.json": {},
	"config.json": {}, "secrets.json": {}, "secrets.yaml": {}, "secrets.yml": {},
}

// ReadSource resolves path strictly within projectDir and returns bounded,
// validated source bytes. It refuses symlinks and all non-regular file kinds.
func ReadSource(ctx context.Context, projectDir, path string) (SourceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SourceSnapshot{}, err
	}
	root, err := canonicalRoot(projectDir)
	if err != nil {
		return SourceSnapshot{}, err
	}
	requested, err := sourcePath(root, projectDir, path)
	if err != nil {
		return SourceSnapshot{}, err
	}
	rel, err := filepath.Rel(root, requested)
	if err != nil || rel == "." {
		return SourceSnapshot{}, errors.New("learn source: a source file path is required")
	}
	rel = filepath.Clean(rel)
	if !validText(filepath.ToSlash(rel)) {
		return SourceSnapshot{}, errors.New("learn source: path contains unsafe text")
	}
	if reason := deniedSourcePath(rel); reason != "" {
		return SourceSnapshot{}, fmt.Errorf("learn source: refused sensitive or generated path (%s)", reason)
	}
	if err := rejectExistingSymlinks(root, requested); err != nil {
		return SourceSnapshot{}, fmt.Errorf("learn source: %w", err)
	}
	pre, err := os.Lstat(requested)
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("learn source: %w", err)
	}
	if pre.Mode()&os.ModeSymlink != 0 || !pre.Mode().IsRegular() {
		return SourceSnapshot{}, errors.New("learn source: path is not a regular file")
	}
	if pre.Mode().Perm()&0o111 != 0 {
		return SourceSnapshot{}, errors.New("learn source: executable files are refused")
	}
	if pre.Size() > MaxSourceBytes {
		return SourceSnapshot{}, fmt.Errorf("learn source: file exceeds %d bytes", MaxSourceBytes)
	}
	f, err := os.Open(requested)
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("learn source: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("learn source: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(pre, info) {
		return SourceSnapshot{}, errors.New("learn source: file changed during validation")
	}
	if info.Size() > MaxSourceBytes {
		return SourceSnapshot{}, fmt.Errorf("learn source: file exceeds %d bytes", MaxSourceBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("learn source: %w", err)
	}
	if int64(len(data)) > MaxSourceBytes {
		return SourceSnapshot{}, fmt.Errorf("learn source: file exceeds %d bytes", MaxSourceBytes)
	}
	if err := ctx.Err(); err != nil {
		return SourceSnapshot{}, err
	}
	if reason := classifySource(data); reason != "" {
		return SourceSnapshot{}, fmt.Errorf("learn source: %s", reason)
	}
	for _, pattern := range sensitiveContentPatterns {
		if pattern.Match(data) {
			return SourceSnapshot{}, errors.New("learn source: possible credential or private key content refused")
		}
	}
	sum := sha256.Sum256(data)
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	return SourceSnapshot{
		RelativePath: filepath.ToSlash(rel),
		SHA256:       hex.EncodeToString(sum[:]),
		Language:     languageForPath(rel),
		Content:      normalized,
		Lines:        splitSourceLines(string(normalized)),
	}, nil
}

// sourcePath maps an absolute path through the caller's spelling of the
// project root before using the canonical root. This matters on systems where
// temporary directories are reached through an OS alias (for example
// /var -> /private/var), without ever evaluating the source path itself.
func sourcePath(root, projectDir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("learn source: a source file path is required")
	}
	if !filepath.IsAbs(path) {
		candidate := filepath.Clean(filepath.Join(root, path))
		if !pathWithin(root, candidate) {
			return "", errors.New("learn source: path escapes active project root")
		}
		return candidate, nil
	}
	requested, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("learn source: resolve path: %w", err)
	}
	requested = filepath.Clean(requested)
	if pathWithin(root, requested) {
		return requested, nil
	}
	spelledRoot, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("learn source: resolve project root: %w", err)
	}
	spelledRoot = filepath.Clean(spelledRoot)
	if !pathWithin(spelledRoot, requested) {
		return "", errors.New("learn source: path escapes active project root")
	}
	rel, err := filepath.Rel(spelledRoot, requested)
	if err != nil {
		return "", errors.New("learn source: path escapes active project root")
	}
	candidate := filepath.Clean(filepath.Join(root, rel))
	if !pathWithin(root, candidate) {
		return "", errors.New("learn source: path escapes active project root")
	}
	return candidate, nil
}

func canonicalRoot(projectDir string) (string, error) {
	if strings.TrimSpace(projectDir) == "" {
		return "", errors.New("learn: project root is required")
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("learn: resolve project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("learn: resolve project root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", errors.New("learn: project root is not a directory")
	}
	return filepath.Clean(root), nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func rejectExistingSymlinks(root, target string) error {
	if !pathWithin(root, target) {
		return errors.New("path escapes project root")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink paths are refused")
		}
	}
	return nil
}

func deniedSourcePath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, raw := range parts {
		name := strings.ToLower(raw)
		if _, denied := deniedPathSegments[name]; denied {
			return name
		}
		if name == "maestro" && i+1 < len(parts) && strings.EqualFold(parts[i+1], "learn") {
			return "maestro/learn"
		}
	}
	base := strings.ToLower(parts[len(parts)-1])
	if _, denied := deniedExactNames[base]; denied || strings.HasPrefix(base, ".env.") {
		return base
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	for _, marker := range []string{"credential", "secret", "private_key", "private-key", "api_key", "api-key", "access_token", "access-token"} {
		if strings.Contains(stem, marker) {
			return marker
		}
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks", ".tfvars"} {
		if strings.HasSuffix(base, ext) {
			return ext
		}
	}
	return ""
}

func classifySource(data []byte) string {
	if hasExecutableSignature(data) {
		return "executable file content is refused"
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "binary file content is refused"
	}
	if !utf8.Valid(data) {
		return "source is not valid UTF-8"
	}
	for _, r := range string(data) {
		if unsafeControl(r) {
			return "source contains unsafe control characters"
		}
	}
	return ""
}

func unsafeControl(r rune) bool {
	if r == '\n' || r == '\r' || r == '\t' {
		return false
	}
	return r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x061c ||
		r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2066 && r <= 0x2069)
}

func validUTF8String(value string) bool { return utf8.ValidString(value) }

func hasExecutableSignature(data []byte) bool {
	if len(data) >= 4 {
		sig := [4]byte{data[0], data[1], data[2], data[3]}
		switch sig {
		case [4]byte{0x7f, 'E', 'L', 'F'},
			[4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xce, 0xfa, 0xed, 0xfe},
			[4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xcf, 0xfa, 0xed, 0xfe},
			[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca}:
			return true
		}
	}
	return len(data) >= 2 && data[0] == 'M' && data[1] == 'Z'
}

func splitSourceLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func languageForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".sql":
		return "sql"
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	default:
		return "text"
	}
}
