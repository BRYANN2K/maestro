//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	mcpProcessSnapshotTimeout       = 500 * time.Millisecond
	mcpProcessTreeStabilizationPass = 3
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree freezes the isolated MCP process group, discovers children
// that deliberately escaped it with setpgid/setsid, and kills descendants
// leaves-first. Group-only termination is insufficient because an untrusted
// stdio server can otherwise leave a tool child and inherited pipes alive.
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 {
		return
	}
	rootPID := cmd.Process.Pid
	signalled := false
	if err := mcpSignalProcessGroup(rootPID, syscall.SIGSTOP); err == nil {
		signalled = true
	}

	descendants := mcpFreezeDescendants(rootPID)
	sort.Slice(descendants, func(i, j int) bool {
		if descendants[i].depth == descendants[j].depth {
			return descendants[i].pid > descendants[j].pid
		}
		return descendants[i].depth > descendants[j].depth
	})
	for _, descendant := range descendants {
		if err := syscall.Kill(descendant.pid, syscall.SIGKILL); err == nil {
			signalled = true
		}
	}
	if err := mcpSignalProcessGroup(rootPID, syscall.SIGKILL); err == nil {
		signalled = true
	}
	if !signalled {
		_ = cmd.Process.Kill()
	}
}

func mcpSignalProcessGroup(rootPID int, signal syscall.Signal) error {
	if rootPID <= 1 {
		return syscall.ESRCH
	}
	return syscall.Kill(-rootPID, signal)
}

type mcpProcessDescendant struct {
	pid   int
	depth int
}

// mcpFreezeDescendants repeats the snapshot after stopping newly discovered
// descendants, closing the fork race between discovery and SIGSTOP.
func mcpFreezeDescendants(rootPID int) []mcpProcessDescendant {
	known := make(map[int]mcpProcessDescendant)
	for pass := 0; pass < mcpProcessTreeStabilizationPass; pass++ {
		parents, err := mcpSnapshotProcessParents()
		if err != nil {
			break
		}
		current := mcpDescendantsOf(rootPID, parents)
		sort.Slice(current, func(i, j int) bool {
			if current[i].depth == current[j].depth {
				return current[i].pid < current[j].pid
			}
			return current[i].depth < current[j].depth
		})
		foundNew := false
		for _, descendant := range current {
			if _, exists := known[descendant.pid]; exists {
				continue
			}
			if err := syscall.Kill(descendant.pid, syscall.SIGSTOP); err == nil {
				known[descendant.pid] = descendant
				foundNew = true
			} else if errors.Is(err, syscall.ESRCH) {
				continue
			}
		}
		if !foundNew {
			break
		}
	}

	out := make([]mcpProcessDescendant, 0, len(known))
	for _, descendant := range known {
		out = append(out, descendant)
	}
	return out
}

func mcpSnapshotProcessParents() (map[int]int, error) {
	psPath, err := mcpSystemPSPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpProcessSnapshotTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, psPath, "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	parents := make(map[int]int)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil || pid <= 1 || parent < 0 {
			continue
		}
		parents[pid] = parent
	}
	return parents, nil
}

func mcpSystemPSPath() (string, error) {
	for _, path := range []string{"/bin/ps", "/usr/bin/ps"} {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Clean(path), nil
		}
	}
	return "", exec.ErrNotFound
}

func mcpDescendantsOf(rootPID int, parents map[int]int) []mcpProcessDescendant {
	children := make(map[int][]int)
	for pid, parent := range parents {
		if pid > 1 && parent > 0 && pid != parent {
			children[parent] = append(children[parent], pid)
		}
	}
	seen := map[int]bool{rootPID: true}
	queue := []mcpProcessDescendant{{pid: rootPID}}
	var descendants []mcpProcessDescendant
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, childPID := range children[parent.pid] {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			child := mcpProcessDescendant{pid: childPID, depth: parent.depth + 1}
			descendants = append(descendants, child)
			queue = append(queue, child)
		}
	}
	return descendants
}
