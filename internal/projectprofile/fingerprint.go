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

const discoveryFingerprintVersion = "maestro-discovery-v2"

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
	root, err := canonicalDirectory(start)
	if err != nil {
		return "", err
	}

	gitRoot, gitErr := repositoryRoot(ctx, root)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	useGitInventory := gitErr == nil && sameDirectory(root, gitRoot)
	view, usedGit, err := inventory(ctx, root, useGitInventory)
	if err != nil {
		return "", err
	}

	digest := sha256.New()
	writeFingerprintField(digest, "version", discoveryFingerprintVersion)
	writeFingerprintField(digest, "inventory-source", strconv.FormatBool(usedGit))
	writeFingerprintField(digest, "inventory-display-truncated", strconv.FormatBool(view.DisplayTruncated))
	writeFingerprintField(digest, "inventory-scan-truncated", strconv.FormatBool(view.ScanTruncated))
	writeFingerprintField(digest, "inventory-total", strconv.Itoa(view.Total))
	writeFingerprintField(digest, "inventory-scanned", strconv.Itoa(view.Scanned))
	writeFingerprintField(digest, "inventory-scanned-bytes", strconv.Itoa(view.ScannedBytes))
	writeFingerprintField(digest, "inventory-excluded", strconv.Itoa(view.Excluded))
	writeFingerprintField(digest, "inventory-unsafe", strconv.Itoa(view.UnsafePaths))
	writeFingerprintField(digest, "inventory-digest", view.Digest)
	head, err := repositoryHEAD(ctx, root, gitErr == nil)
	if err != nil {
		return "", err
	}
	writeFingerprintField(digest, "head", head)

	readBytes := int64(0)
	for _, relative := range view.Candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, lockfile := lockfileManager(filepath.Base(relative)); lockfile {
			info, metadataErr := regularMetadataNoFollow(root, relative, false)
			writeFingerprintField(digest, "lock", relative)
			if metadataErr != nil {
				writeFingerprintField(digest, "lock-error", candidateErrorKind(metadataErr))
			} else {
				writeFingerprintField(digest, "lock-identity", metadataIdentity(info))
			}
			continue
		}
		if !contentDiscoveryCandidate(relative) {
			continue
		}

		writeFingerprintField(digest, "candidate", relative)
		info, metadataErr := regularMetadataNoFollow(root, relative, true)
		if metadataErr != nil {
			writeFingerprintField(digest, "candidate-error", candidateErrorKind(metadataErr))
			continue
		}
		if info.Size() > int64(maxDiscoveryBytes)-readBytes {
			writeFingerprintField(digest, "candidate-error", candidateErrorKind(errDiscoveryReadBudget))
			continue
		}
		data, readErr := readCandidate(root, relative)
		if readErr != nil {
			writeFingerprintField(digest, "candidate-error", candidateErrorKind(readErr))
			continue
		}
		if int64(len(data)) > int64(maxDiscoveryBytes)-readBytes {
			writeFingerprintField(digest, "candidate-error", candidateErrorKind(errDiscoveryReadBudget))
			continue
		}
		readBytes += int64(len(data))
		sum := sha256.Sum256(data)
		writeFingerprintField(digest, "candidate-content", hex.EncodeToString(sum[:]))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
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
	parts := []string{
		"size=" + strconv.FormatInt(info.Size(), 10),
		"mode=" + strconv.FormatUint(uint64(info.Mode()), 10),
		"mtime=" + strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		for _, name := range []string{"Dev", "Ino", "VolumeSerialNumber", "FileIndexHigh", "FileIndexLow", "Ctim", "Ctimespec", "ChangeTime"} {
			field := value.FieldByName(name)
			if field.IsValid() && field.CanInterface() {
				parts = append(parts, name+"="+fmt.Sprint(field.Interface()))
			}
		}
	}
	return strings.Join(parts, ";")
}
