package reader

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/crypto"
	"github.com/sirrobot01/decypharr/internal/nntp"
)

// decryptionBufPool stores *[]byte so Put does not box the slice header.
var decryptionBufPool = sync.Pool{}

func acquireDecryptionBuffer(size int) *[]byte {
	v := decryptionBufPool.Get()
	if v == nil {
		buf := make([]byte, size)
		return &buf
	}
	bufPtr := v.(*[]byte)
	if cap(*bufPtr) < size {
		buf := make([]byte, size)
		return &buf
	}
	*bufPtr = (*bufPtr)[:size]
	return bufPtr
}

func releaseDecryptionBuffer(buf *[]byte) {
	decryptionBufPool.Put(buf)
}

// StreamingReader provides io.ReaderAt over NNTP segments with automatic
// caching, prefetching, and error recovery.
//
// Key features:
//   - Pin/Unpin pattern prevents the "chunk does not exist" race condition
//   - Disk caching with transparent re-download
//   - Request deduplication for concurrent reads of the same segment
//   - Background prefetching for smooth sequential reads
//   - AES-CBC decryption support for encrypted archives
//
// Usage:
//
//	reader, err := NewStreamingReader(ctx, client, segments, opts...)
//	defer reader.Close()
//	n, err := reader.ReadAt(buf, offset)
type StreamingReader struct {
	// Dependencies
	cache   *SegmentCache
	fetcher *SegmentFetcher
	config  Config

	// File metadata
	totalSize int64
	segCount  int

	// Encryption support
	encryption EncryptionConfig

	// Read position for io.Reader interface
	readOffset atomic.Int64

	// Cursor registry: each concurrent consumer owns a Cursor so a tail
	// probe and sequential playback on the same file don't fight over the
	// prefetch queue or the eviction window. defaultCursor backs the
	// StreamingReader.ReadAt entry points, registered lazily on first use.
	cursorMu      sync.Mutex
	cursors       map[*Cursor]struct{}
	defaultCursor *Cursor

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
	logger zerolog.Logger

	// Stats
	stats *ReaderStats
}

// ReadCursor is one consumer's view of a shared StreamingReader, with its
// own seek detection and consume tracking. The cache evicts behind the
// slowest cursor.
type ReadCursor interface {
	ReadAtContext(ctx context.Context, p []byte, off int64) (int, error)
	Prefetch(ctx context.Context, off, length int64)
	Close()
}

// Cursor implements ReadCursor over a StreamingReader.
type Cursor struct {
	sr *StreamingReader

	// lastEndSeg is the final segment of this cursor's most recent read
	// (-1 until the first); only a jump relative to this cursor cancels
	// prefetch.
	lastEndSeg atomic.Int64

	// consumed is the end offset of the last bytes delivered through this
	// cursor (-1 until the first). Not monotonic: a seek-back pulls the
	// mark (and the eviction floor) back, protecting the region being
	// re-read.
	consumed atomic.Int64

	// queuedThrough is the highest segment this cursor has hinted for
	// prefetch (-1 = none), so the per-read hint pass only covers the
	// window delta. Reset on seek.
	queuedThrough atomic.Int64

	closed atomic.Bool
}

// OpenCursor registers and returns a new cursor. Callers must Close it.
func (sr *StreamingReader) OpenCursor() ReadCursor {
	return sr.newCursor()
}

func (sr *StreamingReader) newCursor() *Cursor {
	sr.cursorMu.Lock()
	defer sr.cursorMu.Unlock()
	return sr.registerCursorLocked()
}

// getDefaultCursor lazily creates the cursor behind the ReadAt entry points.
// It is not registered until first use, so a reader driven purely through
// explicit cursors has no phantom consumer pinning the eviction floor.
func (sr *StreamingReader) getDefaultCursor() *Cursor {
	sr.cursorMu.Lock()
	defer sr.cursorMu.Unlock()
	if sr.defaultCursor == nil {
		sr.defaultCursor = sr.registerCursorLocked()
	}
	return sr.defaultCursor
}

func (sr *StreamingReader) registerCursorLocked() *Cursor {
	c := &Cursor{sr: sr}
	c.lastEndSeg.Store(-1)
	c.consumed.Store(-1)
	c.queuedThrough.Store(-1)
	sr.cursors[c] = struct{}{}
	return c
}

func (c *Cursor) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	return c.sr.readAtCursor(ctx, c, p, off)
}

func (c *Cursor) Prefetch(ctx context.Context, off, length int64) {
	c.sr.Prefetch(ctx, off, length)
}

// Close unregisters the cursor so its consume mark stops holding the
// eviction floor back.
func (c *Cursor) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	sr := c.sr
	sr.cursorMu.Lock()
	delete(sr.cursors, c)
	sr.cursorMu.Unlock()
	sr.publishConsumedFloor()
}

// markConsumed records delivery through c and republishes the floor.
func (c *Cursor) markConsumed(end int64) {
	c.consumed.Store(end)
	c.sr.publishConsumedFloor()
}

// publishConsumedFloor hands the cache the minimum consume mark across live
// cursors (the slowest active consumer); cursors that have not delivered
// anything yet are ignored.
func (sr *StreamingReader) publishConsumedFloor() {
	sr.cursorMu.Lock()
	floor := int64(-1)
	for c := range sr.cursors {
		v := c.consumed.Load()
		if v < 0 {
			continue
		}
		if floor < 0 || v < floor {
			floor = v
		}
	}
	sr.cursorMu.Unlock()
	if floor >= 0 {
		sr.cache.SetConsumedFloor(floor)
	}
}

// NewStreamingReader creates a new streaming reader for NNTP segments.
func NewStreamingReader(
	ctx context.Context,
	client *nntp.Client,
	segments []SegmentMeta,
	opts ...Option,
) (*StreamingReader, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("no segments provided")
	}
	if client == nil {
		return nil, fmt.Errorf("NNTP client is required")
	}

	// Apply configuration options
	config := DefaultConfig()
	for _, opt := range opts {
		opt(&config)
	}

	ctx, cancel := context.WithCancel(ctx)
	logger := zerolog.Nop() // Use logger from config if available

	stats := &ReaderStats{}

	// Create cache
	cache, err := NewSegmentCache(ctx, segments, config, stats, logger)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create cache: %w", err)
	}

	// Create fetcher
	fetcher := NewSegmentFetcher(ctx, client, cache, config, stats, logger)

	sr := &StreamingReader{
		cache:     cache,
		fetcher:   fetcher,
		config:    config,
		totalSize: cache.TotalSize(),
		segCount:  cache.SegmentCount(),
		cursors:   make(map[*Cursor]struct{}),
		ctx:       ctx,
		cancel:    cancel,
		logger:    logger,
		stats:     stats,
	}

	return sr, nil
}

// seekAbandonedWindow reports whether a read landing on [startSeg, endSeg]
// after a previous read that ended at prevEnd constitutes a seek that
// abandons the queued read-ahead window. Reads within one prefetch window of
// the previous position (in either direction) are sequential-ish — kernel
// readahead reorders and overlaps small reads constantly — and keep their
// queued hints. Anything further is a jump whose pending hints are dead
// weight.
func seekAbandonedWindow(prevEnd int64, startSeg, endSeg, ahead int) bool {
	if prevEnd < 0 || ahead <= 0 {
		return false
	}
	return int64(startSeg) > prevEnd+int64(ahead) || int64(endSeg) < prevEnd-int64(ahead)
}

// NewStreamingReaderWithEncryption creates an encrypted reader.
func NewStreamingReaderWithEncryption(
	ctx context.Context,
	client *nntp.Client,
	segments []SegmentMeta,
	encConfig EncryptionConfig,
	opts ...Option,
) (*StreamingReader, error) {
	sr, err := NewStreamingReader(ctx, client, segments, opts...)
	if err != nil {
		return nil, err
	}
	sr.encryption = encConfig
	return sr, nil
}

// ReadAt implements io.ReaderAt with blocking semantics.
// Blocks until the requested byte range is available.
//
// THE CRITICAL PATH: Uses Pin/Unpin to prevent the race condition.
func (sr *StreamingReader) ReadAt(p []byte, off int64) (int, error) {
	return sr.ReadAtContext(context.Background(), p, off)
}

// ReadAtContext implements context-aware random reads. The context is carried
// into segment waits and NNTP fetches so DFS/FUSE read timeouts can cancel the
// underlying work instead of waiting for the reader lifetime to end.
//
// This entry point shares one default cursor; independent consumers should
// use OpenCursor.
func (sr *StreamingReader) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	return sr.readAtCursor(ctx, sr.getDefaultCursor(), p, off)
}

func (sr *StreamingReader) readAtCursor(ctx context.Context, cur *Cursor, p []byte, off int64) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sr.closed.Load() {
		return 0, io.ErrClosedPipe
	}

	if len(p) == 0 {
		return 0, nil
	}

	if off >= sr.totalSize {
		return 0, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	sr.stats.Reads.Add(1)

	if sr.encryption.Enabled {
		return sr.readAtEncrypted(ctx, cur, p, off)
	}
	return sr.readAtPlain(ctx, cur, p, off)
}

// readAtPlain handles non-encrypted reads.
func (sr *StreamingReader) readAtPlain(ctx context.Context, cur *Cursor, p []byte, off int64) (int, error) {
	// Clamp to file bounds
	readLen := int64(len(p))
	eofAfter := false
	if off+readLen > sr.totalSize {
		readLen = sr.totalSize - off
		eofAfter = true
	}

	// Determine which segments we need
	startSeg, endSeg := sr.cache.SegmentsForRange(off, readLen)

	// CRITICAL: Pin segments to prevent eviction during read
	sr.cache.PinRange(startSeg, endSeg)
	defer sr.cache.UnpinRange(startSeg, endSeg)

	// On a seek relative to this cursor, drop the read-ahead hints queued
	// for the old position BEFORE queueing the new window — the prefetch
	// workers would otherwise keep downloading data the consumer jumped away
	// from while this read waits for connection slots.
	prevEnd := cur.lastEndSeg.Swap(int64(endSeg))
	if seekAbandonedWindow(prevEnd, startSeg, endSeg, sr.config.PrefetchAhead) {
		sr.fetcher.CancelPendingPrefetch()
		cur.queuedThrough.Store(-1)
	}

	// Queue read-ahead hints for the part of the window this cursor hasn't
	// already hinted (non-blocking).
	prefetchEnd := min(endSeg+sr.config.PrefetchAhead, sr.segCount-1)
	if prefetchEnd > endSeg {
		qStart := max(endSeg+1, int(cur.queuedThrough.Load())+1)
		if qStart <= prefetchEnd {
			sr.fetcher.QueuePrefetchRange(qStart, prefetchEnd)
			cur.queuedThrough.Store(int64(prefetchEnd))
		}
	}

	// Ensure all required segments are available (may block for downloads)
	if err := sr.fetcher.EnsureSegments(ctx, startSeg, endSeg); err != nil {
		sr.stats.ReadErrors.Add(1)
		return 0, err
	}

	// Read data from cache
	n, err := sr.readFromCache(ctx, p[:readLen], off, startSeg, endSeg)
	if err != nil {
		sr.stats.ReadErrors.Add(1)
		return n, err
	}

	sr.stats.BytesRead.Add(int64(n))

	// Record delivery so the sliding-window sweeper can advance its cutoff.
	// Skip zero-byte reads (probe, short EOF) to avoid moving the mark
	// spuriously.
	if n > 0 {
		cur.markConsumed(off + int64(n))
	}

	if eofAfter && int64(n) == readLen {
		return n, io.EOF
	}
	return n, err
}

// readFromCache reads data from the cache, handling segment boundaries.
//
// Uses ReadRangeInto so each pread fetches only the bytes the caller actually
// needs from that segment — no scratch buffer, no read amplification.
// Previously the code read entire segments (~750 KB) even for 4 KB reads,
// which filled the kernel page cache with mostly-unused data and caused
// progressive performance degradation on large files.
func (sr *StreamingReader) readFromCache(ctx context.Context, p []byte, off int64, startSeg, endSeg int) (int, error) {
	totalRead := 0
	// filled is the absolute offset this read has delivered through. Segments
	// must cover disjoint, ascending ranges; see the overlap check below.
	filled := off

	for segIdx := startSeg; segIdx <= endSeg; segIdx++ {
		// Wait for segment to be ready
		if err := sr.cache.WaitForSegment(ctx, segIdx); err != nil {
			return totalRead, err
		}

		// Calculate the intersection of the caller's range with this segment.
		segStart := sr.cache.SegmentOffset(segIdx)
		segEnd := sr.cache.SegmentOffset(segIdx + 1)

		readStart := max(off, segStart)
		readEnd := min(off+int64(len(p)), segEnd)

		if readStart >= readEnd {
			continue
		}
		if readStart < filled {
			// Two segments claim the same bytes. Their copies would land on
			// top of each other in p while totalRead counted both, so the
			// read would report more bytes than p can hold and panic the
			// caller's copy loop. computeOffsets rejects such tables up
			// front; anything reaching here is a bug, not bad metadata.
			return totalRead, fmt.Errorf("segment %d starts at %d, behind delivered offset %d", segIdx, readStart, filled)
		}

		outOffset := readStart - off
		segDataOffset := readStart - segStart
		copyLen := readEnd - readStart

		// Read only the needed slice directly into the output buffer.
		// No intermediate scratch buffer — zero extra allocation, zero amplification.
		n, ok := sr.cache.ReadRangeInto(segIdx, segDataOffset, copyLen, p[outOffset:outOffset+copyLen])
		if !ok {
			// The slot says OnDisk but the bytes aren't readable. Force the
			// state back to Empty before re-fetching: otherwise Fetch's
			// OnDisk fast path would short-circuit and we'd loop straight to
			// the "still missing" error, leaving the segment wedged. This is
			// the self-heal for any segment that ends up OnDisk-but-empty.
			sr.logger.Warn().Int("segment", segIdx).Msg("segment data missing after wait, re-fetching")
			sr.cache.invalidateForRefetch(segIdx)
			if err := sr.fetcher.Fetch(ctx, segIdx); err != nil {
				return totalRead, fmt.Errorf("re-fetch segment %d: %w", segIdx, err)
			}
			n, ok = sr.cache.ReadRangeInto(segIdx, segDataOffset, copyLen, p[outOffset:outOffset+copyLen])
			if !ok {
				return totalRead, fmt.Errorf("segment %d still missing after re-fetch", segIdx)
			}
		}

		totalRead += n
		filled = readEnd
	}

	return totalRead, nil
}

// readAtEncrypted handles AES-CBC encrypted reads.
func (sr *StreamingReader) readAtEncrypted(ctx context.Context, cur *Cursor, p []byte, off int64) (int, error) {
	// AES-CBC requires block-aligned reads
	alignedStart := (off / crypto.BlockSize) * crypto.BlockSize

	// Determine actual end
	reqEnd := min(off+int64(len(p)), sr.totalSize)

	// Round up to next block
	alignedEnd := reqEnd
	if remainder := alignedEnd % crypto.BlockSize; remainder != 0 {
		alignedEnd += crypto.BlockSize - remainder
	}

	bufLen := alignedEnd - alignedStart
	bufPtr := acquireDecryptionBuffer(int(bufLen))
	defer releaseDecryptionBuffer(bufPtr)
	buf := *bufPtr

	// Read aligned data
	n, err := sr.readAtPlain(ctx, cur, buf, alignedStart)
	if n > 0 && len(sr.encryption.Key) > 0 {
		// Handle partial last block
		decryptedLen := int64(n)
		if remainder := decryptedLen % crypto.BlockSize; remainder != 0 {
			// Pad with zeros for decryption
			if int64(len(buf)) >= decryptedLen+(crypto.BlockSize-remainder) {
				for i := int64(0); i < crypto.BlockSize-remainder; i++ {
					buf[decryptedLen+i] = 0
				}
				decryptedLen += crypto.BlockSize - remainder
			}
		}

		// Decrypt
		if decryptErr := sr.decryptInPlace(ctx, buf[:decryptedLen], alignedStart); decryptErr != nil {
			return 0, fmt.Errorf("decrypt: %w", decryptErr)
		}
	}

	if err != nil && err != io.EOF {
		return 0, err
	}

	// Copy requested data to p
	validDataEnd := min(int64(n), reqEnd-alignedStart)

	startOffset := off - alignedStart
	if startOffset >= validDataEnd {
		return 0, err
	}

	copied := copy(p, buf[startOffset:validDataEnd])

	if err == io.EOF && copied == len(p) {
		return copied, nil
	}

	return copied, err
}

// decryptInPlace decrypts data using AES-256-CBC.
func (sr *StreamingReader) decryptInPlace(ctx context.Context, data []byte, offset int64) error {
	if len(data) == 0 {
		return nil
	}

	if len(data)%crypto.BlockSize != 0 {
		return nil // Not block-aligned, can't decrypt
	}

	iv, err := sr.computeIVForOffset(ctx, offset)
	if err != nil {
		return err
	}

	return crypto.DecryptBlock(data, sr.encryption.Key, iv)
}

// computeIVForOffset computes the IV for decrypting at a given offset.
func (sr *StreamingReader) computeIVForOffset(ctx context.Context, offset int64) ([]byte, error) {
	blockOffset := (offset / crypto.BlockSize) * crypto.BlockSize

	if blockOffset == 0 {
		// First block uses original IV
		iv := make([]byte, crypto.BlockSize)
		copy(iv, sr.encryption.IV)
		return iv, nil
	}

	// For other blocks, IV is the previous ciphertext block
	prevBlockOffset := blockOffset - crypto.BlockSize
	iv := make([]byte, crypto.BlockSize)

	// Read previous block (raw, not decrypted) through the default cursor —
	// an IV fill is a peek, not a consumer position.
	n, err := sr.readAtPlain(ctx, sr.getDefaultCursor(), iv, prevBlockOffset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read IV block at %d: %w", prevBlockOffset, err)
	}
	if n < crypto.BlockSize {
		return nil, fmt.Errorf("short read for IV: got %d, need %d", n, crypto.BlockSize)
	}

	return iv, nil
}

// Prefetch triggers segment downloads for the given byte range without blocking.
func (sr *StreamingReader) Prefetch(ctx context.Context, off, length int64) {
	if sr.closed.Load() {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	if off >= sr.totalSize {
		return
	}

	// Clamp to file bounds
	if off+length > sr.totalSize {
		length = sr.totalSize - off
	}

	startSeg, endSeg := sr.cache.SegmentsForRange(off, length)
	sr.fetcher.QueuePrefetchRange(startSeg, endSeg)
}

// Read implements io.Reader using ReadAt with tracked position.
func (sr *StreamingReader) Read(p []byte) (int, error) {
	if sr.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}

	offset := sr.readOffset.Load()
	if offset >= sr.totalSize {
		return 0, io.EOF
	}

	n, err := sr.ReadAt(p, offset)
	if n > 0 {
		sr.readOffset.Add(int64(n))
	}
	return n, err
}

// Seek implements io.Seeker.
func (sr *StreamingReader) Seek(offset int64, whence int) (int64, error) {
	if sr.closed.Load() {
		return 0, io.ErrClosedPipe
	}

	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = sr.readOffset.Load() + offset
	case io.SeekEnd:
		newPos = sr.totalSize + offset
	default:
		return 0, fmt.Errorf("invalid seek whence %d", whence)
	}

	if newPos < 0 {
		return 0, fmt.Errorf("seek before beginning")
	}
	if newPos > sr.totalSize {
		newPos = sr.totalSize
	}

	sr.readOffset.Store(newPos)
	return newPos, nil
}

// Size returns the total size of the virtual file.
func (sr *StreamingReader) Size() int64 {
	return sr.totalSize
}

// Stats returns a snapshot of current statistics.
func (sr *StreamingReader) Stats() map[string]int64 {
	return sr.stats.Snapshot()
}

// Close releases all resources.
func (sr *StreamingReader) Close() error {
	if sr.closed.Swap(true) {
		return nil
	}

	sr.cancel()

	// Close fetcher first (stops downloads)
	sr.fetcher.Close()

	// Then close cache (cleans up files)
	return sr.cache.Close()
}

// Pool manages a pool of readers for efficient resource sharing.
// This is useful when multiple files need to be read concurrently.
type Pool struct {
	client *nntp.Client
	config Config

	readers sync.Map // map[string]*StreamingReader
}

// GetReader returns a reader for the given segments, creating one if needed.
func (rp *Pool) GetReader(
	ctx context.Context,
	key string,
	segments []SegmentMeta,
	encryption EncryptionConfig,
) (*StreamingReader, error) {
	// Check if reader exists
	if v, ok := rp.readers.Load(key); ok {
		return v.(*StreamingReader), nil
	}

	// Create new reader
	var reader *StreamingReader
	var err error
	if encryption.Enabled {
		reader, err = NewStreamingReaderWithEncryption(
			ctx, rp.client, segments, encryption,
			WithMaxDisk(rp.config.MaxDisk),
			WithMaxConnections(rp.config.MaxConnections),
			WithPrefetchAhead(rp.config.PrefetchAhead),
		)
	} else {
		reader, err = NewStreamingReader(
			ctx, rp.client, segments,
			WithMaxDisk(rp.config.MaxDisk),
			WithMaxConnections(rp.config.MaxConnections),
			WithPrefetchAhead(rp.config.PrefetchAhead),
		)
	}
	if err != nil {
		return nil, err
	}

	// Store (race-safe: LoadOrStore)
	actual, loaded := rp.readers.LoadOrStore(key, reader)
	if loaded {
		// Another goroutine beat us, close ours
		_ = reader.Close()
		return actual.(*StreamingReader), nil
	}

	return reader, nil
}

// RemoveReader closes and removes a reader from the pool.
func (rp *Pool) RemoveReader(key string) {
	if v, ok := rp.readers.LoadAndDelete(key); ok {
		_ = v.(*StreamingReader).Close()
	}
}

// Close closes all readers in the pool.
func (rp *Pool) Close() {
	rp.readers.Range(func(key, value any) bool {
		_ = value.(*StreamingReader).Close()
		rp.readers.Delete(key)
		return true
	})
}
