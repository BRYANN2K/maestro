package agentcore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ModelsDevOptions configures the remote catalog client (§10.2).
type ModelsDevOptions struct {
	URL       string        // default https://models.dev/api.json
	CachePath string        // default ~/.maestro/cache/models.json
	TTL       time.Duration // cache freshness, default 5m
	Refresh   time.Duration // background refresh interval, default 60m
	Disabled  bool          // MAESTRO_DISABLE_MODELS_FETCH
	// AsyncStartup lets interactive frontends use stale cache/core immediately
	// and refresh online after their first frame. Batch commands keep the
	// synchronous Load contract by leaving it false.
	AsyncStartup bool
	OnUpdate     func(map[string]CatalogProvider)
}

const (
	modelsDevURL        = "https://models.dev/api.json"
	modelsDevDefaultTTL = 5 * time.Minute
	modelsDevRefresh    = 60 * time.Minute
	modelsDevMaxBytes   = 16 << 20
)

// ModelsDev is the remote catalog client: cache-first, atomic writes,
// background refresh — the opencode pattern (models.dev) in Go.
type ModelsDev struct {
	opts  ModelsDevOptions
	httpc *http.Client
}

// NewModelsDev builds the client.
func NewModelsDev(opts ModelsDevOptions) *ModelsDev {
	if opts.URL == "" {
		opts.URL = modelsDevURL
	}
	if opts.TTL <= 0 {
		opts.TTL = modelsDevDefaultTTL
	}
	if opts.Refresh <= 0 {
		opts.Refresh = modelsDevRefresh
	}
	return &ModelsDev{opts: opts, httpc: &http.Client{Timeout: 10 * time.Second, Transport: providerTransport()}}
}

// Close releases the client's private idle connection pool. In-flight
// requests remain controlled by the contexts passed to Load or Refresh.
func (m *ModelsDev) Close() {
	if m != nil && m.httpc != nil {
		m.httpc.CloseIdleConnections()
	}
}

// DefaultCachePath returns the cache file path.
func DefaultCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".maestro", "cache", "models.json"), nil
}

// Load returns the catalog: disk cache when fresh, then network, then the
// embedded core snapshot.
func (m *ModelsDev) Load(ctx context.Context) (map[string]CatalogProvider, string, error) {
	if !m.opts.Disabled {
		if data, err := readModelsDevCache(m.opts.CachePath); err == nil {
			if fresh, err := cacheFresh(m.opts.CachePath, m.opts.TTL); err == nil && fresh {
				if providers, err := ParseCatalog(data); err == nil {
					return providers, "cache", nil
				}
			}
		}
		if providers, err := m.fetch(ctx); err == nil {
			return providers, "remote", nil
		}
		// Stale cache still beats nothing.
		if data, err := readModelsDevCache(m.opts.CachePath); err == nil {
			if providers, err := ParseCatalog(data); err == nil {
				return providers, "cache", nil
			}
		}
	}
	providers, err := coreCatalog()
	if err != nil {
		return nil, "", err
	}
	return providers, "core", nil
}

// LoadCached never accesses the network. A valid stale cache is preferable to
// blocking an interactive first frame; the caller may Refresh in background.
func (m *ModelsDev) LoadCached() (map[string]CatalogProvider, string, error) {
	if !m.opts.Disabled {
		if data, err := readModelsDevCache(m.opts.CachePath); err == nil {
			if providers, err := ParseCatalog(data); err == nil {
				return providers, "cache", nil
			}
		}
	}
	providers, err := coreCatalog()
	if err != nil {
		return nil, "", err
	}
	return providers, "core", nil
}

// AsyncStartup reports whether the owner should refresh after initialization.
func (m *ModelsDev) AsyncStartup() bool { return m != nil && m.opts.AsyncStartup }

// RefreshInterval is the cadence for a long-lived owner's background loop.
func (m *ModelsDev) RefreshInterval() time.Duration {
	if m == nil {
		return modelsDevRefresh
	}
	return m.opts.Refresh
}

// Refresh fetches the remote catalog immediately, bypassing the TTL cache.
// Disabled clients fall back to the embedded snapshot.
func (m *ModelsDev) Refresh(ctx context.Context) (map[string]CatalogProvider, error) {
	if m.opts.Disabled {
		return coreCatalog()
	}
	return m.fetch(ctx)
}

// fetch pulls the remote catalog and writes the atomic cache.
func (m *ModelsDev) fetch(ctx context.Context) (map[string]CatalogProvider, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.opts.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "maestro/"+Version)
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", m.opts.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", m.opts.URL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, modelsDevMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > modelsDevMaxBytes {
		return nil, fmt.Errorf("fetch %s: response exceeds %d bytes", m.opts.URL, modelsDevMaxBytes)
	}
	providers, err := ParseCatalog(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.opts.URL, err)
	}
	if m.opts.CachePath != "" {
		if err := writeAtomic(m.opts.CachePath, data, 0o644); err == nil {
			_ = os.Chtimes(m.opts.CachePath, time.Now(), time.Now())
		}
	}
	return providers, nil
}

func readModelsDevCache(path string) ([]byte, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Size() > modelsDevMaxBytes {
		return nil, fmt.Errorf("models cache is not a regular file within the %d-byte limit", modelsDevMaxBytes)
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
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) || openedInfo.Size() > modelsDevMaxBytes {
		return nil, fmt.Errorf("models cache changed or exceeds the %d-byte limit", modelsDevMaxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, modelsDevMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > modelsDevMaxBytes {
		return nil, fmt.Errorf("models cache exceeds %d bytes", modelsDevMaxBytes)
	}
	return data, nil
}

// StartRefresh refreshes the catalog in the background.
func (m *ModelsDev) StartRefresh(ctx context.Context) {
	if m.opts.Disabled {
		return
	}
	safeGo("modelsdev refresh", func() {
		ticker := time.NewTicker(m.opts.Refresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if providers, err := m.fetch(ctx); err == nil && m.opts.OnUpdate != nil {
					m.opts.OnUpdate(providers)
				}
			}
		}
	})
}

// Version is set by the command at startup and defaults to an honest local
// build marker for library users.
var Version = "dev"

// cacheFresh reports whether the cache file is younger than ttl.
func cacheFresh(path string, ttl time.Duration) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return time.Since(fi.ModTime()) < ttl, nil
}

// writeAtomic writes via temp + rename.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
