package wire

import (
	"context"
	"fmt"
	"github.com/go-co-op/gocron/v2"
	"github.com/sirrobot01/decypharr/internal/utils"
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

func (s *Store) StartQueueWorkers(ctx context.Context) error {
	// This function is responsible for starting the scheduled tasks
	if ctx == nil {
		ctx = context.Background()
	}

	s.scheduler.RemoveByTags("decypharr-store")

	if jd, err := utils.ConvertToJobDef("30s"); err != nil {
		s.logger.Error().Err(err).Msg("Failed to convert slots tracking interval to job definition")
	} else {
		// Schedule the job
		if _, err := s.scheduler.NewJob(jd, gocron.NewTask(func() {
			s.trackAvailableSlots(ctx)
		}), gocron.WithContext(ctx)); err != nil {
			s.logger.Error().Err(err).Msg("Failed to create slots tracking job")
		} else {
			s.logger.Trace().Msgf("Slots tracking job scheduled for every %s", "30s")
		}
	}

	if s.removeStalledAfter > 0 {
		// Stalled torrents removal job
		if jd, err := utils.ConvertToJobDef("1m"); err != nil {
			s.logger.Error().Err(err).Msg("Failed to convert remove stalled torrents interval to job definition")
		} else {
			// Schedule the job
			if _, err := s.scheduler.NewJob(jd, gocron.NewTask(func() {
				err := s.removeStalledTorrents(ctx)
				if err != nil {
					s.logger.Error().Err(err).Msg("Failed to process remove stalled torrents")
				}
			}), gocron.WithContext(ctx)); err != nil {
				s.logger.Error().Err(err).Msg("Failed to create remove stalled torrents job")
			} else {
				s.logger.Trace().Msgf("Remove stalled torrents job scheduled for every %s", "1m")
			}
		}
	}

	// Start the scheduler
	s.scheduler.Start()
	s.logger.Debug().Msg("Store worker started")
	return nil
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

	if len(availableSlots) == 0 {
		s.logger.Debug().Msg("No debrid clients available or no slots found")
		return // No debrid clients or slots available, nothing to process
	}

	if s.importsQueue.Size() <= 0 {
		// Queue is empty, no need to process
		return
	}

	for name, slots := range availableSlots {
		s.logger.Debug().Msgf("Available slots for %s: %d", name, slots)
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
