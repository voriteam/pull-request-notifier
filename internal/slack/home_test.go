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
		{github.TrackedPR{Draft: true}, "📝 Draft"},
		{github.TrackedPR{ReviewDecision: "APPROVED", CheckState: "SUCCESS"}, "✅ Approved · ✅ CI passing"},
		{github.TrackedPR{ReviewDecision: "CHANGES_REQUESTED", CheckState: "ERROR"}, "🔁 Changes requested · ❌ CI failing"},
		{github.TrackedPR{PendingReviewers: []string{"Sam", "Ana"}, CheckState: "PENDING"}, "⏳ Waiting on Sam and Ana · 🟡 CI running"},
		{github.TrackedPR{ReviewDecision: "REVIEW_REQUIRED", Conflicting: true}, "👤 Needs approval · ⚠️ Merge conflict"},
		{github.TrackedPR{BotVerdict: github.VerdictApprove, UnresolvedThreads: 2}, "🤖 Approved · 💬 2 unresolved"},
		{github.TrackedPR{BotVerdict: github.VerdictRequestChanges, BotVerdictStale: true}, "🤖 Changes requested (older commit)"},
	}
	for _, tc := range cases {
		if got := prStatus(tc.pr); got != tc.want {
			t.Errorf("prStatus(%+v) = %q, want %q", tc.pr, got, tc.want)
		}
	}
}

func TestReadyToMerge(t *testing.T) {
	ready := github.TrackedPR{CheckState: "SUCCESS", ReviewDecision: "REVIEW_REQUIRED", BotVerdict: github.VerdictApprove}
	cases := []struct {
		name string
		edit func(*github.TrackedPR)
		want bool
	}{
		{"bot approved current head, only human approval missing", func(*github.TrackedPR) {}, true},
		{"no bot review", func(p *github.TrackedPR) { p.BotVerdict = "" }, true},
		{"bot approval on an older commit", func(p *github.TrackedPR) { p.BotVerdictStale = true }, false},
		{"bot requested changes", func(p *github.TrackedPR) { p.BotVerdict = github.VerdictRequestChanges }, false},
		{"unresolved thread", func(p *github.TrackedPR) { p.UnresolvedThreads = 1 }, false},
		{"CI running", func(p *github.TrackedPR) { p.CheckState = "PENDING" }, false},
		{"draft", func(p *github.TrackedPR) { p.Draft = true }, false},
		{"merge conflict", func(p *github.TrackedPR) { p.Conflicting = true }, false},
		{"human requested changes", func(p *github.TrackedPR) { p.ReviewDecision = "CHANGES_REQUESTED" }, false},
	}
	for _, tc := range cases {
		pr := ready
		tc.edit(&pr)
		if got := readyToMerge(pr); got != tc.want {
			t.Errorf("%s: readyToMerge = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuthoredMeta(t *testing.T) {
	pr := github.TrackedPR{Repo: "o/r", Number: 7, Additions: 12, Deletions: 3, ReviewDecision: "APPROVED"}
	if got, want := authoredMeta(pr, time.Time{}), "o/r#7 · +12 −3 · ✅ Approved"; got != want {
		t.Errorf("authoredMeta = %q, want %q", got, want)
	}
}

func TestHomeView(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var many []github.TrackedPR
	for i := 1; i <= homeListLimit+3; i++ {
		many = append(many, github.TrackedPR{Repo: "o/r", Number: i, Title: fmt.Sprintf("PR %d", i), URL: "u", AuthorName: "Jane", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-20 * time.Minute)})
	}
	view := HomeView(HomeInput{
		Tracker:  &github.Tracker{Direct: many, DirectTotal: 30},
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
		"Needs your review (30)", "+15 more on GitHub", "o/r#1 · Jane · opened 3h ago · updated 20m ago · +0 −0",
		"Requested from your teams (0)", "No team requests.", "Ready to merge (0)", "Nothing ready yet.", "You have no other open pull requests.",
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
