package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputFilterDropsFragmentedMousePayloads(t *testing.T) {
	f := &inputFilter{}
	if got := f.filter(nil, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;")}); got != nil {
		t.Fatal("mouse prefix should be dropped")
	}
	if got := f.filter(nil, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("10;10M")}); got != nil {
		t.Fatal("mouse suffix should be dropped")
	}
	if f.noisePending {
		t.Fatal("mouse filter remained in a pending state")
	}
	if got := f.filter(nil, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}); got == nil {
		t.Fatal("normal text should pass through")
	}
}

func TestInputFilterPreservesBracketedPasteControls(t *testing.T) {
	f := &inputFilter{noisePending: true, noisePendingAt: time.Now()}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha\t\nbeta"), Paste: true}
	got, ok := f.filter(nil, msg).(tea.KeyMsg)
	if !ok {
		t.Fatal("bracketed paste should pass through the terminal-noise filter")
	}
	if string(got.Runes) != string(msg.Runes) || !got.Paste {
		t.Fatalf("paste changed by filter: %+v", got)
	}
	if f.noisePending {
		t.Fatal("a framed paste should clear stale fragmented-noise state")
	}
}

func TestInputFilterDropsMouseSuffixWithoutPrefix(t *testing.T) {
	f := &inputFilter{}
	for _, payload := range []string{
		"35;10;10M",
		"35;10;10m",
		"[35;10;10M",
		"<65;78;26M",
		"<65;78;26M<65;78;26M<65;78;26M",
		"m64;53;19M64;53;19M",
	} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(payload)}
		if got := f.filter(nil, msg); got != nil {
			t.Fatalf("mouse suffix %q should be dropped", payload)
		}
	}
}

func TestInputFilterStripsMouseReportsAroundUsefulText(t *testing.T) {
	f := &inputFilter{}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("before<65;78;26Mafter")}
	got, ok := f.filter(nil, msg).(tea.KeyMsg)
	if !ok {
		t.Fatal("useful text should remain a key message")
	}
	if text := string(got.Runes); text != "beforeafter" {
		t.Fatalf("filtered text = %q, want beforeafter", text)
	}
}

func TestInputFilterDropsOSCColorResponsesAndPreservesCommand(t *testing.T) {
	f := &inputFilter{}
	for _, payload := range []string{
		"11;rgb:213d/2743/33e7",
		"\x1b]11;rgb:213d/2743/33e7\x07",
		"\x1b]12;rgb:ffff/0000/aaaa\x1b\\",
	} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(payload)}
		if got := f.filter(nil, msg); got != nil {
			t.Fatalf("OSC color response %q should be dropped, got %#v", payload, got)
		}
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("11;rgb:213d/2743/33e7/model")}
	got, ok := f.filter(nil, msg).(tea.KeyMsg)
	if !ok || string(got.Runes) != "/model" {
		t.Fatalf("OSC-prefixed command = %#v, want /model key message", got)
	}
}

func TestInputFilterRepairsOrphanOSCIntroducerOnlyForKnownCommands(t *testing.T) {
	f := &inputFilter{}
	for input, want := range map[string]string{
		"]mcp":                     "/mcp",
		"]/mcp":                    "/mcp",
		"]models":                  "/models",
		"]ordinary":                "]ordinary",
		"]rename Release showcase": "/rename Release showcase",
	} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)}
		got, ok := f.filter(nil, msg).(tea.KeyMsg)
		if !ok {
			t.Fatalf("input %q should remain a key message", input)
		}
		if text := string(got.Runes); text != want {
			t.Fatalf("filtered input %q = %q, want %q", input, text, want)
		}
	}
}

func TestInputFilterDropsCursorPositionReportsAndPreservesCommand(t *testing.T) {
	f := &inputFilter{}
	for _, payload := range []string{
		"[30;1R",
		"\x1b[30;1R",
	} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(payload)}
		if got := f.filter(nil, msg); got != nil {
			t.Fatalf("cursor position report %q should be dropped, got %#v", payload, got)
		}
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("][30;1R/mcp")}
	got, ok := f.filter(nil, msg).(tea.KeyMsg)
	if !ok || string(got.Runes) != "/mcp" {
		t.Fatalf("CPR-prefixed command = %#v, want /mcp key message", got)
	}
}

func TestNormalizeSubmittedInputRepairsReportsSplitAcrossKeyEvents(t *testing.T) {
	for input, want := range map[string]string{
		"]\x1b[30;1R/mcp": "/mcp",
		"][30;1R/mcp":     "/mcp",
		"]ordinary":       "]ordinary",
		"note [30;1Rdone": "note done",
	} {
		if got := normalizeSubmittedInput(input); got != want {
			t.Fatalf("normalizeSubmittedInput(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInputFilterDoesNotDropNormalAltBindings(t *testing.T) {
	for _, runes := range []string{"1", "2", "←", "→"} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(runes), Alt: true}
		if terminalNoiseKey(msg) {
			t.Fatalf("alt+%q should remain a valid binding", runes)
		}
	}
}

func TestInputFilterExpiresIncompletePayload(t *testing.T) {
	f := &inputFilter{noisePending: true, noisePendingAt: time.Now().Add(-time.Second)}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("normal")}
	if got := f.filter(nil, msg); got == nil {
		t.Fatal("stale incomplete payload must not swallow normal input")
	}
}

func TestInputFilterCoalescesMotion(t *testing.T) {
	f := &inputFilter{lastMotion: time.Now().Add(time.Second)}
	motion := tea.MouseMsg{Action: tea.MouseActionMotion, X: 3, Y: 4}
	if got := f.filter(nil, motion); got != nil {
		t.Fatal("motion inside the coalescing window should be dropped")
	}
	f.lastMotion = time.Time{}
	if got := f.filter(nil, motion); got == nil {
		t.Fatal("first motion sample should pass through")
	}
}
