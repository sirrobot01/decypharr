package arr

import (
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchAndGrabMovieRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/api/v3/release" || r.URL.Query().Get("movieId") != "19" {
				t.Errorf("release query = %s", r.URL.String())
			}
			_, _ = fmt.Fprint(w, `[{"guid":"release-guid","title":"Movie.Release","indexer":"Indexer","indexerId":7,"downloadAllowed":true,"unknownField":"preserved"}]`)
		case http.MethodPost:
			if r.URL.Path != "/api/v3/release" {
				t.Errorf("release path = %s", r.URL.Path)
			}
			var body map[string]any
			if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			if body["unknownField"] != "preserved" || body["guid"] != "release-guid" {
				t.Errorf("grab payload = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Radarr})
	releases, err := s.MovieReleases(t.Context(), "arr", 19)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || !releases[0].DownloadAllowed {
		t.Fatalf("releases = %#v", releases)
	}
	if err := s.GrabRelease(t.Context(), "arr", releases[0]); err != nil {
		t.Fatal(err)
	}
}
