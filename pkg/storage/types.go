package storage

import (
	"fmt"
	"math"
	"path"
	"path/filepath"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type (
	SwitcherStatus string
	TorrentState   string
)

const (
	SwitcherStatusPending    SwitcherStatus = "pending"
	SwitcherStatusInProgress SwitcherStatus = "in_progress"
	SwitcherStatusCompleted  SwitcherStatus = "completed"
	SwitcherStatusFailed     SwitcherStatus = "failed"
	SwitcherStatusCancelled  SwitcherStatus = "cancelled"

	EntryStateDownloading TorrentState = "downloading"
	EntryStatePausedDL    TorrentState = "pausedDL"
	EntryStatePausedUP    TorrentState = "pausedUP"
	EntryStateError       TorrentState = "error"
)

// Common errors
var (
	ErrPlacementNotFound     = fmt.Errorf("providerEntry not found")
	ErrAlreadyOnDebrid       = fmt.Errorf("torrent already on this debrid")
	ErrPlacementNotCompleted = fmt.Errorf("providerEntry not completed")
	ErrNoActivePlacement     = fmt.Errorf("no active providerEntry")
)

// Entry is the unified model across debrids and nzbs
type Entry struct {
	Protocol         config.Protocol `msgpack:"protocol" json:"protocol"`                   // torrent or nzb
	InfoHash         string          `msgpack:"info_hash" json:"info_hash"`                 // Primary key - torrent hash
	Name             string          `msgpack:"name" json:"name"`                           // Entry name
	OriginalFilename string          `msgpack:"original_filename" json:"original_filename"` // Original filename from debrid
	Size             int64           `msgpack:"size" json:"size"`                           // Total size in bytes (for QBit compat)
	Bytes            int64           `msgpack:"bytes" json:"bytes"`                         // Actual bytes (debrid uses this)
	Magnet           string          `msgpack:"magnet,omitempty" json:"magnet,omitempty"`   // Magnet link

	IsDownloading  bool  `msgpack:"is_downloading,omitempty" json:"is_downloading,omitempty"`   // Whether currently downloading(this is for local download)
	SizeDownloaded int64 `msgpack:"size_downloaded,omitempty" json:"size_downloaded,omitempty"` // Actual downloaded bytes

	// Multi-Provider ProviderEntry Strategy
	ActiveProvider string                    `msgpack:"active_provider" json:"active_provider"` // Current active debrid
	Providers      map[string]*ProviderEntry `msgpack:"providers" json:"providers"`             // debrid -> ProviderEntry details

	// Files (from debrid cache)
	Files map[string]*File `msgpack:"files" json:"files"` // filename -> File details

	State TorrentState `msgpack:"state" json:"state"` // This is for QBitTorrent compatibility
	// Provider State (from active providerEntry)
	Status   debridTypes.TorrentStatus `msgpack:"status" json:"status"`     // downloaded, downloading, queued, error
	Progress float64                   `msgpack:"progress" json:"progress"` // Download progress (0-100)
	Speed    int64                     `msgpack:"speed" json:"speed"`       // Download speed
	Seeders  int                       `msgpack:"seeders" json:"seeders"`   // Number of seeders

	IsComplete bool `msgpack:"is_complete" json:"is_complete"` // Ready for use
	Bad        bool `msgpack:"bad" json:"bad"`                 // Marked as bad/corrupted

	// Metadata
	Category    string   `msgpack:"category,omitempty" json:"category,omitempty"`         // Category (e.g., sonarr, radarr)
	Tags        []string `msgpack:"tags,omitempty" json:"tags,omitempty"`                 // User-defined tags
	MountPath   string   `msgpack:"mount_path" json:"mount_path"`                         // Mount path for this torrent
	SavePath    string   `msgpack:"save_path,omitempty" json:"save_path,omitempty"`       // Download/symlink folder
	ContentPath string   `msgpack:"content_path,omitempty" json:"content_path,omitempty"` // Final content path

	// Timestamps
	AddedOn     time.Time  `msgpack:"added_on" json:"added_on"`                             // When first added (from debrid)
	CreatedAt   time.Time  `msgpack:"created_at" json:"created_at"`                         // When created in manager
	UpdatedAt   time.Time  `msgpack:"updated_at" json:"updated_at"`                         // Last update time
	CompletedAt *time.Time `msgpack:"completed_at,omitempty" json:"completed_at,omitempty"` // When completed
	ImportedAt  *time.Time `msgpack:"imported_at,omitempty" json:"imported_at,omitempty"`   // When imported by Arr

	// Import Request Data (for processing)
	Action           config.DownloadAction `msgpack:"action,omitempty" json:"action,omitempty"`                       // symlink, download, strm none
	DownloadUncached bool                  `msgpack:"download_uncached,omitempty" json:"download_uncached,omitempty"` // Force uncached download
	CallbackURL      string                `msgpack:"callback_url,omitempty" json:"callback_url,omitempty"`           // Callback URL for completion
	SkipMultiSeason  bool                  `msgpack:"skip_multi_season,omitempty" json:"skip_multi_season,omitempty"` // Skip multi-season detection

	// Error tracking
	LastError     string     `msgpack:"last_error,omitempty" json:"last_error,omitempty"`           // Last error message
	ErrorCount    int        `msgpack:"error_count,omitempty" json:"error_count,omitempty"`         // Number of errors
	LastErrorTime *time.Time `msgpack:"last_error_time,omitempty" json:"last_error_time,omitempty"` // Last error time
}

func (e *Entry) IsTorrent() bool {
	return e.Protocol == config.ProtocolTorrent
}

func (e *Entry) IsNZB() bool {
	return e.Protocol == config.ProtocolNZB
}

func (e *Entry) Validate() error {
	activeProvider := e.GetActiveProvider()
	if activeProvider == nil {
		return fmt.Errorf("no active providerEntry")
	}
	// Check if active providerEntry is completed
	if len(activeProvider.Files) == 0 {
		return fmt.Errorf("no files in active providerEntry")
	}
	return nil
}

// Sanitize replaces any non-finite float64 values with 0 so the entry can be
// safely JSON-encoded. This guards against NaN/Inf produced by division-by-zero
// when a debrid provider reports size=0 for an in-progress torrent.
func (e *Entry) Sanitize() {
	if math.IsNaN(e.Progress) || math.IsInf(e.Progress, 0) {
		e.Progress = 0
	}
	for _, p := range e.Providers {
		if math.IsNaN(p.Progress) || math.IsInf(p.Progress, 0) {
			p.Progress = 0
		}
	}
}

// CanBeFixed checks if the entry can be repaired
// TODO: Add more checks later. This will be done when we add other NZB source like TB(nzb)
func (e *Entry) CanBeFixed() bool {
	return e.IsTorrent()
}

func (e *Entry) CanBeMoved() bool {
	return e.IsTorrent()
}

// EntryItem These are torrents by names.
// This keeps track of multiple torrents with the same folder name
// Comprises only files(which has their respective infohashes) and placements
type EntryItem struct {
	Name  string           `msgpack:"name" json:"name"`   // Folder name
	Files map[string]*File `msgpack:"files" json:"files"` // filename -> File details
	Size  int64            `msgpack:"size" json:"size"`   // Total size of all files
}

func (e *EntryItem) GetFile(filename string) (*File, error) {
	if e.Files == nil {
		return nil, fmt.Errorf("failed to get entry item, file is nil")
	}
	f, exists := e.Files[filename]
	if !exists {
		return nil, fmt.Errorf("failed to get entry item, file does not exist")
	}
	if f.Deleted {
		return nil, fmt.Errorf("failed to get entry item, file is deleted")
	}
	return f, nil
}

func (e *EntryItem) GetSize() int64 {
	size := int64(0)
	for _, f := range e.Files {
		if !f.Deleted {
			size += f.Size
		}
	}
	return size
}

func (e *EntryItem) GetFirstFile() (*File, error) {
	for _, f := range e.Files {
		return f, nil
	}
	return nil, fmt.Errorf("no active files found")
}

func (e *EntryItem) GetActiveFiles() []*File {
	files := make([]*File, 0, len(e.Files))
	for _, f := range e.Files {
		if !f.Deleted {
			files = append(files, f)
		}
	}
	return files
}

type File struct {
	Name      string    `msgpack:"name" json:"name"`
	Path      string    `msgpack:"path,omitempty" json:"path,omitempty"`
	AddedOn   time.Time `msgpack:"added_on" json:"added_on"`
	Size      int64     `msgpack:"size" json:"size"`
	ByteRange *[2]int64 `msgpack:"byte_range,omitempty" json:"byte_range,omitempty"`
	Deleted   bool      `msgpack:"deleted" json:"deleted"`
	InfoHash  string    `msgpack:"infohash,omitempty" json:"infohash,omitempty"` // Parent infohash(might be an nzb or torrent)
}

// ProviderFile represents debrid-specific file information
type ProviderFile struct {
	Id   string `msgpack:"id,omitempty" json:"id,omitempty"`     // For TorBox-style providers (file_id)
	Link string `msgpack:"link,omitempty" json:"link,omitempty"` // For RealDebrid/AllDebrid-style providers (restricted URL)
	Path string `msgpack:"path,omitempty" json:"path,omitempty"` // Path within the debrid's filesystem
}

// ProviderEntry represents a torrent's providerEntry on a specific debrid service
type ProviderEntry struct {
	Provider  string                    `msgpack:"provider,omitempty" json:"provider,omitempty"`
	ID        string                    `msgpack:"debrid_id" json:"id"`                              // ID in that debrid service (e.g., L3734BKKKSBA6)
	AddedAt   time.Time                 `msgpack:"added_at" json:"added_at"`                         // When added to this debrid
	RemovedAt *time.Time                `msgpack:"removed_at,omitempty" json:"removed_at,omitempty"` // When removed (if archived)
	Status    debridTypes.TorrentStatus `msgpack:"status" json:"status"`                             // ProviderEntry status
	Progress  float64                   `msgpack:"progress" json:"progress"`                         // Download progress on this debrid (0-100)

	// Provider-specific file information
	Files map[string]*ProviderFile `msgpack:"files" json:"files"` // filename -> debrid-specific file info

	// Cached data from debrid (avoid re-fetching)
	DownloadedAt *time.Time `msgpack:"downloaded_at,omitempty" json:"downloaded_at,omitempty"` // When download completed on debrid
}

// NeedsUpdate checks if this placement is stale compared to the remote torrent.
// Returns true if the stored placement should be refreshed.
func (p *ProviderEntry) NeedsUpdate(remote *debridTypes.Torrent) bool {
	if p.ID != remote.Id {
		return true // Re-added on debrid with a different ID
	}
	if p.Status != remote.Status {
		return true // Status changed (e.g., downloading → downloaded)
	}

	if len(p.Files) == 0 {
		return true
	}
	return false
}

func (p *ProviderEntry) IsValid() bool {
	if p.ID == "" || p.Provider == "" {
		return false
	}
	// Check if all files have necessary info
	for _, pf := range p.Files {
		if pf.Id == "" || pf.Link == "" {
			return false
		}
	}
	return true
}

// GetActiveProvider returns the active providerEntry
func (e *Entry) GetActiveProvider() *ProviderEntry {
	if e.Providers == nil || e.ActiveProvider == "" {
		return nil
	}
	providerEntry, exists := e.Providers[e.ActiveProvider]
	if !exists {
		return nil
	}
	return providerEntry
}

func (e *Entry) AddUsenetProvider(metadata *NZB) *ProviderEntry {
	if e.Providers == nil {
		e.Providers = make(map[string]*ProviderEntry)
	}
	providerEntry := &ProviderEntry{
		Provider: "usenet",
		ID:       metadata.ID,
		AddedAt:  time.Now(),
		Status:   debridTypes.TorrentStatusDownloaded,
		Files:    make(map[string]*ProviderFile),
	}
	for _, f := range metadata.Files {
		providerEntry.Files[f.Name] = &ProviderFile{
			Id:   f.Name,
			Link: path.Join(e.MountPath, f.Name),
			Path: path.Join(e.MountPath, f.Name),
		}
		e.Providers[f.Name] = providerEntry
	}
	e.Providers["usenet"] = providerEntry
	return providerEntry
}

// AddTorrentProvider adds or updates a providerEntry for a debrid
func (e *Entry) AddTorrentProvider(debridTorrent *debridTypes.Torrent) *ProviderEntry {
	if e.Providers == nil {
		e.Providers = make(map[string]*ProviderEntry)
	}

	providerEntry := &ProviderEntry{
		Provider: debridTorrent.Debrid,
		ID:       debridTorrent.Id,
		AddedAt:  time.Now(),
		Status:   debridTorrent.Status,
		Files:    make(map[string]*ProviderFile),
	}

	for _, f := range debridTorrent.GetFiles() {
		providerEntry.Files[f.Name] = &ProviderFile{
			Id:   f.Id,
			Link: f.Link,
			Path: f.Path,
		}
	}
	e.Providers[debridTorrent.Debrid] = providerEntry
	return providerEntry
}

// ActivatePlacement switches the active debrid
func (e *Entry) ActivatePlacement(debridName string) error {
	if e.Providers == nil {
		return ErrPlacementNotFound
	}

	// Find any providerEntry with this debrid name
	var foundPlacement *ProviderEntry
	for _, providerEntry := range e.Providers {
		if providerEntry.Provider == debridName {
			foundPlacement = providerEntry
			break
		}
	}

	if foundPlacement == nil {
		return ErrPlacementNotFound
	}

	if foundPlacement.Status != debridTypes.TorrentStatusDownloaded {
		return ErrPlacementNotCompleted
	}

	e.ActiveProvider = debridName
	e.UpdatedAt = time.Now()

	return nil
}

// RemoveProvider deletes a debrid torrent from the debrid itself
func (e *Entry) RemoveProvider(debridName string, cleanup func(providerEntry *ProviderEntry) error) {
	if e.Providers == nil {
		return
	}

	// Find and remove all placements with this debrid name
	var keysToDelete []string
	var placementsToCleanup []*ProviderEntry

	for key, providerEntry := range e.Providers {
		if providerEntry.Provider == debridName {
			keysToDelete = append(keysToDelete, key)
			placementsToCleanup = append(placementsToCleanup, providerEntry)
		}
	}

	// Delete the placements
	for _, key := range keysToDelete {
		delete(e.Providers, key)
	}

	// If the providerEntry is the active providerEntry, find a new active providerEntry
	if e.ActiveProvider == debridName {
		e.SwitchToNextProvider()
	}

	// Call cleanup function for each providerEntry if provided
	if cleanup != nil {
		for _, providerEntry := range placementsToCleanup {
			_ = cleanup(providerEntry)
		}
	}
}

// HasProvider checks if torrent exists on a debrid
func (e *Entry) HasProvider(provider string) bool {
	if e.Providers == nil {
		return false
	}
	_, exists := e.Providers[provider]
	return exists
}

// SwitchToNextProvider switches to the next completed providerEntry if available
func (e *Entry) SwitchToNextProvider() {
	if e.Providers == nil {
		return
	}
	for _, providerEntry := range e.Providers {
		if providerEntry.Status == debridTypes.TorrentStatusDownloaded {
			_ = e.ActivatePlacement(providerEntry.Provider)
			return
		}
	}
}

// MarkAsCompleted marks the torrent as completed
func (e *Entry) MarkAsCompleted(contentPath string) {
	e.State = EntryStatePausedUP
	e.IsDownloading = false
	e.IsComplete = true
	e.Progress = 1.0
	e.ContentPath = contentPath
	now := time.Now()
	e.CompletedAt = &now
	e.UpdatedAt = now
}

// MarkAsError marks the torrent as errored
func (e *Entry) MarkAsError(err error) {
	e.State = EntryStateError
	e.IsDownloading = false
	e.LastError = err.Error()
	e.ErrorCount++
	now := time.Now()
	e.LastErrorTime = &now
	e.UpdatedAt = now
}

func (e *Entry) GetFile(filename string) (*File, error) {
	if e.Files == nil {
		return nil, fmt.Errorf("failed to get entry file, files is nil")
	}
	f, exists := e.Files[filename]
	if !exists {
		return nil, fmt.Errorf("failed to get entry file, file not found")
	}
	if f.Deleted {
		return nil, fmt.Errorf("file deleted")
	}
	return f, nil
}

// RunChecks performs integrity checks on the Entry
// Returns whether to refresh and any error encountered
func (e *Entry) RunChecks() (bool, error) {
	if e.Bad {
		return false, fmt.Errorf("entry marked as bad")
	}
	activeProvider := e.GetActiveProvider()
	if activeProvider == nil {
		return true, fmt.Errorf("no active providerEntry") // need to refresh
	}
	if activeProvider.Status != debridTypes.TorrentStatusDownloaded {
		return true, fmt.Errorf("active providerEntry not completed") // need to refresh
	}

	if len(activeProvider.Files) == 0 {
		return true, fmt.Errorf("no files in active providerEntry") // need to refresh
	}

	// Then check all the files exists and files not deleted have links
	for _, f := range e.Files {
		if f.Deleted {
			continue
		}
		pf, exists := activeProvider.Files[f.Name]
		if !exists {
			return true, fmt.Errorf("file %s missing in active providerEntry", f.Name) // need to refresh
		}
		if pf.Link == "" {
			return true, fmt.Errorf("file %s has no link in active providerEntry", f.Name) // need to refresh
		}
	}
	return false, nil
}

func (e *Entry) GetActiveFiles() []*File {
	files := make([]*File, 0, len(e.Files))
	for _, f := range e.Files {
		if !f.Deleted {
			files = append(files, f)
		}
	}
	return files
}
func (e *Entry) GetFolder() string {
	// CHeck if the mount folder is empty or .
	return GetTorrentFolder(config.Get().FolderNaming, e)
}

// IsValid checks if the torrent has essential fields
func (e *Entry) IsValid() bool {
	// Check infohash
	if e.InfoHash == "" || e.Name == "" {
		return false
	}
	// Check if there is at least one providerEntry
	if len(e.Providers) == 0 {
		return false
	}
	activePlacement := e.GetActiveProvider()
	if activePlacement == nil {
		return false
	}
	// Check validity of active providerEntry
	if activePlacement.ID == "" || activePlacement.Provider == "" {
		return false
	}
	return activePlacement.IsValid()
}

// DownloadPath returns the expected download/symlink path for this entry
func (e *Entry) DownloadPath() string {
	return filepath.Join(e.SavePath, utils.RemoveExtension(e.Name))
}

// SwitcherJob tracks the progress of a migration operation
type SwitcherJob struct {
	ID             string         `msgpack:"id" json:"id"`
	InfoHash       string         `msgpack:"infohash" json:"info_hash"`                            // Entry being migrated
	SourceProvider string         `msgpack:"source_provider" json:"source_provider"`               // Source provider
	TargetProvider string         `msgpack:"target_provider" json:"target_provider"`               // Target provider
	Status         SwitcherStatus `msgpack:"status" json:"status"`                                 // Job status
	Progress       float64        `msgpack:"progress" json:"progress"`                             // Progress (0-100)
	Error          string         `msgpack:"error,omitempty" json:"error,omitempty"`               // Error message if failed
	CreatedAt      time.Time      `msgpack:"created_at" json:"created_at"`                         // When job started
	CompletedAt    *time.Time     `msgpack:"completed_at,omitempty" json:"completed_at,omitempty"` // When completed
	KeepOld        bool           `msgpack:"keep_old" json:"keep_old"`                             // Whether to keep old providerEntry(or remove it)
	WaitComplete   bool           `msgpack:"wait_complete" json:"wait_complete"`                   // Whether to wait for download
}

// SystemMigrationStatus tracks overall system migration from legacy to unified
type SystemMigrationStatus struct {
	Running   bool      `msgpack:"running" json:"running"`                           // Whether migration is running
	Total     int       `msgpack:"total" json:"total"`                               // Total torrents to migrate
	Completed int       `msgpack:"completed" json:"completed"`                       // Completed migrations
	Errors    int       `msgpack:"errors" json:"errors"`                             // Number of errors
	StartedAt time.Time `msgpack:"started_at" json:"started_at"`                     // When migration started
	UpdatedAt time.Time `msgpack:"updated_at" json:"updated_at"`                     // Last update
	ErrorList []string  `msgpack:"error_list,omitempty" json:"error_list,omitempty"` // List of errors
}

// CachedTorrent represents the debrid cache JSON format for migration
type CachedTorrent struct {
	ID               string                       `json:"id"`                // Debrid torrent ID
	InfoHash         string                       `json:"info_hash"`         // Entry info hash
	Name             string                       `json:"name"`              // Entry name
	Folder           string                       `json:"folder"`            // Folder name
	Filename         string                       `json:"filename"`          // Filename
	OriginalFilename string                       `json:"original_filename"` // Original filename
	Size             int64                        `json:"size"`              // Size (legacy)
	Bytes            int64                        `json:"bytes"`             // Actual bytes
	Magnet           interface{}                  `json:"magnet"`            // Magnet (can be nil)
	Files            map[string]*debridTypes.File `json:"files"`             // Files map
	Status           string                       `json:"status"`            // Status from debrid
	Added            string                       `json:"added"`             // Added timestamp
	Progress         float64                      `json:"progress"`          // Progress 0-100
	Speed            int64                        `json:"speed"`             // Speed
	Seeders          int                          `json:"seeders"`           // Seeders
	Links            []string                     `json:"links"`             // Download links
	MountPath        string                       `json:"mount_path"`        // Mount path
	DeletedFiles     []string                     `json:"deleted_files"`     // Deleted files
	Debrid           string                       `json:"debrid"`            // Debrid name
	Arr              *arr.Arr                     `json:"arr"`               // Arr association
	AddedOn          string                       `json:"added_on"`          // Added on timestamp
	IsComplete       bool                         `json:"is_complete"`       // Is complete
	Bad              bool                         `json:"bad"`               // Is bad
}

// ToManagedTorrent converts a cached torrent to managed format
func (ct *CachedTorrent) ToManagedTorrent() *Entry {
	now := time.Now()
	// Parse timestamps
	var addedOn, createdAt time.Time
	if ct.AddedOn != "" {
		addedOn, _ = time.Parse(time.RFC3339, ct.AddedOn)
	}
	if addedOn.IsZero() && ct.Added != "" {
		addedOn, _ = time.Parse(time.RFC3339, ct.Added)
	}
	if addedOn.IsZero() {
		addedOn = now
	}
	createdAt = addedOn
	// GetReader category from arr
	var category string
	if ct.Arr != nil {
		category = ct.Arr.Name
	}

	mt := &Entry{
		Protocol:         config.ProtocolTorrent,
		InfoHash:         ct.InfoHash,
		Name:             ct.Name,
		OriginalFilename: ct.OriginalFilename,
		Size:             ct.Size,
		Bytes:            ct.Bytes,
		Magnet:           "",
		ActiveProvider:   ct.Debrid,
		Providers:        make(map[string]*ProviderEntry),
		Status:           debridTypes.TorrentStatus(ct.Status),
		Progress:         ct.Progress,
		Speed:            ct.Speed,
		Seeders:          ct.Seeders,
		IsComplete:       ct.IsComplete,
		Bad:              ct.Bad,
		Category:         category,
		Tags:             []string{},
		MountPath:        ct.MountPath,
		AddedOn:          addedOn,
		CreatedAt:        createdAt,
		UpdatedAt:        now,
		Files:            make(map[string]*File),
	}

	for name, f := range ct.Files {
		mt.Files[name] = &File{
			Name:      f.Name,
			Size:      f.Size,
			ByteRange: f.ByteRange,
			InfoHash:  ct.InfoHash, // Track which torrent this file came from
			Deleted:   f.Deleted,
			AddedOn:   addedOn,
		}
	}

	// Set magnet if present
	if ct.Magnet != nil {
		if mag, ok := ct.Magnet.(string); ok {
			mt.Magnet = mag
		}
	}

	// Create providerEntry for this debrid
	if ct.Debrid != "" && ct.ID != "" {
		var downloadedAt *time.Time
		if ct.IsComplete {
			downloadedAt = &addedOn
		}

		providerEntry := &ProviderEntry{
			Provider:     ct.Debrid,
			ID:           ct.ID,
			AddedAt:      addedOn,
			Status:       debridTypes.TorrentStatus(ct.Status),
			Progress:     ct.Progress,
			DownloadedAt: downloadedAt,
			Files:        make(map[string]*ProviderFile),
		}

		// Populate providerEntry files from cached torrent
		for _, f := range ct.Files {
			providerEntry.Files[f.Name] = &ProviderFile{
				Id:   f.Id,
				Link: f.Link,
				Path: f.Path,
			}
		}

		// Use composite key for providerEntry
		mt.Providers[ct.Debrid] = providerEntry
	}

	// Set completion timestamp if complete
	if ct.IsComplete {
		mt.CompletedAt = &addedOn
	}

	return mt
}

// GetTorrentFolder returns the folder name for a torrent by debrid ID
func GetTorrentFolder(folderNaming config.WebDavFolderNaming, entry *Entry) string {
	var folder string
	switch folderNaming {
	case config.WebDavUseFileName:
		folder = path.Clean(entry.Name)
	case config.WebDavUseOriginalName:
		folder = path.Clean(entry.OriginalFilename)
	case config.WebDavUseFileNameNoExt:
		folder = path.Clean(utils.RemoveExtension(entry.Name))
	case config.WebDavUseOriginalNameNoExt:
		folder = path.Clean(utils.RemoveExtension(entry.OriginalFilename))
	case config.WebdavUseHash:
		folder = entry.InfoHash
	default:
		folder = path.Clean(entry.Name)
	}
	return folder
}
