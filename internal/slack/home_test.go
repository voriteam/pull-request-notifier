package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
)

func TestPRStatus(t *testing.T) {
	cases := []struct {
		pr   github.TrackedPR
		want string
	}{
		{github.TrackedPR{Draft: true, ReviewDecision: "APPROVED"}, "📝 Draft"},
		{github.TrackedPR{ReviewDecision: "APPROVED", CheckState: "SUCCESS"}, "✅ Approved · ✅ CI passing"},
		{github.TrackedPR{ReviewDecision: "CHANGES_REQUESTED", CheckState: "ERROR"}, "🔁 Changes requested · ❌ CI failing"},
		{github.TrackedPR{PendingReviewers: []string{"Sam", "Ana"}, CheckState: "PENDING"}, "⏳ Waiting on Sam and Ana · 🟡 CI running"},
		{github.TrackedPR{Conflicting: true}, "No reviewers requested · ⚠️ Merge conflict"},
	}
	for _, tc := range cases {
		if got := prStatus(tc.pr); got != tc.want {
			t.Errorf("prStatus(%+v) = %q, want %q", tc.pr, got, tc.want)
		}
	}
}

func TestHomeView(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var many []github.TrackedPR
	for i := 1; i <= homeListLimit+3; i++ {
		many = append(many, github.TrackedPR{Repo: "o/r", Number: i, Title: fmt.Sprintf("PR %d", i), URL: "u", AuthorName: "Jane", CreatedAt: now.Add(-3 * time.Hour)})
	}
	view := HomeView(HomeInput{
		Tracker:  &github.Tracker{Direct: many},
		Org:      "o",
		Username: "octocat",
		Prefs:    map[db.Notification]bool{db.NotifyReviewRequests: true},
		Now:      now,
	})
	blocks := view["blocks"].([]block)
	if len(blocks) > 100 {
		t.Errorf("%d blocks exceeds Slack's limit", len(blocks))
	}
	out := mustJSON(t, view)
	for _, want := range []string{
		"Needs your review (18)", "+3 more on GitHub", "o/r#1 · Jane · opened 3h ago",
		"Requested from your teams (0)", "No team requests.", "You have no open pull requests.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if strings.Contains(out, "PR 16") {
		t.Error("view shows more than the list limit")
	}

	prefs := blocks[len(blocks)-1]["elements"].([]element)[0]
	initial := prefs["initial_options"].([]textObj)
	if len(initial) != 1 || initial[0]["value"] != string(db.NotifyReviewRequests) {
		t.Errorf("initial_options = %v", initial)
	}
}

func TestHomeView_NoPrefsOmitsInitialOptions(t *testing.T) {
	view := HomeView(HomeInput{TrackerErr: errors.New("boom"), Prefs: map[db.Notification]bool{}, Now: time.Now()})
	blocks := view["blocks"].([]block)
	if _, ok := blocks[len(blocks)-1]["elements"].([]element)[0]["initial_options"]; ok {
		t.Error("empty selection must omit initial_options")
	}
	if out := mustJSON(t, view); !strings.Contains(out, "Couldn't load your pull requests") {
		t.Error("tracker error not shown")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
