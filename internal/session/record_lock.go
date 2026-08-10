package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	recordLockRetry = 10 * time.Millisecond
	recordLockStale = 2 * time.Minute
)

type recordLock struct {
	dir   string
	token string
}

func (s *Store) withRecordLock(ctx context.Context, project, id string, fn func() error) error {
	if !validComponent(project) || !validComponent(id) {
		return errors.New("lock session: invalid project or id")
	}
	projectDir := filepath.Join(s.dir, project)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return fmt.Errorf("lock session: %w", err)
	}
	lock, err := acquireRecordLock(ctx, filepath.Join(projectDir, "."+id+".lock"))
	if err != nil {
		return err
	}
	defer lock.release()
	return fn()
}

func acquireRecordLock(ctx context.Context, path string) (recordLock, error) {
	token, err := lockToken()
	if err != nil {
		return recordLock{}, fmt.Errorf("lock session: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return recordLock{}, err
		}
		err := os.Mkdir(path, 0o700)
		if err == nil {
			owner := filepath.Join(path, "owner")
			if err := os.WriteFile(owner, []byte(token), 0o600); err != nil {
				_ = os.Remove(path)
				return recordLock{}, fmt.Errorf("lock session: record owner: %w", err)
			}
			return recordLock{dir: path, token: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return recordLock{}, fmt.Errorf("lock session: %w", err)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return recordLock{}, fmt.Errorf("lock session: inspect existing lock: %w", statErr)
		}
		if statErr == nil && !info.IsDir() {
			return recordLock{}, errors.New("lock session: lock path is not a directory")
		}
		if statErr == nil && time.Since(info.ModTime()) > recordLockStale {
			quarantine := path + ".stale-" + token
			if renameErr := os.Rename(path, quarantine); renameErr == nil {
				_ = os.Remove(filepath.Join(quarantine, "owner"))
				_ = os.Remove(quarantine)
				continue
			}
		}
		timer := time.NewTimer(recordLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return recordLock{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lock recordLock) release() {
	owner := filepath.Join(lock.dir, "owner")
	data, err := os.ReadFile(owner)
	if err != nil || string(data) != lock.token {
		return
	}
	_ = os.Remove(owner)
	_ = os.Remove(lock.dir)
}

func lockToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
