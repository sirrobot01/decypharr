package qbit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/testutil"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/store"
)

// MockStore captures AddTorrent calls for testing
type MockStore struct {
	capturedRequests []*store.ImportRequest
	shouldFail       bool
	failError        error
}

func (m *MockStore) AddTorrent(ctx context.Context, req *store.ImportRequest) error {
	m.capturedRequests = append(m.capturedRequests, req)
	if m.shouldFail {
		return m.failError
	}
	return nil
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

// testHandleTorrentsAdd is a helper function to test the handleTorrentsAdd method
func testHandleTorrentsAdd(t *testing.T, firstLastPiecePrio, urls string, torrents [][]byte, expectedMagnetLinks []string, alwaysRmTorrentUrls bool) {
	// Create mock store and QBit with dependency injection
	mockStore := &MockStore{}
	qbit := NewWithDependencies(mockStore, nil, zerolog.Nop(), "testuser", "testpass", "/downloads", []string{"test"}, alwaysRmTorrentUrls)

	// Create HTTP request based on what's provided
	var req *http.Request
	if len(torrents) > 0 {
		// Handle multipart form with torrent files
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)

		if firstLastPiecePrio != "" {
			writer.WriteField("firstLastPiecePrio", firstLastPiecePrio)
		}
		writer.WriteField("category", "test")

		for i, torrentData := range torrents {
			part, err := writer.CreateFormFile("torrents", fmt.Sprintf("torrent_%d.torrent", i))
			if err != nil {
				t.Fatalf("Failed to create form file: %v", err)
			}
			_, err = io.Copy(part, bytes.NewReader(torrentData))
			if err != nil {
				t.Fatalf("Failed to copy torrent data: %v", err)
			}
		}
		writer.Close()

		req = httptest.NewRequest(http.MethodPost, "/api/v2/torrents/add", &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
	} else {
		// Handle URL form
		form := "urls=" + url.QueryEscape(urls)
		if firstLastPiecePrio != "" {
			form += "&firstLastPiecePrio=" + firstLastPiecePrio
		}
		form += "&category=test"

		req = httptest.NewRequest(http.MethodPost, "/api/v2/torrents/add", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	// Add arr to context
	testArr := arr.New("test", "", "", false, false, nil, "", "")
	ctx := context.WithValue(req.Context(), "arr", testArr)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Call the REAL HTTP handler
	qbit.handleTorrentsAdd(w, req)

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

func TestHandleTorrentsAdd_TorrentFiles_RmTrackerUrlsTrue(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	// Use the same torrent file twice. We just want to see if 2 requests are
	// made with the params stripped.
	torrents := [][]byte{torrentData, torrentData}

	// Expected magnet link (without trackers since we're stripping them)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso"

	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleTorrentsAdd(t, "true", "", torrents, expectedMagnetLinks, false)
}

func TestHandleTorrentsAdd_TorrentFiles_RmTrackerUrlsFalse(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	torrents := [][]byte{torrentData, torrentData}

	// Expected magnet link (with trackers preserved)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleTorrentsAdd(t, "false", "", torrents, expectedMagnetLinks, false)
}

func TestHandleTorrentsAdd_MagnetUrls_RmTrackerUrlsTrue(t *testing.T) {
	// Load test torrent file
	magnetData, err := testutil.GetTestDataContent("ubuntu-25.04-desktop-amd64.iso.magnet")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	urls := magnetData + "\n" + magnetData

	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleTorrentsAdd(t, "true", urls, nil, expectedMagnetLinks, false)
}

func TestHandleTorrentsAdd_MagnetUrls_RmTrackerUrlsFalse(t *testing.T) {
	// Load test torrent file
	magnetData, err := testutil.GetTestDataContent("ubuntu-25.04-desktop-amd64.iso.magnet")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	urls := magnetData + "\n" + magnetData

	// Expected magnet link (with trackers preserved)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce"
	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleTorrentsAdd(t, "false", urls, nil, expectedMagnetLinks, false)
}

func TestHandleTorrentsAdd_HttpTorrentUrl_RmTrackerUrlsTrue(t *testing.T) {
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

	testHandleTorrentsAdd(t, "true", server.URL, nil, expectedMagnetLinks, false)
}

func TestHandleTorrentsAdd_HttpTorrentUrl_AlwaysRmTrackerUrlsFalse_RmTrackerUrlsFalse(t *testing.T) {
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

	testHandleTorrentsAdd(t, "false", server.URL, nil, expectedMagnetLinks, false)
}

func TestHandleTorrentsAdd_HttpTorrentUrl_AlwaysRmTrackerUrlsTrue_RmTrackerUrlsFalse(t *testing.T) {
	// Load test torrent file
	torrentData, err := testutil.GetTestDataBytes("ubuntu-25.04-desktop-amd64.iso.torrent")
	if err != nil {
		t.Fatalf("Failed to load test torrent file: %v", err)
	}

	// Use the same torrent file twice. We just want to see if 2 requests are
	// made with the params stripped.
	torrents := [][]byte{torrentData, torrentData}

	// Expected magnet link (without trackers since we're stripping them)
	expectedMagnetLink := "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=ubuntu-25.04-desktop-amd64.iso"

	expectedMagnetLinks := []string{
		expectedMagnetLink,
		expectedMagnetLink,
	}

	testHandleTorrentsAdd(t, "false", "", torrents, expectedMagnetLinks, true)
}
