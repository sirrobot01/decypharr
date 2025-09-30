package decypharr

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/qbit"
	"github.com/sirrobot01/decypharr/pkg/server"
	"github.com/sirrobot01/decypharr/pkg/store"
	"github.com/sirrobot01/decypharr/pkg/version"
	"github.com/sirrobot01/decypharr/pkg/web"
	"github.com/sirrobot01/decypharr/pkg/webdav"
)

func Start(ctx context.Context) error {

	// if umaskStr := os.Getenv("UMASK"); umaskStr != "" {
	// 	umask, err := strconv.ParseInt(umaskStr, 8, 32)
	// 	if err != nil {
	// 		return fmt.Errorf("invalid UMASK value: %s", umaskStr)
	// 	}
	// 	SetUmask(int(umask))
	// }

	restartCh := make(chan struct{}, 1)
	web.SetRestartFunc(func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	})

	svcCtx, cancelSvc := context.WithCancel(ctx)
	defer cancelSvc()

	// Create the logger path if it doesn't exist
	for {
		cfg := config.Get()
		_log := logger.Default()

		// ascii banner
		fmt.Printf(`
+-------------------------------------------------------+
|                                                       |
|  ╔╦╗╔═╗╔═╗╦ ╦╔═╗╦ ╦╔═╗╦═╗╦═╗                          |
|   ║║║╣ ║  └┬┘╠═╝╠═╣╠═╣╠╦╝╠╦╝ (%s)        |
|  ═╩╝╚═╝╚═╝ ┴ ╩  ╩ ╩╩ ╩╩╚═╩╚═                          |
|                                                       |
+-------------------------------------------------------+
|  Log Level: %s                                        |
+-------------------------------------------------------+
`, version.GetInfo(), cfg.LogLevel)

		// Initialize services
		qb := qbit.New()
		wd := webdav.New()

		ui := web.New().Routes()
		webdavRoutes := wd.Routes()
		qbitRoutes := qb.Routes()

		// Register routes
		handlers := map[string]http.Handler{
			"/":       ui,
			"/api/v2": qbitRoutes,
			"/webdav": webdavRoutes,
		}
		srv := server.New(handlers)

		reset := func() {
			// Reset the store and services
			qb.Reset()
			store.Reset()
			// refresh GC
			runtime.GC()
		}

		done := make(chan struct{})
		go func(ctx context.Context) {
			if err := startServices(ctx, cancelSvc, wd, srv); err != nil {
				_log.Error().Err(err).Msg("Error starting services")
				cancelSvc()
			}
			close(done)
		}(svcCtx)

		select {
		case <-ctx.Done():
			// graceful shutdown
			cancelSvc() // propagate to services
			<-done      // wait for them to finish
			_log.Info().Msg("Decypharr has been stopped gracefully.")
			reset() // reset store and services
			return nil

		case <-restartCh:
			cancelSvc() // tell existing services to shut down
			_log.Info().Msg("Restarting Decypharr...")
			<-done // wait for them to finish
			_log.Info().Msg("Decypharr has been restarted.")
			reset() // reset store and services
			// rebuild svcCtx off the original parent
			svcCtx, cancelSvc = context.WithCancel(ctx)
		}
	}
}

func startServices(ctx context.Context, cancelSvc context.CancelFunc, wd *webdav.WebDav, srv *server.Server) error {
	var wg sync.WaitGroup
	errChan := make(chan error)

	_log := logger.Default()

	safeGo := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					_log.Error().
						Interface("panic", r).
						Str("stack", string(stack)).
						Msg("Recovered from panic in goroutine")

					// Send error to channel so the main goroutine is aware
					errChan <- fmt.Errorf("panic: %v", r)
				}
			}()

			if err := f(); err != nil {
				errChan <- err
			}
		}()
	}

	safeGo(func() error {
		return wd.Start(ctx)
	})

	safeGo(func() error {
		return srv.Start(ctx)
	})

	// Start rclone RC server if enabled
	safeGo(func() error {
		rcManager := store.Get().RcloneManager()
		if rcManager == nil {
			return nil
		}
		return rcManager.Start(ctx)
	})

	if cfg := config.Get(); cfg.Repair.Enabled {
		safeGo(func() error {
			repair := store.Get().Repair()
			if repair != nil {
				if err := repair.Start(ctx); err != nil {
					_log.Error().Err(err).Msg("repair failed")
				}
			}
			return nil
		})
	}

	safeGo(func() error {
		store.Get().StartWorkers(ctx)
		return nil
	})

	go func() {
		wg.Wait()
		close(errChan)
	}()

	go func() {
		for err := range errChan {
			if err != nil {
				_log.Error().Err(err).Msg("Service error detected")
				// If the error is critical, return it to stop the main loop
				if ctx.Err() == nil {
					_log.Error().Msg("Stopping services due to error")
					cancelSvc() // Cancel the service context to stop all services
				}
			}
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	_log.Debug().Msg("Services context cancelled")
	return nil
}
