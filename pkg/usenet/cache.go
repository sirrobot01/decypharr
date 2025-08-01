package usenet

import (
	"github.com/chrisfarms/yenc"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"sync/atomic"
	"time"
)

// SegmentCache provides intelligent caching for NNTP segments
type SegmentCache struct {
	cache       *xsync.Map[string, *CachedSegment]
	logger      zerolog.Logger
	maxSize     int64
	currentSize atomic.Int64
}

// CachedSegment represents a cached segment with metadata
type CachedSegment struct {
	MessageID    string    `json:"message_id"`
	Data         []byte    `json:"data"`
	DecodedSize  int64     `json:"decoded_size"`  // Actual size after yEnc decoding
	DeclaredSize int64     `json:"declared_size"` // Size declared in NZB
	CachedAt     time.Time `json:"cached_at"`
	AccessCount  int64     `json:"access_count"`
	LastAccess   time.Time `json:"last_access"`
	FileBegin    int64     `json:"file_begin"` // Start byte offset in the file
	FileEnd      int64     `json:"file_end"`   // End byte offset in the file
}

// NewSegmentCache creates a new segment cache
func NewSegmentCache(logger zerolog.Logger) *SegmentCache {
	sc := &SegmentCache{
		cache:   xsync.NewMap[string, *CachedSegment](),
		logger:  logger.With().Str("component", "segment_cache").Logger(),
		maxSize: 50 * 1024 * 1024, // Default max size 100MB
	}

	return sc
}

// Get retrieves a segment from cache
func (sc *SegmentCache) Get(messageID string) (*CachedSegment, bool) {
	segment, found := sc.cache.Load(messageID)
	if !found {
		return nil, false
	}

	segment.AccessCount++
	segment.LastAccess = time.Now()

	return segment, true
}

// Put stores a segment in cache with intelligent size management
func (sc *SegmentCache) Put(messageID string, data *yenc.Part, declaredSize int64) {
	dataSize := data.Size

	currentSize := sc.currentSize.Load()
	// Check if we need to make room
	wouldExceed := (currentSize + dataSize) > sc.maxSize

	if wouldExceed {
		sc.evictLRU(dataSize)
	}

	segment := &CachedSegment{
		MessageID:    messageID,
		Data:         make([]byte, data.Size),
		DecodedSize:  dataSize,
		DeclaredSize: declaredSize,
		CachedAt:     time.Now(),
		AccessCount:  1,
		LastAccess:   time.Now(),
	}

	copy(segment.Data, data.Body)

	sc.cache.Store(messageID, segment)

	sc.currentSize.Add(dataSize)
}

// evictLRU evicts least recently used segments to make room
func (sc *SegmentCache) evictLRU(neededSpace int64) {
	if neededSpace <= 0 {
		return // No need to evict if no space is needed
	}
	if sc.cache.Size() == 0 {
		return // Nothing to evict
	}

	// Create a sorted list of segments by last access time
	type segmentInfo struct {
		key        string
		segment    *CachedSegment
		lastAccess time.Time
	}

	segments := make([]segmentInfo, 0, sc.cache.Size())
	sc.cache.Range(func(key string, value *CachedSegment) bool {
		segments = append(segments, segmentInfo{
			key:        key,
			segment:    value,
			lastAccess: value.LastAccess,
		})
		return true // continue iteration
	})

	// Sort by last access time (oldest first)
	for i := 0; i < len(segments)-1; i++ {
		for j := i + 1; j < len(segments); j++ {
			if segments[i].lastAccess.After(segments[j].lastAccess) {
				segments[i], segments[j] = segments[j], segments[i]
			}
		}
	}

	// Evict segments until we have enough space
	freedSpace := int64(0)
	for _, seg := range segments {
		if freedSpace >= neededSpace {
			break
		}

		sc.cache.Delete(seg.key)
		freedSpace += int64(len(seg.segment.Data))
	}
}

// Clear removes all cached segments
func (sc *SegmentCache) Clear() {
	sc.cache.Clear()
	sc.currentSize.Store(0)
}

// Delete removes a specific segment from cache
func (sc *SegmentCache) Delete(messageID string) {
	sc.cache.Delete(messageID)
}
