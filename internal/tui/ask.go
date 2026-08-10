package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/bryann2k/maestro/internal/editor"
)

type selectionContext struct {
	Source  string
	Path    string
	Start   editor.Cursor
	End     editor.Cursor
	Text    string
	Message *Message
}

type selectionMenuState struct {
	Context  *selectionContext
	Actions  []string
	Selected int
	X, Y     int
}

type chatPoint struct {
	Row int
	Col int
}

type chatRow struct {
	Message  *Message
	TextLine int
}

func newAskOverlay() *listOverlay {
	return &listOverlay{
		title: "Ask Maestro about selection",
		items: []string{
			"modify  change the selected code",
			"explain  explain the selected code",
			"comment  add or improve comments",
		},
	}
}

func newSelectionMenu(selection *selectionContext, x, y int) *selectionMenuState {
	actions := []string{"explain", "modify with Maestro", "comment", "add to context", "ask Maestro…"}
	if selection != nil && selection.Source == "ide" {
		actions = append([]string{"edit selection"}, actions...)
	}
	return &selectionMenuState{
		Context:  selection,
		Actions:  actions,
		Selected: 0,
		X:        x,
		Y:        y,
	}
}

func promptWithContext(prompt string, refs []selectionContext) string {
	if len(refs) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n<context_from_user_selection>\n")
	for i, ref := range refs {
		label := ref.Path
		if label == "" {
			label = ref.Source
		}
		fmt.Fprintf(&b, "[%d] %s lines %d-%d\n```\n%s\n```\n", i+1, label, ref.Start.Line+1, ref.End.Line+1, ref.Text)
	}
	b.WriteString("</context_from_user_selection>")
	return b.String()
}

func selectionPrompt(action string, selection *selectionContext) string {
	if selection == nil {
		return ""
	}
	verb := map[string]string{
		"modify":  "Modify",
		"explain": "Explain",
		"comment": "Comment on",
		"ask":     "Answer a question about",
	}[action]
	if verb == "" {
		runes := []rune(action)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		verb = string(runes)
	}
	label := "selected text"
	if selection.Source == "ide" {
		label = fmt.Sprintf("selected code in %s", selection.Path)
	}
	return fmt.Sprintf("%s %s (lines %d-%d). Preserve the surrounding design and explain the result.\n\n```\n%s\n```", verb, label, selection.Start.Line+1, selection.End.Line+1, selection.Text)
}

func selectionQuestionPrompt(selection *selectionContext, question string) string {
	if selection == nil {
		return question
	}
	label := "selected text"
	if selection.Source == "ide" {
		label = fmt.Sprintf("selected code in %s", selection.Path)
	}
	return fmt.Sprintf("Answer this question about %s (lines %d-%d).\n\nSelected text:\n```\n%s\n```\n\nQuestion: %s", label, selection.Start.Line+1, selection.End.Line+1, selection.Text, question)
}
