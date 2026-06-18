package manager

import (
	"cmp"
	"errors"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/alldebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/debridlink"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/premiumize"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/realdebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/torbox"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"go.uber.org/ratelimit"
)

var (
	ErrUnsupportedDebridProvider = errors.New("unsupported debrid provider")
)

func (m *Manager) ProviderClient(name string) debrid.Client {
	client, ok := m.clients.Load(name)
	if !ok {
		return nil
	}
	return client
}

func (m *Manager) initDebridClients() {
	cfg := config.Get()
	for _, dc := range cfg.Debrids {
		client, err := m.createClient(dc)
		if err != nil {
			m.logger.Error().Err(err).Str("debrid", dc.Name).Msg("Failed to create debrid client")
			continue
		}
		m.clients.Store(dc.Name, client)
	}
}

// createClient creates a debrid client based on configuration
func (m *Manager) createClient(dc config.Debrid) (debrid.Client, error) {
	var client debrid.Client
	var err error

	rateLimits := map[string]ratelimit.Limiter{}

	mainRL := utils.ParseRateLimit(dc.RateLimit)
	repairRL := utils.ParseRateLimit(cmp.Or(dc.RepairRateLimit, dc.RateLimit))
	downloadRL := utils.ParseRateLimit(cmp.Or(dc.DownloadRateLimit, dc.RateLimit))

	rateLimits["main"] = mainRL
	rateLimits["repair"] = repairRL
	rateLimits["download"] = downloadRL

	switch dc.Provider {
	case "realdebrid":
		client, err = realdebrid.New(dc, rateLimits)
	case "alldebrid":
		client, err = alldebrid.New(dc, rateLimits)
	case "torbox":
		client, err = torbox.New(dc, rateLimits)
	case "debridlink":
		client, err = debridlink.New(dc, rateLimits)
	case "premiumize":
		client, err = premiumize.New(dc, rateLimits)
	default:
		return nil, ErrUnsupportedDebridProvider
	}

	if err != nil {
		return nil, err
	}

	return client, nil
}

// FilterDebrid returns clients that match the filter function
func (m *Manager) FilterDebrid(filter func(debrid.Client) bool) []debrid.Client {
	var filtered []debrid.Client

	m.clients.Range(func(key string, client debrid.Client) bool {
		if client != nil && filter(client) {
			filtered = append(filtered, client)
		}
		return true
	})
	return filtered
}

func (m *Manager) GetIngests() ([]types.IngestData, error) {
	// Use streaming to avoid loading all torrents into memory
	var ingests []types.IngestData
	err := m.storage.ForEach(func(torrent *storage.Entry) error {
		ingests = append(ingests, types.IngestData{
			Debrid: torrent.ActiveProvider,
			Name:   torrent.OriginalFilename,
			Hash:   torrent.InfoHash,
			Size:   torrent.Bytes,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ingests, nil
}

func (m *Manager) GetIngestsByDebrid(debridName string) ([]types.IngestData, error) {
	// Use streaming to avoid loading all torrents into memory
	var ingests []types.IngestData
	err := m.storage.ForEach(func(torrent *storage.Entry) error {
		if !torrent.HasProvider(debridName) {
			return nil
		}
		ingests = append(ingests, types.IngestData{
			Debrid: torrent.ActiveProvider,
			Name:   torrent.OriginalFilename,
			Hash:   torrent.InfoHash,
			Size:   torrent.Bytes,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ingests, nil
}
