package debrid

import (
	"sync"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type Engine struct {
	Clients   map[string]types.Client
	clientsMu sync.Mutex
	Caches    map[string]*Cache
	CacheMu   sync.Mutex
	LastUsed  string

	// Concurrency control for uncached downloads
	uncachedSemaphores map[string]chan struct{} // semaphores per debrid provider
	uncachedMu         sync.RWMutex             // mutex for semaphore map access
}

func NewEngine() *Engine {
	cfg := config.Get()
	clients := make(map[string]types.Client)
	caches := make(map[string]*Cache)
	uncachedSemaphores := make(map[string]chan struct{})

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

		// Initialize semaphore for this debrid provider
		uncachedSemaphores[dc.Name] = make(chan struct{}, dc.MaxConcurrentUncached)
		logger.Info().Msgf("Max concurrent uncached downloads set to %d", dc.MaxConcurrentUncached)
	}

	d := &Engine{
		Clients:            clients,
		LastUsed:           "",
		Caches:             caches,
		uncachedSemaphores: uncachedSemaphores,
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

	d.uncachedMu.Lock()
	d.uncachedSemaphores = make(map[string]chan struct{})
	d.uncachedMu.Unlock()
}

func (d *Engine) GetDebrids() map[string]types.Client {
	return d.Clients
}

// AcquireUncachedSlot acquires a slot for uncached downloads for the specified debrid provider
// Returns true if a slot was acquired, false if all slots are currently in use
func (d *Engine) AcquireUncachedSlot(debridName string) bool {
	d.uncachedMu.RLock()
	semaphore, exists := d.uncachedSemaphores[debridName]
	d.uncachedMu.RUnlock()

	if !exists {
		// If semaphore doesn't exist, allow the operation (backward compatibility)
		return true
	}

	select {
	case semaphore <- struct{}{}:
		// Successfully acquired a slot
		return true
	default:
		// All slots are in use
		return false
	}
}

// ReleaseUncachedSlot releases a slot for uncached downloads for the specified debrid provider
func (d *Engine) ReleaseUncachedSlot(debridName string) {
	d.uncachedMu.RLock()
	semaphore, exists := d.uncachedSemaphores[debridName]
	d.uncachedMu.RUnlock()

	if !exists {
		// If semaphore doesn't exist, nothing to release
		return
	}

	select {
	case <-semaphore:
		// Successfully released a slot
	default:
		// No slots to release (shouldn't happen if properly paired with acquire)
	}
}

// WaitForUncachedSlot blocks until a slot becomes available for uncached downloads
func (d *Engine) WaitForUncachedSlot(debridName string) {
	d.uncachedMu.RLock()
	semaphore, exists := d.uncachedSemaphores[debridName]
	d.uncachedMu.RUnlock()

	if !exists {
		// If semaphore doesn't exist, return immediately (backward compatibility)
		return
	}

	// Block until a slot becomes available
	semaphore <- struct{}{}
}
