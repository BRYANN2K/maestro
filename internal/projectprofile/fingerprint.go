package projectprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

const (
	discoveryFingerprintVersion  = "maestro-discovery-v2"
	validationFingerprintVersion = "maestro-validation-v1"
)

type workspaceState struct {
	discoveryFingerprint  string
	validationFingerprint string
	cacheable             bool
}

// Revalidate verifies that the bounded repository facts captured by Discover
// or GreenfieldDefaults are still current. It deliberately reuses the same
// static inventory and candidate rules as discovery and never runs project
// commands, package managers, hooks, or network clients.
func Revalidate(ctx context.Context, profile ProjectProfile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if profile.DiscoveryFingerprint == "" {
		return &RepositoryChangedError{Mode: profile.Mode}
	}
	current, err := workspaceFingerprint(ctx, profile.Root)
	if err != nil {
		return err
	}
	if current != profile.DiscoveryFingerprint {
		return &RepositoryChangedError{Mode: profile.Mode}
	}
	return nil
}

// workspaceFingerprint hashes a bounded view of facts that can affect static
// project discovery: Git HEAD, the inventory, readable discovery candidates,
// and no-follow metadata identities for lockfiles. Lockfile contents are never
// opened, regardless of their size.
func workspaceFingerprint(ctx context.Context, start string) (string, error) {
	state, err := inspectWorkspace(ctx, start, true)
	if err != nil {
		return "", err
	}
	return state.discoveryFingerprint, nil
}

// workspaceValidationFingerprint recomputes the facts that can invalidate a
// cached profile without re-reading candidate contents on filesystems whose
// metadata exposes a change-time identity. On weaker platforms it hashes the
// bounded candidate contents, preserving correctness at the cost of fewer I/O
// savings.
func workspaceValidationFingerprint(ctx context.Context, start string) (string, bool, error) {
	state, err := inspectWorkspace(ctx, start, false)
	if err != nil {
		return "", false, err
	}
	return state.validationFingerprint, state.cacheable, nil
}

func inspectWorkspace(ctx context.Context, start string, includeContent bool) (workspaceState, error) {
	root, err := canonicalDirectory(start)
	if err != nil {
		return workspaceState{}, err
	}

	gitRoot, gitErr := repositoryRoot(ctx, root)
	if err := ctx.Err(); err != nil {
		return workspaceState{}, err
	}
	useGitInventory := gitErr == nil && sameDirectory(root, gitRoot)
	view, usedGit, err := inventory(ctx, root, useGitInventory)
	if err != nil {
		return workspaceState{}, err
	}

	head, err := repositoryHEAD(ctx, root, gitErr == nil)
	if err != nil {
		return workspaceState{}, err
	}
	validationDigest := sha256.New()
	writeWorkspaceFingerprintHeader(validationDigest, validationFingerprintVersion, view, usedGit, head)
	var discoveryDigest hash.Hash
	if includeContent {
		discoveryDigest = sha256.New()
		writeWorkspaceFingerprintHeader(discoveryDigest, discoveryFingerprintVersion, view, usedGit, head)
	}

	readBytes := int64(0)
	cacheable := true
	for _, relative := range view.Candidates {
		if err := ctx.Err(); err != nil {
			return workspaceState{}, err
		}
		if _, lockfile := lockfileManager(filepath.Base(relative)); lockfile {
			info, metadataErr := regularMetadataNoFollow(root, relative, false)
			writeFingerprintField(validationDigest, "lock", relative)
			if includeContent {
				writeFingerprintField(discoveryDigest, "lock", relative)
			}
			if metadataErr != nil {
				kind := candidateErrorKind(metadataErr)
				writeFingerprintField(validationDigest, "lock-error", kind)
				if includeContent {
					writeFingerprintField(discoveryDigest, "lock-error", kind)
				}
			} else {
				identity := metadataIdentity(info)
				writeFingerprintField(validationDigest, "lock-identity", identity)
				if includeContent {
					writeFingerprintField(discoveryDigest, "lock-identity", identity)
				}
			}
			continue
		}
		if !contentDiscoveryCandidate(relative) {
			continue
		}

		writeFingerprintField(validationDigest, "candidate", relative)
		if includeContent {
			writeFingerprintField(discoveryDigest, "candidate", relative)
		}
		info, metadataErr := regularMetadataNoFollow(root, relative, true)
		if metadataErr != nil {
			kind := candidateErrorKind(metadataErr)
			writeFingerprintField(validationDigest, "candidate-error", kind)
			if includeContent {
				writeFingerprintField(discoveryDigest, "candidate-error", kind)
			}
			cacheable = false
			continue
		}
		identity, strongIdentity := metadataIdentityDetails(info)
		writeFingerprintField(validationDigest, "candidate-identity", identity)
		if !includeContent && strongIdentity {
			continue
		}
		if info.Size() > int64(maxDiscoveryBytes)-readBytes {
			kind := candidateErrorKind(errDiscoveryReadBudget)
			writeFingerprintField(validationDigest, "candidate-read-error", kind)
			if includeContent {
				writeFingerprintField(discoveryDigest, "candidate-error", kind)
			}
			cacheable = false
			continue
		}
		data, readErr := readCandidate(root, relative)
		if readErr != nil {
			kind := candidateErrorKind(readErr)
			writeFingerprintField(validationDigest, "candidate-read-error", kind)
			if includeContent {
				writeFingerprintField(discoveryDigest, "candidate-error", kind)
			}
			cacheable = false
			continue
		}
		if int64(len(data)) > int64(maxDiscoveryBytes)-readBytes {
			kind := candidateErrorKind(errDiscoveryReadBudget)
			writeFingerprintField(validationDigest, "candidate-read-error", kind)
			if includeContent {
				writeFingerprintField(discoveryDigest, "candidate-error", kind)
			}
			cacheable = false
			continue
		}
		readBytes += int64(len(data))
		sum := sha256.Sum256(data)
		encoded := hex.EncodeToString(sum[:])
		if includeContent {
			writeFingerprintField(discoveryDigest, "candidate-content", encoded)
		}
		if !strongIdentity {
			writeFingerprintField(validationDigest, "candidate-content", encoded)
		}
	}
	state := workspaceState{
		validationFingerprint: "sha256:" + hex.EncodeToString(validationDigest.Sum(nil)),
		cacheable:             cacheable,
	}
	if includeContent {
		state.discoveryFingerprint = "sha256:" + hex.EncodeToString(discoveryDigest.Sum(nil))
	}
	return state, nil
}

func writeWorkspaceFingerprintHeader(digest hash.Hash, version string, view inventoryView, usedGit bool, head string) {
	writeFingerprintField(digest, "version", version)
	writeFingerprintField(digest, "inventory-source", strconv.FormatBool(usedGit))
	writeFingerprintField(digest, "inventory-display-truncated", strconv.FormatBool(view.DisplayTruncated))
	writeFingerprintField(digest, "inventory-scan-truncated", strconv.FormatBool(view.ScanTruncated))
	writeFingerprintField(digest, "inventory-total", strconv.Itoa(view.Total))
	writeFingerprintField(digest, "inventory-scanned", strconv.Itoa(view.Scanned))
	writeFingerprintField(digest, "inventory-scanned-bytes", strconv.Itoa(view.ScannedBytes))
	writeFingerprintField(digest, "inventory-excluded", strconv.Itoa(view.Excluded))
	writeFingerprintField(digest, "inventory-unsafe", strconv.Itoa(view.UnsafePaths))
	writeFingerprintField(digest, "inventory-digest", view.Digest)
	writeFingerprintField(digest, "head", head)
}

func contentDiscoveryCandidate(relative string) bool {
	lower := strings.ToLower(filepath.Base(relative))
	switch lower {
	case "go.mod", "go.work", "package.json", "pyproject.toml", "cargo.toml", "makefile", "gnumakefile":
		return true
	default:
		return isCIPath(relative)
	}
}

func repositoryHEAD(ctx context.Context, root string, repository bool) (string, error) {
	if !repository {
		return "none", nil
	}
	out, err := runGit(ctx, root, 1024, "rev-parse", "--verify", "HEAD")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "unborn-or-unavailable", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func sameDirectory(a, b string) bool {
	left, leftErr := os.Stat(a)
	right, rightErr := os.Stat(b)
	return leftErr == nil && rightErr == nil && left.IsDir() && right.IsDir() && os.SameFile(left, right)
}

func writeFingerprintField(digest hash.Hash, name, value string) {
	// Length prefixes make the stream unambiguous even when repository paths
	// contain punctuation used by the field labels.
	_, _ = fmt.Fprintf(digest, "%d:%s%d:%s", len(name), name, len(value), value)
}

func candidateErrorKind(err error) string {
	switch {
	case errors.Is(err, errCandidateSymlink):
		return "symlink"
	case errors.Is(err, errCandidateNonRegular):
		return "non-regular"
	case errors.Is(err, errCandidateOversize):
		return "oversize"
	case errors.Is(err, errCandidateBinary):
		return "binary"
	case errors.Is(err, errCandidateChanged):
		return "changed"
	case errors.Is(err, errDiscoveryReadBudget):
		return "read-budget"
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	default:
		return "unreadable"
	}
}

// metadataIdentity contains stable metadata only. Dev/inode (or the Windows
// file index) catches replacement, while ctime catches same-size writes whose
// mtime was restored. Access time is intentionally excluded because reading a
// manifest must not invalidate its own fingerprint.
func metadataIdentity(info os.FileInfo) string {
	identity, _ := metadataIdentityDetails(info)
	return identity
}

func metadataIdentityDetails(info os.FileInfo) (string, bool) {
	parts := []string{
		"size=" + strconv.FormatInt(info.Size(), 10),
		"mode=" + strconv.FormatUint(uint64(info.Mode()), 10),
		"mtime=" + strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}
	strong := false
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		for _, name := range []string{"Dev", "Ino", "VolumeSerialNumber", "FileIndexHigh", "FileIndexLow", "Ctim", "Ctimespec", "ChangeTime"} {
			field := value.FieldByName(name)
			if field.IsValid() && field.CanInterface() {
				parts = append(parts, name+"="+fmt.Sprint(field.Interface()))
				strong = strong || name == "Ctim" || name == "Ctimespec" || name == "ChangeTime"
			}
		}
	}
	return strings.Join(parts, ";"), strong
}
