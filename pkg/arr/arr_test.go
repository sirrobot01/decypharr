package arr

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestSyncFromConfigAppliesValidHost(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	arrs := New()
	arrs.AddOrUpdate(Arr{Name: "whisparr", Host: "http://old.example", Token: "old-token", Source: SourceAuto})
	arrs.SyncFromConfig([]config.Arr{{
		Name:   "whisparr",
		Host:   "http://new.example",
		Token:  "new-token",
		Source: string(SourceAuto),
	}})

	got, ok := arrs.Get("whisparr")
	if !ok || got.Host != "http://new.example" || got.Token != "new-token" {
		t.Fatalf("synced Arr = %#v", got)
	}
}

func TestSyncFromConfigPreservesResolvedHostForInvalidUpdate(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	arrs := New()
	arrs.AddOrUpdate(Arr{Name: "whisparr", Host: "http://resolved.example", Token: "old-token", Source: SourceAuto})
	arrs.SyncFromConfig([]config.Arr{{
		Name:   "whisparr",
		Host:   "not-a-url",
		Token:  "new-token",
		Source: string(SourceAuto),
	}})

	got, ok := arrs.Get("whisparr")
	if !ok || got.Host != "http://resolved.example" || got.Token != "new-token" {
		t.Fatalf("synced Arr = %#v", got)
	}
}

func TestArrInstanceFingerprintCanonicalizesHost(t *testing.T) {
	first := Arr{Type: Sonarr, Host: "HTTP://Example.COM:80/sonarr/", Token: "first"}.Fingerprint()
	second := Arr{Type: Sonarr, Host: "http://example.com/sonarr", Token: "second"}.Fingerprint()
	if first == "" || first != second {
		t.Fatalf("equivalent Arr hosts produced fingerprints %q and %q", first, second)
	}

	differentPath := Arr{Type: Sonarr, Host: "http://example.com/other"}.Fingerprint()
	differentType := Arr{Type: Radarr, Host: "http://example.com/sonarr"}.Fingerprint()
	if first == differentPath || first == differentType {
		t.Fatal("different Arr instances produced the same fingerprint")
	}
}
