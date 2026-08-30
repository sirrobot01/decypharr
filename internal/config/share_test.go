package config

import (
	"path/filepath"
	"testing"
	"time"
)

// The cache claims disk nobody asked for, so it stays off until an operator
// turns it on.
func TestShareCacheDefaultsOff(t *testing.T) {
	if (ShareCache{}).IsEnabled() {
		t.Fatal("unset cache should be disabled")
	}
	on, off := true, false
	if !(ShareCache{Enabled: &on}).IsEnabled() {
		t.Fatal("explicit true should be enabled")
	}
	if (ShareCache{Enabled: &off}).IsEnabled() {
		t.Fatal("explicit false should be disabled")
	}
}

func TestShareCacheResolvesValues(t *testing.T) {
	s := ShareCache{MaxSize: "20GB", MaxAge: "6h", ChunkSize: "8MB", ReadAhead: "32MB"}
	if got := s.MaxSizeBytes(); got != 20*1000*1000*1000 && got != 20<<30 {
		t.Fatalf("max size = %d", got)
	}
	if got := s.MaxAgeDuration(); got != 6*time.Hour {
		t.Fatalf("max age = %v", got)
	}
	if s.ChunkSizeBytes() == 0 || s.ReadAheadBytes() == 0 {
		t.Fatal("chunk size and read ahead should parse")
	}
}

// An unset or unusable value resolves to zero, which the cache reads as "use
// the package default" — it must never be mistaken for "no budget".
func TestShareCacheUnsetValuesAreZero(t *testing.T) {
	for _, s := range []ShareCache{{}, {MaxSize: "nonsense", MaxAge: "nonsense"}} {
		if s.MaxSizeBytes() != 0 || s.ChunkSizeBytes() != 0 || s.ReadAheadBytes() != 0 {
			t.Fatalf("%#v resolved a nonzero size", s)
		}
		if s.MaxAgeDuration() != 0 {
			t.Fatalf("%#v resolved a nonzero age", s)
		}
	}
}

func TestShareCacheDirDefault(t *testing.T) {
	SetConfigPath(t.TempDir())
	t.Cleanup(Reset)
	on := true

	c := &Config{ShareCache: ShareCache{Enabled: &on}}
	c.setShareCacheDefaults()
	if c.ShareCache.Dir != "" {
		t.Fatalf("no export enabled, but a cache dir was set: %q", c.ShareCache.Dir)
	}

	// An enabled export with the cache left off gains no configuration either.
	c = &Config{NFS: NFS{Enabled: true}}
	c.setShareCacheDefaults()
	if c.ShareCache.Dir != "" {
		t.Fatalf("cache is off, but a cache dir was set: %q", c.ShareCache.Dir)
	}

	c = &Config{NFS: NFS{Enabled: true}, ShareCache: ShareCache{Enabled: &on}}
	c.setShareCacheDefaults()
	if want := filepath.Join(GetMainPath(), "share-cache"); c.ShareCache.Dir != want {
		t.Fatalf("cache dir = %q, want %q", c.ShareCache.Dir, want)
	}

	c = &Config{SMB: SMB{Enabled: true}, ShareCache: ShareCache{Enabled: &on, Dir: "/custom"}}
	c.setShareCacheDefaults()
	if c.ShareCache.Dir != "/custom" {
		t.Fatalf("default overwrote a configured dir: %q", c.ShareCache.Dir)
	}
}
