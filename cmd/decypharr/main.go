package decypharr

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs"
	"github.com/sirrobot01/decypharr/pkg/mount/external"
	"github.com/sirrobot01/decypharr/pkg/mount/rclone"
	"github.com/sirrobot01/decypharr/pkg/server"
	"github.com/sirrobot01/decypharr/pkg/share"
	"github.com/sirrobot01/decypharr/pkg/version"
)

func Start(ctx context.Context) error {
	// Start the global cached time updater to reduce time.Now() syscall overhead
	utils.StartGlobalCachedTime()
	defer utils.StopGlobalCachedTime()

	if umaskStr := os.Getenv("UMASK"); umaskStr != "" {
		umask, err := strconv.ParseInt(umaskStr, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid UMASK value: %s", umaskStr)
		}
		SetUmask(int(umask))
	}

	restartCh := make(chan struct{}, 1)
	restartFunc := func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	}

	mgr := manager.New()

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
		mountMgr := createMountManager(mgr, cfg)
		mgr.SetMountManager(mountMgr)
		srv := server.New(mgr)

		srv.SetRestartFunc(restartFunc)

		resetFunc := func() {

			config.Reset()
			// Stop manager to reset ready channel and cleanup resources
			if err := mgr.Reset(); err != nil {
				_log.Warn().Err(err).Msg("Failed to reset manager")
			}
			// refresh GC
			runtime.GC()
		}

		shutdownFunc := func() {
			config.Reset()
			// Stop manager to cleanup all resources including mounts
			if err := mgr.Stop(); err != nil {
				_log.Warn().Err(err).Msg("Failed to stop manager during shutdown")
			}
			// refresh GC
			runtime.GC()
		}

		serviceResult := make(chan error, 1)
		go func(ctx context.Context) {
			serviceResult <- startServices(ctx, mgr, cancelSvc, srv)
		}(svcCtx)

		select {
		case <-ctx.Done():
			cancelSvc()
			<-serviceResult
			_log.Info().Msg("Decypharr has been stopped gracefully.")
			shutdownFunc()
			return nil

		case <-restartCh:
			cancelSvc()
			_log.Info().Msg("Restarting Decypharr...")
			<-serviceResult
			_log.Info().Msg("Decypharr has been restarted.")
			resetFunc()
			svcCtx, cancelSvc = context.WithCancel(ctx)

		case err := <-serviceResult:
			cancelSvc()
			if err != nil {
				_log.Error().Err(err).Msg("Service stopped unexpectedly")
			}
			shutdownFunc()
			return err
		}
	}
}

func createMountManager(mgr *manager.Manager, cfg *config.Config) manager.MountManager {
	switch cfg.Mount.Type {
	case config.MountTypeRclone:
		return rclone.NewManager(mgr)
	case config.MountTypeDFS:
		return dfs.NewManager(mgr)
	case config.MountTypeExternalRclone:
		return external.NewManager(mgr)
	default:
		return manager.NewStubMountManager()
	}
}

func startServices(ctx context.Context, manager *manager.Manager, cancelSvc context.CancelFunc, srv *server.Server) error {
	var wg sync.WaitGroup
	errChan := make(chan error, 3)

	_log := logger.Default()

	safeGo := func(f func() error) {
		wg.Go(func() {
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
		})
	}

	// NFS and SMB export the same catalog through one cache: a second cache
	// over the same directory would delete the first one's files. Build it
	// before anything starts so a bad cache directory fails immediately.
	cfg := config.Get()
	var export *share.Export
	if cfg.NFS.Enabled || cfg.SMB.Enabled {
		var err error
		if export, err = share.NewExport(ctx, manager, cfg.ShareCache); err != nil {
			return err
		}
		defer func() {
			if err := export.Close(); err != nil {
				_log.Error().Err(err).Msg("Failed to close share export")
			}
		}()
	}

	safeGo(func() error {
		return srv.Start(ctx)
	})

	// Start manager (which handles mounts, processing, etc.)
	safeGo(func() error {
		return manager.Start(ctx)
	})

	if cfg.NFS.Enabled {
		nfs := share.NewNFS(manager, export, cfg.NFS)
		safeGo(func() error {
			return nfs.Start(ctx)
		})
	}

	if cfg.SMB.Enabled {
		smb := share.NewSMB(manager, export, cfg.SMB)
		safeGo(func() error {
			return smb.Start(ctx)
		})
	}

	select {
	case <-ctx.Done():
		wg.Wait()
		_log.Debug().Msg("Services context cancelled")
		return nil
	case err := <-errChan:
		cancelSvc()
		wg.Wait()
		return err
	}
}
