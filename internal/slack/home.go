package slack

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/voriteam/pull-request-notifier/internal/db"
	"github.com/voriteam/pull-request-notifier/internal/github"
)

// homeListLimit keeps a view of three full lists under Slack's 100-block cap.
const homeListLimit = 15

const (
	actionHomeRefresh       = "home_refresh"
	actionNotificationPrefs = "notification_prefs"
)

var notificationLabels = map[db.Notification]string{
	db.NotifyReviewRequests: "Review requests",
	db.NotifyReviews:        "Reviews on my pull requests",
	db.NotifyComments:       "Comments and mentions",
	db.NotifyCIFailures:     "CI failures on my pull requests",
	db.NotifyReminders:      "Review reminders",
	db.NotifyDeploys:        "My pull requests reaching production",
}

// HomeInput is everything the Home tab shows. A nil Tracker with a non-nil
// TrackerErr renders an error line in place of the pull request lists.
type HomeInput struct {
	Tracker    *github.Tracker
	TrackerErr error
	Org        string
	Username   string
	Prefs      map[db.Notification]bool
	Now        time.Time
}

// HomeView builds the App Home view for a linked user.
func HomeView(in HomeInput) map[string]any {
	blocks := []block{
		{
			"type": blockSection,
			"text": mrkdwn(fmt.Sprintf("Updated <!date^%d^{time}|%s>", in.Now.Unix(), in.Now.UTC().Format(time.Kitchen+" MST"))),
			"accessory": element{
				"type":      "button",
				"text":      plain("Refresh"),
				"action_id": actionHomeRefresh,
			},
		},
	}

	if in.Tracker == nil {
		blocks = append(blocks, sectionBlock(fmt.Sprintf("⚠️ Couldn't load your pull requests from GitHub. Try Refresh in a minute.\n_%s_", in.TrackerErr)))
	} else {
		direct, team, authored := github.TrackerSearches(in.Org, in.Username)
		t := in.Tracker
		blocks = append(blocks, prList("Needs your review", t.Direct, t.DirectTotal, direct, "Nothing waiting on you.", in.Now, reviewMeta)...)
		blocks = append(blocks, prList("Requested from your teams", t.Team, t.TeamTotal, team, "No team requests.", in.Now, reviewMeta)...)
		blocks = append(blocks, prList("Your pull requests", t.Authored, t.AuthoredTotal, authored, "You have no open pull requests.", in.Now, authoredMeta)...)
	}

	blocks = append(blocks, block{"type": "divider"}, headerBlock("Notifications"), notificationPrefsBlock(in.Prefs))
	return map[string]any{"type": "home", "blocks": blocks}
}

// LinkHomeView builds the App Home view for a user who hasn't linked GitHub.
func LinkHomeView(oauthURL string) map[string]any {
	return map[string]any{"type": "home", "blocks": LinkGitHubMessage(oauthURL)}
}

func headerBlock(text string) block {
	return block{"type": "header", "text": plain(text)}
}

func prList(title string, prs []github.TrackedPR, total int, query, empty string, now time.Time, meta func(github.TrackedPR, time.Time) string) []block {
	total = max(total, len(prs))
	blocks := []block{{"type": "divider"}, headerBlock(fmt.Sprintf("%s (%d)", title, total))}
	if len(prs) == 0 {
		return append(blocks, contextBlock(empty))
	}
	shown := prs
	if len(shown) > homeListLimit {
		shown = shown[:homeListLimit]
	}
	for _, pr := range shown {
		blocks = append(blocks, sectionBlock(fmt.Sprintf("*<%s|%s>*\n%s", pr.URL, pr.Title, meta(pr, now))))
	}
	if more := total - len(shown); more > 0 {
		link := "https://github.com/search?type=pullrequests&q=" + url.QueryEscape(query)
		blocks = append(blocks, contextBlock(fmt.Sprintf("<%s|+%d more on GitHub>", link, more)))
	}
	return blocks
}

func reviewMeta(pr github.TrackedPR, now time.Time) string {
	parts := []string{fmt.Sprintf("%s#%d", pr.Repo, pr.Number), pr.AuthorName, "opened " + ago(now, pr.CreatedAt), fmt.Sprintf("+%d −%d", pr.Additions, pr.Deletions)}
	if pr.Draft {
		parts = append(parts, "📝 Draft")
	}
	return strings.Join(parts, " · ")
}

func authoredMeta(pr github.TrackedPR, _ time.Time) string {
	return fmt.Sprintf("%s#%d · %s", pr.Repo, pr.Number, prStatus(pr))
}

func prStatus(pr github.TrackedPR) string {
	var parts []string
	switch {
	case pr.Draft:
		parts = append(parts, "📝 Draft")
	case pr.ReviewDecision == "APPROVED":
		parts = append(parts, "✅ Approved")
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		parts = append(parts, "🔁 Changes requested")
	case len(pr.PendingReviewers) > 0:
		parts = append(parts, "⏳ Waiting on "+formatUserList(pr.PendingReviewers))
	default:
		parts = append(parts, "No reviewers requested")
	}
	switch pr.CheckState {
	case "SUCCESS":
		parts = append(parts, "✅ CI passing")
	case "FAILURE", "ERROR":
		parts = append(parts, "❌ CI failing")
	case "PENDING", "EXPECTED":
		parts = append(parts, "🟡 CI running")
	}
	if pr.Conflicting {
		parts = append(parts, "⚠️ Merge conflict")
	}
	return strings.Join(parts, " · ")
}

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func notificationPrefsBlock(prefs map[db.Notification]bool) block {
	var options, initial []textObj
	for _, n := range db.AllNotifications {
		opt := textObj{"text": plain(notificationLabels[n]), "value": string(n)}
		options = append(options, opt)
		if prefs[n] {
			initial = append(initial, opt)
		}
	}
	checkboxes := element{"type": "checkboxes", "action_id": actionNotificationPrefs, "options": options}
	// Slack rejects an empty initial_options array.
	if len(initial) > 0 {
		checkboxes["initial_options"] = initial
	}
	return block{"type": blockActions, "elements": []element{checkboxes}}
}
