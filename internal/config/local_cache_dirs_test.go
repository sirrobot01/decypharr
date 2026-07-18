package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The local cache worker replaces symlinks with real files. It used to scan
// only the qBittorrent download folder, which is the wrong place when the arrs
// import the symlink itself into the media library: the download folder is
// bypassed on import, so caching it burned disk without ever changing what the
// media server actually read. CacheDirs makes the scan target configurable.
func TestLocalCacheDirsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string // substring expected in marshaled JSON ("" = key absent)
	}{
		{"explicit dirs persist", []string{"/data/media/movies", "/data/media/tv"}, `"cache_dirs":["/data/media/movies","/data/media/tv"]`},
		{"single dir persists", []string{"/data/media"}, `"cache_dirs":["/data/media"]`},
		{"unset omits the key", nil, ""},
		{"empty slice omits the key", []string{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(LocalCache{Enabled: true, CacheDirs: tc.in})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(b)

			if tc.want == "" {
				if strings.Contains(got, `"cache_dirs"`) {
					t.Fatalf("expected no cache_dirs key, got %s", got)
				}
			} else if !strings.Contains(got, tc.want) {
				t.Fatalf("expected %s in %s", tc.want, got)
			}

			var out LocalCache
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(out.CacheDirs) != len(tc.in) {
				t.Fatalf("length changed: in=%v out=%v", tc.in, out.CacheDirs)
			}
			for i := range tc.in {
				if out.CacheDirs[i] != tc.in[i] {
					t.Fatalf("value changed at %d: in=%q out=%q", i, tc.in[i], out.CacheDirs[i])
				}
			}
		})
	}
}

// LOCAL_CACHE_DIRS is comma-separated; blanks and stray whitespace must not
// become empty directory entries, which would make the worker scan "".
func TestLocalCacheDirsEnvParsing(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{"single", "/data/media", []string{"/data/media"}},
		{"multiple", "/data/media/movies,/data/media/tv", []string{"/data/media/movies", "/data/media/tv"}},
		{"whitespace trimmed", " /data/movies , /data/tv ", []string{"/data/movies", "/data/tv"}},
		{"blank entries skipped", "/data/movies,,/data/tv,", []string{"/data/movies", "/data/tv"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOCAL_CACHE_DIRS", tc.env)

			c := &Config{}
			c.setDefaults()

			if len(c.LocalCache.CacheDirs) != len(tc.want) {
				t.Fatalf("got %v, want %v", c.LocalCache.CacheDirs, tc.want)
			}
			for i := range tc.want {
				if c.LocalCache.CacheDirs[i] != tc.want[i] {
					t.Fatalf("index %d: got %q, want %q", i, c.LocalCache.CacheDirs[i], tc.want[i])
				}
			}
		})
	}
}
