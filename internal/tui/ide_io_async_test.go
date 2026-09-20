package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
)

func TestDeferredIDEHydrationIsCancellableAndPublishesNoStaleState(t *testing.T) {
	original := ideEditorHydrator
	started := make(chan struct{})
	ideEditorHydrator = func(ctx context.Context, _ ideHydrationPlan) (*editor.Editor, string, bool, error) {
		close(started)
		<-ctx.Done()
		return nil, "", false, ctx.Err()
	}
	defer func() { ideEditorHydrator = original }()

	m, _ := newTestModel(t)
	begin := time.Now()
	cmd := m.ToggleIDE()
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("ToggleIDE blocked for %v", elapsed)
	}
	if cmd == nil || m.ide == nil || !m.ide.hydrating {
		t.Fatalf("deferred hydration state: cmd=%v ide=%v hydrating=%v", cmd != nil, m.ide != nil, m.ide != nil && m.ide.hydrating)
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("hydration command did not start")
	}
	target := m.ide
	m.closeIDE()
	raw := <-result
	msg, ok := raw.(ideOperationMsg)
	if !ok || !errors.Is(msg.err, context.Canceled) {
		t.Fatalf("cancelled hydration = %#v (%T)", raw, raw)
	}
	if next := m.finishIDEOperation(msg); next != nil {
		t.Fatal("stale hydration scheduled follow-up work")
	}
	if m.ide != nil || target.Ed.Buffer().Path != "untitled" {
		t.Fatal("closed IDE accepted stale hydration state")
	}
}

func TestDeferredIDEHydrationDoesNotOverwriteAnEditedShell(t *testing.T) {
	original := ideEditorHydrator
	release := make(chan struct{})
	ideEditorHydrator = func(ctx context.Context, plan ideHydrationPlan) (*editor.Editor, string, bool, error) {
		select {
		case <-release:
			loaded := editor.NewEditor(plan.project)
			loaded.Buffers = []*editor.Buffer{editor.NewBuffer(filepath.Join(plan.project, "restored.go"), []byte("restored\n"))}
			return loaded, "", false, nil
		case <-ctx.Done():
			return nil, "", false, ctx.Err()
		}
	}
	defer func() { ideEditorHydrator = original }()

	m, _ := newTestModel(t)
	cmd := m.ToggleIDE()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	m.ide.Ed.Buffer().InsertText("keep this edit")
	close(release)
	msg := (<-result).(ideOperationMsg)
	m.finishIDEOperation(msg)
	if got := m.ide.Ed.Buffer().String(); got != "keep this edit\n" {
		t.Fatalf("hydration overwrote live editor shell: %q", got)
	}
}

func TestDeferredIDEHydrationAcknowledgesCrashOnlyAfterPublication(t *testing.T) {
	m, project := newTestModel(t)
	m.activeTab = TabIDE
	m.ide = newDeferredIDE(m, project, git.New(project))
	m.ide.hydration.sessionDir = t.TempDir()

	source := editor.NewEditor(project)
	source.Crash.SetDir(m.ide.hydration.sessionDir)
	dirty := editor.NewBuffer(filepath.Join(project, "recover.txt"), []byte("recover me\n"))
	dirty.Dirty = true
	source.Buffers = []*editor.Buffer{dirty}
	if err := source.Crash.Save(source); err != nil {
		t.Fatalf("save crash recovery: %v", err)
	}
	crashPath := filepath.Join(m.ide.hydration.sessionDir, "crash.json")

	cmd := m.beginIDEHydration(m.ide)
	msg, ok := cmd().(ideOperationMsg)
	if !ok || !msg.ackCrash {
		t.Fatalf("hydration = %#v, want crash acknowledgement", msg)
	}
	if _, err := os.Stat(crashPath); err != nil {
		t.Fatalf("worker consumed crash state before publication: %v", err)
	}

	m.ide.Ed.Buffer().InsertText("live edit")
	runIDEEffect(t, m, m.finishIDEOperation(msg))
	if _, err := os.Stat(crashPath); err != nil {
		t.Fatalf("rejected hydration consumed crash state: %v", err)
	}

	m.ide.Ed = editor.NewEditor(project)
	m.ide.Ed.Buffers = []*editor.Buffer{editor.NewBuffer("untitled", nil)}
	cmd = m.beginIDEHydration(m.ide)
	msg, ok = cmd().(ideOperationMsg)
	if !ok || !msg.ackCrash {
		t.Fatalf("second hydration = %#v, want crash acknowledgement", msg)
	}
	runIDEEffect(t, m, m.finishIDEOperation(msg))
	if _, err := os.Stat(crashPath); !os.IsNotExist(err) {
		t.Fatalf("published crash recovery was not acknowledged: %v", err)
	}
}

func TestCommandFileOpenRunsInEffectAndLatestRequestWins(t *testing.T) {
	original := ideFileLoader
	started := make(chan struct{})
	ideFileLoader = func(ctx context.Context, path string) (*editor.Buffer, error) {
		if strings.HasSuffix(path, "slow.txt") {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return editor.NewBuffer(path, []byte("newest\n")), nil
	}
	defer func() { ideFileLoader = original }()

	m, project := newTestModel(t)
	m.activeTab = TabIDE
	m.ide = newDeferredIDE(m, project, git.New(project))

	first := enterIDECommand(t, m, "e slow.txt")
	if first == nil {
		t.Fatal(":e did not return a tea.Cmd")
	}
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- first() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("deferred file loader did not start")
	}

	second := enterIDECommand(t, m, "e newest.txt")
	if second == nil {
		t.Fatal("second :e did not return a tea.Cmd")
	}
	secondMsg := second().(ideOperationMsg)
	_, next := m.Update(secondMsg)
	if next == nil {
		t.Fatal("accepted open did not schedule a gutter refresh")
	}
	if got := filepath.Base(m.ide.Ed.Buffer().Path); got != "newest.txt" {
		t.Fatalf("active buffer = %q, want newest.txt", got)
	}
	runIDEEffect(t, m, next)
	if m.ide.filesLoading {
		t.Fatal("file open superseded hydration without completing the workspace snapshot")
	}

	stale := (<-firstResult).(ideOperationMsg)
	if !errors.Is(stale.err, context.Canceled) {
		t.Fatalf("superseded open error = %v, want context.Canceled", stale.err)
	}
	m.finishIDEOperation(stale)
	if got := filepath.Base(m.ide.Ed.Buffer().Path); got != "newest.txt" {
		t.Fatalf("stale open replaced active buffer with %q", got)
	}
}

func TestHunkStageRunsInCancellableEffect(t *testing.T) {
	original := ideHunkStager
	started := make(chan struct{})
	ideHunkStager = func(ctx context.Context, _ *git.Client, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() { ideHunkStager = original }()

	m, project := newTestModel(t)
	m.activeTab = TabIDE
	m.ide = newDeferredIDE(m, project, git.New(project))
	m.ide.Ed.Buffers = []*editor.Buffer{editor.NewBuffer(filepath.Join(project, "target.txt"), []byte("one\n"))}
	m.ide.Ed.CurBuf = 0

	begin := time.Now()
	cmd := enterIDECommand(t, m, "hunk stage")
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf(":hunk stage blocked Update for %v", elapsed)
	}
	if cmd == nil {
		t.Fatal(":hunk stage did not return a tea.Cmd")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("hunk stage command did not start")
	}
	m.ide.cancelOperation()
	msg := (<-result).(ideOperationMsg)
	if !errors.Is(msg.err, context.Canceled) {
		t.Fatalf("cancelled hunk stage error = %v", msg.err)
	}
	m.finishIDEOperation(msg)
	if m.ide.Ed.Status == "hunks staged" {
		t.Fatal("cancelled hunk stage published success")
	}
}

func TestHunkStageCompletionPublishesOnModelUpdate(t *testing.T) {
	original := ideHunkStager
	ideHunkStager = func(context.Context, *git.Client, string) error { return nil }
	defer func() { ideHunkStager = original }()

	m, project := newTestModel(t)
	m.activeTab = TabIDE
	m.ide = newDeferredIDE(m, project, git.New(project))
	path := filepath.Join(project, "target.txt")
	m.ide.Ed.Buffers = []*editor.Buffer{editor.NewBuffer(path, []byte("one\n"))}
	m.ide.Ed.CurBuf = 0

	cmd := enterIDECommand(t, m, "hunk stage")
	if cmd == nil {
		t.Fatal(":hunk stage did not return a tea.Cmd")
	}
	msg := cmd().(ideOperationMsg)
	_, next := m.Update(msg)
	if m.ide.Ed.Status != "hunks staged" || next == nil {
		t.Fatalf("stage completion: status=%q follow-up=%v", m.ide.Ed.Status, next != nil)
	}
}

func TestEditorSymbolCacheIsRevisionAwareAndBounded(t *testing.T) {
	b := editor.NewBuffer("large.go", []byte("func TooFar() {}\n"+strings.Repeat("line\n", editorSymbolLookback+32)))
	b.Cur.Line = len(b.Lines) - 1
	ide := &IDEState{Ed: editor.NewEditor(".")}
	ide.Ed.Buffers = []*editor.Buffer{b}
	if got := editorSymbol(ide); got != "" {
		t.Fatalf("bounded symbol scan reached distant declaration %q", got)
	}
	firstCache := ide.symbolCache
	if got := editorSymbol(ide); got != "" || ide.symbolCache != firstCache {
		t.Fatalf("stable frame missed cache: symbol=%q cache=%+v", got, ide.symbolCache)
	}

	b = editor.NewBuffer("small.go", []byte("func Old() {}\n"))
	ide.Ed.Buffers = []*editor.Buffer{b}
	if got := editorSymbol(ide); got != "Old" {
		t.Fatalf("initial symbol = %q", got)
	}
	b.Cur.Col = len("func ")
	b.InsertText("New")
	if got := editorSymbol(ide); got != "NewOld" {
		t.Fatalf("symbol after revision = %q, want NewOld", got)
	}
}

var benchmarkEditorSymbol string

func BenchmarkEditorSymbolCachedLargeBuffer(b *testing.B) {
	lines := make([]string, 100_000)
	for i := range lines {
		lines[i] = "ordinary source line"
	}
	lines[len(lines)-8] = "func Nearby() {}"
	buffer := editor.NewBuffer("large.go", []byte(strings.Join(lines, "\n")))
	buffer.Cur.Line = len(buffer.Lines) - 1
	ide := &IDEState{Ed: editor.NewEditor(".")}
	ide.Ed.Buffers = []*editor.Buffer{buffer}
	benchmarkEditorSymbol = editorSymbol(ide) // warm the revision-aware cache
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkEditorSymbol = editorSymbol(ide)
	}
}

func enterIDECommand(t *testing.T, m *Model, command string) tea.Cmd {
	t.Helper()
	if m.ide == nil {
		t.Fatal("IDE is nil")
	}
	m.ide.Ed.SetKeymap("vim")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	for _, r := range command {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg = tea.KeyMsg{Type: tea.KeySpace}
		}
		m.Update(msg)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return cmd
}

func finishIDEHydrationForTest(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("IDE hydration command is nil")
	}
	raw := cmd()
	msg, ok := raw.(ideOperationMsg)
	if !ok || msg.kind != ideOperationHydrate {
		t.Fatalf("IDE hydration returned %T (%#v)", raw, raw)
	}
	return m.finishIDEOperation(msg)
}

// runIDEEffect drives only the bounded IDE command graph. It deliberately
// bypasses Model.arm so unit tests do not execute unrelated timers or pumps.
func runIDEEffect(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	raw := cmd()
	switch msg := raw.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, child := range msg {
			runIDEEffect(t, m, child)
		}
	case ideOperationMsg:
		runIDEEffect(t, m, m.finishIDEOperation(msg))
	case modFilesMsg:
		runIDEEffect(t, m, m.finishModifiedFilesRefresh(msg))
	case ideGutterLoadedMsg:
		runIDEEffect(t, m, m.finishIDEGutterRefresh(msg))
	default:
		t.Fatalf("unexpected IDE effect message %T", raw)
	}
}
