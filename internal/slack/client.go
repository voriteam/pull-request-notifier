package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const slackAPIBase = "https://slack.com/api"

// Client makes authenticated calls to the Slack Web API.
type Client struct {
	token      string
	httpClient *http.Client
}

// NewClient returns a new Slack API client.
func NewClient(token string) *Client {
	return &Client{token: token, httpClient: http.DefaultClient}
}

// PostDM sends a DM to a Slack user by opening/reusing a conversation.
// Returns the message timestamp (ts) on success.
func (c *Client) PostDM(userID string, blocks []Block, fallbackText string) (string, error) {
	// Open a DM channel with the user.
	channelID, err := c.openConversation(userID)
	if err != nil {
		return "", fmt.Errorf("open conversation: %w", err)
	}

	payload := map[string]any{
		"channel": channelID,
		"blocks":  blocks,
		"text":    fallbackText,
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    string `json:"ts"`
	}
	if err := c.post("chat.postMessage", payload, &result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return result.TS, nil
}

// UpdateDM edits an existing DM message by opening the conversation first.
func (c *Client) UpdateDM(userID, ts string, blocks []Block, fallbackText string) error {
	channelID, err := c.openConversation(userID)
	if err != nil {
		return fmt.Errorf("open conversation: %w", err)
	}
	return c.UpdateMessage(channelID, ts, blocks, fallbackText)
}


// UpdateMessage edits an existing Slack message in-place.
func (c *Client) UpdateMessage(channel, ts string, blocks []Block, fallbackText string) error {
	payload := map[string]any{
		"channel": channel,
		"ts":      ts,
		"blocks":  blocks,
		"text":    fallbackText,
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post("chat.update", payload, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("chat.update: %s", result.Error)
	}
	return nil
}

// OpenModal opens a Slack modal using the views.open API.
func (c *Client) OpenModal(triggerID string, view map[string]any) error {
	payload := map[string]any{
		"trigger_id": triggerID,
		"view":       view,
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post("views.open", payload, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("views.open: %s", result.Error)
	}
	return nil
}

// PublishHomeTab publishes or updates the App Home tab for a user.
func (c *Client) PublishHomeTab(userID string, blocks []Block) error {
	payload := map[string]any{
		"user_id": userID,
		"view": map[string]any{
			"type":   "home",
			"blocks": blocks,
		},
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post("views.publish", payload, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("views.publish: %s", result.Error)
	}
	return nil
}

// openConversation opens (or retrieves) a DM channel with a user.
func (c *Client) openConversation(userID string) (string, error) {
	payload := map[string]any{
		"users": userID,
	}

	var result struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := c.post("conversations.open", payload, &result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("conversations.open: %s", result.Error)
	}
	return result.Channel.ID, nil
}

func (c *Client) post(method string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, slackAPIBase+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack %s: status %d: %s", method, resp.StatusCode, string(respBody))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}