package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const apiBase = "https://api.github.com"

// Client makes authenticated calls to the GitHub REST API.
type Client struct {
	httpClient *http.Client
}

// NewClient returns a new GitHub API client.
func NewClient() *Client {
	return &Client{httpClient: http.DefaultClient}
}

// ExchangeCode exchanges a GitHub OAuth code for a user access token.
func (c *Client) ExchangeCode(clientID, clientSecret, code string) (string, error) {
	body := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
	}
	b, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange code: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("github oauth: %s: %s", result.Error, result.ErrorDesc)
	}
	return result.AccessToken, nil
}

// GetAuthenticatedUser returns the GitHub username for the given token.
func (c *Client) GetAuthenticatedUser(token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := c.get(token, "/user", &user); err != nil {
		return "", err
	}
	return user.Login, nil
}

// PostReply posts a reply to a GitHub PR comment.
// For review_comment type, it uses the review comment reply endpoint (threading).
// For pr_comment and review types, it creates a new top-level issue comment.
func (c *Client) PostReply(token, repo string, prNumber int, commentID int64, commentType, body string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}

	payload := map[string]string{"body": body}

	switch commentType {
	case "review_comment":
		// Replies to inline code review comments are threaded on GitHub.
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies", owner, repoName, prNumber, commentID)
		return c.post(token, path, payload, nil)
	default:
		// pr_comment and review body replies both go as top-level issue comments.
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repoName, prNumber)
		return c.post(token, path, payload, nil)
	}
}

// AddReaction adds a GitHub reaction to a comment.
// Reaction must be one of: +1, -1, laugh, confused, heart, hooray, rocket, eyes.
func (c *Client) AddReaction(token, repo string, commentID int64, commentType, reaction string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}

	payload := map[string]string{"content": reaction}

	switch commentType {
	case "review_comment":
		path := fmt.Sprintf("/repos/%s/%s/pulls/comments/%d/reactions", owner, repoName, commentID)
		return c.post(token, path, payload, nil)
	default:
		// pr_comment (issue_comment)
		path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d/reactions", owner, repoName, commentID)
		return c.post(token, path, payload, nil)
	}
}

// --- HTTP helpers ---

func (c *Client) get(token, path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github GET %s: status %d: %s", path, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(token, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, apiBase+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github POST %s: status %d: %s", path, resp.StatusCode, string(respBody))
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
