package notifications

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/sirrobot01/decypharr/internal/config"
)

// DiscordEmbed represents a Discord embed object
type DiscordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
}

// DiscordWebhook represents the Discord webhook payload
type DiscordWebhook struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

// DiscordNotifier sends notifications to Discord webhooks
type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewDiscord creates a new Discord notifier with the specified webhook URL
func NewDiscord(webhookURL string) *DiscordNotifier {
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Name returns the name of this notifier
func (d *DiscordNotifier) Name() string {
	return "discord"
}

// Send dispatches the notification to Discord
func (d *DiscordNotifier) Send(event Event) error {
	if d.webhookURL == "" {
		return nil
	}

	// Create the proper Discord webhook structure
	webhook := DiscordWebhook{
		Embeds: []DiscordEmbed{
			{
				Title:       d.getHeader(event.Type),
				Description: event.Message,
				Color:       d.getColor(event.Status),
			},
		},
	}

	body, err := json.Marshal(webhook)
	if err != nil {
		return fmt.Errorf("failed to marshal discord webhook: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send discord message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord returned error status code: %s, body: %s", resp.Status, string(bodyBytes))
	}

	return nil
}

// getColor returns the appropriate Discord embed color based on status
func (d *DiscordNotifier) getColor(status string) int {
	switch status {
	case "success":
		return 3066993 // Green
	case "error":
		return 15158332 // Red
	case "warning":
		return 15844367 // Yellow/Orange
	case "pending":
		return 3447003 // Blue
	default:
		return 0 // Default
	}
}

// getHeader returns the notification title based on event type
func (d *DiscordNotifier) getHeader(event config.NotificationEvent) string {
	switch event {
	case config.EventDownloadComplete:
		return "[Decypharr] Download Completed"
	case config.EventDownloadFailed:
		return "[Decypharr] Download Failed"
	case config.EventRepairPending:
		return "[Decypharr] Repair Completed, Awaiting action"
	case config.EventRepairComplete:
		return "[Decypharr] Repair Complete"
	case config.EventRepairFailed:
		return "[Decypharr] Repair Failed"
	case config.EventRepairCancelled:
		return "[Decypharr] Repair Cancelled"
	default:
		// Split the event string and capitalize the first letter of each word
		evs := strings.Split(string(event), "_")
		for i, ev := range evs {
			evs[i] = strings.ToTitle(ev)
		}
		return "[Decypharr] " + strings.Join(evs, " ")
	}
}
