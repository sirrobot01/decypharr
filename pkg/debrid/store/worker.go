package store

import (
	"context"
	"github.com/go-co-op/gocron/v2"
	"github.com/sirrobot01/decypharr/internal/utils"
)

func (c *Cache) StartSchedule(ctx context.Context) error {
	// For now, we just want to refresh the listing and download links

	// Stop any existing jobs before starting new ones
	c.scheduler.RemoveByTags("decypharr")

	// Schedule download link refresh job
	if jd, err := utils.ConvertToJobDef(c.downloadLinksRefreshInterval); err != nil {
		c.logger.Error().Err(err).Msg("Failed to convert download link refresh interval to job definition")
	} else {
		// Schedule the job
		if _, err := c.scheduler.NewJob(jd, gocron.NewTask(func() {
			c.refreshDownloadLinks(ctx)
		}), gocron.WithContext(ctx), gocron.WithTags("decypharr")); err != nil {
			c.logger.Error().Err(err).Msg("Failed to create download link refresh job")
		} else {
			c.logger.Debug().Msgf("Download link refresh job scheduled for every %s", c.downloadLinksRefreshInterval)
		}
	}

	// Schedule torrent refresh job
	if jd, err := utils.ConvertToJobDef(c.torrentRefreshInterval); err != nil {
		c.logger.Error().Err(err).Msg("Failed to convert torrent refresh interval to job definition")
	} else {
		// Schedule the job
		if _, err := c.scheduler.NewJob(jd, gocron.NewTask(func() {
			c.refreshTorrents(ctx)
		}), gocron.WithContext(ctx), gocron.WithTags("decypharr")); err != nil {
			c.logger.Error().Err(err).Msg("Failed to create torrent refresh job")
		} else {
			c.logger.Debug().Msgf("Torrent refresh job scheduled for every %s", c.torrentRefreshInterval)
		}
	}

	// Schedule the reset invalid links job
	// This job will run every at 00:00 CET
	// and reset the invalid links in the cache
	if jd, err := utils.ConvertToJobDef("00:00"); err != nil {
		c.logger.Error().Err(err).Msg("Failed to convert link reset interval to job definition")
	} else {
		// Schedule the job
		if _, err := c.cetScheduler.NewJob(jd, gocron.NewTask(func() {
			c.resetInvalidLinks(ctx)
		}), gocron.WithContext(ctx), gocron.WithTags("decypharr")); err != nil {
			c.logger.Error().Err(err).Msg("Failed to create link reset job")
		} else {
			c.logger.Debug().Msgf("Link reset job scheduled for every midnight, CET")
		}
	}

	// Start the scheduler
	c.scheduler.Start()
	c.cetScheduler.Start()
	return nil
}
