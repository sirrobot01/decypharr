//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
)

func TestNewFileUsesMetadataModificationTime(t *testing.T) {
	var managerInstance manager.Manager
	info := managerInstance.RootInfo()
	file := NewFile(nil, &config.FuseConfig{}, info, &logger.RateLimitedLogger{})

	if got := file.modTime(info); !got.Equal(info.ModTime()) {
		t.Fatalf("file timestamp = %v, want metadata timestamp %v", got, info.ModTime())
	}
}

// A node rebuilt for an entry without a stored time must keep its mtime, or
// media servers analyze the file again.
func TestRebuiltFileKeepsFallbackTime(t *testing.T) {
	info := &manager.FileInfo{}
	var first, second fuse.AttrOut
	NewFile(nil, &config.FuseConfig{}, info, &logger.RateLimitedLogger{}).Getattr(context.Background(), nil, &first)
	time.Sleep(1100 * time.Millisecond)
	NewFile(nil, &config.FuseConfig{}, info, &logger.RateLimitedLogger{}).Getattr(context.Background(), nil, &second)

	if first.Mtime != second.Mtime {
		t.Fatalf("mtime changed when the node was rebuilt: %d, then %d", first.Mtime, second.Mtime)
	}
}

func TestHandleKeepsMetadataSnapshot(t *testing.T) {
	initial := testRemoteFileInfo(t, 128, time.Now().Add(-time.Hour))
	replacement := testRemoteFileInfo(t, 256, time.Now())
	file := NewFile(nil, &config.FuseConfig{}, initial, logger.NewRateLimitedLogger())
	handle := &Handle{file: file, info: initial, content: initial.Content()}

	file.updateInfo(replacement)

	if got := file.infoForHandle(handle); got != initial {
		t.Fatal("open handle did not retain its original metadata")
	}
	if got := file.infoForHandle(nil); got != replacement {
		t.Fatal("inode did not expose refreshed metadata to new operations")
	}
}

func TestFileMetadataRefreshIsRaceSafe(t *testing.T) {
	first := testRemoteFileInfo(t, 128, time.Now().Add(-time.Hour))
	second := testRemoteFileInfo(t, 256, time.Now())
	file := NewFile(nil, &config.FuseConfig{}, first, logger.NewRateLimitedLogger())

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range 1000 {
			file.updateInfo(first)
			file.updateInfo(second)
		}
	}()
	go func() {
		defer workers.Done()
		for range 1000 {
			var out fuse.AttrOut
			if errno := file.Getattr(context.Background(), nil, &out); errno != 0 {
				t.Errorf("Getattr failed: %v", errno)
				return
			}
		}
	}()
	workers.Wait()
}
