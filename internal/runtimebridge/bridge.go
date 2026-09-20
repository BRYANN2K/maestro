// Package runtimebridge owns the private, versioned Maestro harness process.
// The executable is resolved beside Maestro, never from an untrusted workspace.
package runtimebridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const MaxFrame = 16 << 20

type Message struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Error  string          `json:"error"`
}

type Handler func(context.Context, Message) (any, error)

func Path() (string, error) {
	if explicit := os.Getenv("MAESTRO_RUNTIME"); explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", errors.New("MAESTRO_RUNTIME must be an absolute executable path")
		}
		if info, err := os.Stat(explicit); err != nil || !info.Mode().IsRegular() {
			return "", errors.New("MAESTRO_RUNTIME does not name a regular executable")
		}
		return explicit, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "maestro-runtime"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(executable), name)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("maestro runtime missing: run make build or reinstall the complete release")
	}
	return path, nil
}

func Available() bool { _, err := Path(); return err == nil }

func Run(ctx context.Context, input any, handler Handler) error {
	path, err := Path()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Runtime diagnostics must not paint over the TUI or leak credentials.
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start Maestro runtime: %w", err)
	}
	defer func() { cancel(); stdin.Close(); _ = cmd.Wait() }()
	encoder := json.NewEncoder(stdin)
	if err = encoder.Encode(input); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), MaxFrame)
	completed := false
	lastID := 0
	for scanner.Scan() {
		var message Message
		if err = json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("invalid runtime frame: %w", err)
		}
		if message.Method == "complete" {
			completed = true
			break
		}
		if message.Method == "failure" {
			return fmt.Errorf("maestro runtime: %s", message.Error)
		}
		if message.ID <= lastID {
			return errors.New("runtime request IDs must increase")
		}
		lastID = message.ID
		result, handleErr := handler(ctx, message)
		if handleErr != nil {
			return handleErr
		}
		if err = encoder.Encode(struct {
			ID     int `json:"id"`
			Result any `json:"result"`
		}{message.ID, result}); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = scanner.Err(); err != nil {
		return fmt.Errorf("runtime protocol: %w", err)
	}
	if !completed {
		return errors.New("maestro runtime ended without completion")
	}
	stdin.Close()
	if err = cmd.Wait(); err != nil {
		return fmt.Errorf("maestro runtime exit: %w", err)
	}
	return nil
}
