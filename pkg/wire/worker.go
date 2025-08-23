package wire

import "context"

func (s *Store) StartWorkers(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Start debrid workers
	if err := s.Debrid().StartWorker(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Failed to start debrid worker")
	} else {
		s.logger.Debug().Msg("Started debrid worker")
	}

	// Cache workers
	for _, cache := range s.Debrid().Caches() {
		if cache == nil {
			continue
		}
		go func() {
			if err := cache.StartWorker(ctx); err != nil {
				s.logger.Error().Err(err).Msg("Failed to start debrid cache worker")
			} else {
				s.logger.Debug().Msgf("Started debrid cache worker for %s", cache.GetConfig().Name)
			}
		}()
	}

	// Store queue workers
	if err := s.StartQueueWorkers(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Failed to start store worker")
	} else {
		s.logger.Debug().Msg("Started store worker")
	}

	// Arr workers
	if err := s.Arr().StartWorker(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Failed to start Arr worker")
	} else {
		s.logger.Debug().Msg("Started Arr worker")
	}
}
