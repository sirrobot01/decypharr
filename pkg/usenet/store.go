package usenet

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sourcegraph/conc/pool"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type fileInfo struct {
	id      string
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.isDir }
func (fi *fileInfo) ID() string         { return fi.id }
func (fi *fileInfo) Sys() interface{}   { return nil }

type Store interface {
	Add(nzb *NZB) error
	Get(nzoID string) *NZB
	GetByName(name string) *NZB
	Update(nzb *NZB) error
	UpdateFile(nzoID string, file *NZBFile) error
	Delete(nzoID string) error
	Count() int
	Filter(category string, limit int, status ...string) []*NZB
	GetHistory(category string, limit int) []*NZB
	UpdateStatus(nzoID string, status string) error
	Close() error
	GetListing(folder string) []os.FileInfo
	Load() error

	// GetQueueItem Queue management

	GetQueueItem(nzoID string) *NZB
	AddToQueue(nzb *NZB)
	RemoveFromQueue(nzoID string)
	GetQueue() []*NZB
	AtomicDelete(nzoID string) error

	RemoveFile(nzoID string, filename string) error

	MarkAsCompleted(nzoID string, storage string) error
}

type store struct {
	storePath  string
	listing    atomic.Value
	badListing atomic.Value
	queue      *xsync.Map[string, *NZB]
	titles     *xsync.Map[string, string] // title -> nzoID
	config     *config.Usenet
	logger     zerolog.Logger
}

func NewStore(config *config.Config, logger zerolog.Logger) Store {
	err := os.MkdirAll(config.NZBsPath(), 0755)
	if err != nil {
		return nil
	}

	s := &store{
		storePath: config.NZBsPath(),
		queue:     xsync.NewMap[string, *NZB](),
		titles:    xsync.NewMap[string, string](),
		config:    config.Usenet,
		logger:    logger,
	}
	return s
}

func (ns *store) Load() error {
	ids, err := ns.getAllIDs()
	if err != nil {
		return err
	}

	listing := make([]os.FileInfo, 0)
	badListing := make([]os.FileInfo, 0)

	for _, id := range ids {
		nzb, err := ns.loadFromFile(id)
		if err != nil {
			continue // Skip if file cannot be loaded
		}

		ns.titles.Store(nzb.Name, nzb.ID)

		fileInfo := &fileInfo{
			id:      nzb.ID,
			name:    nzb.Name,
			size:    nzb.TotalSize,
			mode:    0644,
			modTime: nzb.AddedOn,
			isDir:   true,
		}

		listing = append(listing, fileInfo)
		if nzb.IsBad {
			badListing = append(badListing, fileInfo)
		}
	}

	ns.listing.Store(listing)
	ns.badListing.Store(badListing)

	return nil
}

// getFilePath returns the file path for an NZB
func (ns *store) getFilePath(nzoID string) string {
	return filepath.Join(ns.storePath, nzoID+".json")
}

func (ns *store) loadFromFile(nzoID string) (*NZB, error) {
	filePath := ns.getFilePath(nzoID)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var compact CompactNZB
	if err := json.Unmarshal(data, &compact); err != nil {
		return nil, err
	}

	return compact.toNZB(), nil
}

// saveToFile saves an NZB to file
func (ns *store) saveToFile(nzb *NZB) error {
	filePath := ns.getFilePath(nzb.ID)

	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	compact := nzb.toCompact()
	data, err := json.Marshal(compact) // Use compact JSON
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

func (ns *store) refreshListing() error {
	ids, err := ns.getAllIDs()
	if err != nil {
		return err
	}

	listing := make([]os.FileInfo, 0, len(ids))
	badListing := make([]os.FileInfo, 0, len(ids))

	for _, id := range ids {
		nzb, err := ns.loadFromFile(id)
		if err != nil {
			continue // Skip if file cannot be loaded
		}
		fileInfo := &fileInfo{
			id:      nzb.ID,
			name:    nzb.Name,
			size:    nzb.TotalSize,
			mode:    0644,
			modTime: nzb.AddedOn,
			isDir:   true,
		}
		listing = append(listing, fileInfo)
		ns.titles.Store(nzb.Name, nzb.ID)
		if nzb.IsBad {
			badListing = append(badListing, fileInfo)
		}
	}

	// Update all structures atomically
	ns.listing.Store(listing)
	ns.badListing.Store(badListing)

	// Refresh rclone if configured
	go func() {
		if err := ns.refreshRclone(); err != nil {
			ns.logger.Error().Err(err).Msg("Failed to refresh rclone")
		}
	}()

	return nil
}

func (ns *store) Add(nzb *NZB) error {
	if nzb == nil {
		return fmt.Errorf("nzb cannot be nil")
	}
	if err := ns.saveToFile(nzb); err != nil {
		return err
	}

	ns.titles.Store(nzb.Name, nzb.ID)

	go func() {
		_ = ns.refreshListing()
	}()
	return nil
}

func (ns *store) GetByName(name string) *NZB {

	if nzoID, exists := ns.titles.Load(name); exists {
		return ns.Get(nzoID)
	}
	return nil
}

func (ns *store) GetQueueItem(nzoID string) *NZB {
	if item, exists := ns.queue.Load(nzoID); exists {
		return item
	}
	return nil
}

func (ns *store) AddToQueue(nzb *NZB) {
	if nzb == nil {
		return
	}
	ns.queue.Store(nzb.ID, nzb)
}

func (ns *store) RemoveFromQueue(nzoID string) {
	if nzoID == "" {
		return
	}
	ns.queue.Delete(nzoID)
}

func (ns *store) GetQueue() []*NZB {
	var queueItems []*NZB
	ns.queue.Range(func(_ string, value *NZB) bool {
		queueItems = append(queueItems, value)
		return true // continue iteration
	})
	return queueItems
}

func (ns *store) Get(nzoID string) *NZB {
	nzb, err := ns.loadFromFile(nzoID)
	if err != nil {
		return nil
	}
	return nzb
}
func (ns *store) Update(nzb *NZB) error {
	if err := ns.saveToFile(nzb); err != nil {
		return err
	}
	return nil
}

func (ns *store) Delete(nzoID string) error {
	return ns.AtomicDelete(nzoID)
}

// AtomicDelete performs an atomic delete operation across all data structures
func (ns *store) AtomicDelete(nzoID string) error {
	if nzoID == "" {
		return fmt.Errorf("nzoID cannot be empty")
	}

	filePath := ns.getFilePath(nzoID)

	// Get NZB info before deletion for cleanup
	nzb := ns.Get(nzoID)
	if nzb == nil {
		// Check if file exists on disk even if not in cache
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return nil // Already deleted
		}
	}
	ns.queue.Delete(nzoID)

	if nzb != nil {
		ns.titles.Delete(nzb.Name)
	}

	if currentListing := ns.listing.Load(); currentListing != nil {
		oldListing := currentListing.([]os.FileInfo)
		newListing := make([]os.FileInfo, 0, len(oldListing))
		for _, fi := range oldListing {
			if fileInfo, ok := fi.(*fileInfo); ok && fileInfo.id != nzoID {
				newListing = append(newListing, fi)
			}
		}
		ns.listing.Store(newListing)
	}

	if currentListing := ns.badListing.Load(); currentListing != nil {
		oldListing := currentListing.([]os.FileInfo)
		newListing := make([]os.FileInfo, 0, len(oldListing))
		for _, fi := range oldListing {
			if fileInfo, ok := fi.(*fileInfo); ok && fileInfo.id != nzoID {
				newListing = append(newListing, fi)
			}
		}
		ns.badListing.Store(newListing)
	}

	// Remove file from disk
	return os.Remove(filePath)
}

func (ns *store) RemoveFile(nzoID string, filename string) error {
	if nzoID == "" || filename == "" {
		return fmt.Errorf("nzoID and filename cannot be empty")
	}

	nzb := ns.Get(nzoID)
	if nzb == nil {
		return fmt.Errorf("nzb with nzoID %s not found", nzoID)
	}
	err := nzb.MarkFileAsRemoved(filename)
	if err != nil {
		return err
	}
	if err := ns.Update(nzb); err != nil {
		return fmt.Errorf("failed to update nzb after removing file %s: %w", filename, err)
	}
	// Refresh listing after file removal
	_ = ns.refreshListing()
	// Remove file from rclone cache if configured
	return nil
}

func (ns *store) getAllIDs() ([]string, error) {
	var ids []string

	err := filepath.WalkDir(ns.storePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(d.Name(), ".json") {
			id := strings.TrimSuffix(d.Name(), ".json")
			ids = append(ids, id)
		}
		return nil
	})

	return ids, err
}

func (ns *store) Filter(category string, limit int, status ...string) []*NZB {
	ids, err := ns.getAllIDs()
	if err != nil {
		return nil
	}

	statusSet := make(map[string]struct{})
	for _, s := range status {
		statusSet[s] = struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := pool.New().WithContext(ctx).WithMaxGoroutines(10)

	var results []*NZB
	var mu sync.Mutex
	var found atomic.Int32

	for _, id := range ids {
		id := id
		p.Go(func(ctx context.Context) error {
			// Early exit if limit reached
			if limit > 0 && found.Load() >= int32(limit) {
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				nzb := ns.Get(id)
				if nzb == nil {
					return nil
				}

				// Apply filters
				if category != "" && nzb.Category != category {
					return nil
				}

				if len(statusSet) > 0 {
					if _, exists := statusSet[nzb.Status]; !exists {
						return nil
					}
				}

				// Add to results with limit check
				mu.Lock()
				if limit == 0 || len(results) < limit {
					results = append(results, nzb)
					found.Add(1)

					// Cancel if we hit the limit
					if limit > 0 && len(results) >= limit {
						cancel()
					}
				}
				mu.Unlock()

				return nil
			}
		})
	}

	if err := p.Wait(); err != nil {
		return nil
	}
	return results
}

func (ns *store) Count() int {
	ids, err := ns.getAllIDs()
	if err != nil {
		return 0
	}
	return len(ids)
}

func (ns *store) GetHistory(category string, limit int) []*NZB {
	return ns.Filter(category, limit, "completed", "failed", "error")
}

func (ns *store) UpdateStatus(nzoID string, status string) error {
	nzb := ns.Get(nzoID)
	if nzb == nil {
		return fmt.Errorf("nzb with nzoID %s not found", nzoID)
	}

	nzb.Status = status
	nzb.LastActivity = time.Now()

	if status == "completed" {
		nzb.CompletedOn = time.Now()
		nzb.Progress = 100
		nzb.Percentage = 100
	}
	if status == "failed" {
		// Remove from cache if failed
		err := ns.Delete(nzb.ID)
		if err != nil {
			return err
		}
	}

	return ns.Update(nzb)
}

func (ns *store) Close() error {
	// Clear cache
	ns.queue = xsync.NewMap[string, *NZB]()
	// Clear listings
	ns.listing = atomic.Value{}
	ns.badListing = atomic.Value{}
	// Clear titles
	ns.titles = xsync.NewMap[string, string]()
	return nil
}

func (ns *store) UpdateFile(nzoID string, file *NZBFile) error {
	if nzoID == "" || file == nil {
		return fmt.Errorf("nzoID and file cannot be empty")
	}

	nzb := ns.Get(nzoID)
	if nzb == nil {
		return fmt.Errorf("nzb with nzoID %s not found", nzoID)
	}

	// Update file in NZB
	for i, f := range nzb.Files {
		if f.Name == file.Name {
			nzb.Files[i] = *file
			break
		}
	}

	if err := ns.Update(nzb); err != nil {
		return fmt.Errorf("failed to update nzb after updating file %s: %w", file.Name, err)
	}

	// Refresh listing after file update
	return ns.refreshListing()
}

func (ns *store) GetListing(folder string) []os.FileInfo {
	switch folder {
	case "__bad__":
		if badListing, ok := ns.badListing.Load().([]os.FileInfo); ok {
			return badListing
		}
		return []os.FileInfo{}
	default:
		if listing, ok := ns.listing.Load().([]os.FileInfo); ok {
			return listing
		}
		return []os.FileInfo{}
	}
}

func (ns *store) MarkAsCompleted(nzoID string, storage string) error {
	if nzoID == "" {
		return fmt.Errorf("nzoID cannot be empty")
	}

	// Get NZB from queue
	queueNZB := ns.GetQueueItem(nzoID)
	if queueNZB == nil {
		return fmt.Errorf("NZB %s not found in queue", nzoID)
	}

	// Update NZB status
	queueNZB.Status = "completed"
	queueNZB.Storage = storage
	queueNZB.CompletedOn = time.Now()
	queueNZB.LastActivity = time.Now()
	queueNZB.Progress = 100
	queueNZB.Percentage = 100

	// Atomically: remove from queue and add to storage
	ns.queue.Delete(nzoID)
	if err := ns.Add(queueNZB); err != nil {
		// Rollback: add back to queue if storage fails
		ns.queue.Store(nzoID, queueNZB)
		return fmt.Errorf("failed to store completed NZB: %w", err)
	}

	return nil
}

func (ns *store) refreshRclone() error {

	if ns.config.RcUrl == "" {
		return nil
	}

	client := http.DefaultClient
	// Create form data
	data := ns.buildRcloneRequestData()

	if err := ns.sendRcloneRequest(client, "vfs/forget", data); err != nil {
		ns.logger.Error().Err(err).Msg("Failed to send rclone vfs/forget request")
	}

	if err := ns.sendRcloneRequest(client, "vfs/refresh", data); err != nil {
		ns.logger.Error().Err(err).Msg("Failed to send rclone vfs/refresh request")
	}

	return nil
}

func (ns *store) buildRcloneRequestData() string {
	return "dir=__all__"
}

func (ns *store) sendRcloneRequest(client *http.Client, endpoint, data string) error {
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/%s", ns.config.RcUrl, endpoint), strings.NewReader(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if ns.config.RcUser != "" && ns.config.RcPass != "" {
		req.SetBasicAuth(ns.config.RcUser, ns.config.RcPass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			ns.logger.Error().Err(err).Msg("Failed to close response body")
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to perform %s: %s - %s", endpoint, resp.Status, string(body))
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
