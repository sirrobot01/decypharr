package repair

import (
	"github.com/puzpuzpuz/xsync/v4"
	"golang.org/x/sync/singleflight"
)

type errorCache struct {
	flights singleflight.Group
	results *xsync.Map[string, error]
}

func newErrorCache() *errorCache {
	return &errorCache{results: xsync.NewMap[string, error]()}
}

func (c *errorCache) do(key string, operation func() error) error {
	if c == nil || key == "" {
		return operation()
	}
	if result, ok := c.results.Load(key); ok {
		return result
	}
	_, err, _ := c.flights.Do(key, func() (any, error) {
		if result, ok := c.results.Load(key); ok {
			return nil, result
		}
		result := operation()
		c.results.Store(key, result)
		return nil, result
	})
	return err
}
