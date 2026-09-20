//go:build darwin || linux || freebsd || openbsd || netbsd

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Exercises the compiled OpenTUI child through the production Go entry point.
func TestOpenTUIProductionStartupAndQuit(t *testing.T) {
	if os.Getenv("MAESTRO_TEST_OPENTUI_HELPER") == "1" {
		if err := runTUI(options{engine: "native", dir: os.Getenv(ptyTUIRepoEnv)}, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(71)
		}
		os.Exit(0)
	}
	ui := os.Getenv("MAESTRO_UI")
	if ui == "" {
		t.Skip("make ui-test builds and exercises the compiled interface")
	}
	runtime := os.Getenv("MAESTRO_RUNTIME")
	if runtime == "" {
		runtime = filepath.Join(filepath.Dir(ui), "maestro-runtime")
	}
	for _, size := range []pty.Winsize{{Rows: 48, Cols: 160}, {Rows: 24, Cols: 80}} {
		t.Run(fmt.Sprintf("%dx%d", size.Cols, size.Rows), func(t *testing.T) {
			root := t.TempDir()
			if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			home := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestOpenTUIProductionStartupAndQuit$")
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "TERM=xterm-256color", "COLORTERM=truecolor", "LANG=en_US.UTF-8", "MAESTRO_DISABLE_MODELS_FETCH=1", "MAESTRO_NO_UPDATE_CHECK=1", "MAESTRO_UI=" + ui, "MAESTRO_RUNTIME=" + runtime, "MAESTRO_TEST_OPENTUI_HELPER=1", ptyTUIRepoEnv + "=" + root}
			terminal, err := pty.StartWithSize(cmd, &size)
			if err != nil {
				t.Fatal(err)
			}
			reads := streamPTY(terminal)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				terminal.Close()
				if !waited {
					cmd.Process.Kill()
					<-done
				}
			}()
			var transcript []byte
			until := func(needle string) {
				t.Helper()
				timer := time.NewTimer(60 * time.Second)
				defer timer.Stop()
				for !bytes.Contains(transcript, []byte(needle)) {
					select {
					case r, ok := <-reads:
						if !ok {
							t.Fatalf("closed before %q: %s", needle, ptyTranscriptTail(transcript))
						}
						transcript = append(transcript, r.data...)
						if bytes.Contains(r.data, []byte("\x1b]11;?")) {
							terminal.Write([]byte("\x1b]11;rgb:0b0b/0c0c/1010\x1b\\"))
						}
						if bytes.Contains(r.data, []byte("\x1b[6n")) {
							terminal.Write([]byte("\x1b[1;1R"))
						}
					case err := <-done:
						waited = true
						t.Fatalf("exited before %q: %v %s", needle, err, ptyTranscriptTail(transcript))
					case <-timer.C:
						t.Fatalf("timeout %q: %s", needle, ptyTranscriptTail(transcript))
					}
				}
			}
			until("Good software starts")
			terminal.Write([]byte{0x10})
			until("CONNECTIONS")
			terminal.Write([]byte{0x1b})
			time.Sleep(120 * time.Millisecond)
			terminal.Write([]byte{0x11})
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			for !waited {
				select {
				case r, ok := <-reads:
					if ok {
						transcript = append(transcript, r.data...)
					} else {
						reads = nil
					}
				case err := <-done:
					waited = true
					if err != nil {
						t.Fatalf("quit: %v %s", err, ptyTranscriptTail(transcript))
					}
				case <-timer.C:
					t.Fatal("quit blocked")
				}
			}
			// Drain teardown writes queued before the process exit.
			if reads != nil {
				for r := range reads {
					transcript = append(transcript, r.data...)
				}
			}
			for _, sequence := range []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?25h", "\x1b[?1006l", "\x1b[?2004l"} {
				if !bytes.Contains(transcript, []byte(sequence)) {
					t.Fatalf("missing terminal mode %q", sequence)
				}
			}
		})
	}
}
