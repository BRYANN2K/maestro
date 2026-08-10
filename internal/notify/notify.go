// Package notify sends desktop notifications when the terminal is not
// focused (§5.9): auto / native / osc / bell / disabled.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Mode is the notification backend.
type Mode string

// Notification modes.
const (
	ModeAuto     Mode = "auto"
	ModeNative   Mode = "native"
	ModeOSC      Mode = "osc"
	ModeBell     Mode = "bell"
	ModeDisabled Mode = "disabled"
)

// Valid reports whether the mode is known.
func (m Mode) Valid() bool {
	switch m {
	case ModeAuto, ModeNative, ModeOSC, ModeBell, ModeDisabled:
		return true
	}
	return false
}

// Manager sends notifications.
type Manager struct {
	Mode Mode
}

// New builds a manager with the given mode.
func New(mode Mode) *Manager { return &Manager{Mode: mode} }

// Notify sends a notification.
func (m *Manager) Notify(title, body string) {
	switch m.Mode {
	case ModeDisabled:
		return
	case ModeBell:
		fmt.Fprint(os.Stderr, "\a")
	case ModeOSC:
		// iTerm2 / modern terminals.
		fmt.Fprintf(os.Stderr, "\x1b]9;%s\x07", body)
	case ModeNative:
		m.native(title, body)
	case ModeAuto:
		if isSSH() {
			fmt.Fprintf(os.Stderr, "\x1b]9;%s\x07", body)
		} else {
			m.native(title, body)
		}
	}
}

// native uses the platform notification mechanism.
func (m *Manager) native(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification "%s" with title "%s"`, escape(body), escape(title))).Run()
	case "linux":
		_ = exec.Command("notify-send", title, body).Run()
	}
}

// isSSH detects an SSH session via the env.
func isSSH() bool {
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func escape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', r)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
