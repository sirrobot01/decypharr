package manager

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sourcegraph/conc/pool"
)

const (
	MaxCacheWarmWorkers = 10
	MaxNZBPreCacheFiles = 5
	CacheWarmTimeout    = 60 * time.Second

	// Container metadata lives at the head (streamable MP4 moov, EBML header)
	// or the tail (non-streamable MP4 moov, MKV cues/seek index), so warming
	// head+tail covers what a downstream ffprobe/import scan will seek to.
	cacheWarmHeadSize = 2 * 1024 * 1024 // 2MB
	cacheWarmTailSize = 2 * 1024 * 1024 // 2MB
)

type MountManager interface {
	Start(ctx context.Context) error
	Stop() error
	Stats() map[string]any
	IsReady() bool
	Type() string
	Refresh(dirs []string) error
}

func (m *Manager) RefreshEntries(refreshMount bool) {
	// Refresh entries
	m.entry.Refresh()

	// Refresh mount if needed
	if refreshMount {
		go func() {
			_ = m.RefreshMount()
		}()
	}
}

func (m *Manager) RefreshMount() error {
	dirs := strings.FieldsFunc(m.config.RefreshDirs, func(r rune) bool {
		return r == ',' || r == '&'
	})
	if len(dirs) == 0 {
		dirs = []string{"__all__"}
	}

	// Call event handler if set
	if m.mountManager != nil {
		return m.mountManager.Refresh(dirs)
	}
	return nil
}

// WarmFileCache reads the head and tail of each media file through the mount
// to warm the VFS disk cache, so a subsequent media probe or import scan over
// the mount is fast. This replaces spawning ffprobe: the read pattern is
// deterministic, needs no external binary, and warms the exact bytes a
// downstream probe seeks to (see cacheWarmHeadSize/cacheWarmTailSize).
func (m *Manager) WarmFileCache(filePaths []string) error {
	if len(filePaths) == 0 {
		return nil
	}

	// Use a worker pool to limit concurrency and avoid overwhelming the system
	p := pool.New().WithMaxGoroutines(min(len(filePaths), MaxCacheWarmWorkers))

	for _, fp := range filePaths {
		if !utils.IsMediaFile(fp) {
			continue
		}
		p.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), CacheWarmTimeout)
			defer cancel()
			if err := m.warmOneFile(ctx, fp); err != nil {
				// Log error but continue
				m.logger.Warn().
					Err(err).
					Str("file", fp).
					Msg("cache warm failed")
			}
		})
	}

	p.Wait()
	return nil
}

// warmOneFile reads the head and (for large enough files) the tail of path,
// going through the mount so the FUSE/VFS cache is populated.
func (m *Manager) warmOneFile(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	if size == 0 {
		return nil
	}

	head := min(int64(cacheWarmHeadSize), size)
	if err := drainRange(ctx, f, 0, head); err != nil {
		return err
	}

	// Only warm the tail when it doesn't overlap the head we just read.
	if size > int64(cacheWarmHeadSize)+int64(cacheWarmTailSize) {
		if err := drainRange(ctx, f, size-int64(cacheWarmTailSize), int64(cacheWarmTailSize)); err != nil {
			return err
		}
	}
	return nil
}

// drainRange reads length bytes starting at off, in chunks, discarding the
// data and checking ctx between chunks so a stalled mount can't pin a worker
// past CacheWarmTimeout.
func drainRange(ctx context.Context, r io.ReaderAt, off, length int64) error {
	const chunk = 1 << 20 // 1MB
	buf := make([]byte, chunk)
	for read := int64(0); read < length; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(length-read, chunk)
		got, err := r.ReadAt(buf[:n], off+read)
		read += int64(got)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
	return nil
}

type stubMountManager struct{}

func (s *stubMountManager) Refresh(dirs []string) error {
	return nil
}

func NewStubMountManager() MountManager {
	return &stubMountManager{}
}

func (s *stubMountManager) Start(ctx context.Context) error {
	return nil
}
func (s *stubMountManager) Stop() error {
	return nil
}
func (s *stubMountManager) Stats() map[string]any {
	return map[string]any{
		"message": "no mount configured",
	}
}
func (s *stubMountManager) IsReady() bool {
	return false
}
func (s *stubMountManager) Type() string {
	return "none"
}
