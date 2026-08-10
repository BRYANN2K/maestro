package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/updatecheck"
)

type releaseChecker interface {
	Check(context.Context, bool) (updatecheck.Result, error)
}

type updateCheckMsg struct {
	result updatecheck.Result
	err    error
	manual bool
}

// EnableUpdateChecks configures the public, read-only release checker. Dev
// builds intentionally return an error and therefore never make a background
// request while tests or local source builds are running.
func (m *Model) EnableUpdateChecks(currentVersion string) error {
	m.updateChecker = nil
	cachePath, err := updatecheck.DefaultCachePath()
	if err != nil {
		return err
	}
	checker, err := updatecheck.New(updatecheck.Options{
		CurrentVersion: currentVersion,
		CachePath:      cachePath,
	})
	if err != nil {
		return err
	}
	m.updateChecker = checker
	return nil
}

func (m *Model) automaticUpdateChecksEnabled() bool {
	if m.updateChecker == nil || m.orch == nil || m.orch.SettingsSnapshot().DisableUpdateChecks {
		return false
	}
	if updateChecksDisabledByEnvironment() {
		return false
	}
	return true
}

func updateChecksDisabledByEnvironment() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAESTRO_NO_UPDATE_CHECK"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (m *Model) checkForUpdates(force, manual bool) tea.Cmd {
	if m.updateChecker == nil {
		return nil
	}
	if m.updateCheckPending {
		if manual && !m.updateCheckManual {
			m.updateManualQueued = true
		}
		return nil
	}
	if !manual && !m.automaticUpdateChecksEnabled() {
		return nil
	}
	m.updateCheckPending = true
	m.updateCheckManual = manual
	checker := m.updateChecker
	return func() tea.Msg {
		result, err := checker.Check(context.Background(), force)
		return updateCheckMsg{result: result, err: err, manual: manual}
	}
}

func (m *Model) handleUpdateCheck(msg updateCheckMsg) tea.Cmd {
	queuedManual := m.updateManualQueued && !msg.manual
	m.updateCheckPending = false
	m.updateCheckManual = false
	m.updateManualQueued = false
	if msg.err != nil {
		if msg.manual {
			detail := truncateRunes(safeIDEPlainText(msg.err.Error()), 120)
			m.appendError("error: update check failed · " + detail)
			m.status.pushToast("error", "update check failed", 4*time.Second)
			m.renderMessages()
		}
		if queuedManual {
			return tea.Batch(m.arm(), m.checkForUpdates(true, true))
		}
		return m.arm()
	}
	if msg.result.Available {
		m.availableUpdate = msg.result.Latest
		m.status.pushToast("info", "update v"+msg.result.Latest+" available · /update", 8*time.Second)
	} else {
		m.availableUpdate = ""
	}
	if msg.manual {
		m.appendSystem(m.updateSummary(msg.result))
		if !msg.result.Available {
			m.status.pushToast("success", "Maestro is up to date", 3*time.Second)
		}
		m.renderMessages()
	}
	if queuedManual {
		return tea.Batch(m.arm(), m.checkForUpdates(true, true))
	}
	return m.arm()
}

func (m *Model) updateSummary(result updatecheck.Result) string {
	if result.Available {
		return fmt.Sprintf(
			"update: v%s available · current v%s\ninstall: %s\nrelease: %s",
			result.Latest, result.Current, updatecheck.InstallCommand, result.ReleasePage,
		)
	}
	return fmt.Sprintf("update: Maestro v%s is current · automatic checks %s", result.Current, m.updateCheckState())
}

func (m *Model) updateCheckState() string {
	if updateChecksDisabledByEnvironment() {
		return "off · MAESTRO_NO_UPDATE_CHECK"
	}
	if m.orch.SettingsSnapshot().DisableUpdateChecks {
		return "off"
	}
	return "on · every 24h"
}

func (m *Model) handleUpdateCommand(args []string) tea.Cmd {
	if len(args) > 1 {
		m.appendError("error: update usage: /update [check|status|on|off]")
		m.renderMessages()
		return nil
	}
	action := "check"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "", "check":
		if m.updateChecker == nil {
			m.appendSystem("update: unavailable in a development build")
			m.renderMessages()
			return nil
		}
		m.status.pushToast("info", "checking for updates…", 3*time.Second)
		return m.checkForUpdates(true, true)
	case "status":
		text := "update: automatic checks " + m.updateCheckState()
		if m.availableUpdate != "" {
			text += " · v" + m.availableUpdate + " available"
		}
		m.appendSystem(text)
		m.renderMessages()
		return nil
	case "on", "off":
		next := m.orch.SettingsSnapshot()
		next.DisableUpdateChecks = action == "off"
		if err := m.orch.UpdateSettings(context.Background(), next); err != nil {
			m.appendError("error: " + safeIDEPlainText(err.Error()))
			m.renderMessages()
			return nil
		}
		if action == "off" {
			m.availableUpdate = ""
			m.appendSystem("update: automatic checks off")
			m.status.pushToast("info", "automatic update checks off", 3*time.Second)
			m.renderMessages()
			return nil
		}
		m.appendSystem("update: automatic checks on · every 24h")
		m.renderMessages()
		return m.checkForUpdates(true, true)
	default:
		m.appendError("error: update usage: /update [check|status|on|off]")
		m.renderMessages()
		return nil
	}
}
