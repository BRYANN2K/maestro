package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/session"
)

func TestInteractiveStartupPreservesSessionAfterBranchChange(t *testing.T) {
	for _, phase := range []session.Phase{session.PhaseChat, session.PhaseReview, session.PhaseArchive} {
		t.Run(string(phase), func(t *testing.T) {
			dir := newTestRepo(t)
			gitRun(t, dir, "checkout", "-b", "maestro/spawn")
			sessionsDir := t.TempDir()
			previous := newWorkspaceOrchestrator(t, dir, sessionsDir)
			previous.sess.Phase = phase
			previous.sess.SpecID = "pending-change"
			previous.sess.Conversation = []session.ConversationTurn{{Role: "user", Content: "Keep the previous discussion"}}
			previous.sess.PermQueue = []session.Permission{{ID: "old-approval", Status: "approved"}}
			previous.sess.Review = &session.ReviewResult{Level: "pass", GitRef: "refs/heads/maestro/spawn"}
			if err := previous.save(); err != nil {
				t.Fatal(err)
			}
			old := previous.Session()
			if err := previous.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(sessionsDir, old.Project, old.ID+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			gitRun(t, dir, "checkout", "main")
			opts := Options{ProjectDir: dir, SessionsDir: sessionsDir, In: strings.NewReader(""), Out: &bytes.Buffer{}, Runner: &fakeRunner{}}
			if _, err := New(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "now has ref") {
				t.Fatalf("noninteractive restore must remain strict: %v", err)
			}
			opts.RecoverChangedBranch = true
			current, err := New(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			fresh := current.Session()
			if fresh.ID == old.ID || fresh.Phase != session.PhaseChat || fresh.WorkspaceRef != "refs/heads/main" || filepathKey(fresh.Worktree) != filepathKey(dir) {
				t.Fatalf("incorrect fresh session: %+v", fresh)
			}
			if fresh.SpecID != "" || fresh.Review != nil || len(fresh.PermQueue) != 0 || len(fresh.Conversation) != 0 {
				t.Fatal("old authority was transferred onto main")
			}
			if err := current.LoadSession(t.Context(), old.ID); err == nil || !strings.Contains(err.Error(), "now has ref") {
				t.Fatalf("explicit resume bypassed identity: %v", err)
			}
			if current.Session().ID != fresh.ID {
				t.Fatal("failed resume changed the current session")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("previous session was rewritten")
			}
			restarted, err := New(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if restarted.Session().ID != fresh.ID {
				t.Fatal("restart created another fresh session")
			}
		})
	}
}
