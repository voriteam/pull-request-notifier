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
	ReviewDecision   string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	CheckState       string // SUCCESS, FAILURE, ERROR, PENDING, EXPECTED, or ""
	PendingReviewers []string
}

const trackerQuery = `
fragment pr on PullRequest {
  number title url isDraft createdAt additions deletions mergeable reviewDecision
  repository { nameWithOwner }
  author { login ... on User { name } }
  reviewRequests(first: 10) { nodes { requestedReviewer {
    ... on User { login name }
    ... on Team { name }
  } } }
  commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
}
query($direct: String!, $any: String!, $authored: String!) {
  direct: search(type: ISSUE, query: $direct, first: 25) { issueCount nodes { ...pr } }
  any: search(type: ISSUE, query: $any, first: 25) { issueCount nodes { ...pr } }
  authored: search(type: ISSUE, query: $authored, first: 25) { issueCount nodes { ...pr } }
}`

type gqlPR struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	IsDraft        bool      `json:"isDraft"`
	CreatedAt      time.Time `json:"createdAt"`
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

func (p gqlPR) tracked() TrackedPR {
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
		ReviewDecision: p.ReviewDecision,
	}
	for _, rr := range p.ReviewRequests.Nodes {
		if name := orLogin(rr.RequestedReviewer.Name, rr.RequestedReviewer.Login); name != "" {
			t.PendingReviewers = append(t.PendingReviewers, name)
		}
	}
	if n := len(p.Commits.Nodes); n > 0 && p.Commits.Nodes[n-1].Commit.StatusCheckRollup != nil {
		t.CheckState = p.Commits.Nodes[n-1].Commit.StatusCheckRollup.State
	}
	return t
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
// requests in the installation's org, at most 25 per list.
func (c *Client) ListTrackedPRs(ctx context.Context, username string) (*Tracker, string, error) {
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
		t.Direct = append(t.Direct, p.tracked())
	}
	for _, p := range resp.Data.Any.Nodes {
		if !isDirect[p.URL] {
			t.Team = append(t.Team, p.tracked())
		}
	}
	for _, p := range resp.Data.Authored.Nodes {
		t.Authored = append(t.Authored, p.tracked())
	}
	return t, org, nil
}
