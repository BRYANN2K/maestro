package projectprofile

import (
	"context"
	"errors"
	"sync"
	"unsafe"
)

const (
	// Project setup normally touches one root, but keeping a few recent roots
	// avoids repeated discovery when several workspaces are open. Both entry
	// count and retained profile size are bounded; unusually large monorepo
	// profiles simply bypass the cache.
	maxProfileCacheEntries   = 8
	maxProfileCacheBytes     = 4 << 20
	maxProfileCacheEntrySize = 1 << 20
)

var sharedProfileCache = newProfileCache()

type profileCacheEntry struct {
	profile               ProjectProfile
	validationFingerprint string
	weight                int
	lastUsed              uint64
}

type profileCacheFlight struct {
	done    chan struct{}
	profile ProjectProfile
	err     error
	waiters int
}

// profileCache is an in-process, bounded LRU. A flight covers validation as
// well as a possible rediscovery, so concurrent callers neither re-scan nor
// re-read the same root independently.
type profileCache struct {
	mu      sync.Mutex
	entries map[string]profileCacheEntry
	flights map[string]*profileCacheFlight
	bytes   int
	clock   uint64
}

func newProfileCache() *profileCache {
	return &profileCache{
		entries: make(map[string]profileCacheEntry),
		flights: make(map[string]*profileCacheFlight),
	}
}

// resolve returns one independently owned profile. The resolver receives a
// defensive copy of the cached entry, if any, and decides whether validation
// permits reuse. Context-cancelled leaders do not poison other waiters: a
// still-live waiter retries and becomes the next leader.
func (cache *profileCache) resolve(
	ctx context.Context,
	key string,
	resolver func(context.Context, *profileCacheEntry) (ProjectProfile, string, bool, error),
) (ProjectProfile, error) {
	for {
		if err := ctx.Err(); err != nil {
			return ProjectProfile{}, err
		}

		cache.mu.Lock()
		if flight := cache.flights[key]; flight != nil {
			flight.waiters++
			done := flight.done
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return ProjectProfile{}, ctx.Err()
			case <-done:
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ProjectProfile{}, ctxErr
				}
				if errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded) {
					continue
				}
				return cloneProjectProfile(flight.profile), flight.err
			}
		}

		var cached *profileCacheEntry
		if entry, ok := cache.entries[key]; ok {
			cache.clock++
			entry.lastUsed = cache.clock
			cache.entries[key] = entry
			copyEntry := entry
			copyEntry.profile = cloneProjectProfile(entry.profile)
			cached = &copyEntry
		}
		flight := &profileCacheFlight{done: make(chan struct{})}
		cache.flights[key] = flight
		cache.mu.Unlock()

		profile, validationFingerprint, cacheable, err := resolver(ctx, cached)

		cache.mu.Lock()
		if err == nil && cacheable {
			cache.putLocked(key, profile, validationFingerprint)
		}
		flight.profile = cloneProjectProfile(profile)
		flight.err = err
		delete(cache.flights, key)
		close(flight.done)
		cache.mu.Unlock()
		// The resolver's profile is already independent: cached input was cloned
		// above, while a cold discovery creates fresh storage. Cache and waiters
		// receive their own clones before this value is returned to the leader.
		return profile, err
	}
}

// invalidate removes only the version observed by the caller. This prevents
// a slow validation from deleting a newer profile installed by another call.
func (cache *profileCache) invalidate(key, validationFingerprint string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok || entry.validationFingerprint != validationFingerprint {
		return
	}
	cache.bytes -= entry.weight
	delete(cache.entries, key)
}

func (cache *profileCache) putLocked(key string, profile ProjectProfile, validationFingerprint string) {
	if previous, ok := cache.entries[key]; ok {
		cache.bytes -= previous.weight
		delete(cache.entries, key)
	}
	weight := retainedProfileBytes(profile) + len(validationFingerprint)
	if weight > maxProfileCacheEntrySize || weight > maxProfileCacheBytes {
		return
	}
	for len(cache.entries) >= maxProfileCacheEntries || cache.bytes+weight > maxProfileCacheBytes {
		var oldestKey string
		var oldest uint64
		first := true
		for candidateKey, candidate := range cache.entries {
			if first || candidate.lastUsed < oldest {
				oldestKey, oldest, first = candidateKey, candidate.lastUsed, false
			}
		}
		if first {
			break
		}
		cache.bytes -= cache.entries[oldestKey].weight
		delete(cache.entries, oldestKey)
	}
	cache.clock++
	cache.entries[key] = profileCacheEntry{
		profile:               cloneProjectProfile(profile),
		validationFingerprint: validationFingerprint,
		weight:                weight,
		lastUsed:              cache.clock,
	}
	cache.bytes += weight
}

func cloneProjectProfile(profile ProjectProfile) ProjectProfile {
	copyProfile := profile
	copyProfile.Stacks = cloneStrings(profile.Stacks)
	copyProfile.Units = cloneUnits(profile.Units)
	copyProfile.Commands = cloneCommands(profile.Commands)
	copyProfile.Evidence = cloneEvidence(profile.Evidence)
	copyProfile.Unknowns = cloneStrings(profile.Unknowns)
	return copyProfile
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func cloneCommands(values []Command) []Command {
	if values == nil {
		return nil
	}
	out := make([]Command, len(values))
	copy(out, values)
	return out
}

func cloneEvidence(values []Evidence) []Evidence {
	if values == nil {
		return nil
	}
	out := make([]Evidence, len(values))
	copy(out, values)
	return out
}

// retainedProfileBytes accounts for all slice backing arrays and immutable
// string payloads retained by a defensive clone. Allocator size-class
// rounding is deliberately excluded; the separate entry-count limit remains
// a hard upper bound even when rounding differs by platform.
func retainedProfileBytes(profile ProjectProfile) int {
	bytes := int(unsafe.Sizeof(profile))
	bytes += len(profile.Root) + len(profile.Name) + len(profile.DiscoveryFingerprint)
	bytes += len(profile.Stacks) * int(unsafe.Sizeof(string("")))
	for _, value := range profile.Stacks {
		bytes += len(value)
	}
	bytes += len(profile.Unknowns) * int(unsafe.Sizeof(string("")))
	for _, value := range profile.Unknowns {
		bytes += len(value)
	}
	bytes += len(profile.Units) * int(unsafe.Sizeof(Unit{}))
	for _, unit := range profile.Units {
		bytes += len(unit.Path) + len(unit.Name)
		for _, values := range [][]string{unit.Stacks, unit.Manifests, unit.Lockfiles} {
			bytes += len(values) * int(unsafe.Sizeof(string("")))
			for _, value := range values {
				bytes += len(value)
			}
		}
	}
	bytes += len(profile.Commands) * int(unsafe.Sizeof(Command{}))
	for _, command := range profile.Commands {
		bytes += len(command.Name) + len(command.Run) + len(command.Cwd) + len(command.Source) + len(command.Confidence)
	}
	bytes += len(profile.Evidence) * int(unsafe.Sizeof(Evidence{}))
	for _, evidence := range profile.Evidence {
		bytes += len(evidence.Kind) + len(evidence.Value) + len(evidence.Source) + len(evidence.Confidence)
	}
	return bytes
}
