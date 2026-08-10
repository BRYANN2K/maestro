//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agent

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
	legacyProcessWaitDelay       = 2 * time.Second
	processSnapshotTimeout       = 500 * time.Millisecond
	processTreeStabilizationPass = 3
)

// configureProcessTree puts each vendor CLI in its own process group. Go's
// default CommandContext cancellation kills only the direct child, allowing
// wrappers and their tool subprocesses to outlive Maestro while inherited
// stdout pipes keep cmd.Wait blocked.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return killProcessTree(cmd.Process)
	}
	// Defensive bound for descendants that deliberately escape their group
	// while retaining an inherited pipe. Normal descendants are killed above;
	// WaitDelay ensures the UI still regains control in bounded time.
	cmd.WaitDelay = legacyProcessWaitDelay
}

// killProcessTree first freezes the vendor process group, while parent/child
// relationships are still intact, then discovers and freezes descendants that
// deliberately escaped that group with setpgid(2) or setsid(2). Killing only
// -rootPID leaves those tool processes running and lets them keep inherited
// stdout pipes open after the TUI has reported a successful cancellation.
//
// Process discovery is deliberately best effort: if ps is unavailable or
// times out, the isolated root process group is still killed. Descendants are
// stopped before any ancestor is killed, which avoids reparenting the exact
// processes we need to identify.
func killProcessTree(process *os.Process) error {
	if process == nil || process.Pid <= 1 {
		return os.ErrProcessDone
	}

	rootPID := process.Pid
	signalled := false
	var firstErr error

	if err := signalProcessGroup(rootPID, syscall.SIGSTOP); err == nil {
		signalled = true
	} else if !errors.Is(err, syscall.ESRCH) {
		firstErr = err
	}

	descendants := freezeDescendants(rootPID)
	// Leaves first. All discovered processes are stopped, so their identities
	// and parent links cannot change between ordering and termination.
	sort.Slice(descendants, func(i, j int) bool {
		if descendants[i].depth == descendants[j].depth {
			return descendants[i].pid > descendants[j].pid
		}
		return descendants[i].depth > descendants[j].depth
	})
	for _, descendant := range descendants {
		if err := syscall.Kill(descendant.pid, syscall.SIGKILL); err == nil {
			signalled = true
		} else if !errors.Is(err, syscall.ESRCH) && firstErr == nil {
			firstErr = err
		}
	}

	// A negative PID addresses the isolated vendor process group. SIGKILL is
	// intentional: Escape-Escape is an explicit request for an immediate stop.
	if err := signalProcessGroup(rootPID, syscall.SIGKILL); err == nil {
		signalled = true
	} else if !errors.Is(err, syscall.ESRCH) && firstErr == nil {
		firstErr = err
	}

	if !signalled {
		// If process-group signalling is unavailable, at least guarantee the
		// direct child cannot keep Maestro blocked.
		if err := process.Kill(); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrProcessDone) && firstErr == nil {
			firstErr = err
		}
	}
	if signalled {
		return nil
	}
	if firstErr != nil {
		return firstErr
	}
	return os.ErrProcessDone
}

func signalProcessGroup(rootPID int, signal syscall.Signal) error {
	if rootPID <= 1 {
		return syscall.ESRCH
	}
	return syscall.Kill(-rootPID, signal)
}

type processDescendant struct {
	pid   int
	depth int
}

// freezeDescendants repeats the snapshot after stopping every newly found
// descendant. This closes the race where an escaped tool forks between the
// first process-table snapshot and the SIGSTOP delivered to that tool.
func freezeDescendants(rootPID int) []processDescendant {
	known := make(map[int]processDescendant)
	for pass := 0; pass < processTreeStabilizationPass; pass++ {
		parents, err := snapshotProcessParents()
		if err != nil {
			break
		}
		current := descendantsOf(rootPID, parents)
		foundNew := false
		sort.Slice(current, func(i, j int) bool {
			if current[i].depth == current[j].depth {
				return current[i].pid < current[j].pid
			}
			return current[i].depth < current[j].depth
		})
		for _, descendant := range current {
			if _, exists := known[descendant.pid]; exists {
				continue
			}
			// Shallow descendants are stopped first so they cannot create more
			// work while deeper descendants from the snapshot are frozen.
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

	result := make([]processDescendant, 0, len(known))
	for _, descendant := range known {
		result = append(result, descendant)
	}
	return result
}

func snapshotProcessParents() (map[int]int, error) {
	psPath, err := systemPSPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), processSnapshotTimeout)
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

func systemPSPath() (string, error) {
	for _, path := range []string{"/bin/ps", "/usr/bin/ps"} {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Clean(path), nil
		}
	}
	return "", exec.ErrNotFound
}

func descendantsOf(rootPID int, parents map[int]int) []processDescendant {
	children := make(map[int][]int)
	for pid, parent := range parents {
		if pid > 1 && parent > 0 && pid != parent {
			children[parent] = append(children[parent], pid)
		}
	}

	seen := map[int]bool{rootPID: true}
	queue := []processDescendant{{pid: rootPID}}
	var descendants []processDescendant
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, childPID := range children[parent.pid] {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			child := processDescendant{pid: childPID, depth: parent.depth + 1}
			descendants = append(descendants, child)
			queue = append(queue, child)
		}
	}
	return descendants
}
