package manager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type fallbackCallRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *fallbackCallRecorder) add(provider string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, provider)
}

func (r *fallbackCallRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type torrentAttemptSnapshot struct {
	id               string
	name             string
	debrid           string
	status           debridTypes.TorrentStatus
	downloadUncached bool
	fileCount        int
}

type fakeDebridClient struct {
	cfg                  config.Debrid
	recorder             *fallbackCallRecorder
	supportsAvailability bool
	available            bool
	submitFn             func(*debridTypes.Torrent) (*debridTypes.Torrent, error)
	checkFn              func(*debridTypes.Torrent) (*debridTypes.Torrent, error)
	deleteFn             func(string) error
	mu                   sync.Mutex
	submitCalls          int
	checkCalls           int
	availableCalls       int
	submitSnapshots      []torrentAttemptSnapshot
	deleteIDs            []string
}

var _ common.Client = (*fakeDebridClient)(nil)

func (f *fakeDebridClient) SubmitMagnet(torrent *debridTypes.Torrent) (*debridTypes.Torrent, error) {
	f.mu.Lock()
	f.submitCalls++
	f.submitSnapshots = append(f.submitSnapshots, torrentAttemptSnapshot{
		id:               torrent.Id,
		name:             torrent.Name,
		debrid:           torrent.Debrid,
		status:           torrent.Status,
		downloadUncached: torrent.DownloadUncached,
		fileCount:        len(torrent.Files),
	})
	f.mu.Unlock()
	f.recorder.add(f.cfg.Name)
	if f.submitFn != nil {
		return f.submitFn(torrent)
	}
	torrent.Id = f.cfg.Name + "-id"
	torrent.Debrid = f.cfg.Name
	return torrent, nil
}

func (f *fakeDebridClient) CheckStatus(torrent *debridTypes.Torrent) (*debridTypes.Torrent, error) {
	f.mu.Lock()
	f.checkCalls++
	f.mu.Unlock()
	if f.checkFn != nil {
		return f.checkFn(torrent)
	}
	torrent.Status = debridTypes.TorrentStatusDownloaded
	return torrent, nil
}

func (f *fakeDebridClient) DeleteTorrent(torrentID string) error {
	f.mu.Lock()
	f.deleteIDs = append(f.deleteIDs, torrentID)
	f.mu.Unlock()
	if f.deleteFn != nil {
		return f.deleteFn(torrentID)
	}
	return nil
}

func (f *fakeDebridClient) IsAvailable(infohashes []string) map[string]bool {
	f.mu.Lock()
	f.availableCalls++
	f.mu.Unlock()
	result := make(map[string]bool, len(infohashes))
	for _, hash := range infohashes {
		result[hash] = f.available
	}
	return result
}

func (f *fakeDebridClient) Config() config.Debrid  { return f.cfg }
func (f *fakeDebridClient) Logger() zerolog.Logger { return zerolog.Nop() }
func (f *fakeDebridClient) SupportsInstantAvailability() bool {
	return f.supportsAvailability
}
func (f *fakeDebridClient) SupportsCheck() bool { return true }
func (f *fakeDebridClient) GetDownloadLink(string, *debridTypes.File) (debridTypes.DownloadLink, error) {
	return debridTypes.DownloadLink{}, nil
}
func (f *fakeDebridClient) UpdateTorrent(*debridTypes.Torrent) error        { return nil }
func (f *fakeDebridClient) GetTorrent(string) (*debridTypes.Torrent, error) { return nil, nil }
func (f *fakeDebridClient) GetTorrents() ([]*debridTypes.Torrent, error)    { return nil, nil }
func (f *fakeDebridClient) RefreshDownloadLinks() error                     { return nil }
func (f *fakeDebridClient) CheckFile(context.Context, string, string) error { return nil }
func (f *fakeDebridClient) AccountManager() *account.Manager                { return nil }
func (f *fakeDebridClient) GetProfile() (*debridTypes.Profile, error)       { return nil, nil }
func (f *fakeDebridClient) GetAvailableSlots() (int, error)                 { return 1, nil }
func (f *fakeDebridClient) SyncAccounts()                                   {}
func (f *fakeDebridClient) DeleteLink(debridTypes.DownloadLink) error       { return nil }
func (f *fakeDebridClient) SpeedTest(context.Context) debridTypes.SpeedTestResult {
	return debridTypes.SpeedTestResult{}
}

func (f *fakeDebridClient) counts() (submit, check, available int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submitCalls, f.checkCalls, f.availableCalls
}

func (f *fakeDebridClient) snapshots() []torrentAttemptSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]torrentAttemptSnapshot(nil), f.submitSnapshots...)
}

func (f *fakeDebridClient) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleteIDs...)
}

func fallbackTestManager(clients ...*fakeDebridClient) *Manager {
	clientMap := xsync.NewMap[string, common.Client]()
	for _, client := range clients {
		clientMap.Store(client.cfg.Name, client)
	}
	return &Manager{clients: clientMap, logger: zerolog.Nop()}
}

func fallbackTestRequest(selected string, fallback bool, downloadUncached *bool) *ImportRequest {
	return &ImportRequest{
		SelectedDebrid:    selected,
		FallbackOnFailure: fallback,
		DownloadUncached:  downloadUncached,
		Magnet: &utils.Magnet{
			Name:     "original-name",
			InfoHash: "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
			Size:     1234,
			Link:     "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		},
		Arr: &arr.Arr{Name: "radarr"},
	}
}

func boolPointer(value bool) *bool { return &value }

func TestNewTorrentRequestCarriesArrFallbackPolicy(t *testing.T) {
	arrConfig := &arr.Arr{
		Name:              "radarr",
		SelectedDebrid:    "primary",
		FallbackOnFailure: true,
	}
	magnet := &utils.Magnet{Name: "test", InfoHash: "ABCDEF"}

	request := NewTorrentRequest("", "/downloads", magnet, arrConfig, config.DownloadActionSymlink, nil, "", ImportTypeQBit, false)
	if request.SelectedDebrid != "primary" || !request.FallbackOnFailure {
		t.Fatalf("request lost Arr routing policy: selected=%q fallback=%v", request.SelectedDebrid, request.FallbackOnFailure)
	}
}

func TestFilterDebridUsesPriorityThenConfigOrder(t *testing.T) {
	first := &fakeDebridClient{cfg: config.Debrid{Name: "first", ConfigOrder: 0}}
	second := &fakeDebridClient{cfg: config.Debrid{Name: "second", Priority: 5, ConfigOrder: 1}}
	third := &fakeDebridClient{cfg: config.Debrid{Name: "third", Priority: 2, ConfigOrder: 2}}
	tied := &fakeDebridClient{cfg: config.Debrid{Name: "tied", Priority: 2, ConfigOrder: 3}}
	manager := fallbackTestManager(tied, third, second, first)

	clients := manager.FilterDebrid(func(common.Client) bool { return true })
	names := make([]string, 0, len(clients))
	for _, client := range clients {
		names = append(names, client.Config().Name)
	}

	want := []string{"first", "third", "tied", "second"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected provider order: got %v, want %v", names, want)
	}
}

func TestSendToDebridKeepsSelectedProviderPinnedByDefault(t *testing.T) {
	recorder := &fallbackCallRecorder{}
	primary := &fakeDebridClient{
		cfg:      config.Debrid{Name: "primary", DownloadUncached: true, Priority: 2},
		recorder: recorder,
		submitFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return nil, errors.New("primary rejected")
		},
	}
	backup := &fakeDebridClient{
		cfg:      config.Debrid{Name: "backup", DownloadUncached: true, Priority: 1},
		recorder: recorder,
	}

	_, err := fallbackTestManager(backup, primary).SendToDebrid(context.Background(), fallbackTestRequest("primary", false, boolPointer(true)))
	if err == nil {
		t.Fatal("expected pinned provider failure")
	}
	if got := recorder.snapshot(); strings.Join(got, ",") != "primary" {
		t.Fatalf("unexpected attempts: %v", got)
	}
	if submit, _, _ := backup.counts(); submit != 0 {
		t.Fatalf("backup was called while fallback was disabled: %d submissions", submit)
	}
}

func TestSendToDebridSelectedProviderRemainsFirstAndSuccessStops(t *testing.T) {
	recorder := &fallbackCallRecorder{}
	primary := &fakeDebridClient{
		cfg:      config.Debrid{Name: "primary", DownloadUncached: true, Priority: 50},
		recorder: recorder,
	}
	backup := &fakeDebridClient{
		cfg:      config.Debrid{Name: "backup", DownloadUncached: true, Priority: 1},
		recorder: recorder,
	}

	torrent, err := fallbackTestManager(backup, primary).SendToDebrid(context.Background(), fallbackTestRequest("primary", true, boolPointer(true)))
	if err != nil {
		t.Fatalf("SendToDebrid returned error: %v", err)
	}
	if torrent.Debrid != "primary" {
		t.Fatalf("unexpected provider: got %q, want primary", torrent.Debrid)
	}
	if got := recorder.snapshot(); strings.Join(got, ",") != "primary" {
		t.Fatalf("fallback should stop after selected provider succeeds, got attempts %v", got)
	}
}

func TestSendToDebridFallsBackAfterSubmitFailure(t *testing.T) {
	recorder := &fallbackCallRecorder{}
	primary := &fakeDebridClient{
		cfg:      config.Debrid{Name: "primary", DownloadUncached: true, Priority: 2},
		recorder: recorder,
		submitFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return nil, errors.New("MAGNET_TOO_MANY_ACTIVE")
		},
	}
	backup := &fakeDebridClient{
		cfg:      config.Debrid{Name: "backup", DownloadUncached: true, Priority: 1},
		recorder: recorder,
	}

	torrent, err := fallbackTestManager(backup, primary).SendToDebrid(context.Background(), fallbackTestRequest("primary", true, boolPointer(true)))
	if err != nil {
		t.Fatalf("SendToDebrid returned error: %v", err)
	}
	if torrent.Debrid != "backup" {
		t.Fatalf("unexpected fallback provider: %q", torrent.Debrid)
	}
	if got := recorder.snapshot(); strings.Join(got, ",") != "primary,backup" {
		t.Fatalf("unexpected attempt order: %v", got)
	}
}

func TestSendToDebridCleansStatusFailureWithReturnedID(t *testing.T) {
	primary := &fakeDebridClient{
		cfg: config.Debrid{Name: "primary", DownloadUncached: true, Priority: 1},
		checkFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return &debridTypes.Torrent{Id: "status-id"}, errors.New("status failed")
		},
	}
	backup := &fakeDebridClient{cfg: config.Debrid{Name: "backup", DownloadUncached: true, Priority: 2}}

	torrent, err := fallbackTestManager(primary, backup).SendToDebrid(context.Background(), fallbackTestRequest("", false, boolPointer(true)))
	if err != nil {
		t.Fatalf("SendToDebrid returned error: %v", err)
	}
	if torrent.Debrid != "backup" {
		t.Fatalf("unexpected fallback provider: %q", torrent.Debrid)
	}
	deleted := primary.deleted()
	if len(deleted) != 1 || deleted[0] != "status-id" {
		t.Fatalf("wrong cleanup IDs: got %v, want [status-id]", deleted)
	}
}

func TestSendToDebridCacheOnlyProviderNeverStartsUncached(t *testing.T) {
	primary := &fakeDebridClient{
		cfg: config.Debrid{Name: "primary", DownloadUncached: true, Priority: 1},
		submitFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return nil, errors.New("primary rejected")
		},
	}
	backup := &fakeDebridClient{
		cfg:                  config.Debrid{Name: "backup", DownloadUncached: false, Priority: 2},
		supportsAvailability: true,
		available:            false,
	}

	_, err := fallbackTestManager(primary, backup).SendToDebrid(context.Background(), fallbackTestRequest("", false, boolPointer(true)))
	if err == nil {
		t.Fatal("expected all providers to reject the torrent")
	}
	if !strings.Contains(err.Error(), `provider "backup" availability check failed`) {
		t.Fatalf("missing provider-labelled cache error: %v", err)
	}
	submit, check, available := backup.counts()
	if submit != 0 || check != 0 || available != 1 {
		t.Fatalf("cache-only backup calls: submit=%d check=%d available=%d", submit, check, available)
	}
}

func TestSendToDebridCachedBackupKeepsUncachedDisabled(t *testing.T) {
	primary := &fakeDebridClient{
		cfg: config.Debrid{Name: "primary", DownloadUncached: true, Priority: 1},
		submitFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return nil, errors.New("primary rejected")
		},
	}
	backup := &fakeDebridClient{
		cfg:                  config.Debrid{Name: "backup", DownloadUncached: false, Priority: 2},
		supportsAvailability: true,
		available:            true,
	}

	torrent, err := fallbackTestManager(primary, backup).SendToDebrid(context.Background(), fallbackTestRequest("", false, boolPointer(true)))
	if err != nil {
		t.Fatalf("SendToDebrid returned error: %v", err)
	}
	if torrent.Debrid != "backup" {
		t.Fatalf("unexpected fallback provider: %q", torrent.Debrid)
	}
	snapshots := backup.snapshots()
	if len(snapshots) != 1 || snapshots[0].downloadUncached {
		t.Fatalf("cache-only provider received uncached permission: %+v", snapshots)
	}
}

func TestSendToDebridUsesFreshTorrentForEachAttempt(t *testing.T) {
	primary := &fakeDebridClient{
		cfg: config.Debrid{Name: "primary", DownloadUncached: true, Priority: 1},
		submitFn: func(torrent *debridTypes.Torrent) (*debridTypes.Torrent, error) {
			torrent.Id = "primary-id"
			torrent.Name = "mutated-name"
			torrent.Debrid = "primary"
			torrent.Status = debridTypes.TorrentStatusError
			torrent.Files["leaked"] = debridTypes.File{Name: "leaked"}
			return torrent, errors.New("primary rejected")
		},
	}
	backup := &fakeDebridClient{cfg: config.Debrid{Name: "backup", DownloadUncached: true, Priority: 2}}

	_, err := fallbackTestManager(primary, backup).SendToDebrid(context.Background(), fallbackTestRequest("", false, boolPointer(true)))
	if err != nil {
		t.Fatalf("SendToDebrid returned error: %v", err)
	}
	snapshots := backup.snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("unexpected backup snapshots: %+v", snapshots)
	}
	got := snapshots[0]
	if got.id != "" || got.name != "original-name" || got.debrid != "" || got.status != "" || got.fileCount != 0 {
		t.Fatalf("provider state leaked into backup attempt: %+v", got)
	}
	if deleted := primary.deleted(); len(deleted) != 1 || deleted[0] != "primary-id" {
		t.Fatalf("partial submit was not cleaned correctly: %v", deleted)
	}
}

func TestSendToDebridAllFailuresAreNonNilAndProviderLabelled(t *testing.T) {
	emptyID := &fakeDebridClient{
		cfg: config.Debrid{Name: "empty-id", DownloadUncached: true},
		submitFn: func(torrent *debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return torrent, nil
		},
	}

	_, err := fallbackTestManager(emptyID).SendToDebrid(context.Background(), fallbackTestRequest("", false, nil))
	if err == nil {
		t.Fatal("expected a non-nil aggregate error")
	}
	if !strings.Contains(err.Error(), `provider "empty-id" submit failed`) || !strings.Contains(err.Error(), "empty torrent id") {
		t.Fatalf("aggregate error lacks provider context: %v", err)
	}
}

func TestSendToDebridJoinsErrorsFromEveryAttempt(t *testing.T) {
	first := &fakeDebridClient{
		cfg: config.Debrid{Name: "first", DownloadUncached: true, Priority: 1},
		submitFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return nil, errors.New("first rejection")
		},
	}
	second := &fakeDebridClient{
		cfg: config.Debrid{Name: "second", DownloadUncached: true, Priority: 2},
		submitFn: func(*debridTypes.Torrent) (*debridTypes.Torrent, error) {
			return nil, errors.New("second rejection")
		},
	}

	_, err := fallbackTestManager(second, first).SendToDebrid(context.Background(), fallbackTestRequest("", false, nil))
	if err == nil {
		t.Fatal("expected an aggregate error")
	}
	for _, expected := range []string{`provider "first" submit failed`, `provider "second" submit failed`} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("aggregate error %q is missing %q", err, expected)
		}
	}
}

func TestProviderAllowsUncachedTreatsProviderPolicyAsCeiling(t *testing.T) {
	tests := []struct {
		name            string
		providerAllows  bool
		requestOverride *bool
		want            bool
	}{
		{name: "provider disabled request nil", providerAllows: false, want: false},
		{name: "provider disabled request enabled", providerAllows: false, requestOverride: boolPointer(true), want: false},
		{name: "provider enabled request disabled", providerAllows: true, requestOverride: boolPointer(false), want: false},
		{name: "provider enabled request nil", providerAllows: true, want: true},
		{name: "provider enabled request enabled", providerAllows: true, requestOverride: boolPointer(true), want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := providerAllowsUncached(test.providerAllows, test.requestOverride); got != test.want {
				t.Fatalf("providerAllowsUncached() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSendToDebridHonorsCanceledContextBeforeProviderCall(t *testing.T) {
	client := &fakeDebridClient{cfg: config.Debrid{Name: "primary", DownloadUncached: true}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fallbackTestManager(client).SendToDebrid(ctx, fallbackTestRequest("", false, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if submit, _, _ := client.counts(); submit != 0 {
		t.Fatalf("provider called after cancellation: %d submissions", submit)
	}
}
