package store

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) addToQueue(importReq *ImportRequest) error {
	if importReq.Magnet == nil {
		return fmt.Errorf("magnet is required")
	}

	if importReq.Arr == nil {
		return fmt.Errorf("arr is required")
	}

	importReq.Status = "queued"
	importReq.CompletedAt = time.Time{}
	importReq.Error = nil
	err := s.importsQueue.Push(importReq)
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) StartQueueSchedule(ctx context.Context) error {
	// Start the slots processing in a separate goroutine
	go func() {
		if err := s.processSlotsQueue(ctx); err != nil {
			s.logger.Error().Err(err).Msg("Error processing slots queue")
		}
	}()

	// Start the remove stalled torrents processing in a separate goroutine
	go func() {
		if err := s.processRemoveStalledTorrents(ctx); err != nil {
			s.logger.Error().Err(err).Msg("Error processing remove stalled torrents")
		}
	}()

	return nil
}

func (s *Store) processSlotsQueue(ctx context.Context) error {
	s.trackAvailableSlots(ctx) // Initial tracking of available slots

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.trackAvailableSlots(ctx)
		}
	}
}

func (s *Store) processRemoveStalledTorrents(ctx context.Context) error {
	if s.removeStalledAfter <= 0 {
		return nil // No need to remove stalled torrents if the duration is not set
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.removeStalledTorrents(ctx); err != nil {
				s.logger.Error().Err(err).Msg("Error removing stalled torrents")
			}
		}
	}
}

func (s *Store) trackAvailableSlots(ctx context.Context) {
	// This function tracks the available slots for each debrid client
	availableSlots := make(map[string]int)

	for name, deb := range s.debrid.Debrids() {
		slots, err := deb.Client().GetAvailableSlots()
		if err != nil {
			continue
		}
		availableSlots[name] = slots
	}

	if s.importsQueue.Size() <= 0 {
		// Queue is empty, no need to process
		return
	}

	for _, slots := range availableSlots {
		// If slots are available, process the next import request from the queue
		for slots > 0 {
			select {
			case <-ctx.Done():
				return // Exit if context is done
			default:
				if err := s.processFromQueue(ctx); err != nil {
					s.logger.Error().Err(err).Msg("Error processing from queue")
					return // Exit on error
				}
				slots-- // Decrease the available slots after processing
			}
		}
	}
}

func (s *Store) processFromQueue(ctx context.Context) error {
	// Pop the next import request from the queue
	importReq, err := s.importsQueue.Pop()
	if err != nil {
		return err
	}
	if importReq == nil {
		return nil
	}
	return s.AddTorrent(ctx, importReq)
}

func (s *Store) removeStalledTorrents(ctx context.Context) error {
	// This function checks for stalled torrents and removes them
	stalledTorrents := s.torrents.GetStalledTorrents(s.removeStalledAfter)
	if len(stalledTorrents) == 0 {
		return nil // No stalled torrents to remove
	}

	for _, torrent := range stalledTorrents {
		s.logger.Warn().Msgf("Removing stalled torrent: %s", torrent.Name)
		s.torrents.Delete(torrent.Hash, torrent.Category, true) // Remove from store and delete from debrid
	}

	return nil
}
