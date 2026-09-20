// Package rlm hosts the pinned Prime Agent CPython kernel. Python execution
// has shell-level authority and must pass Maestro's tool approval gate.
package rlm

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:assets
var assets embed.FS

type Host func(context.Context, map[string]any) (any, error)
type Kernel struct {
	mu      sync.Mutex
	root    string
	cmd     *exec.Cmd
	input   io.WriteCloser
	events  chan map[string]any
	stop    context.CancelFunc
	cleanup func()
	counter int
	Host    Host
}

func New(root string, host Host) *Kernel { return &Kernel{root: root, Host: host} }
func (k *Kernel) start() error {
	dir, err := os.MkdirTemp("", "maestro-rlm-")
	if err != nil {
		return err
	}
	k.cleanup = func() { _ = os.RemoveAll(dir) }
	err = fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel("assets", path)
		dest := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		b, e := assets.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(dest, b, 0600)
	})
	if err != nil {
		k.cleanup()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	k.stop = cancel
	// -I ignores workspace PYTHONPATH and user site packages. Only our extracted
	// source path is inserted explicitly before importing the kernel.
	code := "import sys,runpy;assert sys.version_info >= (3,11), 'Maestro RLM requires Python 3.11+';sys.path.insert(0," + strconv.Quote(dir) + ");runpy.run_module('rlm.repl',run_name='__main__',alter_sys=True)"
	k.cmd = exec.CommandContext(ctx, "python3", "-I", "-B", "-u", "-c", code)
	isolateKernel(k.cmd)
	k.cmd.Env = append(os.Environ(), "RLM_SESSION_DIR="+dir, "RLM_HARNESS_STATE_DIR="+filepath.Join(dir, "harness"), "PRIME_AGENT_KERNEL_OWNER_PID="+strconv.Itoa(os.Getpid()))
	k.cmd.Dir = k.root
	k.cmd.WaitDelay = time.Second
	k.input, err = k.cmd.StdinPipe()
	if err != nil {
		cancel()
		k.cleanup()
		return err
	}
	output, err := k.cmd.StdoutPipe()
	if err != nil {
		cancel()
		k.input.Close()
		k.cleanup()
		return err
	}
	if err = k.cmd.Start(); err != nil {
		cancel()
		k.input.Close()
		k.cleanup()
		return err
	}
	k.events = make(chan map[string]any, 32)
	events := k.events
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 65536), 1<<20)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				cancel()
				return
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
		if scanner.Err() != nil {
			cancel()
		}
	}()
	return nil
}
func (k *Kernel) Close() { k.mu.Lock(); defer k.mu.Unlock(); k.closeLocked() }
func (k *Kernel) closeLocked() {
	if k.cmd != nil {
		encoder := json.NewEncoder(k.input)
		_ = encoder.Encode(map[string]any{"type": "interrupt"})
		_ = encoder.Encode(map[string]any{"type": "shutdown"})
		k.input.Close()
		done := make(chan struct{})
		cmd := k.cmd
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(750 * time.Millisecond):
			k.stop()
			<-done
		}
		k.stop()
		k.cmd = nil
	}

	if k.cleanup != nil {
		k.cleanup()
		k.cleanup = nil
	}
}

// Execute preserves globals between cells in this run. Timeout or output
// overflow destroys the kernel, preventing hidden background continuation.
func (k *Kernel) Execute(ctx context.Context, code string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(code) > 128<<10 {
		return "", errors.New("RLM cell exceeds 128 KiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	fresh := k.cmd == nil
	if fresh {
		if err := k.start(); err != nil {
			return "", err
		}
		code = "import rlm, asyncio, pathlib, hashlib\n" + code
	}
	k.counter++
	id := strconv.Itoa(k.counter)
	if err := json.NewEncoder(k.input).Encode(map[string]any{"type": "execute", "id": id, "code": code}); err != nil {
		k.closeLocked()
		return "", err
	}
	var out strings.Builder
	var cellErr error
	for {
		select {
		case <-ctx.Done():
			k.closeLocked()
			return out.String(), ctx.Err()
		case event, ok := <-k.events:
			if !ok {
				k.closeLocked()
				return out.String(), errors.New("RLM kernel disconnected")
			}
			switch event["event"] {
			case "ready":
				if event["protocol"] != float64(3) {
					k.closeLocked()
					return "", errors.New("unsupported RLM protocol")
				}
			case "stdout", "stderr", "result":
				text, _ := event["text"].(string)
				if out.Len()+len(text) > 1<<20 {
					k.closeLocked()
					return out.String(), errors.New("RLM output exceeds 1 MiB; summarize or slice the context")
				}
				out.WriteString(text)
			case "error":
				cellErr = fmt.Errorf("RLM %v: %v", event["ename"], event["evalue"])
			case "host_request":
				data, _ := event["data"].(map[string]any)
				var result any
				err := errors.New("unsupported RLM host capability")
				if k.Host != nil {
					result, err = k.Host(ctx, data)
				}
				reply := map[string]any{"status": "ok", "result": result}
				if err != nil {
					reply = map[string]any{"status": "error", "error": err.Error()}
				}
				if err = json.NewEncoder(k.input).Encode(map[string]any{"type": "host_reply", "id": event["id"], "data": reply}); err != nil {
					k.closeLocked()
					return out.String(), err
				}
			case "done":
				if event["id"] == id {
					if event["status"] != "ok" && cellErr == nil {
						cellErr = errors.New("RLM cell failed")
					}
					return out.String(), cellErr
				}
			}
		}
	}
}
