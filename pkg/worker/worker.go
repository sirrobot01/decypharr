package worker

import (
	"context"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/service"
	"sync"
	"time"
)

var (
	_logInstance zerolog.Logger
)

func getLogger() zerolog.Logger {
	return _logInstance
}

func Start(ctx context.Context) error {
	cfg := config.Get()
	// Start Arr Refresh Worker
	_logInstance = logger.New("worker")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cleanUpQueuesWorker(ctx, cfg)
	}()
	wg.Wait()
	return nil
}

func cleanUpQueuesWorker(ctx context.Context, cfg *config.Config) {
	// Start Clean up Queues Worker
	_logger := getLogger()
	_logger.Debug().Msg("Clean up Queues Worker started")
	cleanupCtx := context.WithValue(ctx, "worker", "cleanup")
	cleanupTicker := time.NewTicker(time.Duration(10) * time.Second)

	var cleanupMutex sync.Mutex

	for {
		select {
		case <-cleanupCtx.Done():
			_logger.Debug().Msg("Clean up Queues Worker stopped")
			return
		case <-cleanupTicker.C:
			if cleanupMutex.TryLock() {
				go func() {
					defer cleanupMutex.Unlock()
					cleanUpQueues()
				}()
			}
		}
	}
}

func cleanUpQueues() {
	// Clean up queues
	_logger := getLogger()
	for _, a := range service.GetService().Arr.GetAll() {
		if !a.Cleanup {
			continue
		}
		if err := a.CleanupQueue(); err != nil {
			_logger.Error().Err(err).Msg("Error cleaning up queue")
		}
	}
}
