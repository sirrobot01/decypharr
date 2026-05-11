package manager

import (
	"context"

	"github.com/go-co-op/gocron/v2"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
)

// runInitialCalls performs any initial calls of worker functions
// for example, call the trackAvailableSlots and processQueuedEntries functions once
func (m *Manager) runInitialCalls(ctx context.Context) {
	// Initial call to track available slots
	go m.refreshDownloadLinks(ctx)
	go m.trackAvailableSlots(ctx)
	go m.processQueuedEntries()
	go m.syncAccounts()
}

func (m *Manager) syncAccounts() {
	// Sync accounts for all debrids
	m.clients.Range(func(debridName string, debridClient debrid.Client) bool {
		if debridClient == nil {
			return true
		}
		debridClient.SyncAccounts()
		return true
	})
}

func (m *Manager) refreshDownloadLinks(ctx context.Context) {
	// Refresh download links for all debrids
	m.clients.Range(func(debridName string, debridClient debrid.Client) bool {
		if debridClient == nil {
			return true
		}
		m.refreshDebridDownloadLinks(ctx, debridName, debridClient)
		return true
	})
}

func (m *Manager) addQueueProcessorJob(ctx context.Context) error {
	// This function is responsible for starting queue processing scheduled tasks

	if jd, err := utils.ConvertToJobDef("30s"); err != nil {
		m.logger.Error().Err(err).Msg("Failed to convert slots tracking interval to job definition")
	} else {
		// Schedule the job
		if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
			m.trackAvailableSlots(ctx)
		}), gocron.WithContext(ctx)); err != nil {
			m.logger.Error().Err(err).Msg("Failed to create slots tracking job")
		} else {
			m.logger.Debug().Msgf("Slots tracking job scheduled for every %s", "30s")
		}
	}

	if jd, err := utils.ConvertToJobDef(m.config.RefreshInterval); err != nil {
		m.logger.Error().Err(err).Msg("Failed to convert queue processing interval to job definition")
	} else {
		// Schedule the job
		if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
			m.processQueuedEntries()
		}), gocron.WithContext(ctx)); err != nil {
			m.logger.Error().Err(err).Msg("Failed to create slots tracking job")
		} else {
			m.logger.Debug().Msgf("Queue processing job scheduled for every %s", m.config.RefreshInterval)
		}
	}

	if m.config.RemoveStalledAfter != "" {
		// Stalled torrents removal job
		if jd, err := utils.ConvertToJobDef("1m"); err != nil {
			m.logger.Error().Err(err).Msg("Failed to convert remove stalled torrents interval to job definition")
		} else {
			// Schedule the job
			if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
				err := m.queue.DeleteStalled()
				if err != nil {
					m.logger.Error().Err(err).Msg("Failed to process remove stalled torrents")
				}
			}), gocron.WithContext(ctx)); err != nil {
				m.logger.Error().Err(err).Msg("Failed to create remove stalled torrents job")
			} else {
				m.logger.Debug().Msgf("Remove stalled torrents job scheduled for every %s", "1m")
			}
		}
	}

	// NZB refresh job for pending archives (every 5 minutes)
	if m.usenet != nil {
		if jd, err := utils.ConvertToJobDef("10m"); err != nil {
			m.logger.Error().Err(err).Msg("Failed to convert NZB refresh interval to job definition")
		} else {
			if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
				if err := m.syncNZBs(ctx); err != nil {
					m.logger.Error().Err(err).Msg("Failed to refresh NZBs")
				}
			}), gocron.WithContext(ctx), gocron.WithName("nzb-refresh")); err != nil {
				m.logger.Error().Err(err).Msg("Failed to create NZB refresh job")
			} else {
				m.logger.Debug().Msg("NZB refresh job scheduled for every 5m")
			}
		}
	}
	return nil
}

func (m *Manager) StartWorker(ctx context.Context) error {
	// Stop any existing jobs before starting new ones
	m.scheduler.RemoveByTags("decypharr")

	// Call the initial calls
	m.runInitialCalls(ctx)

	if err := m.addQueueProcessorJob(ctx); err != nil {
		return err
	}
	// Schedule per-debrid refresh jobs
	m.clients.Range(func(debridName string, debridClient debrid.Client) bool {
		if debridClient == nil {
			return true
		}

		debridConfig := debridClient.Config()

		// Schedule download link refresh job for this debrid
		if jd, err := utils.ConvertToJobDef(debridConfig.DownloadLinksRefreshInterval); err != nil {
			m.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to convert download link refresh interval to job definition")
		} else {
			jobName := debridName + "-download-links"
			if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
				m.refreshDebridDownloadLinks(ctx, debridName, debridClient)
			}), gocron.WithContext(ctx), gocron.WithName(jobName)); err != nil {
				m.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to create download link refresh job")
			} else {
				m.logger.Debug().Str("debrid", debridName).Msgf("Download link refresh job scheduled for every %s", debridConfig.DownloadLinksRefreshInterval)
			}
		}

		// Schedule torrent refresh job for this debrid
		if jd, err := utils.ConvertToJobDef(debridConfig.TorrentsRefreshInterval); err != nil {
			m.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to convert torrent refresh interval to job definition")
		} else {
			jobName := debridName + "-torrents"
			if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
				if err := m.refreshTorrents(ctx, debridName, debridClient); err != nil {
					m.logger.Error().Err(err).Str("debrid", debridName).Msg("Torrent refresh failed")
				}
				m.RefreshEntries(true)
			}), gocron.WithContext(ctx), gocron.WithName(jobName)); err != nil {
				m.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to create torrent refresh job")
			} else {
				m.logger.Debug().Str("debrid", debridName).Msgf("Torrent refresh job scheduled for every %s", debridConfig.TorrentsRefreshInterval)
			}
		}

		// Schedule account syncTorrents job for this debrid
		if jd, err := utils.ConvertToJobDef(config.DefaultAccountSyncInterval); err != nil {
			m.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to convert account syncTorrents interval to job definition")
		} else {
			jobName := debridName + "-account-syncTorrents"
			if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
				debridClient.SyncAccounts()
			}), gocron.WithContext(ctx), gocron.WithName(jobName)); err != nil {
				m.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to create account syncTorrents job")
			} else {
				m.logger.Debug().Str("debrid", debridName).Msgf("Account syncTorrents job scheduled for every %s", config.DefaultAccountSyncInterval)
			}
		}

		return true
	})

	// Schedule the reset invalid links job
	// This job will run every at 00:00 CET
	// and reset the invalid links in the cache
	if jd, err := utils.ConvertToJobDef("00:00"); err != nil {
		m.logger.Error().Err(err).Msg("Failed to convert link reset interval to job definition")
	} else {
		// Schedule the job
		if _, err := m.cetScheduler.NewJob(jd, gocron.NewTask(func() {
			// Reset link cache at midnight CET
			m.linkService.Clear()
			m.logger.Debug().Msg("Cleared link service cache")
		}), gocron.WithContext(ctx)); err != nil {
			m.logger.Error().Err(err).Msg("Failed to create link reset job")
		} else {
			m.logger.Debug().Msgf("Link reset job scheduled for every midnight, CET")
		}
	}

	// Arr monitoring job
	if jd, err := utils.ConvertToJobDef("10s"); err != nil {
		m.logger.Error().Err(err).Msg("Failed to convert arr monitoring interval to job definition")
	} else {
		// Schedule the job
		if _, err := m.scheduler.NewJob(jd, gocron.NewTask(func() {
			// Reset invalid download links map at midnight CET
			m.arr.Monitor()
		}), gocron.WithContext(ctx)); err != nil {
			m.logger.Error().Err(err).Msg("Failed to create arr monitoring job")
		} else {
			m.logger.Debug().Msgf("Arr monitoring job scheduled for every %s", "10s")
		}
	}

	// Register the health checker sweep with the scheduler if enabled.
	if m.repair != nil {
		if err := m.repair.Start(ctx); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to start repair service")
		}
	}

	// Start the scheduler
	m.scheduler.Start()
	m.cetScheduler.Start()
	return nil
}
