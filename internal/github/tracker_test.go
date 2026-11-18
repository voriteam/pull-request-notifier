package github

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestListTrackedPRs(t *testing.T) {
	c := newFixtureClient(t, map[string]string{"/graphql": "tracker.json"})
	c.installOrg = "voriteam"

	got, org, err := c.ListTrackedPRs(context.Background(), "octocat", TrackerOptions{ReviewBot: "review-bot[bot]"})
	if err != nil {
		t.Fatal(err)
	}
	if org != "voriteam" {
		t.Errorf("org %q", org)
	}

	if got.DirectTotal != 1 || got.TeamTotal != 1 || got.AuthoredTotal != 40 {
		t.Errorf("totals: %d %d %d", got.DirectTotal, got.TeamTotal, got.AuthoredTotal)
	}
	if len(got.Direct) != 1 {
		t.Fatalf("direct: %+v", got.Direct)
	}
	d := got.Direct[0]
	if d.Repo != "voriteam/backend" || d.Number != 812 || d.AuthorName != "Jane Doe" || d.CheckState != "PENDING" ||
		d.Additions != 120 || d.UpdatedAt.IsZero() || len(d.PendingReviewers) != 1 || d.PendingReviewers[0] != "octocat" {
		t.Errorf("direct PR: %+v", d)
	}

	if len(got.Team) != 1 || got.Team[0].Number != 77 {
		t.Fatalf("team should exclude direct requests: %+v", got.Team)
	}
	if tm := got.Team[0]; tm.AuthorName != "sam" || len(tm.PendingReviewers) != 1 || tm.PendingReviewers[0] != "Frontend" {
		t.Errorf("team PR: %+v", tm)
	}

	if len(got.Authored) != 1 {
		t.Fatalf("authored: %+v", got.Authored)
	}
	if a := got.Authored[0]; !a.Conflicting || a.ReviewDecision != "APPROVED" || a.CheckState != "" {
		t.Errorf("authored PR: %+v", a)
	}
	if a := got.Authored[0]; a.UnresolvedThreads != 1 || a.BotVerdict != VerdictApprove || !a.BotVerdictStale {
		t.Errorf("authored PR review state: threads %d, verdict %q, stale %v", a.UnresolvedThreads, a.BotVerdict, a.BotVerdictStale)
	}
}

func TestListTrackedPRs_GraphQLErrors(t *testing.T) {
	c := newFixtureClient(t, map[string]string{"/graphql": "tracker_errors.json"})
	c.installOrg = "voriteam"

	_, _, err := c.ListTrackedPRs(context.Background(), "octocat", TrackerOptions{ReviewBot: "review-bot[bot]"})
	if err == nil || !strings.Contains(err.Error(), "rate limited; try later") {
		t.Fatalf("err = %v", err)
	}
}

func TestBotVerdict(t *testing.T) {
	cases := []struct{ state, body, want string }{
		{"APPROVED", "", VerdictApprove},
		{"CHANGES_REQUESTED", "", VerdictRequestChanges},
		{"COMMENTED", "✅ APPROVE\n\nLooks good, though REQUEST_CHANGES appears later.", VerdictApprove},
		{"COMMENTED", "  🔄 REQUEST_CHANGES\nFix it.", VerdictRequestChanges},
		{"COMMENTED", "Just a note.\nI would APPROVE after a fix.", ""},
	}
	for _, tc := range cases {
		if got := botVerdict(tc.state, tc.body); got != tc.want {
			t.Errorf("botVerdict(%q, %q) = %q, want %q", tc.state, tc.body, got, tc.want)
		}
	}
}

func TestListTrackedPRs_NoReviewBot(t *testing.T) {
	c := newFixtureClient(t, map[string]string{"/graphql": "tracker.json"})
	c.installOrg = "voriteam"

	got, _, err := c.ListTrackedPRs(context.Background(), "octocat", TrackerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Authored[0].BotVerdict; v != "" {
		t.Errorf("verdict without a configured bot: %q", v)
	}
}

func TestListTrackedPRs_IgnoredChecks(t *testing.T) {
	c := newFixtureClient(t, map[string]string{"/graphql": "tracker.json"})
	c.installOrg = "voriteam"

	got, _, err := c.ListTrackedPRs(context.Background(), "octocat", TrackerOptions{IgnoredChecks: regexp.MustCompile("^policy-bot")})
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Direct[0].CheckState; s != "SUCCESS" {
		t.Errorf("check state with policy-bot ignored: %q", s)
	}
}

func TestCheckState(t *testing.T) {
	rollup := func(state string, truncated bool, cs ...gqlCheckContext) *gqlRollup {
		r := &gqlRollup{State: state}
		r.Contexts.PageInfo.HasNextPage = truncated
		r.Contexts.Nodes = cs
		return r
	}
	passed := gqlCheckContext{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}
	skipped := gqlCheckContext{Name: "deploy", Status: "COMPLETED", Conclusion: "SKIPPED"}
	running := gqlCheckContext{Name: "test", Status: "IN_PROGRESS"}
	timedOut := gqlCheckContext{Name: "e2e", Status: "COMPLETED", Conclusion: "TIMED_OUT"}
	policy := gqlCheckContext{Context: "policy-bot: main", State: "PENDING"}
	statusFailed := gqlCheckContext{Context: "lint", State: "ERROR"}
	ignored := regexp.MustCompile("^policy-bot")

	cases := []struct {
		name    string
		rollup  *gqlRollup
		ignored *regexp.Regexp
		want    string
	}{
		{"no rollup", nil, ignored, ""},
		{"no pattern keeps GitHub's state", rollup("PENDING", false, passed, policy), nil, "PENDING"},
		{"truncated keeps GitHub's state", rollup("PENDING", true, passed, policy), ignored, "PENDING"},
		{"ignored pending check", rollup("PENDING", false, passed, skipped, policy), ignored, "SUCCESS"},
		{"running check", rollup("PENDING", false, passed, running, policy), ignored, "PENDING"},
		{"failure beats pending", rollup("FAILURE", false, running, timedOut), ignored, "FAILURE"},
		{"failed status", rollup("FAILURE", false, passed, statusFailed), ignored, "FAILURE"},
		{"only ignored checks", rollup("PENDING", false, policy), ignored, ""},
	}
	for _, tc := range cases {
		if got := checkState(tc.rollup, tc.ignored); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
