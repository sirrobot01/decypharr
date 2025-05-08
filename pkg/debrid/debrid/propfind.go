package debrid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sync"
	"time"
)

type PropfindResponse struct {
	Data        []byte
	GzippedData []byte
	Ts          time.Time
}

type PropfindCache struct {
	sync.RWMutex
	data map[string]PropfindResponse
}

func NewPropfindCache() *PropfindCache {
	return &PropfindCache{
		data: make(map[string]PropfindResponse),
	}
}

func generateCacheKey(urlPath string) string {
	cleanPath := path.Clean(urlPath)

	// Create a more collision-resistant key by hashing
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("propfind:%s", cleanPath)))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *PropfindCache) Get(url string) (PropfindResponse, bool) {
	key := generateCacheKey(url)
	c.RLock()
	defer c.RUnlock()
	val, exists := c.data[key]
	return val, exists
}

// Set stores an item in the cache
func (c *PropfindCache) Set(url string, value PropfindResponse) {
	key := generateCacheKey(url)
	c.Lock()
	defer c.Unlock()
	c.data[key] = value
}

func (c *PropfindCache) Remove(urlPath string) {
	key := generateCacheKey(urlPath)
	c.Lock()
	defer c.Unlock()
	delete(c.data, key)
}
