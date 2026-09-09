package torbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
)

func TestCheckFileHonorsCancellation(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	for _, cancelBefore := range []bool{true, false} {
		name := "during request"
		if cancelBefore {
			name = "before request"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			provider := &Torbox{Host: server.URL, client: request.New(request.WithMaxRetries(0))}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelBefore {
				provider.downloadPresentLoaded = true
				cancel()
			}
			done := make(chan error, 1)
			go func() { done <- provider.CheckFile(ctx, "hash", "torbox://17/1") }()
			if !cancelBefore {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("request did not start")
				}
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("CheckFile = %v, want context.Canceled", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("check did not stop after cancellation")
			}
		})
	}
}
