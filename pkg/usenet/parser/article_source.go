package parser

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sirrobot01/decypharr/internal/nntp"
	"golang.org/x/sync/singleflight"
)

const (
	defaultArticleBodyCacheBytes  = int64(128 << 20)
	defaultArticleMetadataEntries = 32 << 10
	maxCachedArticlePrefix        = 512
)

// ArticleSource is the only network-facing dependency used by NZB analysis.
// Returned bodies are immutable and may be shared between concurrent callers.
type ArticleSource interface {
	Header(context.Context, string, int) (*nntp.YencMetadata, error)
	Body(context.Context, string) ([]byte, error)
	Stat(context.Context, string) error
	IsAvailable(string) bool
	Metrics() ArticleMetrics
}

// ArticleMetrics describes analyzer traffic independently of NNTP connection
// pool sizing. All counters are cumulative for the lifetime of the source.
type ArticleMetrics struct {
	HeaderRequests  int64
	BodyRequests    int64
	StatRequests    int64
	NetworkBodies   int64
	NetworkStats    int64
	CacheHits       int64
	SharedLoads     int64
	BytesFetched    int64
	CachedBodies    int
	CachedBodyBytes int64
	CachedEntries   int
}

type articleBackend interface {
	Fetch(context.Context, string) ([]byte, *nntp.YencMetadata, error)
	Stat(context.Context, string) error
}

type nntpArticleBackend struct {
	manager *nntp.Client
}

func (b nntpArticleBackend) Fetch(ctx context.Context, messageID string) ([]byte, *nntp.YencMetadata, error) {
	var (
		body     []byte
		metadata *nntp.YencMetadata
	)
	err := b.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
		decoded, observed, fetchErr := conn.GetDecodedBodyWithMetadata(messageID)
		if fetchErr == nil {
			body = decoded
			metadata = observed
		}
		return fetchErr
	})
	return body, metadata, err
}

func (b nntpArticleBackend) Stat(ctx context.Context, messageID string) error {
	_, _, err := b.manager.Stat(ctx, messageID)
	return err
}

type articleCacheEntry struct {
	metadata     nntp.YencMetadata
	hasMetadata  bool
	available    bool
	prefix       []byte
	body         []byte
	bodySize     int
	lruElement   *list.Element
	entryElement *list.Element
}

type articleObservation struct {
	body     []byte
	metadata nntp.YencMetadata
}

// ArticleBroker deduplicates, bounds, and caches every article observation
// made by the parser and archive analyzers.
type ArticleBroker struct {
	backend    articleBackend
	slots      chan struct{}
	bodyLimit  int64
	entryLimit int
	flights    singleflight.Group

	mu        sync.Mutex
	entries   map[string]*articleCacheEntry
	lru       list.List
	entryLRU  list.List
	bodyBytes int64

	headerRequests atomic.Int64
	bodyRequests   atomic.Int64
	statRequests   atomic.Int64
	networkBodies  atomic.Int64
	networkStats   atomic.Int64
	cacheHits      atomic.Int64
	sharedLoads    atomic.Int64
	bytesFetched   atomic.Int64
}

// NewArticleBroker creates a source backed by the configured NNTP client.
func NewArticleBroker(manager *nntp.Client, maxConcurrent int, bodyCacheBytes int64) *ArticleBroker {
	return newArticleBroker(nntpArticleBackend{manager: manager}, maxConcurrent, bodyCacheBytes)
}

func newArticleBroker(backend articleBackend, maxConcurrent int, bodyCacheBytes int64) *ArticleBroker {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if bodyCacheBytes < 0 {
		bodyCacheBytes = 0
	}
	return &ArticleBroker{
		backend:    backend,
		slots:      make(chan struct{}, maxConcurrent),
		bodyLimit:  bodyCacheBytes,
		entryLimit: defaultArticleMetadataEntries,
		entries:    make(map[string]*articleCacheEntry),
	}
}

// Header returns exact yEnc metadata and up to maxSnippet decoded bytes.
func (b *ArticleBroker) Header(ctx context.Context, messageID string, maxSnippet int) (*nntp.YencMetadata, error) {
	b.headerRequests.Add(1)
	if maxSnippet < 0 {
		maxSnippet = 0
	}
	if metadata, ok := b.cachedHeader(messageID, maxSnippet); ok {
		b.cacheHits.Add(1)
		return metadata, nil
	}

	observation, err := b.load(ctx, messageID)
	if err != nil {
		return nil, err
	}
	metadata := observation.metadata
	if maxSnippet > 0 {
		metadata.Snippet = bytes.Clone(observation.body[:min(maxSnippet, len(observation.body))])
	}
	return &metadata, nil
}

// Body returns the complete decoded yEnc article body.
func (b *ArticleBroker) Body(ctx context.Context, messageID string) ([]byte, error) {
	b.bodyRequests.Add(1)
	if body, _, ok := b.cachedBody(messageID); ok {
		b.cacheHits.Add(1)
		return body, nil
	}
	observation, err := b.load(ctx, messageID)
	if err != nil {
		return nil, err
	}
	return observation.body, nil
}

// Stat checks article availability, reusing any successful BODY observation.
func (b *ArticleBroker) Stat(ctx context.Context, messageID string) error {
	b.statRequests.Add(1)
	if b.IsAvailable(messageID) {
		b.cacheHits.Add(1)
		return nil
	}

	result := b.flights.DoChan("stat\x00"+messageID, func() (any, error) {
		if b.IsAvailable(messageID) {
			return struct{}{}, nil
		}
		if err := b.acquire(ctx); err != nil {
			return nil, err
		}
		defer b.release()

		b.networkStats.Add(1)
		if err := b.backend.Stat(ctx, messageID); err != nil {
			return nil, err
		}
		b.markAvailable(messageID)
		return struct{}{}, nil
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case loaded := <-result:
		if loaded.Shared {
			b.sharedLoads.Add(1)
		}
		return loaded.Err
	}
}

// IsAvailable reports whether a successful BODY or STAT has already proved
// that the article exists.
func (b *ArticleBroker) IsAvailable(messageID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[messageID]
	if entry != nil {
		b.touchEntryLocked(entry)
	}
	return entry != nil && entry.available
}

// Metrics returns a consistent snapshot of broker counters and cache size.
func (b *ArticleBroker) Metrics() ArticleMetrics {
	b.mu.Lock()
	cachedBodies := b.lru.Len()
	cachedBytes := b.bodyBytes
	cachedEntries := len(b.entries)
	b.mu.Unlock()

	return ArticleMetrics{
		HeaderRequests:  b.headerRequests.Load(),
		BodyRequests:    b.bodyRequests.Load(),
		StatRequests:    b.statRequests.Load(),
		NetworkBodies:   b.networkBodies.Load(),
		NetworkStats:    b.networkStats.Load(),
		CacheHits:       b.cacheHits.Load(),
		SharedLoads:     b.sharedLoads.Load(),
		BytesFetched:    b.bytesFetched.Load(),
		CachedBodies:    cachedBodies,
		CachedBodyBytes: cachedBytes,
		CachedEntries:   cachedEntries,
	}
}

func (b *ArticleBroker) load(ctx context.Context, messageID string) (articleObservation, error) {
	if messageID == "" {
		return articleObservation{}, fmt.Errorf("article message ID is empty")
	}
	if body, metadata, ok := b.cachedBody(messageID); ok {
		b.cacheHits.Add(1)
		return articleObservation{body: body, metadata: metadata}, nil
	}

	result := b.flights.DoChan("body\x00"+messageID, func() (any, error) {
		if body, metadata, ok := b.cachedBody(messageID); ok {
			return articleObservation{body: body, metadata: metadata}, nil
		}
		if err := b.acquire(ctx); err != nil {
			return nil, err
		}
		defer b.release()

		b.networkBodies.Add(1)
		body, metadata, err := b.backend.Fetch(ctx, messageID)
		if err != nil {
			return nil, err
		}
		if metadata == nil {
			return nil, fmt.Errorf("article %s returned no yEnc metadata", messageID)
		}
		b.bytesFetched.Add(int64(len(body)))
		b.store(messageID, body, metadata)
		return articleObservation{body: body, metadata: *metadata}, nil
	})

	select {
	case <-ctx.Done():
		return articleObservation{}, ctx.Err()
	case loaded := <-result:
		if loaded.Shared {
			b.sharedLoads.Add(1)
		}
		if loaded.Err != nil {
			return articleObservation{}, loaded.Err
		}
		observation, ok := loaded.Val.(articleObservation)
		if !ok {
			return articleObservation{}, fmt.Errorf("unexpected article observation type %T", loaded.Val)
		}
		return observation, nil
	}
}

func (b *ArticleBroker) cachedHeader(messageID string, maxSnippet int) (*nntp.YencMetadata, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entries[messageID]
	if entry == nil || !entry.hasMetadata {
		return nil, false
	}
	b.touchEntryLocked(entry)
	want := min(maxSnippet, entry.bodySize)
	if want > len(entry.prefix) {
		return nil, false
	}
	metadata := entry.metadata
	if want > 0 {
		metadata.Snippet = bytes.Clone(entry.prefix[:want])
	}
	return &metadata, true
}

func (b *ArticleBroker) cachedBody(messageID string) ([]byte, nntp.YencMetadata, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entries[messageID]
	if entry == nil || entry.body == nil || !entry.hasMetadata {
		return nil, nntp.YencMetadata{}, false
	}
	b.touchEntryLocked(entry)
	if entry.lruElement != nil {
		b.lru.MoveToFront(entry.lruElement)
	}
	return entry.body, entry.metadata, true
}

func (b *ArticleBroker) store(messageID string, body []byte, metadata *nntp.YencMetadata) {
	// Own the key before it is retained. Message ids decoded from a v2 .meta
	// blob alias the file's whole decompressed message-id region, so a single
	// retained id pins that NZB's entire segment map - a repair sweep filled
	// this cache with ids from hundreds of NZBs and held ~1.4GB, because the
	// entry limit bounds the key count, not the bytes each key keeps alive.
	messageID = strings.Clone(messageID)

	b.mu.Lock()
	defer b.mu.Unlock()

	entry := b.entries[messageID]
	if entry == nil {
		entry = b.newEntryLocked(messageID)
	} else {
		b.touchEntryLocked(entry)
	}
	if entry.body != nil {
		b.bodyBytes -= int64(len(entry.body))
		if entry.lruElement != nil {
			b.lru.Remove(entry.lruElement)
		}
	}

	entry.metadata = *metadata
	entry.metadata.Snippet = nil
	entry.hasMetadata = true
	entry.available = true
	entry.bodySize = len(body)
	entry.prefix = bytes.Clone(body[:min(len(body), maxCachedArticlePrefix)])
	entry.body = nil
	entry.lruElement = nil

	if b.bodyLimit > 0 && int64(len(body)) <= b.bodyLimit {
		entry.body = body
		entry.lruElement = b.lru.PushFront(messageID)
		b.bodyBytes += int64(len(body))
	}
	b.evictLocked()
	b.evictEntriesLocked()
}

func (b *ArticleBroker) markAvailable(messageID string) {
	messageID = strings.Clone(messageID) // owned before retention; see store
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[messageID]
	if entry == nil {
		entry = b.newEntryLocked(messageID)
	} else {
		b.touchEntryLocked(entry)
	}
	entry.available = true
	b.evictEntriesLocked()
}

func (b *ArticleBroker) newEntryLocked(messageID string) *articleCacheEntry {
	entry := &articleCacheEntry{}
	entry.entryElement = b.entryLRU.PushFront(messageID)
	b.entries[messageID] = entry
	return entry
}

func (b *ArticleBroker) touchEntryLocked(entry *articleCacheEntry) {
	if entry.entryElement != nil {
		b.entryLRU.MoveToFront(entry.entryElement)
	}
}

func (b *ArticleBroker) evictLocked() {
	for b.bodyBytes > b.bodyLimit {
		oldest := b.lru.Back()
		if oldest == nil {
			return
		}
		messageID := oldest.Value.(string)
		b.lru.Remove(oldest)
		entry := b.entries[messageID]
		if entry == nil || entry.body == nil {
			continue
		}
		b.bodyBytes -= int64(len(entry.body))
		entry.body = nil
		entry.lruElement = nil
	}
}

func (b *ArticleBroker) evictEntriesLocked() {
	for b.entryLimit >= 0 && len(b.entries) > b.entryLimit {
		oldest := b.entryLRU.Back()
		if oldest == nil {
			return
		}
		messageID := oldest.Value.(string)
		b.entryLRU.Remove(oldest)
		entry := b.entries[messageID]
		if entry == nil {
			continue
		}
		if entry.body != nil {
			b.bodyBytes -= int64(len(entry.body))
			if entry.lruElement != nil {
				b.lru.Remove(entry.lruElement)
			}
		}
		delete(b.entries, messageID)
	}
}

func (b *ArticleBroker) acquire(ctx context.Context) error {
	select {
	case b.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *ArticleBroker) release() {
	<-b.slots
}
