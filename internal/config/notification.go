package config

import "slices"

// NotificationEvent defines the type of notification event
type NotificationEvent string

const (
	EventDownloadComplete NotificationEvent = "download_complete"
	EventDownloadFailed   NotificationEvent = "download_failed"
	EventRepairPending    NotificationEvent = "repair_pending"
	EventRepairComplete   NotificationEvent = "repair_complete"
	EventRepairFailed     NotificationEvent = "repair_failed"
	EventRepairCancelled  NotificationEvent = "repair_cancelled"
)

// Notifications holds all notification configuration
type Notifications struct {
	// Enabled controls whether notifications are globally enabled
	Enabled bool `json:"enabled,omitempty"`

	// WebhookURL is the Discord webhook URL for sending notifications
	WebhookURL string `json:"webhook_url,omitempty"`

	// CallbackURL is an HTTP endpoint for status callbacks
	CallbackURL string `json:"callback_url,omitempty"`

	// Events is a list of enabled notification events
	// If empty, all events are enabled
	Events []NotificationEvent `json:"events,omitempty"`
}

// IsEventEnabled checks if a specific event is enabled for notifications
func (n *Notifications) IsEventEnabled(event NotificationEvent) bool {
	if !n.Enabled {
		return false
	}
	// If no events specified, all events are enabled
	if len(n.Events) == 0 {
		return true
	}
	return slices.Contains(n.Events, event)
}
