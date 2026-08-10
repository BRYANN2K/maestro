package session

import "testing"

func TestReviewFingerprintPersists(t *testing.T) {
	store := NewStore(t.TempDir())
	sess := New("review-fingerprint-project")
	sess.WorkspaceRef = "refs/heads/main"
	sess.Review = &ReviewResult{
		Level:       "pass",
		Summary:     "Review: pass",
		Fingerprint: "0123456789abcdef",
		GitRef:      "refs/heads/main",
		GitHead:     "fedcba9876543210",
	}
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context(), sess.Project, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Review == nil || loaded.Review.Fingerprint != sess.Review.Fingerprint || loaded.Review.GitRef != sess.Review.GitRef || loaded.Review.GitHead != sess.Review.GitHead || loaded.WorkspaceRef != sess.WorkspaceRef {
		t.Fatalf("loaded review = %+v, want fingerprint %q", loaded.Review, sess.Review.Fingerprint)
	}
}
