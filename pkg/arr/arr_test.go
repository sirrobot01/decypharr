package arr

import (
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/config"
)

func TestSyncFromConfigHost(t *testing.T) {
	tests := []struct {
		name     string
		newHost  string
		wantHost string
	}{
		{"valid new host replaces the stored one", "http://sonarr:8989", "http://sonarr:8989"},
		{"invalid new host keeps the stored one", "not a url", "http://old:8989"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Storage{arrs: xsync.NewMap[string, *Arr]()}
			s.arrs.Store("sonarr", New("sonarr", "http://old:8989", "token", false, nil, "", "manual"))

			s.SyncFromConfig([]config.Arr{{Name: "sonarr", Host: tt.newHost, Token: "token"}})

			if got := s.Get("sonarr").Host; got != tt.wantHost {
				t.Fatalf("host = %q, want %q", got, tt.wantHost)
			}
		})
	}
}
