package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

func TestImportPreservesPreparationErrors(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	mgr := manager.New()
	t.Cleanup(func() { _ = mgr.Stop() })
	unavailable := httptest.NewServer(http.NotFoundHandler())
	defer unavailable.Close()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("urls", "invalid://torrent"); err != nil {
		t.Fatal(err)
	}
	file, err := form.CreateFormFile("files", "broken.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "not a torrent"); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("nzbURLs", unavailable.URL+"/missing.nzb"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/add", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	(&Server{manager: mgr}).handleAddContent(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var results []manager.ImportRequest
	if err := json.Unmarshal(response.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	sources := []string{"invalid://torrent", "broken.torrent", "/missing.nzb"}
	if len(results) != len(sources) {
		t.Fatalf("results=%d, want %d", len(results), len(sources))
	}
	for i, source := range sources {
		if results[i].Status != "error" || !strings.Contains(results[i].Error, source) || strings.Contains(results[i].Error, "<nil>") {
			t.Errorf("result %d lost the source error: %+v", i, results[i])
		}
	}
}
