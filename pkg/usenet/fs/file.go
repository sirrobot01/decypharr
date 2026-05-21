package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/usenet/fs/reader"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

type File struct {
	ctx             context.Context
	volume          *types.Volume
	info            volumeInfo
	reader          io.ReadCloser                          // Sequential reader (for Read() method)
	streamingReader atomic.Pointer[reader.StreamingReader] // Streaming reader for ReadAt()
	readerOnce      sync.Once                              // Ensures streaming reader created exactly once
	readerErr       error                                  // Error from streaming reader creation
	manager         *nntp.Client                           // Connection manager
	maxConcurrent   int                                    // Max concurrent connections for this file's reader
	prefetchSize    int64                                  // Prefetch size in bytes
	pos             atomic.Int64
	logger          zerolog.Logger
	closed          atomic.Bool
}

func (vf *File) Read(p []byte) (int, error) {
	if vf.closed.Load() {
		return 0, fs.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	curPos := vf.pos.Load()
	if curPos >= vf.volume.Size {
		return 0, io.EOF
	}
	remaining := vf.volume.Size - curPos
	if remaining <= 0 {
		return 0, io.EOF
	}
	err := vf.ensureReader() // Pass the current position to ensureReader
	if err != nil {
		return 0, err
	}
	readLen := len(p)
	if int64(readLen) > remaining {
		readLen = int(remaining)
	}
	n, readErr := vf.reader.Read(p[:readLen])
	if n > 0 {
		vf.pos.Add(int64(n))
		remaining -= int64(n)
		if remaining == 0 {
			_ = vf.reader.Close()
			vf.reader = nil
		}
	}
	if readErr != nil {
		if errors.Is(readErr, io.EOF) {
			if vf.reader != nil {
				_ = vf.reader.Close()
				vf.reader = nil
			}
			if n == 0 {
				return 0, io.EOF
			}
			return n, io.EOF
		}
		return n, readErr
	}
	if n == 0 {
		return 0, io.EOF
	}

	// Check if we've reached the end of the Volume
	if vf.pos.Load() >= vf.volume.Size {
		if vf.reader != nil {
			_ = vf.reader.Close()
			vf.reader = nil
		}
	}
	// Check if we've read all requested bytes
	if int64(n) < int64(readLen) {
		return n, io.EOF
	}

	return n, nil
}

func (vf *File) ReadAt(p []byte, off int64) (int, error) {
	if vf.closed.Load() {
		return 0, fs.ErrClosed
	}
	if off < 0 {
		return 0, fmt.Errorf("rar: negative read offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= vf.volume.Size {
		return 0, io.EOF
	}

	remaining := vf.volume.Size - off
	if remaining <= 0 {
		return 0, io.EOF
	}

	toRead := int64(len(p))
	eofAfter := false
	if toRead > remaining {
		toRead = remaining
		eofAfter = true
	}

	// Use streaming reader
	reader := vf.getOrCreateStreamingReader()
	if reader == nil {
		return 0, fmt.Errorf("failed to create streaming reader for volume %s", vf.volume.Name)
	}
	n, readErr := reader.ReadAt(p[:int(toRead)], off)

	if readErr != nil {
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return n, io.EOF
		}
		return n, readErr
	}
	if eofAfter {
		return n, io.EOF
	}
	return n, nil
}

// getOrCreateStreamingReader returns the streaming reader, creating it if needed.
// Uses sync.Once to ensure exactly one reader is created even with concurrent calls.
func (vf *File) getOrCreateStreamingReader() *reader.StreamingReader {
	vf.readerOnce.Do(func() {
		// Manager must be provided by FS
		if vf.manager == nil {
			vf.readerErr = fmt.Errorf("no connection client available for streaming reader")
			vf.logger.Error().Msg("No connection client available for streaming reader")
			return
		}

		// Convert volume segments to reader format
		segments := reader.VolumeToSegmentMeta(vf.volume)
		if len(segments) == 0 {
			vf.readerErr = fmt.Errorf("no segments found for streaming reader")
			vf.logger.Error().Msg("No segments found for streaming reader")
			return
		}

		// Build encryption config
		encConfig := reader.EncryptionFromVolume(vf.volume)

		// Configure the reader
		cfg := config.Get()
		readerConfig := reader.DefaultConfig()
		readerConfig.MaxConnections = vf.maxConcurrent
		readerConfig.PrefetchAhead = reader.PrefetchAheadSegments(vf.prefetchSize, segments)
		readerConfig.DiskPath = cfg.Usenet.DiskBufferPath

		var r *reader.StreamingReader
		var err error

		if encConfig.Enabled {
			r, err = reader.NewStreamingReaderWithEncryption(
				vf.ctx,
				vf.manager,
				segments,
				encConfig,
				reader.WithMaxDisk(readerConfig.MaxDisk),
				reader.WithMaxConnections(readerConfig.MaxConnections),
				reader.WithPrefetchAhead(readerConfig.PrefetchAhead),
				reader.WithDiskPath(readerConfig.DiskPath),
			)
		} else {
			r, err = reader.NewStreamingReader(
				vf.ctx,
				vf.manager,
				segments,
				reader.WithMaxDisk(readerConfig.MaxDisk),
				reader.WithMaxConnections(readerConfig.MaxConnections),
				reader.WithPrefetchAhead(readerConfig.PrefetchAhead),
				reader.WithDiskPath(readerConfig.DiskPath),
			)
		}

		if err != nil {
			vf.readerErr = err
			vf.logger.Error().Err(err).Msg("Failed to create streaming reader")
			return
		}
		vf.streamingReader.Store(r)
	})

	return vf.streamingReader.Load()
}

func (vf *File) Seek(offset int64, whence int) (int64, error) {
	if vf.closed.Load() {
		return 0, fs.ErrClosed
	}

	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = vf.pos.Load() + offset
	case io.SeekEnd:
		newPos = vf.volume.Size + offset
	default:
		return 0, fmt.Errorf("rar: invalid seek whence %d", whence)
	}

	if newPos < 0 {
		return 0, fmt.Errorf("rar: seek before beginning of file")
	}
	if newPos > vf.volume.Size {
		newPos = vf.volume.Size
	}
	if newPos != vf.pos.Load() {
		if vf.reader != nil {
			_ = vf.reader.Close()
			vf.reader = nil
		}
	}
	vf.pos.Store(newPos)
	return vf.pos.Load(), nil
}

func (vf *File) newReaderForRange(start, end int64) (io.ReadCloser, error) {
	// For sequential reads, we create a new StreamingReader and seek to start position
	if vf.manager == nil {
		return nil, fmt.Errorf("no connection client available")
	}

	// Convert volume segments to reader format
	segments := reader.VolumeToSegmentMeta(vf.volume)
	if len(segments) == 0 {
		totalSegs := len(vf.volume.Segments)
		var lastSegEnd int64
		if totalSegs > 0 {
			lastSegEnd = vf.volume.Segments[totalSegs-1].EndOffset
		}
		return nil, fmt.Errorf("rar: no segments found for range %d-%d (volume size: %d, total segments: %d, last segment ends at: %d)",
			start, end, vf.volume.Size, totalSegs, lastSegEnd)
	}

	// Build encryption config
	encConfig := reader.EncryptionFromVolume(vf.volume)

	// Configure the reader
	cfg := config.Get()
	readerConfig := reader.DefaultConfig()
	readerConfig.MaxConnections = vf.maxConcurrent
	readerConfig.PrefetchAhead = reader.PrefetchAheadSegments(vf.prefetchSize, segments)
	readerConfig.DiskPath = cfg.Usenet.DiskBufferPath

	var r *reader.StreamingReader
	var err error

	if encConfig.Enabled {
		r, err = reader.NewStreamingReaderWithEncryption(
			vf.ctx,
			vf.manager,
			segments,
			encConfig,
			reader.WithMaxDisk(readerConfig.MaxDisk),
			reader.WithMaxConnections(readerConfig.MaxConnections),
			reader.WithPrefetchAhead(readerConfig.PrefetchAhead),
			reader.WithDiskPath(readerConfig.DiskPath),
		)
	} else {
		r, err = reader.NewStreamingReader(
			vf.ctx,
			vf.manager,
			segments,
			reader.WithMaxDisk(readerConfig.MaxDisk),
			reader.WithMaxConnections(readerConfig.MaxConnections),
			reader.WithPrefetchAhead(readerConfig.PrefetchAhead),
			reader.WithDiskPath(readerConfig.DiskPath),
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create streaming reader: %w", err)
	}

	// Seek to start position for sequential reading
	if start > 0 {
		if _, err := r.Seek(start, io.SeekStart); err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("failed to seek to start position: %w", err)
		}
	}

	return r, nil
}

func (vf *File) Write(p []byte) (int, error) {
	if vf.closed.Load() {
		return 0, fs.ErrClosed
	}
	return 0, fmt.Errorf("rar: write not supported on read-only Volume")
}

func (vf *File) ensureReader() error {
	if vf.reader != nil {
		return nil
	}
	start := vf.pos.Load()
	end := vf.volume.Size - 1
	reader, err := vf.newReaderForRange(start, end)
	if err != nil {
		return err
	}
	vf.reader = reader
	return nil
}

func (vf *File) Stat() (fs.FileInfo, error) {
	return vf.info, nil
}

func (vf *File) Close() error {
	if vf.closed.Swap(true) {
		return nil
	}

	// Close streaming reader if used
	if vf.streamingReader.Load() != nil {
		_ = vf.streamingReader.Load().Close()
		vf.streamingReader.Store(nil)
	}

	// Close sequential reader
	if vf.reader != nil {
		err := vf.reader.Close()
		vf.reader = nil
		return err
	}
	return nil
}

type volumeInfo struct {
	name string
	size int64
}

func (vi volumeInfo) Name() string       { return vi.name }
func (vi volumeInfo) Size() int64        { return vi.size }
func (vi volumeInfo) Mode() fs.FileMode  { return 0444 }
func (vi volumeInfo) ModTime() time.Time { return time.Time{} }
func (vi volumeInfo) IsDir() bool        { return false }
func (vi volumeInfo) Sys() any           { return nil }
