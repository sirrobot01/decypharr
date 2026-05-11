package notifications

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	json "github.com/bytedance/sonic"
)

// CallbackPayload represents the HTTP callback payload
type CallbackPayload struct {
	Hash        string `json:"hash,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status"`
	Event       string `json:"event"`
	Category    string `json:"category,omitempty"`
	Debrid      string `json:"debrid,omitempty"`
	ContentPath string `json:"content_path,omitempty"`
	Error       string `json:"error,omitempty"`
	Message     string `json:"message,omitempty"`
}

// CallbackNotifier sends HTTP callbacks to a configured URL
type CallbackNotifier struct {
	callbackURL string
	client      *http.Client
}

// NewCallback creates a new callback notifier with the specified URL
func NewCallback(callbackURL string) *CallbackNotifier {
	return &CallbackNotifier{
		callbackURL: callbackURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Name returns the name of this notifier
func (c *CallbackNotifier) Name() string {
	return "callback"
}

// Send dispatches the notification via HTTP POST
func (c *CallbackNotifier) Send(event Event) error {
	if c.callbackURL == "" {
		return nil
	}

	// Build the callback payload
	payload := CallbackPayload{
		Status:  event.Status,
		Event:   string(event.Type),
		Message: event.Message,
	}

	// Add entry details if available
	if event.Entry != nil {
		payload.Hash = event.Entry.InfoHash
		payload.Name = event.Entry.Name
		payload.Category = event.Entry.Category
		payload.Debrid = event.Entry.ActiveProvider
		payload.ContentPath = event.Entry.ContentPath
	}

	// Add error message if present
	if event.Error != nil {
		payload.Error = event.Error.Error()
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal callback payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.callbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send callback request: %w", err)
	}
	defer resp.Body.Close()

	// We don't fail on non-2xx responses for callbacks - just log the issue
	// The caller will handle logging
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback returned non-2xx status: %s", resp.Status)
	}

	return nil
}
