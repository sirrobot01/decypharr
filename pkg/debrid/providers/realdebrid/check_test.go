package realdebrid

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestCheckFileHonorsCancellation(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	for _, operation := range []string{"check", "download link"} {
		for _, cancelBefore := range []bool{true, false} {
			name := "during request"
			if cancelBefore {
				name = "before request"
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				started := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					<-r.Context().Done()
				}))
				defer server.Close()
				provider := &RealDebrid{Host: server.URL, repairClient: request.New(request.WithMaxRetries(0))}
				provider.accountsManager = account.NewManager(config.Debrid{Name: "realdebrid", DownloadAPIKeys: []string{"token"}}, nil, zerolog.Nop())
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if cancelBefore {
					cancel()
				}
				done := make(chan error, 1)
				go func() {
					if operation == "download link" {
						_, err := provider.GetDownloadLink(ctx, "id", &types.File{Link: "https://example.test/file"})
						done <- err
						return
					}
					done <- provider.CheckFile(ctx, "hash", "file-id")
				}()
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
}
