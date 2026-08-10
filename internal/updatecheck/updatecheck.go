// Package updatecheck provides Maestro's bounded, read-only release check.
// It reads the public npm latest dist-tag published by the release workflow;
// it never talks to GitHub Actions and never receives CI credentials.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// LatestEndpoint is public and contains only the npm package metadata for
	// the release workflow's stable dist-tag.
	LatestEndpoint = "https://registry.npmjs.org/%40bryann2k%2Fmaestro/latest"
	ReleasePage    = "https://www.npmjs.com/package/@bryann2k/maestro"
	InstallCommand = "npm install -g @bryann2k/maestro@latest"

	DefaultTTL     = 24 * time.Hour
	DefaultTimeout = 5 * time.Second
	maxResponse    = 64 << 10
	maxCache       = 4 << 10
)

// Result is safe to retain in the TUI. Versions have already passed strict
// semantic-version parsing and therefore contain no terminal controls.
type Result struct {
	Current     string
	Latest      string
	Available   bool
	CheckedAt   time.Time
	FromCache   bool
	ReleasePage string
}

// Options supplies release and persistence boundaries. Endpoint and Client
// are injectable for deterministic tests; production uses the fixed public
// npm endpoint and a redirect-restricted client.
type Options struct {
	CurrentVersion string
	Endpoint       string
	CachePath      string
	TTL            time.Duration
	Timeout        time.Duration
	Client         *http.Client
	Now            func() time.Time
}

// Checker performs cached release checks.
type Checker struct {
	current   version
	currentID string
	endpoint  *url.URL
	cachePath string
	ttl       time.Duration
	timeout   time.Duration
	client    *http.Client
	now       func() time.Time
}

type cacheRecord struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	Failed    bool      `json:"failed,omitempty"`
}

type npmLatest struct {
	Version string `json:"version"`
}

// New validates every static boundary before a network request can be made.
func New(opts Options) (*Checker, error) {
	currentID := strings.TrimSpace(strings.TrimPrefix(opts.CurrentVersion, "v"))
	current, err := parseVersion(currentID)
	if err != nil {
		return nil, fmt.Errorf("current version: %w", err)
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = LatestEndpoint
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Host == "" || (parsedEndpoint.Scheme != "https" && parsedEndpoint.Scheme != "http") {
		return nil, errors.New("update endpoint must be an absolute HTTP(S) URL")
	}
	if parsedEndpoint.Scheme == "http" && parsedEndpoint.Hostname() != "localhost" && parsedEndpoint.Hostname() != "127.0.0.1" && parsedEndpoint.Hostname() != "::1" {
		return nil, errors.New("update endpoint must use HTTPS")
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	client := opts.Client
	if client == nil {
		allowedScheme, allowedHost := parsedEndpoint.Scheme, parsedEndpoint.Host
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("too many update redirects")
				}
				if req.URL.Scheme != allowedScheme || req.URL.Host != allowedHost {
					return errors.New("update redirect left the trusted registry")
				}
				return nil
			},
		}
	}
	return &Checker{
		current: current, currentID: currentID, endpoint: parsedEndpoint,
		cachePath: opts.CachePath, ttl: ttl, timeout: timeout, client: client, now: now,
	}, nil
}

// DefaultCachePath returns the per-user, non-project cache location.
func DefaultCachePath() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if base == "" {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve user cache: %w", err)
		}
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("user cache directory must be absolute")
	}
	return filepath.Join(base, "maestro", "update.json"), nil
}

// Check returns cached data younger than the configured TTL unless force is
// true. Cache corruption is ignored and repaired from the trusted endpoint.
func (c *Checker) Check(ctx context.Context, force bool) (Result, error) {
	now := c.now().UTC()
	previous, previousErr := readCache(c.cachePath)
	if !force {
		if previousErr == nil && now.Sub(previous.CheckedAt) >= 0 && now.Sub(previous.CheckedAt) < c.ttl {
			latest, parseErr := parseVersion(previous.Latest)
			if parseErr == nil {
				return c.result(previous.Latest, latest, previous.CheckedAt, true), nil
			}
			if previous.Failed {
				return Result{}, errors.New("check updates: recent registry check failed; use /update to retry")
			}
		}
	}
	latestID, latest, err := c.fetchLatest(ctx)
	if err != nil {
		failed := cacheRecord{CheckedAt: now, Failed: true}
		if previousErr == nil {
			failed.Latest = previous.Latest
		}
		_ = writeCache(c.cachePath, failed)
		// An automatic refresh may keep a previously validated release result;
		// an explicit /update always reports the fresh failure.
		if !force && previousErr == nil {
			if previousLatest, parseErr := parseVersion(previous.Latest); parseErr == nil {
				return c.result(previous.Latest, previousLatest, previous.CheckedAt, true), nil
			}
		}
		return Result{}, err
	}
	_ = writeCache(c.cachePath, cacheRecord{CheckedAt: now, Latest: latestID})
	return c.result(latestID, latest, now, false), nil
}

func (c *Checker) fetchLatest(ctx context.Context) (string, version, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.endpoint.String(), nil)
	if err != nil {
		return "", version{}, fmt.Errorf("check updates: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Keep the request deliberately generic: the registry sees no project,
	// session, installation identifier, or locally installed version.
	req.Header.Set("User-Agent", "maestro-update-check")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", version{}, fmt.Errorf("check updates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return "", version{}, fmt.Errorf("check updates: registry returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return "", version{}, fmt.Errorf("check updates: read registry response: %w", err)
	}
	if len(data) > maxResponse {
		return "", version{}, errors.New("check updates: registry response exceeded 64 KiB")
	}
	var payload npmLatest
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", version{}, errors.New("check updates: registry returned invalid JSON")
	}
	latestID := strings.TrimSpace(strings.TrimPrefix(payload.Version, "v"))
	latest, err := parseVersion(latestID)
	if err != nil {
		return "", version{}, fmt.Errorf("check updates: registry version: %w", err)
	}
	return latestID, latest, nil
}

func (c *Checker) result(latestID string, latest version, checkedAt time.Time, cached bool) Result {
	return Result{
		Current: c.currentID, Latest: latestID,
		Available: c.current.compare(latest) < 0,
		CheckedAt: checkedAt, FromCache: cached, ReleasePage: ReleasePage,
	}
}

func readCache(path string) (cacheRecord, error) {
	if path == "" {
		return cacheRecord{}, os.ErrNotExist
	}
	info, err := os.Lstat(path)
	if err != nil {
		return cacheRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCache {
		return cacheRecord{}, errors.New("update cache is not a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheRecord{}, err
	}
	var record cacheRecord
	if err := json.Unmarshal(data, &record); err != nil || record.CheckedAt.IsZero() {
		return cacheRecord{}, errors.New("update cache is invalid")
	}
	return record, nil
}

func writeCache(path string, record cacheRecord) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".update-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("refusing to replace non-regular update cache")
	}
	return os.Rename(name, path)
}

type version struct {
	major, minor, patch uint64
	pre                 []string
}

func parseVersion(raw string) (version, error) {
	if raw == "" || len(raw) > 64 || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return version{}, errors.New("invalid semantic version")
	}
	versionParts := strings.Split(raw, "+")
	if len(versionParts) > 2 {
		return version{}, errors.New("invalid semantic version build metadata")
	}
	withoutBuild := versionParts[0]
	if len(versionParts) == 2 {
		if err := validateIdentifiers(versionParts[1], true); err != nil {
			return version{}, fmt.Errorf("invalid semantic version build metadata: %w", err)
		}
	}
	parts := strings.SplitN(withoutBuild, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return version{}, errors.New("semantic version must have major.minor.patch")
	}
	numbers := make([]uint64, 3)
	for i, part := range core {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version{}, errors.New("invalid semantic version number")
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return version{}, errors.New("invalid semantic version number")
			}
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return version{}, errors.New("semantic version number is too large")
		}
		numbers[i] = n
	}
	v := version{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return version{}, errors.New("empty semantic version prerelease")
		}
		if err := validateIdentifiers(parts[1], false); err != nil {
			return version{}, fmt.Errorf("invalid semantic version prerelease: %w", err)
		}
		v.pre = strings.Split(parts[1], ".")
	}
	return v, nil
}

func (v version) compare(other version) int {
	for _, pair := range [][2]uint64{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(v.pre) == 0 && len(other.pre) == 0 {
		return 0
	}
	if len(v.pre) == 0 {
		return 1
	}
	if len(other.pre) == 0 {
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(other.pre); i++ {
		left, right := v.pre[i], other.pre[i]
		if left == right {
			continue
		}
		leftNumeric, rightNumeric := numeric(left), numeric(right)
		switch {
		case leftNumeric && rightNumeric:
			// Numeric prerelease identifiers are unbounded by SemVer. Compare
			// their canonical decimal strings instead of overflowing uint64.
			if len(left) < len(right) || (len(left) == len(right) && left < right) {
				return -1
			}
			return 1
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case left < right:
			return -1
		default:
			return 1
		}
	}
	if len(v.pre) < len(other.pre) {
		return -1
	}
	if len(v.pre) > len(other.pre) {
		return 1
	}
	return 0
}

func validateIdentifiers(raw string, allowLeadingZero bool) error {
	if raw == "" {
		return errors.New("identifier is empty")
	}
	for _, identifier := range strings.Split(raw, ".") {
		if identifier == "" {
			return errors.New("identifier is empty")
		}
		for _, r := range identifier {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return errors.New("identifier contains an invalid character")
			}
		}
		if !allowLeadingZero && numeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return errors.New("numeric identifier has a leading zero")
		}
	}
	return nil
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
