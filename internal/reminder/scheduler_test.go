package reminder

import (
	"context"
	"os"
	"testing"
	"time"
	_ "time/tzdata" // ensure LoadLocation works without system zoneinfo

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
	"github.com/voriteam/pull-request-notifier/internal/slack"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	f, err := os.CreateTemp("", "pr-notifier-reminder-*.db")
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

type fakeSender struct {
	tz   map[string]*time.Location
	sent []string // slack user IDs that received a DM
}

func (f *fakeSender) GetUserTimezone(_ context.Context, userID string) *time.Location {
	return f.tz[userID]
}

func (f *fakeSender) PostDM(_ context.Context, userID string, _ []slack.Block, _ string) (string, error) {
	f.sent = append(f.sent, userID)
	return "ts-" + userID, nil
}

type fakeLister struct {
	prs map[string][]github.AssignedPR
}

func (f *fakeLister) ListReviewRequestedPRs(_ context.Context, username string) ([]github.AssignedPR, error) {
	return f.prs[username], nil
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

const (
	la = "America/Los_Angeles"
	ny = "America/New_York"
)

func somePRs() []github.AssignedPR {
	return []github.AssignedPR{{Repo: "owner/repo", Number: 1, Title: "Fix it", URL: "https://example/1"}}
}

func TestScheduler_FiresAtLocalHourAndDedupes(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sender := &fakeSender{tz: map[string]*time.Location{"U1": mustLoc(t, la)}}
	lister := &fakeLister{prs: map[string][]github.AssignedPR{"octocat": somePRs()}}
	s := New(store, lister, sender, []int{9, 13, 15}, false, time.UTC)

	// 16:02 UTC on a weekday == 09:02 PDT (hour 9, within the 5-min window).
	now := time.Date(2026, 5, 27, 16, 2, 0, 0, time.UTC)

	s.runOnce(context.Background(), now)
	if len(sender.sent) != 1 || sender.sent[0] != "U1" {
		t.Fatalf("expected one DM to U1, got %v", sender.sent)
	}

	// Second run in the same slot must not re-send.
	s.runOnce(context.Background(), now)
	if len(sender.sent) != 1 {
		t.Fatalf("expected no additional DM after dedupe, got %v", sender.sent)
	}
}

func TestScheduler_RespectsPerUserTimezone(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("la-user", "ULA", "", "", nil); err != nil {
		t.Fatalf("upsert la: %v", err)
	}
	if err := store.UpsertUserMapping("ny-user", "UNY", "", "", nil); err != nil {
		t.Fatalf("upsert ny: %v", err)
	}

	sender := &fakeSender{tz: map[string]*time.Location{
		"ULA": mustLoc(t, la),
		"UNY": mustLoc(t, ny),
	}}
	lister := &fakeLister{prs: map[string][]github.AssignedPR{
		"la-user": somePRs(),
		"ny-user": somePRs(),
	}}
	s := New(store, lister, sender, []int{9}, false, time.UTC)

	// 16:02 UTC == 09:02 PDT (fires) but 12:02 EDT (does not fire).
	now := time.Date(2026, 5, 27, 16, 2, 0, 0, time.UTC)
	s.runOnce(context.Background(), now)

	if len(sender.sent) != 1 || sender.sent[0] != "ULA" {
		t.Fatalf("expected only LA user to be reminded, got %v", sender.sent)
	}
}

func TestScheduler_SkipsWeekendsWhenWeekdaysOnly(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sender := &fakeSender{tz: map[string]*time.Location{"U1": mustLoc(t, la)}}
	lister := &fakeLister{prs: map[string][]github.AssignedPR{"octocat": somePRs()}}
	s := New(store, lister, sender, []int{9}, true, time.UTC)

	// 2026-05-30 is a Saturday; 16:02 UTC == 09:02 PDT Saturday.
	now := time.Date(2026, 5, 30, 16, 2, 0, 0, time.UTC)
	s.runOnce(context.Background(), now)

	if len(sender.sent) != 0 {
		t.Fatalf("expected no DM on weekend, got %v", sender.sent)
	}
}

func TestScheduler_OutsideWindow(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sender := &fakeSender{tz: map[string]*time.Location{"U1": mustLoc(t, la)}}
	lister := &fakeLister{prs: map[string][]github.AssignedPR{"octocat": somePRs()}}
	s := New(store, lister, sender, []int{9}, false, time.UTC)

	// 16:10 UTC == 09:10 PDT — past the 5-minute window.
	now := time.Date(2026, 5, 27, 16, 10, 0, 0, time.UTC)
	s.runOnce(context.Background(), now)

	if len(sender.sent) != 0 {
		t.Fatalf("expected no DM outside the window, got %v", sender.sent)
	}
}

func TestScheduler_NoPRsNoDMButMarked(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sender := &fakeSender{tz: map[string]*time.Location{"U1": mustLoc(t, la)}}
	lister := &fakeLister{prs: map[string][]github.AssignedPR{}} // no PRs for anyone
	s := New(store, lister, sender, []int{9}, false, time.UTC)

	now := time.Date(2026, 5, 27, 16, 2, 0, 0, time.UTC)
	s.runOnce(context.Background(), now)

	if len(sender.sent) != 0 {
		t.Fatalf("expected no DM when there are no PRs, got %v", sender.sent)
	}
	// The slot should still be marked so we don't re-query every minute.
	marked, err := store.AlreadyReminded("U1", "2026-05-27", 9)
	if err != nil {
		t.Fatalf("already reminded: %v", err)
	}
	if !marked {
		t.Fatal("expected slot to be marked even with no PRs")
	}
}

func TestScheduler_FallsBackToDefaultTZ(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertUserMapping("octocat", "U1", "", "", nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// No timezone known for U1 → falls back to the default (LA).
	sender := &fakeSender{tz: map[string]*time.Location{}}
	lister := &fakeLister{prs: map[string][]github.AssignedPR{"octocat": somePRs()}}
	s := New(store, lister, sender, []int{9}, false, mustLoc(t, la))

	now := time.Date(2026, 5, 27, 16, 2, 0, 0, time.UTC)
	s.runOnce(context.Background(), now)

	if len(sender.sent) != 1 {
		t.Fatalf("expected DM using default tz, got %v", sender.sent)
	}
}
