//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"strings"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/orchestrator"

	"github.com/creack/pty"
)

const (
	ptyTUIHelperEnv = "MAESTRO_TEST_TUI_PTY_HELPER"
	ptyTUIRepoEnv   = "MAESTRO_TEST_TUI_PTY_REPO"
)

type ptyReadResult struct {
	data []byte
	err  error
}

type ptyModePair struct {
	name    string
	enter   []byte
	restore []byte
}

var ptyModePairs = []ptyModePair{
	{name: "alternate screen", enter: []byte("\x1b[?1049h"), restore: []byte("\x1b[?1049l")},
	{name: "cursor visibility", enter: []byte("\x1b[?25l"), restore: []byte("\x1b[?25h")},
	{name: "focus reporting", enter: []byte("\x1b[?1004h"), restore: []byte("\x1b[?1004l")},
	{name: "bracketed paste", enter: []byte("\x1b[?2004h"), restore: []byte("\x1b[?2004l")},
	{name: "cell-motion mouse", enter: []byte("\x1b[?1002h"), restore: []byte("\x1b[?1002l")},
	{name: "SGR mouse", enter: []byte("\x1b[?1006h"), restore: []byte("\x1b[?1006l")},
	{name: "cursor color", enter: []byte("\x1b]12;#"), restore: []byte("\x1b]112\x07")},
}

// TestRunTUICtrlQRestoresTerminalPTY executes the classic renderer boundary
// in a child of this test binary. The child owns a real controlling PTY, so
// the captured bytes are the actual terminal setup and teardown writes rather
// than renderer output produced against an in-memory writer.
func TestRunTUICtrlQRestoresTerminalPTY(t *testing.T) {
	if os.Getenv(ptyTUIHelperEnv) == "1" {
		err := runClassicTUI(options{engine: "native", dir: os.Getenv(ptyTUIRepoEnv)}, os.Stdout, os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "PTY helper runTUI: %v\n", err)
			os.Exit(71)
		}
		os.Exit(0)
	}

	runTUIStartupPTY(t, false)
}

func TestRunTUIChangedBranchRestoresTerminalPTY(t *testing.T) {
	runTUIStartupPTY(t, true)
}

func runTUIStartupPTY(t *testing.T, changedBranch bool) {
	t.Helper()

	repo := t.TempDir()
	gitInit := exec.Command("git", "init", "--quiet")
	gitInit.Dir = repo
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("initialize temporary Git repository: %v\n%s", err, output)
	}
	home := t.TempDir()
	if changedBranch {
		t.Setenv("HOME", home)
		runGit := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		runGit("checkout", "-b", "maestro/spawn")
		runGit("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
		orch, err := orchestrator.New(t.Context(), orchestrator.Options{ProjectDir: repo, SessionsDir: filepath.Join(home, ".maestro", "sessions"), In: strings.NewReader(""), Out: &bytes.Buffer{}})
		if err != nil {
			t.Fatal(err)
		}
		if err := orch.Close(); err != nil {
			t.Fatal(err)
		}
		runGit("checkout", "-b", "main")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunTUICtrlQRestoresTerminalPTY$")
	cmd.Env = ptyTUIChildEnv(os.Environ(), home, repo)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 28, Cols: 100})
	if err != nil {
		t.Fatalf("start TUI helper under PTY: %v", err)
	}

	reads := streamPTY(ptmx)
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	childWaited := false
	ptyClosed := false
	t.Cleanup(func() {
		if !ptyClosed {
			_ = ptmx.Close()
			ptyClosed = true
		}
		if childWaited {
			return
		}
		_ = cmd.Process.Kill()
		select {
		case <-wait:
			childWaited = true
		case <-time.After(2 * time.Second):
			t.Errorf("PTY helper did not terminate after forced cleanup")
		}
	})

	var transcript []byte
	entryTimer := time.NewTimer(15 * time.Second)
	defer entryTimer.Stop()
	for !hasAllPTYModeEntries(transcript) {
		select {
		case result, ok := <-reads:
			if !ok {
				t.Fatalf("PTY closed before all terminal modes were entered; transcript tail=%s", ptyTranscriptTail(transcript))
			}
			transcript = append(transcript, result.data...)
			if result.err != nil {
				t.Fatalf("read PTY before mode entry: %v; transcript tail=%s", result.err, ptyTranscriptTail(transcript))
			}
		case childErr := <-wait:
			childWaited = true
			t.Fatalf("TUI helper exited before mode entry: %v; transcript tail=%s", childErr, ptyTranscriptTail(transcript))
		case <-entryTimer.C:
			t.Fatalf("timed out waiting for TUI terminal mode entry; transcript tail=%s", ptyTranscriptTail(transcript))
		}
	}
	entryEnd := len(transcript)

	if _, err := ptmx.Write([]byte{0x11}); err != nil { // Ctrl+Q
		t.Fatalf("send Ctrl+Q to TUI PTY: %v", err)
	}

	exitTimer := time.NewTimer(15 * time.Second)
	defer exitTimer.Stop()
	readClosed := false
	var childErr error
	for !childWaited || !readClosed {
		select {
		case result, ok := <-reads:
			if !ok {
				readClosed = true
				reads = nil
				continue
			}
			transcript = append(transcript, result.data...)
			if result.err != nil {
				readClosed = true
				reads = nil
			}
		case childErr = <-wait:
			childWaited = true
			wait = nil
		case <-exitTimer.C:
			t.Fatalf("TUI helper did not exit cleanly after Ctrl+Q; transcript tail=%s", ptyTranscriptTail(transcript))
		}
	}
	if childErr != nil {
		t.Fatalf("TUI helper exit after Ctrl+Q: %v; transcript tail=%s", childErr, ptyTranscriptTail(transcript))
	}
	if err := ptmx.Close(); err != nil {
		t.Fatalf("close PTY: %v", err)
	}
	ptyClosed = true

	entered, restored := transcript[:entryEnd], transcript[entryEnd:]
	assertPTYModeRestoration(t, entered, restored, transcript)
}

func ptyTUIChildEnv(base []string, home, repo string) []string {
	overrides := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home + "/.config",
		"MAESTRO_SESSIONS_DIR=" + home + "/.maestro/sessions",
		"MAESTRO_DISABLE_MODELS_FETCH=1",
		"MAESTRO_NO_UPDATE_CHECK=1",
		"MAESTRO_MOUSE=cell",
		"MAESTRO_COLOR=truecolor",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		ptyTUIHelperEnv + "=1",
		ptyTUIRepoEnv + "=" + repo,
	}
	replaced := map[string]bool{"NO_COLOR": true}
	for _, item := range overrides {
		key, _, _ := strings.Cut(item, "=")
		replaced[key] = true
	}
	env := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if !replaced[key] {
			env = append(env, item)
		}
	}
	return append(env, overrides...)
}

func streamPTY(ptmx *os.File) <-chan ptyReadResult {
	results := make(chan ptyReadResult, 64)
	go func() {
		defer close(results)
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				results <- ptyReadResult{data: data}
			}
			if err != nil {
				results <- ptyReadResult{err: err}
				return
			}
		}
	}()
	return results
}

func hasAllPTYModeEntries(output []byte) bool {
	for _, mode := range ptyModePairs {
		if !bytes.Contains(output, mode.enter) {
			return false
		}
	}
	return true
}

func assertPTYModeRestoration(t *testing.T, entered, restored, transcript []byte) {
	t.Helper()
	for _, mode := range ptyModePairs {
		if !bytes.Contains(entered, mode.enter) {
			t.Errorf("missing %s entry sequence %q before Ctrl+Q", mode.name, mode.enter)
		}
		if !bytes.Contains(restored, mode.restore) {
			t.Errorf("missing %s restore sequence %q after Ctrl+Q; transcript tail=%s", mode.name, mode.restore, ptyTranscriptTail(transcript))
		}
		if bytes.LastIndex(transcript, mode.restore) < bytes.LastIndex(transcript, mode.enter) {
			t.Errorf("%s was re-enabled after its final restore", mode.name)
		}
	}
	if bytes.Contains(entered, []byte("\x1b[?1003h")) {
		t.Error("default cell-motion configuration unexpectedly enabled all-motion mouse reporting")
	}
	if !hasValidCursorColorEntry(entered) {
		t.Errorf("cursor color entry was not a complete OSC 12 #RRGGBB sequence; transcript tail=%s", ptyTranscriptTail(transcript))
	}
}

func hasValidCursorColorEntry(output []byte) bool {
	const hexDigits = "0123456789abcdefABCDEF"
	prefix := []byte("\x1b]12;#")
	for start := bytes.Index(output, prefix); start >= 0; {
		value := output[start+len(prefix):]
		if len(value) >= 7 && value[6] == '\a' {
			valid := true
			for _, digit := range value[:6] {
				if !strings.ContainsRune(hexDigits, rune(digit)) {
					valid = false
					break
				}
			}
			if valid {
				return true
			}
		}
		next := bytes.Index(output[start+len(prefix):], prefix)
		if next < 0 {
			break
		}
		start += len(prefix) + next
	}
	return false
}

func ptyTranscriptTail(output []byte) string {
	const limit = 4096
	if len(output) > limit {
		output = output[len(output)-limit:]
	}
	return fmt.Sprintf("%q", output)
}
