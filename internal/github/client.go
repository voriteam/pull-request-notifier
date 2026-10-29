package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrUnauthorized is returned when GitHub responds with 401 Unauthorized.
var ErrUnauthorized = errors.New("github: 401 unauthorized")

var tracer = otel.Tracer("pull-request-notifier/github")

const apiBase = "https://api.github.com"

type cachedName struct {
	name      string
	expiresAt time.Time
}

// Client makes authenticated calls to the GitHub REST API.
type Client struct {
	httpClient     *http.Client
	baseURL        string
	appID          string
	privateKey     *rsa.PrivateKey
	installationID int64

	mu              sync.Mutex
	installToken    string
	installTokenExp time.Time
	installOrg      string

	nameMu    sync.RWMutex
	nameCache map[string]cachedName
}

// NewClient returns a new GitHub API client with GitHub App authentication.
func NewClient(appID, privateKeyPEM, installationID string) (*Client, error) {
	c := &Client{httpClient: http.DefaultClient, baseURL: apiBase, nameCache: make(map[string]cachedName)}

	if appID != "" && privateKeyPEM != "" && installationID != "" {
		// Support PEM keys passed with literal \n (e.g. from .env files or k8s secrets).
		privateKeyPEM = strings.ReplaceAll(privateKeyPEM, `\n`, "\n")
		key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("parse github app private key: %w", err)
		}
		instID, err := strconv.ParseInt(installationID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse installation id: %w", err)
		}
		c.appID = appID
		c.privateKey = key
		c.installationID = instID
	}

	return c, nil
}

// generateJWT creates a short-lived JWT signed with the GitHub App's private key.
func (c *Client) generateJWT() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    c.appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(c.privateKey)
}

// GetInstallationToken returns a cached or fresh installation access token.
func (c *Client) GetInstallationToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return cached token if it has at least 1 minute of life.
	if c.installToken != "" && time.Now().Add(time.Minute).Before(c.installTokenExp) {
		return c.installToken, nil
	}

	jwtToken, err := c.generateJWT()
	if err != nil {
		return "", fmt.Errorf("generate jwt: %w", err)
	}

	path := fmt.Sprintf("/app/installations/%d/access_tokens", c.installationID)
	req, err := http.NewRequest(http.MethodPost, apiBase+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create installation token: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode installation token: %w", err)
	}

	c.installToken = result.Token
	c.installTokenExp = result.ExpiresAt
	return c.installToken, nil
}

// GetUserDisplayName returns the full name for a GitHub login, falling back to the login itself.
// Results are cached for 4 hours.
func (c *Client) GetUserDisplayName(ctx context.Context, login string) string {
	c.nameMu.RLock()
	if cached, ok := c.nameCache[login]; ok && time.Now().Before(cached.expiresAt) {
		c.nameMu.RUnlock()
		return cached.name
	}
	c.nameMu.RUnlock()

	name := login // default fallback
	token, err := c.GetInstallationToken()
	if err == nil {
		var user struct {
			Name string `json:"name"`
		}
		if err := c.get(ctx, token, "/users/"+login, &user); err == nil && user.Name != "" {
			name = user.Name
		}
	}

	c.nameMu.Lock()
	c.nameCache[login] = cachedName{name: name, expiresAt: time.Now().Add(4 * time.Hour)}
	c.nameMu.Unlock()

	return name
}

// IsBot returns true if the GitHub user appears to be a bot account.
func IsBot(login, userType string) bool {
	return userType == "Bot" || strings.HasSuffix(login, "[bot]")
}

// PRActivity holds aggregated review and comment activity for a PR.
type PRActivity struct {
	Approvals     []string       // display names of users who approved
	Commenters    map[string]int // display name → count
	TotalComments int
}

// GetPRActivity fetches reviews and comments for a PR using an installation token.
// When includeBots is false, comments from bot accounts are excluded from counts.
func (c *Client) GetPRActivity(ctx context.Context, repo string, prNumber int, includeBots bool) (*PRActivity, error) {
	token, err := c.GetInstallationToken()
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}

	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	activity := &PRActivity{
		Commenters: make(map[string]int),
	}

	// Fetch reviews
	var reviews []struct {
		User  struct{ Login string } `json:"user"`
		State string                 `json:"state"`
	}
	reviewPath := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repoName, prNumber)
	if err := c.get(ctx, token, reviewPath, &reviews); err != nil {
		slog.Error("fetch pr reviews", "err", err)
	} else {
		// Track the latest review state per user.
		latestState := make(map[string]string)
		for _, r := range reviews {
			latestState[r.User.Login] = r.State
		}
		for login, state := range latestState {
			if strings.EqualFold(state, "APPROVED") {
				activity.Approvals = append(activity.Approvals, c.GetUserDisplayName(ctx, login))
			}
		}
	}

	// Fetch review comments (inline code comments)
	reviewCommentsPath := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, repoName, prNumber)
	var reviewComments []struct {
		User struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	}
	if err := c.get(ctx, token, reviewCommentsPath, &reviewComments); err != nil {
		slog.Error("fetch pr review comments", "err", err)
	} else {
		for _, rc := range reviewComments {
			if !includeBots && IsBot(rc.User.Login, rc.User.Type) {
				continue
			}
			activity.Commenters[c.GetUserDisplayName(ctx, rc.User.Login)]++
			activity.TotalComments++
		}
	}

	// Fetch issue comments (top-level PR comments)
	issueCommentsPath := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, repoName, prNumber)
	var issueComments []struct {
		User struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	}
	if err := c.get(ctx, token, issueCommentsPath, &issueComments); err != nil {
		slog.Error("fetch pr issue comments", "err", err)
	} else {
		for _, ic := range issueComments {
			if !includeBots && IsBot(ic.User.Login, ic.User.Type) {
				continue
			}
			activity.Commenters[c.GetUserDisplayName(ctx, ic.User.Login)]++
			activity.TotalComments++
		}
	}

	return activity, nil
}

// AssignedPR is an open pull request awaiting a user's review.
type AssignedPR struct {
	Repo   string // "owner/repo"
	Number int
	Title  string
	URL    string
}

// ListReviewRequestedPRs returns open PRs in the installation's org where the
// given user is directly requested as a reviewer (i.e. an individual review
// request they have not yet acted on). PRs reachable only because a team the
// user belongs to is requested are intentionally excluded.
func (c *Client) ListReviewRequestedPRs(ctx context.Context, username string) ([]AssignedPR, error) {
	token, err := c.GetInstallationToken()
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}

	org, err := c.getInstallationOrg(token)
	if err != nil {
		return nil, fmt.Errorf("get installation org: %w", err)
	}

	query := fmt.Sprintf("is:open is:pr archived:false org:%s user-review-requested:%s", org, username)
	path := "/search/issues?per_page=100&q=" + url.QueryEscape(query)

	var result struct {
		Items []struct {
			Number        int    `json:"number"`
			Title         string `json:"title"`
			HTMLURL       string `json:"html_url"`
			RepositoryURL string `json:"repository_url"`
		} `json:"items"`
	}
	if err := c.get(ctx, token, path, &result); err != nil {
		return nil, err
	}

	prs := make([]AssignedPR, 0, len(result.Items))
	for _, item := range result.Items {
		prs = append(prs, AssignedPR{
			Repo:   strings.TrimPrefix(item.RepositoryURL, apiBase+"/repos/"),
			Number: item.Number,
			Title:  item.Title,
			URL:    item.HTMLURL,
		})
	}
	return prs, nil
}

// Comparison is the result of comparing two commits.
type Comparison struct {
	// Status is "ahead", "behind", "identical" or "diverged", from base to head.
	Status  string
	Commits []CommitSummary
}

// CommitSummary is one commit in a Comparison, oldest first.
type CommitSummary struct {
	SHA     string
	Message string
}

// CompareCommits returns the commits reachable from head but not base,
// following pagination past the API's 250-commit default.
func (c *Client) CompareCommits(ctx context.Context, repo, base, head string) (*Comparison, error) {
	token, err := c.GetInstallationToken()
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	cmp := &Comparison{}
	for page := 1; ; page++ {
		var resp struct {
			Status       string `json:"status"`
			TotalCommits int    `json:"total_commits"`
			Commits      []struct {
				SHA    string `json:"sha"`
				Commit struct {
					Message string `json:"message"`
				} `json:"commit"`
			} `json:"commits"`
		}
		path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s?per_page=100&page=%d", owner, repoName, base, head, page)
		if err := c.get(ctx, token, path, &resp); err != nil {
			return nil, err
		}
		cmp.Status = resp.Status
		for _, commit := range resp.Commits {
			cmp.Commits = append(cmp.Commits, CommitSummary{SHA: commit.SHA, Message: commit.Commit.Message})
		}
		if len(resp.Commits) == 0 || len(cmp.Commits) >= resp.TotalCommits {
			return cmp, nil
		}
	}
}

// PullSummary is the subset of a pull request needed to announce it.
type PullSummary struct {
	Number int
	Title  string
	URL    string
	Author string
	Branch string
	// BaseBranch is the branch the PR targets.
	BaseBranch string
	Merged     bool
}

type apiPull struct {
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	HTMLURL  string  `json:"html_url"`
	MergedAt *string `json:"merged_at"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p apiPull) summary() PullSummary {
	return PullSummary{
		Number:     p.Number,
		Title:      p.Title,
		URL:        p.HTMLURL,
		Author:     p.User.Login,
		Branch:     p.Head.Ref,
		BaseBranch: p.Base.Ref,
		Merged:     p.MergedAt != nil,
	}
}

// GetPull returns a pull request by number.
func (c *Client) GetPull(ctx context.Context, repo string, number int) (*PullSummary, error) {
	token, err := c.GetInstallationToken()
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	var pr apiPull
	if err := c.get(ctx, token, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repoName, number), &pr); err != nil {
		return nil, err
	}
	s := pr.summary()
	return &s, nil
}

// ListPullsForCommit returns the pull requests associated with a commit.
func (c *Client) ListPullsForCommit(ctx context.Context, repo, sha string) ([]PullSummary, error) {
	token, err := c.GetInstallationToken()
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	var prs []apiPull
	if err := c.get(ctx, token, fmt.Sprintf("/repos/%s/%s/commits/%s/pulls", owner, repoName, sha), &prs); err != nil {
		return nil, err
	}
	out := make([]PullSummary, 0, len(prs))
	for _, pr := range prs {
		out = append(out, pr.summary())
	}
	return out, nil
}

// OAuthToken holds the tokens returned by GitHub's OAuth token endpoint.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds until access token expires; 0 if not provided
}

// ExchangeCode exchanges a GitHub OAuth code for a user access token.
func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code string) (*OAuthToken, error) {
	body := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
	}
	b, _ := json.Marshal(body)

	ctx, span := tracer.Start(ctx, "github.ExchangeCode")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(b))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		err := fmt.Errorf("github oauth: %s: %s", result.Error, result.ErrorDesc)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return &OAuthToken{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
	}, nil
}

// RefreshToken exchanges a refresh token for new access and refresh tokens.
func (c *Client) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*OAuthToken, error) {
	body := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}
	b, _ := json.Marshal(body)

	ctx, span := tracer.Start(ctx, "github.RefreshToken")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(b))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if result.Error != "" {
		err := fmt.Errorf("github oauth refresh: %s: %s", result.Error, result.ErrorDesc)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return &OAuthToken{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
	}, nil
}

// GetAuthenticatedUser returns the GitHub username for the given token.
func (c *Client) GetAuthenticatedUser(ctx context.Context, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, token, "/user", &user); err != nil {
		return "", err
	}
	return user.Login, nil
}

// PostReply posts a reply to a GitHub PR comment.
// For review_comment type, it uses the review comment reply endpoint (threading).
// For pr_comment and review types, it creates a new top-level issue comment.
func (c *Client) PostReply(ctx context.Context, token, repo string, prNumber int, commentID int64, commentType, body string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}

	payload := map[string]string{"body": body}

	switch commentType {
	case "review_comment":
		// Replies to inline code review comments are threaded on GitHub.
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies", owner, repoName, prNumber, commentID)
		return c.post(ctx, token, path, payload, nil)
	default:
		// pr_comment and review body replies both go as top-level issue comments.
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repoName, prNumber)
		return c.post(ctx, token, path, payload, nil)
	}
}

// AddReaction adds a GitHub reaction to a comment.
// Reaction must be one of: +1, -1, laugh, confused, heart, hooray, rocket, eyes.
func (c *Client) AddReaction(ctx context.Context, token, repo string, commentID int64, commentType, reaction string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}

	payload := map[string]string{"content": reaction}

	switch commentType {
	case "review_comment":
		path := fmt.Sprintf("/repos/%s/%s/pulls/comments/%d/reactions", owner, repoName, commentID)
		return c.post(ctx, token, path, payload, nil)
	default:
		// pr_comment (issue_comment)
		path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d/reactions", owner, repoName, commentID)
		return c.post(ctx, token, path, payload, nil)
	}
}

// IsOrgMember checks whether a GitHub user is a member of the org where the app is installed.
func (c *Client) IsOrgMember(ctx context.Context, username string) (bool, error) {
	token, err := c.GetInstallationToken()
	if err != nil {
		return false, fmt.Errorf("get installation token: %w", err)
	}

	// Get the installation's org name.
	org, err := c.getInstallationOrg(token)
	if err != nil {
		return false, err
	}

	path := fmt.Sprintf("/orgs/%s/members/%s", org, username)
	req, err := http.NewRequest(http.MethodGet, apiBase+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("check org membership: %w", err)
	}
	resp.Body.Close()

	return resp.StatusCode == 204, nil
}

func (c *Client) getInstallationOrg(token string) (string, error) {
	c.mu.Lock()
	org := c.installOrg
	c.mu.Unlock()
	if org != "" {
		return org, nil
	}

	jwtToken, err := c.generateJWT()
	if err != nil {
		return "", err
	}

	path := fmt.Sprintf("/app/installations/%d", c.installationID)
	req, err := http.NewRequest(http.MethodGet, apiBase+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get installation: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode installation: %w", err)
	}

	c.mu.Lock()
	c.installOrg = result.Account.Login
	c.mu.Unlock()

	return result.Account.Login, nil
}

// --- HTTP helpers ---

func (c *Client) get(ctx context.Context, token, path string, out any) error {
	ctx, span := tracer.Start(ctx, "github.GET", trace.WithAttributes(
		attribute.String("http.method", "GET"),
		attribute.String("github.api.path", path),
	))
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("github GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("github GET %s: status %d: %s", path, resp.StatusCode, string(body))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(ctx context.Context, token, path string, body, out any) error {
	ctx, span := tracer.Start(ctx, "github.POST", trace.WithAttributes(
		attribute.String("http.method", "POST"),
		attribute.String("github.api.path", path),
	))
	defer span.End()

	b, err := json.Marshal(body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+path, bytes.NewReader(b))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("github POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("github POST %s: status %d: %s", path, resp.StatusCode, string(respBody))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if resp.StatusCode == 401 {
			return fmt.Errorf("%w: %s", ErrUnauthorized, string(respBody))
		}
		return err
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// SplitRepo parses "owner/repo" into its components. Exported for testing.
func SplitRepo(repo string) (string, string, error) {
	return splitRepo(repo)
}

func splitRepo(repo string) (string, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}
