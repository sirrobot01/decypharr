package webdav

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// streamCopyBufPool holds the copy buffers StreamResponse pipes sessions
// through; every session.Read costs a lock pass and watchdog arming, so
// copy granularity multiplies all of it.
var streamCopyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1<<20)
		return &b
	},
}

func (h *Handler) StreamResponse(entry *storage.Entry, name string, size int64, w http.ResponseWriter, r *http.Request) error {
	start, end := resolveRange(r.Header.Get("Range"), size)
	if start < 0 {
		start = 0
	}
	if end < 0 || end >= size {
		end = size - 1
	}
	if size <= 0 || start >= size || end < start {
		return customerror.NewError(
			fmt.Errorf("invalid byte range %d-%d for size %d", start, end, size),
			http.StatusRequestedRangeNotSatisfiable, "server.invalid_range", false, false)
	}

	// Extract client identifier from User-Agent header
	client := r.UserAgent()
	if client == "" {
		client = "Unknown"
	}

	owner := manager.RewindOwnerApplication
	cfg := config.Get()
	if cfg.Mount.Type == config.MountTypeRclone &&
		strings.EqualFold(cfg.Mount.Rclone.VfsCacheMode, "full") &&
		strings.HasPrefix(strings.ToLower(client), "rclone/") {
		owner = manager.RewindOwnerDownstream
	}
	stream, err := h.manager.OpenStreamWithRewindOwner(r.Context(), entry, name, start, client, owner)
	if err != nil {
		return customerror.NewError(err, http.StatusInternalServerError, "server.internal_error", false, false)
	}
	defer stream.Close()

	// Fast-fail before headers: an unreachable link becomes a proper error
	// status instead of a dead 200.
	if err := stream.Prime(); err != nil {
		return customerror.FromError(err)
	}

	length := end - start + 1
	w.Header().Set("Content-Type", utils.GetContentType(name))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	statusCode := http.StatusOK
	if start > 0 || end < size-1 {
		statusCode = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	w.WriteHeader(statusCode)

	// The wrapper struct hides w's ReaderFrom so io.CopyBuffer uses the
	// pooled buffer instead of net/http's 32KB one.
	bufPtr := streamCopyBufPool.Get().(*[]byte)
	_, err = io.CopyBuffer(struct{ io.Writer }{w}, io.LimitReader(stream, length), *bufPtr)
	streamCopyBufPool.Put(bufPtr)
	if err != nil {
		return customerror.NewError(err, http.StatusInternalServerError, "server.internal_error", false, true)
	}
	return nil
}

// resolveRange maps a Range header to file-relative offsets (byte-ranged
// split-archive slices are handled by the stream transport, not here).
// Returns (0, -1) — whole file — when there is no header, when it is
// unparsable, or when it holds multiple ranges: RFC 7233 lets a server
// ignore a Range header it can't satisfy structurally.
func resolveRange(rangeHeader string, size int64) (int64, int64) {
	if rangeHeader == "" {
		return 0, -1
	}
	ranges, err := parseRange(rangeHeader, size)
	if err != nil || len(ranges) != 1 {
		return 0, -1
	}
	return ranges[0].start, ranges[0].end
}
