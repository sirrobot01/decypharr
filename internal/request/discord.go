package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"io"
	"net/http"
	"strings"
)

type DiscordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
}

type DiscordWebhook struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

func getDiscordColor(status string) int {
	switch status {
	case "success":
		return 3066993
	case "error":
		return 15158332
	case "warning":
		return 15844367
	case "pending":
		return 3447003
	default:
		return 0
	}
}

func getDiscordHeader(event string) string {
	switch event {
	case "download_complete":
		return "[Decypharr] Download Completed"
	case "download_failed":
		return "[Decypharr] Download Failed"
	case "repair_pending":
		return "[Decypharr] Repair Completed, Awaiting action"
	case "repair_complete":
		return "[Decypharr] Repair Complete"
	case "repair_cancelled":
		return "[Decypharr] Repair Cancelled"
	default:
		// split the event string and capitalize the first letter of each word
		evs := strings.Split(event, "_")
		for i, ev := range evs {
			evs[i] = strings.ToTitle(ev)
		}
		return "[Decypharr] %s" + strings.Join(evs, " ")
	}
}

func SendDiscordMessage(event string, status string, message string) error {
	cfg := config.Get()
	webhookURL := cfg.DiscordWebhook
	if webhookURL == "" {
		return nil
	}

	// Create the proper Discord webhook structure

	webhook := DiscordWebhook{
		Embeds: []DiscordEmbed{
			{
				Title:       getDiscordHeader(event),
				Description: message,
				Color:       getDiscordColor(status),
			},
		},
	}

	payload, err := json.Marshal(webhook)
	if err != nil {
		return fmt.Errorf("failed to marshal discord payload: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create discord request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send discord message: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord returned error status code: %s, body: %s", resp.Status, string(bodyBytes))
	}

	return nil
}
