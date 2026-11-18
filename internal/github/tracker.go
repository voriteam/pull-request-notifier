package github

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Tracker is a user's open pull request workload.
type Tracker struct {
	Direct   []TrackedPR // the user is personally requested
	Team     []TrackedPR // only a team the user belongs to is requested
	Authored []TrackedPR

	// Totals count every match on GitHub; the lists above hold at most 25.
	DirectTotal, TeamTotal, AuthoredTotal int
}

// TrackedPR is an open pull request with its review and CI state.
type TrackedPR struct {
	Repo             string
	Number           int
	Title            string
	URL              string
	AuthorName       string
	Additions        int
	Deletions        int
	Draft            bool
	Conflicting      bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ReviewDecision   string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	CheckState       string // SUCCESS, FAILURE, ERROR, PENDING, EXPECTED, or ""
	PendingReviewers []string

	// Set only on authored PRs.
	UnresolvedThreads int
	BotVerdict        string // the review bot's latest verdict, or "" if it hasn't reviewed
	BotVerdictStale   bool   // the verdict covers an older commit than the head
}

// Review bot verdicts. A bot that only leaves comment reviews signals its
// verdict by putting one of these words on the review body's first line.
const (
	VerdictApprove        = "APPROVE"
	VerdictRequestChanges = "REQUEST_CHANGES"
)

const trackerQuery = `
fragment pr on PullRequest {
  number title url isDraft createdAt updatedAt additions deletions mergeable reviewDecision
  repository { nameWithOwner }
  author { login ... on User { name } }
  reviewRequests(first: 10) { nodes { requestedReviewer {
    ... on User { login name }
    ... on Team { name }
  } } }
  commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
}
fragment mine on PullRequest {
  ...pr
  headRefOid
  reviewThreads(first: 100) { nodes { isResolved } }
  reviews(last: 20) { nodes { author { login } state commit { oid } body } }
}
query($direct: String!, $any: String!, $authored: String!) {
  direct: search(type: ISSUE, query: $direct, first: 25) { issueCount nodes { ...pr } }
  any: search(type: ISSUE, query: $any, first: 25) { issueCount nodes { ...pr } }
  authored: search(type: ISSUE, query: $authored, first: 25) { issueCount nodes { ...mine } }
}`

type gqlPR struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	IsDraft        bool      `json:"isDraft"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	Mergeable      string    `json:"mergeable"`
	ReviewDecision string    `json:"reviewDecision"`
	Repository     struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Author struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"author"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
				Name  string `json:"name"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	HeadRefOid    string `json:"headRefOid"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Reviews struct {
		Nodes []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State  string `json:"state"`
			Commit struct {
				OID string `json:"oid"`
			} `json:"commit"`
			Body string `json:"body"`
		} `json:"nodes"`
	} `json:"reviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type gqlSearch struct {
	IssueCount int     `json:"issueCount"`
	Nodes      []gqlPR `json:"nodes"`
}

func (p gqlPR) tracked(reviewBot string) TrackedPR {
	t := TrackedPR{
		Repo:           p.Repository.NameWithOwner,
		Number:         p.Number,
		Title:          p.Title,
		URL:            p.URL,
		AuthorName:     orLogin(p.Author.Name, p.Author.Login),
		Additions:      p.Additions,
		Deletions:      p.Deletions,
		Draft:          p.IsDraft,
		Conflicting:    p.Mergeable == "CONFLICTING",
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
		ReviewDecision: p.ReviewDecision,
	}
	for _, rr := range p.ReviewRequests.Nodes {
		if name := orLogin(rr.RequestedReviewer.Name, rr.RequestedReviewer.Login); name != "" {
			t.PendingReviewers = append(t.PendingReviewers, name)
		}
	}
	for _, th := range p.ReviewThreads.Nodes {
		if !th.IsResolved {
			t.UnresolvedThreads++
		}
	}
	for _, r := range p.Reviews.Nodes {
		if reviewBot == "" || !strings.EqualFold(r.Author.Login, reviewBot) {
			continue
		}
		if v := botVerdict(r.State, r.Body); v != "" {
			t.BotVerdict = v
			t.BotVerdictStale = r.Commit.OID != p.HeadRefOid
		}
	}
	if n := len(p.Commits.Nodes); n > 0 && p.Commits.Nodes[n-1].Commit.StatusCheckRollup != nil {
		t.CheckState = p.Commits.Nodes[n-1].Commit.StatusCheckRollup.State
	}
	return t
}

func botVerdict(state, body string) string {
	switch state {
	case "APPROVED":
		return VerdictApprove
	case "CHANGES_REQUESTED":
		return VerdictRequestChanges
	}
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	switch {
	case strings.Contains(first, VerdictRequestChanges):
		return VerdictRequestChanges
	case strings.Contains(first, VerdictApprove):
		return VerdictApprove
	}
	return ""
}

func orLogin(name, login string) string {
	if name != "" {
		return name
	}
	return login
}

// TrackerSearches returns the GitHub search queries behind each Tracker list,
// for linking to the full results on github.com.
func TrackerSearches(org, username string) (direct, team, authored string) {
	base := "is:open is:pr archived:false org:" + org
	return base + " user-review-requested:" + username,
		base + " review-requested:" + username,
		base + " author:" + username + " sort:updated-desc"
}

// ListTrackedPRs returns the user's open review requests and authored pull
// requests in the installation's org, at most 25 per list. reviewBot is the
// login of a bot whose reviews set BotVerdict; "" skips it.
func (c *Client) ListTrackedPRs(ctx context.Context, username, reviewBot string) (*Tracker, string, error) {
	reviewBot = strings.TrimSuffix(reviewBot, "[bot]")
	token, err := c.GetInstallationToken()
	if err != nil {
		return nil, "", fmt.Errorf("get installation token: %w", err)
	}
	org, err := c.getInstallationOrg(token)
	if err != nil {
		return nil, "", fmt.Errorf("get installation org: %w", err)
	}

	direct, anyReq, authored := TrackerSearches(org, username)
	body := map[string]any{
		"query":     trackerQuery,
		"variables": map[string]string{"direct": direct, "any": anyReq, "authored": authored},
	}
	var resp struct {
		Data struct {
			Direct   gqlSearch `json:"direct"`
			Any      gqlSearch `json:"any"`
			Authored gqlSearch `json:"authored"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.post(ctx, token, "/graphql", body, &resp); err != nil {
		return nil, "", err
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, len(resp.Errors))
		for i, e := range resp.Errors {
			msgs[i] = e.Message
		}
		return nil, "", fmt.Errorf("github graphql: %s", strings.Join(msgs, "; "))
	}

	t := &Tracker{
		DirectTotal:   resp.Data.Direct.IssueCount,
		TeamTotal:     resp.Data.Any.IssueCount - resp.Data.Direct.IssueCount,
		AuthoredTotal: resp.Data.Authored.IssueCount,
	}
	isDirect := make(map[string]bool)
	for _, p := range resp.Data.Direct.Nodes {
		isDirect[p.URL] = true
		t.Direct = append(t.Direct, p.tracked(reviewBot))
	}
	for _, p := range resp.Data.Any.Nodes {
		if !isDirect[p.URL] {
			t.Team = append(t.Team, p.tracked(reviewBot))
		}
	}
	for _, p := range resp.Data.Authored.Nodes {
		t.Authored = append(t.Authored, p.tracked(reviewBot))
	}
	return t, org, nil
}
