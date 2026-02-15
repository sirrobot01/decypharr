package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Fixer handles torrent repair with cascading re-insertion across debrids
type Fixer struct {
	manager *Manager

	// Track re-insertion attempts and failures
	failedToReinsert   *xsync.Map[string, struct{}]      // infohash:debrid -> failed completely
	inFlightRepairs    *xsync.Map[string, *FixerRequest] // infohash -> repair request
	providerOrder      []string                          // Order of providers to try (from config)
	maxReinsertRetries int
}

// FixerRequest tracks an ongoing repair operation
type FixerRequest struct {
	InfoHash         string
	CurrentDebrid    string
	AttemptedDebrids []string
	StartedAt        time.Time
	LastAttempt      time.Time
	result           chan *FixResult
}

// FixResult is the result of a fix operation
type FixResult struct {
	Success       bool
	NewDebrid     string
	Error         error
	AttemptsCount int
}

// NewFixer creates a new Fixer instance
func NewFixer(manager *Manager) *Fixer {
	// GetReader debrid order from config
	cfg := config.Get()
	debridOrder := make([]string, 0, len(cfg.Debrids))
	for _, d := range cfg.Debrids {
		debridOrder = append(debridOrder, d.Name)
	}

	return &Fixer{
		manager:            manager,
		failedToReinsert:   xsync.NewMap[string, struct{}](),
		inFlightRepairs:    xsync.NewMap[string, *FixerRequest](),
		providerOrder:      debridOrder,
		maxReinsertRetries: 2, // retry each debrid up to 2 times
	}
}

// FixTorrent attempts to fix a broken torrent by re-inserting across debrids
// Strategy:
// 1. Try to re-insert on current active debrid, except if skipCurrent is true
// 2. If fails, cascade through other debrids in config order
// 3. Skip debrids where torrent already exists (unless they're also broken)
// 4. Mark as completely failed if all debrids fail
func (f *Fixer) FixTorrent(ctx context.Context, entry *storage.Entry, skipCurrent bool) (*FixResult, error) {
	if entry == nil {
		return nil, fmt.Errorf("entry is nil")
	}
	if !entry.CanBeFixed() {
		return &FixResult{
			Success:       false,
			Error:         fmt.Errorf("entry %s cannot be fixed", entry.Name),
			AttemptsCount: 0,
		}, nil
	}
	// Check if repair is already in flight
	if req, exists := f.inFlightRepairs.Load(entry.InfoHash); exists {

		// Wait for existing repair to complete
		select {
		case result := <-req.result:
			return result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Minute):
			return nil, fmt.Errorf("repair timeout for %s", entry.Name)
		}
	}

	// Create new repair request
	req := &FixerRequest{
		InfoHash:         entry.InfoHash,
		AttemptedDebrids: make([]string, 0),
		StartedAt:        time.Now(),
		LastAttempt:      time.Now(),
		result:           make(chan *FixResult, 1),
	}
	f.inFlightRepairs.Store(entry.InfoHash, req)
	defer f.inFlightRepairs.Delete(entry.InfoHash)
	req.CurrentDebrid = entry.ActiveProvider

	// Build debrid attempt order: current debrid first, then others in config order
	attemptOrder := f.buildAttemptOrder(entry, skipCurrent)

	var lastErr error
	totalAttempts := 0

	for _, debridName := range attemptOrder {

		// Check if entry has been marked as failed to re-insert
		if f.IsFailedToReinsert(entry.InfoHash, debridName) {
			continue
		}

		select {
		case <-ctx.Done():
			result := &FixResult{Success: false, Error: ctx.Err(), AttemptsCount: totalAttempts}
			req.result <- result
			return result, ctx.Err()
		default:
		}

		req.AttemptedDebrids = append(req.AttemptedDebrids, debridName)
		req.LastAttempt = time.Now()

		f.manager.logger.Trace().
			Str("debrid", debridName).
			Str("infohash", entry.InfoHash).
			Int("attempt", totalAttempts+1).
			Msg("Attempting re-insertion")

		// Attempt re-insertion on this debrid
		success, err := f.MoveTorrent(entry, debridName, true)
		totalAttempts++

		if success {
			f.manager.logger.Info().
				Str("debrid", debridName).
				Str("name", entry.Name).
				Str("infohash", entry.InfoHash).
				Msg("Successfully re-inserted entry")

			// Mark as successful
			f.ResetFailureState(entry.InfoHash)

			result := &FixResult{
				Success:       true,
				NewDebrid:     debridName,
				Error:         nil,
				AttemptsCount: totalAttempts,
			}
			req.result <- result
			return result, nil
		}

		lastErr = err
		// Add failed state for this debrid
		f.failedToReinsert.Store(fmt.Sprintf("%s:%s", entry.InfoHash, debridName), struct{}{})
	}

	// All debrids failed - mark as completely failed
	f.manager.logger.Error().
		Str("infohash", entry.InfoHash).
		Int("attempts", totalAttempts).
		Msg("All re-insertion attempts failed")

	f.failedToReinsert.Store(entry.InfoHash, struct{}{})

	// Mark entry as bad
	entry.Bad = true
	entry.UpdatedAt = time.Now()
	_ = f.manager.AddOrUpdate(entry, func(t *storage.Entry) {
		f.manager.RefreshEntries(true)
	})

	result := &FixResult{
		Success:       false,
		Error:         fmt.Errorf("all re-insertion attempts failed: %w", lastErr),
		AttemptsCount: totalAttempts,
	}
	req.result <- result
	return result, result.Error
}

// MoveTorrent attempts to re-insert a torrent on a specific debrid
func (f *Fixer) MoveTorrent(entry *storage.Entry, debridName string, reinsert bool) (bool, error) {
	// Check if entry can be moved
	if entry == nil {
		return false, fmt.Errorf("entry is nil")
	}
	if !entry.CanBeMoved() {
		return false, fmt.Errorf("entry %s cannot be moved", entry.Name)
	}

	defer func() {
		// Save to storage
		_ = f.manager.AddOrUpdate(entry, nil) // No need to refresh mounts
	}()

	client := f.manager.ProviderClient(debridName)
	if client == nil {
		return false, fmt.Errorf("debrid client %s not found", debridName)
	}

	// Check if placement already exists on this debrid
	placement, hasPlacement := entry.Providers[entry.ActiveProvider]
	var oldID string
	if hasPlacement && placement != nil && placement.ID != "" && !reinsert {
		// Activate the existing placement
		if err := entry.ActivatePlacement(debridName); err != nil {
			return false, fmt.Errorf("failed to activate existing placement: %w", err)
		}
		entry.Bad = false
		entry.UpdatedAt = time.Now()
		return true, nil
	}
	if placement != nil {
		oldID = placement.ID
	}

	// Construct magnet
	magnet, err := utils.GetMagnetInfo(entry.Magnet, f.manager.config.AlwaysRmTrackerUrls)
	if err != nil {
		magnet = utils.ConstructMagnet(entry.InfoHash, entry.Name)
	}

	// Try to load .torrent file for better re-insertion success
	if torrentData, err := storage.LoadTorrentFile(entry.InfoHash); err == nil {
		magnet.File = torrentData
	}

	// Submit to debrid
	newDebridTorrent := &types.Torrent{
		Name:             entry.Name,
		Magnet:           magnet,
		InfoHash:         entry.InfoHash,
		Size:             entry.Size,
		Files:            make(map[string]types.File),
		DownloadUncached: false,
	}

	if config.Get().AlwaysRmTrackerUrls && newDebridTorrent.Magnet.IsTorrent() {
		if sanitized, err := utils.GetTorrentInfo(newDebridTorrent.Magnet.File, true); err == nil {
			newDebridTorrent.Magnet.File = sanitized.File
			newDebridTorrent.Magnet.Link = sanitized.Link
		}
	}

	newDebridTorrent, err = client.SubmitMagnet(newDebridTorrent)
	if err != nil {
		return false, fmt.Errorf("failed to submit magnet: %w", err)
	}

	if newDebridTorrent == nil || newDebridTorrent.Id == "" {
		return false, fmt.Errorf("failed to submit magnet: empty entry")
	}

	// Check status
	newDebridTorrent.DownloadUncached = false
	newDebridTorrent, err = client.CheckStatus(newDebridTorrent)
	if err != nil {
		// Delete the failed entry
		if newDebridTorrent != nil && newDebridTorrent.Id != "" {
			_ = client.DeleteTorrent(newDebridTorrent.Id)
		}
		return false, fmt.Errorf("failed to check status: %w", err)
	}

	// Verify files have links
	if len(newDebridTorrent.Files) == 0 {
		_ = client.DeleteTorrent(newDebridTorrent.Id)
		return false, fmt.Errorf("no files in entry after re-insertion")
	}

	for _, f := range newDebridTorrent.GetFiles() {
		if f.Link == "" && f.Id == "" {
			_ = client.DeleteTorrent(newDebridTorrent.Id)
			return false, fmt.Errorf("empty link/id for file %s", f.Name)
		}
	}

	addedOn := newDebridTorrent.Added
	if addedOn.IsZero() {
		addedOn = time.Now()
	}

	// Update entry with new placement
	_ = entry.AddTorrentProvider(newDebridTorrent)
	// Update global file metadata (revives files that previously existed)
	if entry.Files == nil {
		entry.Files = make(map[string]*storage.File)
	}
	for _, f := range newDebridTorrent.GetFiles() {
		if existing, exists := entry.Files[f.Name]; exists {
			existing.Size = f.Size
			existing.ByteRange = f.ByteRange
			existing.Deleted = false
			existing.InfoHash = entry.InfoHash
			existing.AddedOn = addedOn
		} else {
			entry.Files[f.Name] = &storage.File{
				Name:      f.Name,
				Size:      f.Size,
				ByteRange: f.ByteRange,
				Deleted:   false,
				InfoHash:  entry.InfoHash,
				AddedOn:   addedOn,
			}
		}
	}

	// Activate this debrid
	if err := entry.ActivatePlacement(debridName); err != nil {
		f.manager.logger.Warn().Err(err).Msg("failed to activate placement")
	}

	entry.Bad = false
	entry.UpdatedAt = time.Now()

	// Delete old entry from debrid if different ID
	if oldID != "" && oldID != newDebridTorrent.Id {
		go func() {
			_ = client.DeleteTorrent(oldID)
		}()
	}

	return true, nil
}

// buildAttemptOrder creates the order of debrids to attempt re-insertion
// Priority: current active debrid first, then others in config order
// If skipCurrent is true, current active debrid is skipped
func (f *Fixer) buildAttemptOrder(torrent *storage.Entry, skipCurrent bool) []string {
	order := make([]string, 0, len(f.providerOrder))

	// AddOrUpdate other debrids in config order
	for _, debridName := range f.providerOrder {
		if debridName == torrent.ActiveProvider && skipCurrent {
			continue
		}
		order = append(order, debridName)
	}

	return order
}

// IsFailedToReinsert checks if a torrent has been marked as failed to re-insert
func (f *Fixer) IsFailedToReinsert(infohash, debrid string) bool {
	_, failed := f.failedToReinsert.Load(fmt.Sprintf("%s:%s", infohash, debrid))
	return failed
}

// ResetFailureState manually resets the failure state for a torrent
func (f *Fixer) ResetFailureState(infohash string) {
	f.failedToReinsert.Delete(infohash)
}
