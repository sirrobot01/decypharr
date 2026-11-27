package arr

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestEffectiveDownloadAction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instance config.DownloadAction
		fallback config.DownloadAction
		want     config.DownloadAction
	}{
		{"unset uses the global default", "", config.DownloadActionSymlink, config.DownloadActionSymlink},
		{"set wins over the global default", config.DownloadActionDownload, config.DownloadActionSymlink, config.DownloadActionDownload},
		{"none is a real setting", config.DownloadActionNone, config.DownloadActionDownload, config.DownloadActionNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Arr{Name: "tv", DownloadAction: tc.instance}.EffectiveDownloadAction(tc.fallback)
			if got != tc.want {
				t.Fatalf("EffectiveDownloadAction = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDownloadActionSurvivesConfigSync(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	arrs := New()
	arrs.SyncFromConfig([]config.Arr{
		{Name: "sonarr", Host: "http://sonarr.example", Token: "a", DownloadAction: config.DownloadActionDownload, Source: string(SourceManual)},
		{Name: "radarr", Host: "http://radarr.example", Token: "b", Source: string(SourceManual)},
	})

	sonarr, _ := arrs.Get("sonarr")
	if sonarr.DownloadAction != config.DownloadActionDownload {
		t.Fatalf("sonarr DownloadAction = %q, want %q", sonarr.DownloadAction, config.DownloadActionDownload)
	}
	radarr, _ := arrs.Get("radarr")
	if radarr.DownloadAction != "" {
		t.Fatalf("radarr DownloadAction = %q, want it left unset", radarr.DownloadAction)
	}

	got := map[string]config.DownloadAction{}
	for _, c := range arrs.SyncToConfig() {
		got[c.Name] = c.DownloadAction
	}
	if got["sonarr"] != config.DownloadActionDownload || got["radarr"] != "" {
		t.Fatalf("SyncToConfig download actions = %#v", got)
	}
}

func TestArrIsZeroCountsDownloadAction(t *testing.T) {
	if (config.Arr{DownloadAction: config.DownloadActionDownload}).IsZero() {
		t.Fatal("an arr that only sets download_action must not read as empty")
	}
}
