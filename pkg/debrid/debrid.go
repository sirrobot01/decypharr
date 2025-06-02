package debrid

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/alldebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/debrid_link"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/realdebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/torbox"
	"github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"strings"
	"sync"
)

type Storage struct {
	clients     map[string]types.Client
	clientsLock sync.Mutex
	caches      map[string]*store.Cache
	cachesLock  sync.Mutex
	LastUsed    string
}

func NewStorage() *Storage {
	cfg := config.Get()
	clients := make(map[string]types.Client)

	_logger := logger.Default()

	caches := make(map[string]*store.Cache)

	for _, dc := range cfg.Debrids {
		client, err := createDebridClient(dc)
		if err != nil {
			_logger.Error().Err(err).Str("Debrid", dc.Name).Msg("failed to connect to debrid client")
			continue
		}
		_log := client.GetLogger()
		if dc.UseWebDav {
			caches[dc.Name] = store.NewDebridCache(dc, client)
			_log.Info().Msg("Debrid Service started with WebDAV")
		} else {
			_log.Info().Msg("Debrid Service started")
		}
		clients[dc.Name] = client
	}

	d := &Storage{
		clients:  clients,
		LastUsed: "",
		caches:   caches,
	}
	return d
}

func (d *Storage) GetClient(name string) types.Client {
	d.clientsLock.Lock()
	defer d.clientsLock.Unlock()
	client, exists := d.clients[name]
	if !exists {
		return nil
	}
	return client
}

func (d *Storage) Reset() {
	d.clientsLock.Lock()
	d.clients = make(map[string]types.Client)
	d.clientsLock.Unlock()

	d.cachesLock.Lock()
	d.caches = make(map[string]*store.Cache)
	d.cachesLock.Unlock()
	d.LastUsed = ""
}

func (d *Storage) GetClients() map[string]types.Client {
	d.clientsLock.Lock()
	defer d.clientsLock.Unlock()
	clientsCopy := make(map[string]types.Client)
	for name, client := range d.clients {
		clientsCopy[name] = client
	}
	return clientsCopy
}

func (d *Storage) GetCaches() map[string]*store.Cache {
	d.clientsLock.Lock()
	defer d.clientsLock.Unlock()
	cachesCopy := make(map[string]*store.Cache)
	for name, cache := range d.caches {
		cachesCopy[name] = cache
	}
	return cachesCopy
}

func (d *Storage) FilterClients(filter func(types.Client) bool) map[string]types.Client {
	d.clientsLock.Lock()
	defer d.clientsLock.Unlock()
	filteredClients := make(map[string]types.Client)
	for name, client := range d.clients {
		if filter(client) {
			filteredClients[name] = client
		}
	}
	return filteredClients
}

func (d *Storage) FilterCaches(filter func(*store.Cache) bool) map[string]*store.Cache {
	d.cachesLock.Lock()
	defer d.cachesLock.Unlock()
	filteredCaches := make(map[string]*store.Cache)
	for name, cache := range d.caches {
		if filter(cache) {
			filteredCaches[name] = cache
		}
	}
	return filteredCaches
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

func ProcessTorrent(ctx context.Context, store *Storage, selectedDebrid string, magnet *utils.Magnet, a *arr.Arr, isSymlink, overrideDownloadUncached bool) (*types.Torrent, error) {

	debridTorrent := &types.Torrent{
		InfoHash: magnet.InfoHash,
		Magnet:   magnet,
		Name:     magnet.Name,
		Arr:      a,
		Size:     magnet.Size,
		Files:    make(map[string]types.File),
	}

	clients := store.FilterClients(func(c types.Client) bool {
		if selectedDebrid != "" && c.GetName() != selectedDebrid {
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

	for index, db := range clients {
		_logger := db.GetLogger()
		_logger.Info().
			Str("Debrid", db.GetName()).
			Str("Arr", a.Name).
			Str("Hash", debridTorrent.InfoHash).
			Str("Name", debridTorrent.Name).
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
		_logger.Info().Str("id", dbt.Id).Msgf("Torrent: %s submitted to %s", dbt.Name, db.GetName())
		store.LastUsed = index

		torrent, err := db.CheckStatus(dbt, isSymlink)
		if err != nil && torrent != nil && torrent.Id != "" {
			// Delete the torrent if it was not downloaded
			go func(id string) {
				_ = db.DeleteTorrent(id)
			}(torrent.Id)
		}
		return torrent, err
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("failed to process torrent: no clients available")
	}
	if len(errs) == 1 {
		return nil, fmt.Errorf("failed to process torrent: %w", errs[0])
	} else {
		errStrings := make([]string, 0, len(errs))
		for _, err := range errs {
			errStrings = append(errStrings, err.Error())
		}
		return nil, fmt.Errorf("failed to process torrent: %s", strings.Join(errStrings, ", "))
	}
}
