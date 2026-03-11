package db_test

import (
	"os"
	"testing"

	"github.com/voriteam/pull-request-notifier/internal/db"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	f, err := os.CreateTemp("", "pr-notifier-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(path) })

	store, err := db.New(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUserMapping(t *testing.T) {
	store := newTestStore(t)

	// Upsert a mapping.
	if err := store.UpsertUserMapping("octocat", "U123456", "ghs_token"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Look it up by GitHub username.
	slackID, err := store.GetMappingByGitHubUsername("octocat")
	if err != nil {
		t.Fatalf("get by github: %v", err)
	}
	if slackID != "U123456" {
		t.Errorf("got slack_user_id %q, want U123456", slackID)
	}

	// Look it up by Slack user ID.
	mapping, err := store.GetMappingBySlackUserID("U123456")
	if err != nil {
		t.Fatalf("get by slack: %v", err)
	}
	if mapping == nil {
		t.Fatal("expected mapping, got nil")
	}
	if mapping.GitHubUsername != "octocat" {
		t.Errorf("got github_username %q, want octocat", mapping.GitHubUsername)
	}
	if mapping.GitHubToken != "ghs_token" {
		t.Errorf("got github_token %q, want ghs_token", mapping.GitHubToken)
	}

	// Upsert again to update the token.
	if err := store.UpsertUserMapping("octocat", "U123456", "ghs_new_token"); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	mapping, _ = store.GetMappingBySlackUserID("U123456")
	if mapping.GitHubToken != "ghs_new_token" {
		t.Errorf("token not updated, got %q", mapping.GitHubToken)
	}

	// Unknown username returns empty string.
	unknown, err := store.GetMappingByGitHubUsername("unknown-user")
	if err != nil {
		t.Fatalf("unknown get: %v", err)
	}
	if unknown != "" {
		t.Errorf("expected empty string for unknown user, got %q", unknown)
	}
}

func TestOAuthState(t *testing.T) {
	store := newTestStore(t)

	if err := store.SaveOAuthState("state-abc", "U999"); err != nil {
		t.Fatalf("save state: %v", err)
	}

	// Consume it.
	slackID, err := store.ConsumeOAuthState("state-abc")
	if err != nil {
		t.Fatalf("consume state: %v", err)
	}
	if slackID != "U999" {
		t.Errorf("got %q, want U999", slackID)
	}

	// Consuming again returns empty (state was deleted).
	slackID2, err := store.ConsumeOAuthState("state-abc")
	if err != nil {
		t.Fatalf("consume again: %v", err)
	}
	if slackID2 != "" {
		t.Errorf("expected empty on second consume, got %q", slackID2)
	}

	// Unknown state returns empty.
	slackID3, err := store.ConsumeOAuthState("nonexistent")
	if err != nil {
		t.Fatalf("unknown consume: %v", err)
	}
	if slackID3 != "" {
		t.Errorf("expected empty for unknown state, got %q", slackID3)
	}
}

func TestPRMessages(t *testing.T) {
	store := newTestStore(t)

	info := db.PRInfo{Author: "alice", Title: "Test PR", URL: "https://github.com/owner/repo/pull/42", FilesChanged: 3, Additions: 10, Deletions: 5}
	if err := store.SavePRMessage("owner/repo", 42, "U111", "ts-001", info); err != nil {
		t.Fatalf("save pr message: %v", err)
	}
	if err := store.SavePRMessage("owner/repo", 42, "U222", "ts-002", info); err != nil {
		t.Fatalf("save pr message 2: %v", err)
	}

	msgs, err := store.GetPRMessages("owner/repo", 42)
	if err != nil {
		t.Fatalf("get pr messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// No messages for a different PR.
	msgs2, err := store.GetPRMessages("owner/repo", 99)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if len(msgs2) != 0 {
		t.Errorf("expected 0 messages for unknown PR, got %d", len(msgs2))
	}
}

func TestCommentMessages(t *testing.T) {
	store := newTestStore(t)

	if err := store.SaveCommentMessage("owner/repo", 42, 1001, "review_comment", "U333", "ts-100"); err != nil {
		t.Fatalf("save comment message: %v", err)
	}

	cm, err := store.GetCommentMessage("ts-100")
	if err != nil {
		t.Fatalf("get comment message: %v", err)
	}
	if cm == nil {
		t.Fatal("expected comment message, got nil")
	}
	if cm.Repo != "owner/repo" || cm.PRNumber != 42 || cm.CommentID != 1001 {
		t.Errorf("unexpected comment message: %+v", cm)
	}
	if cm.CommentType != "review_comment" {
		t.Errorf("unexpected comment type: %q", cm.CommentType)
	}

	// Unknown TS returns nil.
	cm2, err := store.GetCommentMessage("nonexistent-ts")
	if err != nil {
		t.Fatalf("get unknown: %v", err)
	}
	if cm2 != nil {
		t.Errorf("expected nil for unknown ts, got %+v", cm2)
	}
}
