package notifications

import (
	"github.com/dylanmazurek/decypharr/internal/config"
	"github.com/dylanmazurek/decypharr/pkg/storage"
)

// Event represents a notification event to be dispatched
type Event struct {
	// Type is the event type (e.g., download_complete, repair_failed)
	Type config.NotificationEvent

	// Status indicates the outcome (success, error, warning, pending)
	Status string

	// Entry is the storage entry related to this event (optional)
	Entry *storage.Entry

	// Message is the human-readable notification message
	Message string

	// Error is the error that occurred, if any
	Error error
}

// Notifier is the interface for sending notifications
type Notifier interface {
	// Send dispatches the notification event
	Send(event Event) error

	// Name returns the name of this notifier
	Name() string
}
