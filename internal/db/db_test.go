package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

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

	expiry := time.Now().Add(8 * time.Hour).Truncate(time.Second)

	// Upsert a mapping with refresh token and expiry.
	if err := store.UpsertUserMapping("octocat", "U123456", "ghs_token", "ghr_refresh", &expiry); err != nil {
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
	if mapping.RefreshToken != "ghr_refresh" {
		t.Errorf("got refresh_token %q, want ghr_refresh", mapping.RefreshToken)
	}
	if mapping.TokenExpiresAt == nil {
		t.Fatal("expected token_expires_at, got nil")
	}
	if !mapping.TokenExpiresAt.Truncate(time.Second).Equal(expiry) {
		t.Errorf("got token_expires_at %v, want %v", mapping.TokenExpiresAt, expiry)
	}

	// Upsert again to update the token.
	if err := store.UpsertUserMapping("octocat", "U123456", "ghs_new_token", "ghr_new_refresh", nil); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	mapping, _ = store.GetMappingBySlackUserID("U123456")
	if mapping.GitHubToken != "ghs_new_token" {
		t.Errorf("token not updated, got %q", mapping.GitHubToken)
	}
	if mapping.RefreshToken != "ghr_new_refresh" {
		t.Errorf("refresh token not updated, got %q", mapping.RefreshToken)
	}
	if mapping.TokenExpiresAt != nil {
		t.Errorf("expected nil token_expires_at after update with nil, got %v", mapping.TokenExpiresAt)
	}

	// Upsert with no refresh token (empty string).
	if err := store.UpsertUserMapping("octocat2", "U789", "ghs_tok2", "", nil); err != nil {
		t.Fatalf("upsert no refresh: %v", err)
	}
	mapping2, _ := store.GetMappingBySlackUserID("U789")
	if mapping2.RefreshToken != "" {
		t.Errorf("expected empty refresh_token, got %q", mapping2.RefreshToken)
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

func TestReminderSends(t *testing.T) {
	store := newTestStore(t)

	// Initially not reminded.
	sent, err := store.AlreadyReminded("U1", "2026-05-27", 9)
	if err != nil {
		t.Fatalf("already reminded: %v", err)
	}
	if sent {
		t.Fatal("expected not reminded initially")
	}

	// Mark sent, then it should report reminded.
	if err := store.MarkReminderSent("U1", "2026-05-27", 9); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	sent, err = store.AlreadyReminded("U1", "2026-05-27", 9)
	if err != nil {
		t.Fatalf("already reminded: %v", err)
	}
	if !sent {
		t.Fatal("expected reminded after mark")
	}

	// Marking the same slot again is a no-op (idempotent).
	if err := store.MarkReminderSent("U1", "2026-05-27", 9); err != nil {
		t.Fatalf("mark sent again: %v", err)
	}

	// A different hour, date, or user is a distinct slot.
	for _, c := range []struct {
		user, date string
		hour       int
	}{
		{"U1", "2026-05-27", 13},
		{"U1", "2026-05-28", 9},
		{"U2", "2026-05-27", 9},
	} {
		sent, err := store.AlreadyReminded(c.user, c.date, c.hour)
		if err != nil {
			t.Fatalf("already reminded %+v: %v", c, err)
		}
		if sent {
			t.Errorf("expected distinct slot %+v to be unreminded", c)
		}
	}
}

func TestNotificationPrefs(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatal(err)
	}

	prefs, err := store.GetNotificationPrefs("U1")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range db.AllNotifications {
		if want := n != db.NotifyDeploys; prefs[n] != want {
			t.Errorf("default %s = %v, want %v", n, prefs[n], want)
		}
	}

	on := map[db.Notification]bool{db.NotifyReviewRequests: true, db.NotifyDeploys: true}
	if err := store.SetNotificationPrefs("U1", on); err != nil {
		t.Fatal(err)
	}
	prefs, err = store.GetNotificationPrefs("U1")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range db.AllNotifications {
		if prefs[n] != on[n] {
			t.Errorf("after set %s = %v, want %v", n, prefs[n], on[n])
		}
	}

	if got, _ := store.GetNotifySlackID("octocat", db.NotifyReviewRequests); got != "U1" {
		t.Errorf("enabled notification: got %q, want U1", got)
	}
	if got, _ := store.GetNotifySlackID("octocat", db.NotifyComments); got != "" {
		t.Errorf("muted notification: got %q, want empty", got)
	}
	if got, _ := store.GetNotifySlackID("nobody", db.NotifyReviewRequests); got != "" {
		t.Errorf("unlinked user: got %q, want empty", got)
	}
	if _, err := store.GetNotifySlackID("octocat", "user_mappings; --"); err == nil {
		t.Error("unknown notification: want error")
	}
}

func TestClaimDelivery(t *testing.T) {
	store := newTestStore(t)
	for i, want := range []bool{true, false} {
		got, err := store.ClaimDelivery("guid-1")
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if got != want {
			t.Errorf("claim %d = %v, want %v", i, got, want)
		}
	}
	if got, _ := store.ClaimDelivery("guid-2"); !got {
		t.Error("a different guid should claim")
	}
}

func TestRedeliveryCount(t *testing.T) {
	store := newTestStore(t)
	for want := range 3 {
		if n, err := store.RedeliveryCount("guid-1"); err != nil || n != want {
			t.Fatalf("count = %d, %v, want %d", n, err, want)
		}
		if err := store.RecordRedelivery("guid-1"); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if n, _ := store.RedeliveryCount("guid-2"); n != 0 {
		t.Errorf("other guid count = %d", n)
	}
}

func TestMigrate_AddsRedeliveryRequestsToAnExistingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE webhook_redeliveries (guid TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO webhook_redeliveries (guid) VALUES ('guid-1')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := db.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordRedelivery("guid-1"); err != nil {
		t.Fatal(err)
	}
	if n, err := store.RedeliveryCount("guid-1"); err != nil || n != 1 {
		t.Errorf("count = %d, %v, want 1", n, err)
	}
}
