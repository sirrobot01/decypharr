package notifications

import (
	"sync"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
)

// Service manages and dispatches notifications to all configured notifiers
type Service struct {
	config    *config.Notifications
	notifiers []Notifier
	logger    zerolog.Logger
	mu        sync.RWMutex
}

// New creates a new notification service based on the provided configuration
func New(cfg *config.Notifications, logger zerolog.Logger) *Service {
	s := &Service{
		config: cfg,
		logger: logger.With().Str("component", "notifications").Logger(),
	}

	s.initNotifiers()

	return s
}

func (s *Service) initNotifiers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.notifiers = make([]Notifier, 0)

	if !s.config.Enabled {
		return
	}

	if s.config.WebhookURL != "" {
		s.notifiers = append(s.notifiers, NewDiscord(s.config.WebhookURL))
	}

	if s.config.CallbackURL != "" {
		s.notifiers = append(s.notifiers, NewCallback(s.config.CallbackURL))
	}
}

// Notify sends an event to all enabled notifiers asynchronously
func (s *Service) Notify(event Event) {
	if !s.IsEventEnabled(event.Type) {
		return
	}

	s.mu.RLock()
	notifiers := s.notifiers
	s.mu.RUnlock()

	for _, notifier := range notifiers {
		go func() {
			if err := notifier.Send(event); err != nil {
				s.logger.Error().
					Err(err).
					Str("notifier", notifier.Name()).
					Str("event", string(event.Type)).
					Msg("Failed to send notification")
			} else {
				s.logger.Trace().
					Str("notifier", notifier.Name()).
					Str("event", string(event.Type)).
					Msg("Notification sent successfully")
			}
		}()
	}
}

// IsEventEnabled checks if a specific event type is enabled for notifications
func (s *Service) IsEventEnabled(eventType config.NotificationEvent) bool {
	return s.config.IsEventEnabled(eventType)
}

// IsEnabled returns whether notifications are globally enabled
func (s *Service) IsEnabled() bool {
	return s.config.Enabled && len(s.notifiers) > 0
}

// Reload reinitialized notifiers based on current config
func (s *Service) Reload(cfg *config.Notifications) {
	s.config = cfg
	s.initNotifiers()
}
