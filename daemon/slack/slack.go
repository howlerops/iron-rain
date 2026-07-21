// Package slack posts Iron Rain agent events to a Slack channel via an Incoming Webhook, so a team
// sees when an agent finishes, errors, needs approval, stalls, or opens a PR — without leaving
// Slack. It's outbound-only (no bot, no Socket Mode): the daemon POSTs to a webhook URL the user
// pastes in, which keeps it dependency-light and matches the local-daemon model. Bidirectional
// delegation (start/approve FROM Slack) is a separate, larger integration.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client posts messages to one Slack Incoming Webhook URL.
type Client struct {
	url  string
	http *http.Client
}

// New returns a Client for the given webhook URL (https://hooks.slack.com/services/...).
func New(webhookURL string) *Client {
	return &Client{url: webhookURL, http: &http.Client{Timeout: 10 * time.Second}}
}

// Post sends a message (Slack mrkdwn) to the webhook.
func (c *Client) Post(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack webhook: %s", resp.Status)
	}
	return nil
}

// Format renders a notification as Slack mrkdwn with a category emoji.
func Format(title, body, category string) string {
	emoji := map[string]string{
		"AGENT_FINISHED": "✅",
		"AGENT_ERROR":    "❌",
		"APPROVAL":       "🔐",
		"AGENT_STALLED":  "⚠️",
		"TESTS_FAILED":   "🧪",
	}[category]
	if emoji == "" {
		emoji = "🐺"
	}
	if body == "" {
		return emoji + " *" + title + "*"
	}
	return emoji + " *" + title + "*\n" + body
}
