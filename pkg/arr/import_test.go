package arr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManualImportPreservesArrMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		arrType  Type
		path     string
		identity string
		quality  string
	}{
		{
			name: "radarr strm", arrType: Radarr, path: "/downloads/Movie/Movie.strm",
			identity: `"movie":{"id":42}`,
			quality:  `{"quality":{"id":3,"name":"WEBDL-1080p"},"revision":{"version":1,"real":0,"isRepack":false}}`,
		},
		{
			name: "radarr symlink", arrType: Radarr, path: "/downloads/Movie/Movie.mkv",
			identity: `"movie":{"id":42}`,
			quality:  `{"quality":{"id":31,"name":"Remux-2160p","source":"bluray","resolution":2160,"modifier":"remux"},"revision":{"version":2,"real":1,"isRepack":true}}`,
		},
		{
			name: "sonarr episodes", arrType: Sonarr, path: "/downloads/Show/Show.S01E01-E02.mkv",
			identity: `"series":{"id":7},"seasonNumber":1,"episodes":[{"id":11},{"id":12}],"releaseType":"singleEpisode"`,
			quality:  `{"quality":{"id":3,"name":"WEBDL-1080p","source":"web","resolution":1080},"revision":{"version":1,"real":0,"isRepack":false}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wantQuality any
			if err := json.Unmarshal([]byte(tc.quality), &wantQuality); err != nil {
				t.Fatal(err)
			}
			var posts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != "secret" {
					t.Error("missing Arr API key")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v3/manualimport":
					if got := r.URL.Query().Get("downloadId"); got != "download-1" {
						t.Errorf("downloadId = %q, want download-1", got)
					}
					_, _ = fmt.Fprintf(w, `[{"path":%q,"folderName":"Release",%s,"quality":%s,"languages":[{"id":1,"name":"English"}],"releaseGroup":"Group","indexerFlags":4}]`, tc.path, tc.identity, tc.quality)
				case "POST /api/v3/command":
					posts.Add(1)
					var command struct {
						Name       string           `json:"name"`
						ImportMode string           `json:"importMode"`
						Files      []map[string]any `json:"files"`
					}
					if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
						t.Error(err)
						http.Error(w, "invalid command", http.StatusBadRequest)
						return
					}
					if command.Name != "ManualImport" || command.ImportMode != "copy" || len(command.Files) != 1 {
						t.Errorf("command = %+v", command)
						http.Error(w, "invalid command", http.StatusBadRequest)
						return
					}
					file := command.Files[0]
					if got := file["quality"]; !reflect.DeepEqual(got, wantQuality) {
						t.Errorf("quality = %#v, want %#v", got, wantQuality)
					}
					if file["downloadId"] != "download-1" || file["path"] != tc.path || file["folderName"] != "Release" || file["releaseGroup"] != "Group" || file["indexerFlags"] != float64(4) {
						t.Errorf("import file = %#v", file)
					}
					wantLanguages := []any{map[string]any{"id": float64(1), "name": "English"}}
					if !reflect.DeepEqual(file["languages"], wantLanguages) {
						t.Errorf("languages = %#v, want %#v", file["languages"], wantLanguages)
					}
					if tc.arrType == Radarr {
						if file["movieId"] != float64(42) {
							t.Errorf("movieId = %v, want 42", file["movieId"])
						}
						for _, key := range []string{"seriesId", "seasonNumber", "episodeIds", "releaseType"} {
							if _, exists := file[key]; exists {
								t.Errorf("Radarr file contains Sonarr field %q", key)
							}
						}
					} else {
						if file["seriesId"] != float64(7) || !reflect.DeepEqual(file["episodeIds"], []any{float64(11), float64(12)}) || file["releaseType"] != "singleEpisode" {
							t.Errorf("Sonarr identity = %#v", file)
						}
						if _, exists := file["movieId"]; exists {
							t.Error("Sonarr file contains movieId")
						}
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, `{"id":31,"status":"queued"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			s := testService(Arr{Host: server.URL, Token: "secret", Type: tc.arrType})
			if err := s.ManualImport(t.Context(), "arr", "download-1"); err != nil {
				t.Fatal(err)
			}
			if got := posts.Load(); got != 1 {
				t.Fatalf("command submissions = %d, want 1", got)
			}
		})
	}
}

func TestManualImportRejectsIncompleteCandidates(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantError string
	}{
		{"no files", `[]`, "no files found"},
		{"missing movie", `[{"path":"/downloads/Movie.strm","quality":{"quality":{"id":3}}}]`, "no movie matched"},
		{"null movie", `[{"path":"/downloads/Movie.strm","movie":null,"quality":{"quality":{"id":3}}}]`, "no movie matched"},
		{"missing quality", `[{"path":"/downloads/Movie.strm","movie":{"id":42}}]`, "no quality returned"},
		{"null quality", `[{"path":"/downloads/Movie.strm","movie":{"id":42},"quality":null}]`, "no quality returned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v3/manualimport":
					_, _ = fmt.Fprint(w, tc.response)
				case "POST /api/v3/command":
					posts.Add(1)
					w.WriteHeader(http.StatusCreated)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			s := testService(Arr{Host: server.URL, Token: "secret", Type: Radarr})
			err := s.ManualImport(t.Context(), "arr", "download-1")
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("ManualImport error = %v, want %q", err, tc.wantError)
			}
			if got := posts.Load(); got != 0 {
				t.Fatalf("command submissions = %d, want 0", got)
			}
		})
	}
}
