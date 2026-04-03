package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("pull-request-notifier/slack")

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
func (c *Client) PostDM(ctx context.Context, userID string, blocks []Block, fallbackText string) (string, error) {
	// Open a DM channel with the user.
	channelID, err := c.openConversation(ctx, userID)
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
	if err := c.post(ctx, "chat.postMessage", payload, &result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return result.TS, nil
}

// UpdateDM edits an existing DM message by opening the conversation first.
func (c *Client) UpdateDM(ctx context.Context, userID, ts string, blocks []Block, fallbackText string) error {
	channelID, err := c.openConversation(ctx, userID)
	if err != nil {
		return fmt.Errorf("open conversation: %w", err)
	}
	return c.UpdateMessage(ctx, channelID, ts, blocks, fallbackText)
}

// UpdateMessage edits an existing Slack message in-place.
func (c *Client) UpdateMessage(ctx context.Context, channel, ts string, blocks []Block, fallbackText string) error {
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
	if err := c.post(ctx, "chat.update", payload, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("chat.update: %s", result.Error)
	}
	return nil
}

// OpenModal opens a Slack modal using the views.open API.
func (c *Client) OpenModal(ctx context.Context, triggerID string, view map[string]any) error {
	payload := map[string]any{
		"trigger_id": triggerID,
		"view":       view,
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post(ctx, "views.open", payload, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("views.open: %s", result.Error)
	}
	return nil
}

// openConversation opens (or retrieves) a DM channel with a user.
func (c *Client) openConversation(ctx context.Context, userID string) (string, error) {
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
	if err := c.post(ctx, "conversations.open", payload, &result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("conversations.open: %s", result.Error)
	}
	return result.Channel.ID, nil
}

func (c *Client) post(ctx context.Context, method string, body any, out any) error {
	ctx, span := tracer.Start(ctx, "slack."+method, trace.WithAttributes(
		attribute.String("slack.api.method", method),
	))
	defer span.End()

	b, err := json.Marshal(body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackAPIBase+"/"+method, bytes.NewReader(b))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("slack %s: status %d: %s", method, resp.StatusCode, string(respBody))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
