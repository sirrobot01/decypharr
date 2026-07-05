package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

//go:fix inline
func ptrTime(t time.Time) *time.Time {
	return new(t)
}

// This is in-charge of moving torrents between different debrid services

// SwitchTorrent moves a torrent from one debrid to another
func (m *Manager) SwitchTorrent(ctx context.Context, infohash, target string, keepOld, waitComplete bool) (*storage.SwitcherJob, error) {
	// GetReader the entry
	entry, err := m.GetEntry(infohash)
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	// Check if already on target debrid
	if entry.ActiveProvider == target {
		return nil, storage.ErrAlreadyOnDebrid
	}

	// Need to actually migrate - create job
	job := &storage.SwitcherJob{
		ID:             uuid.New().String(),
		InfoHash:       infohash,
		SourceProvider: entry.ActiveProvider,
		TargetProvider: target,
		Status:         storage.SwitcherStatusPending,
		Progress:       0,
		CreatedAt:      time.Now(),
		KeepOld:        keepOld,
		WaitComplete:   waitComplete,
	}

	// Store job
	m.migrationJobs.Store(job.ID, job)

	// Start migration in background
	go m.executeMigration(job, entry)

	return job, nil
}

// executeMigration performs the actual torrent migration - COMPLETE IMPLEMENTATION
func (m *Manager) executeMigration(job *storage.SwitcherJob, torrent *storage.Entry) {
	m.logger.Info().
		Str("job_id", job.ID).
		Str("torrent", torrent.Name).
		Str("source", job.SourceProvider).
		Str("target", job.TargetProvider).
		Msg("Starting torrent migration")
	job.Status = storage.SwitcherStatusInProgress

	// GetReader target debrid client
	targetClient := m.ProviderClient(job.TargetProvider)
	if targetClient == nil {
		job.Status = storage.SwitcherStatusFailed
		job.Error = fmt.Sprintf("target debrid %s not found", job.TargetProvider)
		job.CompletedAt = new(time.Now())
		return
	}
	// Submit to target debrid

	job.Progress = 10

	success, err := m.fixer.MoveTorrent(torrent, job.TargetProvider, false) // false = don't force re-download

	if err != nil || !success {
		job.Status = storage.SwitcherStatusFailed
		job.Error = fmt.Sprintf("failed to move torrent to target debrid: %v", err)
		job.CompletedAt = new(time.Now())
		m.logger.Error().
			Err(err).
			Str("job_id", job.ID).
			Msg("Failed to move torrent to target debrid")
		return
	}

	// Handle source placement
	// This removes the old placement
	if !job.KeepOld {
		// Archive and optionally delete from source
		// Find source placement for this debrid
		var sourcePlacement *storage.ProviderEntry
		for _, p := range torrent.Providers {
			if p.Provider == job.SourceProvider {
				sourcePlacement = p
				break
			}
		}

		if sourcePlacement != nil {
			torrent.RemoveProvider(job.SourceProvider, func(placement *storage.ProviderEntry) error {
				return m.RemoveFromProvider(placement)
			})
		}
	}

	// Save updated torrent
	if err := m.AddOrUpdate(torrent, func(t *storage.Entry) {
		m.RefreshEntries(false)
	}); err != nil {
		job.Status = storage.SwitcherStatusFailed
		job.Error = fmt.Sprintf("failed to update torrent: %v", err)
		m.logger.Error().Err(err).Msg("Failed to update torrent after migration")
	} else {
		job.Status = storage.SwitcherStatusCompleted
		job.Progress = 100
	}

	job.CompletedAt = new(time.Now())

	m.logger.Info().
		Str("job_id", job.ID).
		Str("status", string(job.Status)).
		Msg("Migration completed")
}
