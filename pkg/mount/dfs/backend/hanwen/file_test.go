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

	if !file.createdAt.Equal(info.ModTime()) {
		t.Fatalf("file timestamp = %v, want metadata timestamp %v", file.createdAt, info.ModTime())
	}
}

func TestNewFileChoosesStableFallbackTime(t *testing.T) {
	info := &manager.FileInfo{}
	file := NewFile(nil, &config.FuseConfig{}, info, &logger.RateLimitedLogger{})

	if file.createdAt.IsZero() {
		t.Fatal("file timestamp must be initialized when metadata has no modification time")
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
