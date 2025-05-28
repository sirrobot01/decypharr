package decypharr

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/qbit"
	"github.com/sirrobot01/decypharr/pkg/server"
	"github.com/sirrobot01/decypharr/pkg/service"
	"github.com/sirrobot01/decypharr/pkg/version"
	"github.com/sirrobot01/decypharr/pkg/web"
	"github.com/sirrobot01/decypharr/pkg/webdav"
	"github.com/sirrobot01/decypharr/pkg/worker"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
)

func Start(ctx context.Context) error {

	if umaskStr := os.Getenv("UMASK"); umaskStr != "" {
		umask, err := strconv.ParseInt(umaskStr, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid UMASK value: %s", umaskStr)
		}
		SetUmask(int(umask))
	}

	restartCh := make(chan struct{}, 1)
	web.SetRestartFunc(func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	})

	svcCtx, cancelSvc := context.WithCancel(ctx)
	defer cancelSvc()

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

		ui := web.New(qb).Routes()
		webdavRoutes := wd.Routes()
		qbitRoutes := qb.Routes()

		// Register routes
		handlers := map[string]http.Handler{
			"/":       ui,
			"/api/v2": qbitRoutes,
			"/webdav": webdavRoutes,
		}
		srv := server.New(handlers)

		done := make(chan struct{})
		go func(ctx context.Context) {
			if err := startServices(ctx, wd, srv); err != nil {
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
			return nil

		case <-restartCh:
			cancelSvc() // tell existing services to shut down
			_log.Info().Msg("Restarting Decypharr...")
			<-done // wait for them to finish
			qb.Reset()
			service.Reset()

			// rebuild svcCtx off the original parent
			svcCtx, cancelSvc = context.WithCancel(ctx)
			runtime.GC()

			config.Reload()
			service.Reset()
			// loop will restart services automatically
		}
	}
}

func startServices(ctx context.Context, wd *webdav.WebDav, srv *server.Server) error {
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

	safeGo(func() error {
		return worker.Start(ctx)
	})

	safeGo(func() error {
		arr := service.GetService().Arr
		if arr == nil {
			return nil
		}
		return arr.StartSchedule(ctx)
	})

	if cfg := config.Get(); cfg.Repair.Enabled {
		safeGo(func() error {
			r := service.GetService().Repair
			if r != nil {
				if err := r.Start(ctx); err != nil {
					_log.Error().Err(err).Msg("repair failed")
				}
			}
			return nil
		})
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	go func() {
		for err := range errChan {
			if err != nil {
				_log.Error().Err(err).Msg("Service error detected")
				// Don't shut down the whole app
			}
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	_log.Debug().Msg("Services context cancelled")
	return nil
}
