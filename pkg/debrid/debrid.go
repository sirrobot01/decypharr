package debrid

import (
	"context"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/alldebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/debrid_link"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/realdebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/torbox"
	debridStore "github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"sync"
)

type Debrid struct {
	cache  *debridStore.Cache // Could be nil if not using WebDAV
	client types.Client       // HTTP client for making requests to the debrid service
}

func (de *Debrid) Client() types.Client {
	return de.client
}

func (de *Debrid) Cache() *debridStore.Cache {
	return de.cache
}

func (de *Debrid) Reset() {
	if de.cache != nil {
		de.cache.Reset()
	}
}

type Storage struct {
	debrids  map[string]*Debrid
	mu       sync.RWMutex
	lastUsed string
}

func NewStorage(rcManager *rclone.Manager) *Storage {
	cfg := config.Get()

	_logger := logger.Default()

	debrids := make(map[string]*Debrid)

	bindAddress := cfg.BindAddress
	if bindAddress == "" {
		bindAddress = "localhost"
	}
	webdavUrl := fmt.Sprintf("http://%s:%s%s/webdav", bindAddress, cfg.Port, cfg.URLBase)

	for _, dc := range cfg.Debrids {
		client, err := createDebridClient(dc)
		if err != nil {
			_logger.Error().Err(err).Str("Debrid", dc.Name).Msg("failed to connect to debrid client")
			continue
		}
		var (
			cache   *debridStore.Cache
			mounter *rclone.Mount
		)
		_log := client.Logger()
		if dc.UseWebDav {
			if cfg.Rclone.Enabled && rcManager != nil {
				mounter = rclone.NewMount(dc.Name, webdavUrl, rcManager)
			}
			cache = debridStore.NewDebridCache(dc, client, mounter)
			_log.Info().Msg("Debrid Service started with WebDAV")
		} else {
			_log.Info().Msg("Debrid Service started")
		}
		debrids[dc.Name] = &Debrid{
			cache:  cache,
			client: client,
		}
	}

	d := &Storage{
		debrids:  debrids,
		lastUsed: "",
	}
	return d
}

func (d *Storage) Debrid(name string) *Debrid {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if debrid, exists := d.debrids[name]; exists {
		return debrid
	}
	return nil
}

func (d *Storage) Debrids() map[string]*Debrid {
	d.mu.RLock()
	defer d.mu.RUnlock()
	debridsCopy := make(map[string]*Debrid)
	for name, debrid := range d.debrids {
		if debrid != nil {
			debridsCopy[name] = debrid
		}
	}
	return debridsCopy
}

func (d *Storage) Client(name string) types.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if client, exists := d.debrids[name]; exists {
		return client.client
	}
	return nil
}

func (d *Storage) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Reset all debrid clients and caches
	for _, debrid := range d.debrids {
		if debrid != nil {
			debrid.Reset()
		}
	}

	// Reinitialize the debrids map
	d.debrids = make(map[string]*Debrid)
	d.lastUsed = ""
}

func (d *Storage) Clients() map[string]types.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	clientsCopy := make(map[string]types.Client)
	for name, debrid := range d.debrids {
		if debrid != nil && debrid.client != nil {
			clientsCopy[name] = debrid.client
		}
	}
	return clientsCopy
}

func (d *Storage) Caches() map[string]*debridStore.Cache {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cachesCopy := make(map[string]*debridStore.Cache)
	for name, debrid := range d.debrids {
		if debrid != nil && debrid.cache != nil {
			cachesCopy[name] = debrid.cache
		}
	}
	return cachesCopy
}

func (d *Storage) FilterClients(filter func(types.Client) bool) map[string]types.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	filteredClients := make(map[string]types.Client)
	for name, client := range d.debrids {
		if client != nil && filter(client.client) {
			filteredClients[name] = client.client
		}
	}
	return filteredClients
}

func createDebridClient(dc config.Debrid) (types.Client, error) {
	switch dc.Name {
	case "realdebrid":
		return realdebrid.New(dc)
	case "torbox":
		return torbox.New(dc)
	case "debridlink":
		return debrid_link.New(dc)
	case "alldebrid":
		return alldebrid.New(dc)
	default:
		return realdebrid.New(dc)
	}
}

func Process(ctx context.Context, store *Storage, selectedDebrid string, magnet *utils.Magnet, a *arr.Arr, action string, overrideDownloadUncached bool) (*types.Torrent, error) {

	debridTorrent := &types.Torrent{
		InfoHash: magnet.InfoHash,
		Magnet:   magnet,
		Name:     magnet.Name,
		Arr:      a,
		Size:     magnet.Size,
		Files:    make(map[string]types.File),
	}

	clients := store.FilterClients(func(c types.Client) bool {
		if selectedDebrid != "" && c.Name() != selectedDebrid {
			return false
		}
		return true
	})

	if len(clients) == 0 {
		return nil, fmt.Errorf("no debrid clients available")
	}

	errs := make([]error, 0, len(clients))

	// Override first, arr second, debrid third

	if overrideDownloadUncached {
		debridTorrent.DownloadUncached = true
	} else if a.DownloadUncached != nil {
		// Arr cached is set
		debridTorrent.DownloadUncached = *a.DownloadUncached
	} else {
		debridTorrent.DownloadUncached = false
	}

	for _, db := range clients {
		_logger := db.Logger()
		_logger.Info().
			Str("Debrid", db.Name()).
			Str("Arr", a.Name).
			Str("Hash", debridTorrent.InfoHash).
			Str("Name", debridTorrent.Name).
			Str("Action", action).
			Msg("Processing torrent")

		if !overrideDownloadUncached && a.DownloadUncached == nil {
			debridTorrent.DownloadUncached = db.GetDownloadUncached()
		}

		dbt, err := db.SubmitMagnet(debridTorrent)
		if err != nil || dbt == nil || dbt.Id == "" {
			errs = append(errs, err)
			continue
		}
		dbt.Arr = a
		_logger.Info().Str("id", dbt.Id).Msgf("Torrent: %s submitted to %s", dbt.Name, db.Name())
		store.lastUsed = db.Name()

		torrent, err := db.CheckStatus(dbt)
		if err != nil && torrent != nil && torrent.Id != "" {
			// Delete the torrent if it was not downloaded
			go func(id string) {
				_ = db.DeleteTorrent(id)
			}(torrent.Id)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if torrent == nil {
			errs = append(errs, fmt.Errorf("torrent %s returned nil after checking status", dbt.Name))
			continue
		}
		return torrent, nil
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("failed to process torrent: no clients available")
	}
	joinedErrors := errors.Join(errs...)
	return nil, fmt.Errorf("failed to process torrent: %w", joinedErrors)
}
