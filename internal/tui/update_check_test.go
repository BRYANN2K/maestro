package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/updatecheck"
)

type fakeReleaseChecker struct {
	result updatecheck.Result
	err    error
	calls  atomic.Int32
	force  atomic.Bool
}

func (f *fakeReleaseChecker) Check(_ context.Context, force bool) (updatecheck.Result, error) {
	f.calls.Add(1)
	f.force.Store(force)
	return f.result, f.err
}

func TestAutomaticUpdateCheckNotifiesWithoutTranscriptNoise(t *testing.T) {
	m, _ := newTestModel(t)
	checker := &fakeReleaseChecker{result: updatecheck.Result{
		Current: "1.0.0", Latest: "1.1.0", Available: true,
		CheckedAt: time.Now(), ReleasePage: updatecheck.ReleasePage,
	}}
	m.updateChecker = checker
	before := len(m.messages)
	cmd := m.checkForUpdates(false, false)
	if cmd == nil {
		t.Fatal("automatic update check returned nil")
	}
	feed(m, cmd())
	if checker.calls.Load() != 1 || checker.force.Load() {
		t.Fatalf("automatic calls=%d force=%v", checker.calls.Load(), checker.force.Load())
	}
	if len(m.messages) != before {
		t.Fatalf("automatic check added transcript noise: %d -> %d", before, len(m.messages))
	}
	if m.availableUpdate != "1.1.0" {
		t.Fatalf("available update = %q", m.availableUpdate)
	}
	if len(m.status.toasts) == 0 || !strings.Contains(m.status.toasts[len(m.status.toasts)-1].Msg, "/update") {
		t.Fatalf("notification toast missing: %#v", m.status.toasts)
	}
	// The temporary toast intentionally overlays the footer. Once it expires,
	// the release badge remains discoverable without transcript noise.
	m.status.toasts = nil
	m.SetSize(160, 40)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "UPDATE v1.1.0") {
		t.Fatalf("persistent update status missing: %q", view[len(view)-min(len(view), 400):])
	}
}

func TestUpdateCommandReportsInstallAndCanDisableChecks(t *testing.T) {
	m, _ := newTestModel(t)
	checker := &fakeReleaseChecker{result: updatecheck.Result{
		Current: "1.0.0", Latest: "1.2.0", Available: true,
		CheckedAt: time.Now(), ReleasePage: updatecheck.ReleasePage,
	}}
	m.updateChecker = checker
	m.input.Set("/update")
	cmd := m.send()
	if cmd == nil {
		t.Fatal("/update returned nil")
	}
	feed(m, cmd())
	if !checker.force.Load() {
		t.Fatal("manual update check did not bypass the cache")
	}
	last := m.messages[len(m.messages)-1].Text
	for _, want := range []string{"v1.2.0 available", updatecheck.InstallCommand, updatecheck.ReleasePage} {
		if !strings.Contains(last, want) {
			t.Fatalf("manual update output %q missing %q", last, want)
		}
	}

	m.input.Set("/update off")
	if cmd := m.send(); cmd != nil {
		t.Fatal("/update off unexpectedly started a worker")
	}
	if !m.orch.SettingsSnapshot().DisableUpdateChecks || m.availableUpdate != "" {
		t.Fatalf("disable state = %+v, available=%q", m.orch.SettingsSnapshot(), m.availableUpdate)
	}
	if cmd := m.checkForUpdates(false, false); cmd != nil {
		t.Fatal("disabled automatic check returned a command")
	}
}

func TestUpdateCheckOptOutEnvironmentAndManualErrorSafety(t *testing.T) {
	m, _ := newTestModel(t)
	checker := &fakeReleaseChecker{err: errors.New("provider\x1b]52;c;stolen\a unavailable")}
	m.updateChecker = checker
	t.Setenv("MAESTRO_NO_UPDATE_CHECK", "1")
	if cmd := m.checkForUpdates(false, false); cmd != nil {
		t.Fatal("environment opt-out returned an automatic command")
	}
	cmd := m.checkForUpdates(true, true)
	if cmd == nil {
		t.Fatal("manual check was blocked by automatic opt-out")
	}
	feed(m, cmd())
	view := m.LastAssistantText()
	_ = view
	for _, message := range m.messages {
		if strings.ContainsAny(message.Text, "\x1b\a") {
			t.Fatalf("unsafe update error reached transcript: %q", message.Text)
		}
	}
}

func TestManualCheckQueuesBehindStartupCheck(t *testing.T) {
	m, _ := newTestModel(t)
	m.updateChecker = &fakeReleaseChecker{result: updatecheck.Result{
		Current: "1.0.0", Latest: "1.0.0", ReleasePage: updatecheck.ReleasePage,
	}}
	m.updateCheckPending = true
	m.updateCheckManual = false
	if cmd := m.checkForUpdates(true, true); cmd != nil || !m.updateManualQueued {
		t.Fatalf("manual request was not coalesced: cmd=%v queued=%v", cmd != nil, m.updateManualQueued)
	}
	cmd := m.handleUpdateCheck(updateCheckMsg{result: updatecheck.Result{
		Current: "1.0.0", Latest: "1.0.0", ReleasePage: updatecheck.ReleasePage,
	}})
	if cmd == nil || !m.updateCheckPending || !m.updateCheckManual || m.updateManualQueued {
		t.Fatalf("queued manual request was not started: pending=%v manual=%v queued=%v", m.updateCheckPending, m.updateCheckManual, m.updateManualQueued)
	}
}

func TestDevelopmentBuildDoesNotEnableReleaseNetwork(t *testing.T) {
	m, _ := newTestModel(t)
	if err := m.EnableUpdateChecks("dev"); err == nil {
		t.Fatal("development build enabled update checks")
	}
	if m.updateChecker != nil {
		t.Fatal("development build retained a checker")
	}
}

func TestSettingsExposeUpdateCheckPreference(t *testing.T) {
	m, _ := newTestModel(t)
	overlay := newSettingsOverlay(m)
	overlay.section = settingsGeneral
	rows := overlay.rows()
	if len(rows) != 3 || rows[2].Kind != settingUpdates || overlay.value(rows[2]) != "on · every 24h" {
		t.Fatalf("general settings rows = %+v", rows)
	}
	overlay.selected = 2
	overlay.change(m, rows[2], 1)
	if !m.orch.SettingsSnapshot().DisableUpdateChecks || overlay.value(rows[2]) != "off" {
		t.Fatalf("update preference was not saved: %+v", m.orch.SettingsSnapshot())
	}
}
