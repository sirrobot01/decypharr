package arr

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestAPIRootFollowsTypeAndVersion(t *testing.T) {
	cases := []struct {
		name     string
		instance Arr
		want     string
	}{
		{"sonarr defaults to v3", Arr{Type: Sonarr}, APIv3},
		{"radarr defaults to v3", Arr{Type: Radarr}, APIv3},
		{"lidarr defaults to v1", Arr{Type: Lidarr}, APIv1},
		{"readarr defaults to v1", Arr{Type: Readarr}, APIv1},
		{"unknown defaults to v3", Arr{Type: Others}, APIv3},
		{"prowlarr defaults to v3", Arr{Type: Prowlarr}, APIv3},
		{"explicit v1 overrides a v3 type", Arr{Type: Radarr, APIVersion: "v1"}, APIv1},
		{"explicit v3 overrides a v1 type", Arr{Type: Readarr, APIVersion: "v3"}, APIv3},
		{"an unknown version falls back to the type", Arr{Type: Readarr, APIVersion: "v3-eros"}, APIv1},
		{"full path form is accepted", Arr{APIVersion: "api/v1"}, APIv1},
	}
	for _, tc := range cases {
		if got := tc.instance.APIRoot(); got != tc.want {
			t.Errorf("%s: APIRoot() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestInferTypeRecognizesForks(t *testing.T) {
	cases := []struct {
		host, name string
		want       Type
	}{
		{"http://chaptarr:8789", "", Readarr},
		{"", "Chaptarr", Readarr},
		{"http://prowlarr:9696", "", Prowlarr},
		{"", "Prowlarr", Prowlarr},
		{"http://sonarr:8989", "", Sonarr},
	}
	for _, tc := range cases {
		if got := inferType(tc.host, tc.name); got != tc.want {
			t.Errorf("inferType(%q, %q) = %q, want %q", tc.host, tc.name, got, tc.want)
		}
	}
}

func TestFromConfigHonorsDeclaredTypeAndVersion(t *testing.T) {
	instance := fromConfig(config.Arr{Name: "books", Host: "http://books:8789", Token: "t", Type: "Readarr", APIVersion: "v1"})
	if instance.Type != Readarr || instance.APIVersion != "v1" || instance.APIRoot() != APIv1 {
		t.Fatalf("fromConfig = %#v (APIRoot %q)", instance, instance.APIRoot())
	}
}

func TestHasQueueExcludesProwlarr(t *testing.T) {
	if (Arr{Type: Prowlarr}).HasQueue() {
		t.Error("Prowlarr should not report a download queue")
	}
	for _, kind := range []Type{Sonarr, Radarr, Lidarr, Readarr, Others, ""} {
		if !(Arr{Type: kind}).HasQueue() {
			t.Errorf("type %q should report a download queue", kind)
		}
	}
}

func TestProbeFallsBackToTheOtherAPIRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/status" {
			_, _ = w.Write([]byte(`{"appName":"Chaptarr"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	// The name says nothing about the application, so the probe must classify
	// it from the app name the v1 endpoint reports.
	got, err := New().Probe(t.Context(), Arr{Host: server.URL, Token: "t", Name: "books"})
	if err != nil {
		t.Fatal(err)
	}
	if got != Readarr {
		t.Fatalf("Probe type = %q, want %q", got, Readarr)
	}
}

func TestProbeAsksTheDeclaredVersionFirst(t *testing.T) {
	var v1Calls, v3Calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/status":
			v1Calls.Add(1)
			_, _ = w.Write([]byte(`{"appName":"Lidarr"}`))
		case "/api/v3/system/status":
			v3Calls.Add(1)
			_, _ = w.Write([]byte(`{"appName":"Lidarr"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	instance := Arr{Host: server.URL, Token: "t", Name: "music", Type: Lidarr}
	if _, err := New().Probe(t.Context(), instance); err != nil {
		t.Fatal(err)
	}
	if v1Calls.Load() != 1 || v3Calls.Load() != 0 {
		t.Fatalf("probe calls v1=%d v3=%d, want v1=1 v3=0", v1Calls.Load(), v3Calls.Load())
	}
}

func TestCleanupQueuesSkipsProwlarr(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	service := testService(Arr{Host: server.URL, Token: "t", Type: Prowlarr})
	service.CleanupQueues(t.Context())

	if got := calls.Load(); got != 0 {
		t.Fatalf("prowlarr queue calls = %d, want 0", got)
	}
}

func TestCleanupQueuesStopsRetryingAnArrWithoutQueue(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	// An unrecognised app answers 404: it must be asked once, then skipped.
	service := testService(Arr{Host: server.URL, Token: "t", Type: Others})
	service.CleanupQueues(t.Context())
	service.CleanupQueues(t.Context())

	if got := calls.Load(); got != 1 {
		t.Fatalf("queue endpoint calls = %d, want 1", got)
	}
}

func TestCleanupQueuesUsesV1ForReadarrFamily(t *testing.T) {
	var v1, v3 atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/queue":
			v1.Add(1)
			_, _ = w.Write([]byte(`{"totalRecords":0,"records":[]}`))
		case "/api/v3/queue":
			v3.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	service := testService(Arr{Host: server.URL, Token: "t", Type: Readarr})
	service.CleanupQueues(t.Context())

	if v1.Load() != 1 || v3.Load() != 0 {
		t.Fatalf("queue calls v1=%d v3=%d, want v1=1 v3=0", v1.Load(), v3.Load())
	}
}

func TestResolveTypeCorrectsAGuessedTypeOnce(t *testing.T) {
	var statusCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/status" {
			statusCalls.Add(1)
			_, _ = w.Write([]byte(`{"appName":"Chaptarr"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	// The name says Sonarr, the instance serves a v1 application: the guess must
	// be corrected by the first probe, then trusted without another request.
	instance := fromConfig(config.Arr{Name: "sonarr-books", Host: server.URL, Token: "t"})
	service := New()
	service.arrs[instance.Name] = instance

	if got := service.ResolveType(t.Context(), "sonarr-books"); got != Readarr {
		t.Fatalf("ResolveType = %q, want %q", got, Readarr)
	}
	if got := service.ResolveType(t.Context(), "sonarr-books"); got != Readarr {
		t.Fatalf("second ResolveType = %q, want %q", got, Readarr)
	}
	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("status calls = %d, want 1", got)
	}
	updated, _ := service.Get("sonarr-books")
	if updated.Type != Readarr {
		t.Fatalf("instance type = %q, want %q", updated.Type, Readarr)
	}
}

func TestResolveTypeTrustsADeclaredType(t *testing.T) {
	var statusCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		statusCalls.Add(1)
		_, _ = w.Write([]byte(`{"appName":"Sonarr"}`))
	}))
	t.Cleanup(server.Close)

	instance := fromConfig(config.Arr{Name: "books", Host: server.URL, Token: "t", Type: "readarr"})
	service := New()
	service.arrs[instance.Name] = instance

	if got := service.ResolveType(t.Context(), "books"); got != Readarr {
		t.Fatalf("ResolveType = %q, want %q", got, Readarr)
	}
	if got := statusCalls.Load(); got != 0 {
		t.Fatalf("a declared type must not be probed, %d status calls", got)
	}
}

func TestSyncToConfigKeepsOnlyADeclaredType(t *testing.T) {
	// "chaptarr" is already configured without a type: the guess the service
	// makes must not be written into that entry.
	if _, err := config.Update(func(c *config.Config) error {
		c.Arrs = []config.Arr{{Name: "chaptarr", Host: "http://chaptarr:8789", Token: "t"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = config.Update(func(c *config.Config) error {
			c.Arrs = nil
			return nil
		})
	})

	service := New()
	for _, configured := range []config.Arr{
		{Name: "chaptarr", Host: "http://chaptarr:8789", Token: "t"},
		{Name: "prowlarr-extra", Host: "http://prowlarr:9696", Token: "t"},
		{Name: "books", Host: "http://books:8789", Token: "t", Type: "Readarr", APIVersion: "v1"},
	} {
		instance := fromConfig(configured)
		service.arrs[instance.Name] = instance
	}

	byName := make(map[string]config.Arr)
	for _, saved := range service.SyncToConfig() {
		byName[saved.Name] = saved
	}
	// "chaptarr" is a configured entry, "prowlarr-extra" has none yet: neither
	// may carry the type the service guessed for it.
	for _, name := range []string{"chaptarr", "prowlarr-extra"} {
		if got := byName[name].Type; got != "" {
			t.Errorf("%s: guessed type persisted as %q, want no type", name, got)
		}
	}
	if got := byName["books"].Type; got != "readarr" {
		t.Errorf("declared type = %q, want %q", got, "readarr")
	}
	if got := byName["books"].APIVersion; got != "v1" {
		t.Errorf("declared api version = %q, want %q", got, "v1")
	}
}

func TestCleanupQueuesRetriesAfterConfigSync(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	service := testService(Arr{Host: server.URL, Token: "t", Type: Others})
	service.CleanupQueues(t.Context())
	if got := calls.Load(); got != 1 {
		t.Fatalf("queue endpoint calls = %d, want 1", got)
	}

	// The configuration changed: the instance is asked again, in case it now
	// exposes a queue.
	service.SyncFromConfig([]config.Arr{{Name: "arr", Host: server.URL, Token: "t", Type: "others"}})
	service.CleanupQueues(t.Context())
	if got := calls.Load(); got != 2 {
		t.Fatalf("queue endpoint calls after sync = %d, want 2", got)
	}
}
