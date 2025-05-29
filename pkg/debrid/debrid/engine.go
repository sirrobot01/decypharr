package debrid

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"sync"
)

type Engine struct {
	Clients   map[string]types.Client
	clientsMu sync.Mutex
	Caches    map[string]*Cache
	cacheMu   sync.Mutex
	LastUsed  string
}

func NewEngine() *Engine {
	cfg := config.Get()
	clients := make(map[string]types.Client)

	_logger := logger.Default()

	caches := make(map[string]*Cache)

	for _, dc := range cfg.Debrids {
		client, err := createDebridClient(dc)
		if err != nil {
			_logger.Error().Err(err).Str("Debrid", dc.Name).Msg("failed to connect to debrid client")
			continue
		}
		_log := client.GetLogger()
		if dc.UseWebDav {
			caches[dc.Name] = New(dc, client)
			_log.Info().Msg("Debrid Service started with WebDAV")
		} else {
			_log.Info().Msg("Debrid Service started")
		}
		clients[dc.Name] = client
	}

	d := &Engine{
		Clients:  clients,
		LastUsed: "",
		Caches:   caches,
	}
	return d
}

func (d *Engine) GetClient(name string) types.Client {
	d.clientsMu.Lock()
	defer d.clientsMu.Unlock()
	return d.Clients[name]
}

func (d *Engine) Reset() {
	d.clientsMu.Lock()
	d.Clients = make(map[string]types.Client)
	d.clientsMu.Unlock()

	d.cacheMu.Lock()
	d.Caches = make(map[string]*Cache)
	d.cacheMu.Unlock()
}

func (d *Engine) GetDebrids() map[string]types.Client {
	return d.Clients
}
