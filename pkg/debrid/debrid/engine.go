package debrid

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"sync"
)

type Engine struct {
	Clients   map[string]types.Client
	clientsMu sync.Mutex
	Caches    map[string]*Cache
	CacheMu   sync.Mutex
	LastUsed  string
}

func NewEngine() *Engine {
	cfg := config.Get()
	clients := make(map[string]types.Client)

	caches := make(map[string]*Cache)

	for _, dc := range cfg.Debrids {
		client := createDebridClient(dc)
		logger := client.GetLogger()
		if dc.UseWebDav {
			caches[dc.Name] = New(dc, client)
			logger.Info().Msg("Debrid Service started with WebDAV")
		} else {
			logger.Info().Msg("Debrid Service started")
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

	d.CacheMu.Lock()
	d.Caches = make(map[string]*Cache)
	d.CacheMu.Unlock()
}

func (d *Engine) GetDebrids() map[string]types.Client {
	return d.Clients
}
