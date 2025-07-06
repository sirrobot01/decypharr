package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/testutil"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/store"
)

// setupTestConfig creates a temporary config for tests
func setupTestConfig(t *testing.T, alwaysRmTrackerUrls bool) func() {
	// Create a temporary directory for test config
	tempDir, err := os.MkdirTemp("", "decypharr-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}

	// Create a minimal config.json file
	configContent := fmt.Sprintf(
		`{
			"qbittorrent": {
				"download_folder": "/test/downloads",
				"always_rm_tracker_urls": %v
			},
			"debrids": [
				{
				"name": "realdebrid",
				"api_key": "test",
				"folder": "test"
				}
			]
		}`, alwaysRmTrackerUrls)

	configFile := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Set the config path and force reload
	config.SetConfigPath(tempDir)
	config.Reload()

	// Return cleanup function
	return func() {
		os.RemoveAll(tempDir)
		config.Reload() // Reset config state
	}
}

// MockArrStorage implements ArrStorageInterface for testing
type MockArrStorage struct {
	arrs map[string]*arr.Arr
}

func (m *MockArrStorage) Get(name string) *arr.Arr {
	return m.arrs[name]
}

// MockStore captures AddTorrent calls for testing
type MockStore struct {
	capturedRequests []*store.ImportRequest
	shouldFail       bool
	failError        error
	arrStorage       ArrStorageInterface
}

func (m *MockStore) AddTorrent(ctx context.Context, req *store.ImportRequest) error {
	m.capturedRequests = append(m.capturedRequests, req)
	if m.shouldFail {
		return m.failError
	}
	return nil
}

func (m *MockStore) Arr() ArrStorageInterface {
	return m.arrStorage
}

func (m *MockStore) Reset() {
	m.capturedRequests = nil
	m.shouldFail = false
	m.failError = nil
}

func (m *MockStore) GetLastRequest() *store.ImportRequest {
	if len(m.capturedRequests) == 0 {
		return nil
	}
	return m.capturedRequests[len(m.capturedRequests)-1]
}

func (m *MockStore) GetAllRequests() []*store.ImportRequest {
	return m.capturedRequests
}

// testHandleAddContent is a helper function to test the handleAddContent method
func testHandleAddContent(t *testing.T, rmTrackerUrls bool, AlwaysRmTrackerUrls bool, urls string, files [][]byte, expectedMagnetLinks []string) {
	// Setup test config with alwaysRmTrackerUrls = false (so we can test the form parameter)
	cleanup := setupTestConfig(t, AlwaysRmTrackerUrls)
	defer cleanup()

	// Create mock store and Web with dependency injection
	arrStorage := &MockArrStorage{
		arrs: make(map[string]*arr.Arr),
	}
	mockStore := &MockStore{
		arrStorage: arrStorage,
	}
	web := NewWithDependencies(mockStore, zerolog.Nop())

	// Create multipart form data for all requests (handleAddContent expects multipart)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	writer.WriteField("rmTrackerUrls", fmt.Sprintf("%v", rmTrackerUrls))
	writer.WriteField("arr", "test")
	writer.WriteField("action", "symlink")
	writer.WriteField("debrid", "realdebrid")
	writer.WriteField("downloadFolder", "/test/downloads")

	// Add URLs if provided
	if urls != "" {
		writer.WriteField("urls", urls)
	}

	// Add files if provided
	for i, fileData := range files {
		part, err := writer.CreateFormFile("files", fmt.Sprintf("torrent_%d.torrent", i))
		if err != nil {
			t.Fatalf("Failed to create form file: %v", err)
		}
		_, err = io.Copy(part, bytes.NewReader(fileData))
		if err != nil {
			t.Fatalf("Failed to copy file data: %v", err)
		}
	}
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/add", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	w := httptest.NewRecorder()

	// Call the REAL HTTP handler
	web.handleAddContent(w, req)

	// Check HTTP response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d. Response: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// Verify the expected number of ImportRequests
	allRequests := mockStore.GetAllRequests()
	if len(allRequests) != len(expectedMagnetLinks) {
		t.Errorf("Expected %d ImportRequests, got %d", len(expectedMagnetLinks), len(allRequests))
	}

	// Verify each magnet link matches expected
	for i, importReq := range allRequests {
		if i < len(expectedMagnetLinks) {
			if importReq.Magnet.Link != expectedMagnetLinks[i] {
				t.Errorf("ImportRequest %d: Expected magnet link '%s', got '%s'", i, expectedMagnetLinks[i], importReq.Magnet.Link)
			}
		}
	}
}

func TestHandleAddContent_TorrentFiles_RmTrackerUrlsTrue(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	files := [][]byte{torrentData, torrentData}

	// Expected magnet link (without trackers since we're stripping them)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleAddContent(t, true, false, "", files, expectedMagnetLinks)
}

func TestHandleAddContent_TorrentFiles_RmTrackerUrlsFalse(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	files := [][]byte{torrentData, torrentData}

	// Expected magnet link (with trackers preserved)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleAddContent(t, false, false, "", files, expectedMagnetLinks)
}

func TestHandleAddContent_MagnetUrls_RmTrackerUrlsTrue(t *testing.T) {
	// Load test magnet URL
	magnetData, err := testutil.GetTestDataContent("ubuntu-25.04-desktop-amd64.iso.magnet")
	if err != nil {
		t.Fatalf("Failed to load test magnet URL: %v", err)
	}

	urls := magnetData + "\n" + magnetData

	// Expected magnet link (without trackers since we're stripping them)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleAddContent(t, true, false, urls, nil, expectedMagnetLinks)
}

func TestHandleAddContent_MagnetUrls_RmTrackerUrlsFalse(t *testing.T) {
	// Load test magnet URL
	magnetData, err := testutil.GetTestDataContent("ubuntu-25.04-desktop-amd64.iso.magnet")
	if err != nil {
		t.Fatalf("Failed to load test magnet URL: %v", err)
	}

	urls := magnetData + "\n" + magnetData

	// Expected magnet link (with trackers preserved)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleAddContent(t, false, false, urls, nil, expectedMagnetLinks)
}

func TestHandleAddContent_HttpTorrentUrl_RmTrackerUrlsTrue(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	// Create a test HTTP server that serves the torrent file
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Write(torrentData)
	}))
	defer server.Close()

	// Expected magnet link (without trackers since we're stripping them)
	expectedMagnetLinks := []string{
		"magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso",
	}

	testHandleAddContent(t, true, false, server.URL, nil, expectedMagnetLinks)
}

func TestHandleAddContent_HttpTorrentUrl_RmTrackerUrlsFalse(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	// Create a test HTTP server that serves the torrent file
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Write(torrentData)
	}))
	defer server.Close()

	// Expected magnet link (with trackers preserved)
	expectedMagnetLinks := []string{
		"magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce",
	}

	testHandleAddContent(t, false, false, server.URL, nil, expectedMagnetLinks)
}

func TestHandleAddContent_TorrentFiles_RmTrackerUrlsTrue_AlwaysRmTrackerUrlsTrue(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	files := [][]byte{torrentData, torrentData}

	// Expected magnet link (without trackers since we're stripping them)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleAddContent(t, false, true, "", files, expectedMagnetLinks)
}