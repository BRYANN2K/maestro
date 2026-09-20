package projectprofile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileCacheCoalescesScansAndCandidateReads(t *testing.T) {
	cache := newProfileCache()
	const callers = 32
	const candidateReadsPerDiscovery = 12
	var scans atomic.Int64
	var candidateReads atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	start := make(chan struct{})

	profile := ProjectProfile{
		SchemaVersion: SchemaVersion,
		Mode:          ModeBrownfield,
		Root:          "/cache-test",
		Name:          "coalesced",
		Stacks:        []string{"go"},
		Units:         []Unit{{Path: ".", Stacks: []string{"go"}, Manifests: []string{"go.mod"}}},
		Unknowns:      []string{"purpose"},
	}
	resolver := func(_ context.Context, cached *profileCacheEntry) (ProjectProfile, string, bool, error) {
		scans.Add(1)
		if cached != nil {
			return cached.profile, cached.validationFingerprint, true, nil
		}
		candidateReads.Add(candidateReadsPerDiscovery)
		close(entered)
		<-release
		return profile, "validation-1", true, nil
	}

	results := make([]ProjectProfile, callers)
	errs := make([]error, callers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	for index := range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			results[index], errs[index] = cache.resolve(t.Context(), "root", resolver)
		}()
	}
	ready.Wait()
	close(start)
	<-entered
	waitForProfileCacheWaiters(t, cache, "root", callers-1)
	close(release)
	done.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("concurrent cache miss scans = %d, want 1", got)
	}
	if got := candidateReads.Load(); got != candidateReadsPerDiscovery {
		t.Fatalf("concurrent cache miss candidate reads = %d, want %d", got, candidateReadsPerDiscovery)
	}

	// Every caller owns its slices; mutating one result cannot alter another
	// result or the retained cache entry.
	results[0].Stacks[0] = "mutated"
	results[0].Units[0].Stacks[0] = "mutated"
	results[0].Unknowns[0] = "mutated"
	again, err := cache.resolve(t.Context(), "root", resolver)
	if err != nil {
		t.Fatal(err)
	}
	if again.Stacks[0] != "go" || again.Units[0].Stacks[0] != "go" || again.Unknowns[0] != "purpose" {
		t.Fatalf("caller mutation escaped defensive clone: %+v", again)
	}
	if got := candidateReads.Load(); got != candidateReadsPerDiscovery {
		t.Fatalf("validated cache hit re-read candidates: got %d reads", got)
	}
}

func TestDiscoverCacheInvalidatesSameSizeRestoredMtimeAndInventory(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "package.json")
	writeFixture(t, root, "package.json", `{"name":"old-app","scripts":{"test":"vitest"}}`)
	beforeInfo, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}

	first, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "old-app" || second.Name != "old-app" {
		t.Fatalf("initial cached names = %q, %q", first.Name, second.Name)
	}
	first.Name = "caller-mutation"
	first.Stacks[0] = "caller-mutation"
	if second.Name != "old-app" || second.Stacks[0] != "node" {
		t.Fatalf("Discover returned aliased profiles: %+v", second)
	}

	// Keep size and mtime stable: change-time/file identity (or the bounded
	// content fallback on weak platforms) must still invalidate the cache.
	writeFixture(t, root, "package.json", `{"name":"new-app","scripts":{"test":"vitest"}}`)
	if err := os.Chtimes(manifest, beforeInfo.ModTime(), beforeInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Name != "new-app" {
		t.Fatalf("same-size, restored-mtime mutation served stale profile: %+v", changed)
	}
	if changed.DiscoveryFingerprint == second.DiscoveryFingerprint {
		t.Fatal("content mutation did not update the discovery fingerprint")
	}

	writeFixture(t, root, "engine/Cargo.toml", "[package]\nname = \"engine\"\nversion = \"0.1.0\"\n")
	withInventoryChange, err := Discover(t.Context(), root, ModeBrownfield)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(withInventoryChange.Stacks, "rust") || !hasUnit(withInventoryChange, "engine") {
		t.Fatalf("inventory mutation served stale profile: %+v", withInventoryChange)
	}
}

func TestProfileCacheEnforcesEntryAndByteBounds(t *testing.T) {
	cache := newProfileCache()
	for index := 0; index < maxProfileCacheEntries+4; index++ {
		key := fmt.Sprintf("root-%02d", index)
		_, err := cache.resolve(t.Context(), key, func(context.Context, *profileCacheEntry) (ProjectProfile, string, bool, error) {
			return ProjectProfile{SchemaVersion: SchemaVersion, Mode: ModeBrownfield, Root: key, Name: key}, "validation", true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	entries, retained := len(cache.entries), cache.bytes
	cache.mu.Unlock()
	if entries != maxProfileCacheEntries {
		t.Fatalf("cache entries = %d, want bounded LRU size %d", entries, maxProfileCacheEntries)
	}
	if retained > maxProfileCacheBytes {
		t.Fatalf("cache retained estimate = %d, limit %d", retained, maxProfileCacheBytes)
	}

	oversizeKey := "oversize"
	_, err := cache.resolve(t.Context(), oversizeKey, func(context.Context, *profileCacheEntry) (ProjectProfile, string, bool, error) {
		return ProjectProfile{
			SchemaVersion: SchemaVersion,
			Mode:          ModeBrownfield,
			Root:          oversizeKey,
			Unknowns:      []string{strings.Repeat("x", maxProfileCacheEntrySize+1)},
		}, "validation", true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	_, retainedOversize := cache.entries[oversizeKey]
	cache.mu.Unlock()
	if retainedOversize {
		t.Fatal("oversize project profile was retained")
	}
}

func TestProfileCacheCanceledLeaderDoesNotPoisonWaiter(t *testing.T) {
	cache := newProfileCache()
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	firstEntered := make(chan struct{})
	var calls atomic.Int64
	resolver := func(ctx context.Context, _ *profileCacheEntry) (ProjectProfile, string, bool, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-ctx.Done()
			return ProjectProfile{}, "", false, ctx.Err()
		}
		return ProjectProfile{SchemaVersion: SchemaVersion, Mode: ModeBrownfield, Root: "/retry", Name: "retry"}, "validation", true, nil
	}

	leaderErr := make(chan error, 1)
	go func() {
		_, err := cache.resolve(leaderCtx, "root", resolver)
		leaderErr <- err
	}()
	<-firstEntered
	waiterResult := make(chan ProjectProfile, 1)
	waiterErr := make(chan error, 1)
	go func() {
		profile, err := cache.resolve(t.Context(), "root", resolver)
		waiterResult <- profile
		waiterErr <- err
	}()
	waitForProfileCacheWaiters(t, cache, "root", 1)
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context cancellation", err)
	}
	if err := <-waiterErr; err != nil {
		t.Fatalf("live waiter inherited leader cancellation: %v", err)
	}
	if profile := <-waiterResult; profile.Name != "retry" {
		t.Fatalf("waiter profile = %+v", profile)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resolver calls = %d, want canceled leader plus one retry", got)
	}
}

func BenchmarkDiscoverBrownfieldRepeated(b *testing.B) {
	root := b.TempDir()
	for index := 0; index < 32; index++ {
		relative := filepath.Join(fmt.Sprintf("service-%02d", index), "package.json")
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		content := fmt.Sprintf(`{"name":"service-%02d","scripts":{"test":"vitest","build":"vite build"}}`, index)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	// Keep this benchmark focused on bounded filesystem scans and candidate
	// reads rather than failed Git subprocess startup.
	emptyPath := b.TempDir()
	b.Setenv("PATH", emptyPath)
	info, err := os.Stat(filepath.Join(root, "service-00", "package.json"))
	if err != nil {
		b.Fatal(err)
	}
	_, strongIdentity := metadataIdentityDetails(info)

	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, _, _, err := discoverBrownfieldUncached(context.Background(), root); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(3, "inventory-scans/op")
		b.ReportMetric(96, "candidate-reads/op")
	})

	b.Run("validated-cache", func(b *testing.B) {
		if _, err := Discover(context.Background(), root, ModeBrownfield); err != nil {
			b.Fatal(err)
		}
		candidateReads := float64(32)
		if strongIdentity {
			candidateReads = 0
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := Discover(context.Background(), root, ModeBrownfield); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(1, "inventory-scans/op")
		b.ReportMetric(candidateReads, "candidate-reads/op")
	})
}

func waitForProfileCacheWaiters(t *testing.T, cache *profileCache, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cache.mu.Lock()
		flight := cache.flights[key]
		got := 0
		if flight != nil {
			got = flight.waiters
		}
		cache.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache flight waiters = %d, want %d", got, want)
		}
		runtime.Gosched()
	}
}
