package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode"
)

func TestFocusOutputMarkdownEmphasizesOnlyProseLabels(t *testing.T) {
	in := "Done: tests pass\n\nNext: open the TUI\n\n```text\nNext: literal code\n```\n    Fix: indented code\nStateful: unchanged"
	got := focusOutputMarkdown(in)
	for _, want := range []string{"**Done:** tests pass", "**Next:** open the TUI"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, want := range []string{"```text\nNext: literal code\n```", "    Fix: indented code", "Stateful: unchanged"} {
		if !strings.Contains(got, want) {
			t.Errorf("rewrote protected text %q:\n%s", want, got)
		}
	}
}

func TestFocusOutputMarkdownIsIdempotent(t *testing.T) {
	value := "Blocked: missing token\nFix: set API_KEY"
	once := focusOutputMarkdown(value)
	if twice := focusOutputMarkdown(once); twice != once {
		t.Fatalf("focus markup is not idempotent:\nonce:  %q\ntwice: %q", once, twice)
	}
}

func TestFocusOutputMarkdownPreservesLongAndStreamingFences(t *testing.T) {
	for name, delimiters := range map[string][2]string{
		"backticks": {"````markdown", "````"},
		"tildes":    {"~~~~text", "~~~~"},
	} {
		t.Run(name, func(t *testing.T) {
			shorter := delimiters[1][:3]
			streaming := delimiters[0] + "\nNext: literal code\n" + shorter + "\nFix: still literal code\n"
			if got := focusOutputMarkdown(streaming); got != streaming {
				t.Fatalf("incomplete long fence was rewritten:\n got: %q\nwant: %q", got, streaming)
			}
			closed := streaming + delimiters[1] + "\nNext: after"
			want := streaming + delimiters[1] + "\n**Next:** after"
			if got := focusOutputMarkdown(closed); got != want {
				t.Fatalf("matching long fence handling:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestFocusOutputMarkdownDoesNotEnforcePromptCapsDestructively(t *testing.T) {
	paragraph := strings.Repeat("one complete idea remains readable; ", 40)
	value := "State: detailed\n" + paragraph + "\n1. one\n2. two\n3. three\n4. four\n5. five\n6. six\n7. seven"
	got := focusOutputMarkdown(value)
	want := "**State:** detailed\n" + paragraph + "\n1. one\n2. two\n3. three\n4. four\n5. five\n6. six\n7. seven"
	if got != want {
		t.Fatalf("renderer truncated or rewrote human content:\n got: %q\nwant: %q", got, want)
	}
}

func TestFocusOutputMarkdownKeepsHierarchyWithoutColor(t *testing.T) {
	styles := NewStyles(ThemeForName("charmtone"))
	renderer, err := newMarkdownRenderer(styles)
	if err != nil {
		t.Fatal(err)
	}
	plain := asciiProfile(t, renderer.Render(focusOutputMarkdown("Cause: timeout\nFix: retry once\nNext: inspect logs"), 48))
	for _, want := range []string{"Cause:", "Fix:", "Next:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("ASCII render lost %q hierarchy: %q", want, plain)
		}
	}
}

func TestFocusLearningErrorUsesCauseAndDeterministicFixOnly(t *testing.T) {
	got, ok := focusLearningError(errors.New("learn source: open missing.go\x1b[31m: no such file"))
	if !ok || !strings.HasPrefix(got, "Cause: learn source:") || !strings.Contains(got, "\nFix: Choose a readable") {
		t.Fatalf("learn error presentation = %q, %v", got, ok)
	}
	for _, r := range got {
		if r == '\x1b' || (unicode.IsControl(r) && r != '\n' && r != '\t') {
			t.Fatalf("learn error retained terminal control: %q", got)
		}
	}
	if got, ok := focusLearningError(errors.New("provider failed unexpectedly")); ok || got != "" {
		t.Fatalf("unknown failure received an invented fix: %q, %v", got, ok)
	}
	got, ok = focusLearningError(errors.New("learn proposal: staging is unavailable"))
	if !ok || !strings.Contains(got, "\nFix: Verify the project") {
		t.Fatalf("Learn staging recovery = %q, %v", got, ok)
	}
	got, ok = focusLearningError(errors.New("learn runner: subscription execution cannot confine embedded source access; choose a native/API model for /learn"))
	if !ok || !strings.Contains(got, "\nFix: Choose a native/API model") {
		t.Fatalf("Learn confidentiality recovery = %q, %v", got, ok)
	}
}

func TestLearnFailureRendersCauseAndFixInTUI(t *testing.T) {
	m, _ := newTestModel(t)
	msg := primaryBatchMessage(t, m.runLearn("missing-source.go", false))
	m.Update(msg)
	if len(m.messages) == 0 {
		t.Fatal("learn failure did not append a message")
	}
	last := m.messages[len(m.messages)-1]
	if last.State != "error" || !strings.HasPrefix(last.Text, "Cause: learn source:") || !strings.Contains(last.Text, "\nFix: ") {
		t.Fatalf("learn failure message = %+v", last)
	}
}

func TestAssistantMarkdownNeutralizesControlsWithoutMutatingTranscript(t *testing.T) {
	raw := "Next: inspect\n```text\n\x1b]52;c;YXR0YWNr\a\u202E\n```"
	safe := terminalSafeMarkdownText(raw)
	if strings.ContainsAny(safe, "\x1b\a\u202e") || !strings.Contains(safe, "␛]52;c;YXR0YWNr␇�") {
		t.Fatalf("safe markdown projection = %q", safe)
	}

	m, _ := newTestModel(t)
	msg := &Message{Role: "assistant", Text: raw, State: "chat"}
	view := asciiProfile(t, m.renderRoleMessage(msg, 60))
	if strings.ContainsAny(view, "\x1b\a\u202e") || !strings.Contains(view, "Next: inspect") {
		t.Fatalf("assistant render emitted unsafe controls or lost prose: %q", view)
	}
	if msg.Text != raw {
		t.Fatalf("display projection mutated transcript: %q", msg.Text)
	}
}
