package arr

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

func TestListSonarrLibraryFilesPreservesMultiEpisodeFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series":
			_, _ = fmt.Fprint(w, `[{"id":4}]`)
		case "/api/v3/episodefile":
			if r.URL.Query().Get("seriesId") != "4" {
				t.Errorf("seriesId = %q", r.URL.Query().Get("seriesId"))
			}
			_, _ = fmt.Fprint(w, `[{"id":31,"seriesId":4,"seasonNumber":2,"path":"/shows/file.mkv","size":1000}]`)
		case "/api/v3/episode":
			_, _ = fmt.Fprint(w, `[{"id":12,"episodeFileId":31},{"id":11,"episodeFileId":31}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	files, err := s.LibraryFiles(t.Context(), "arr")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !reflect.DeepEqual(files[0].EpisodeIDs, []int{11, 12}) {
		t.Fatalf("files = %#v", files)
	}
}

func TestListRadarrLibraryFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `[{"id":8,"movieFile":{"id":42,"movieId":8,"path":"/movies/file.mkv","size":2000}}]`)
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Radarr})
	files, err := s.LibraryFiles(t.Context(), "arr")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ArrFileID != 42 || files[0].MovieID != 8 {
		t.Fatalf("files = %#v", files)
	}
}

func TestLibraryFilesForMediaReadsOnlyTheNamedSeries(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v3/episodefile":
			if got := r.URL.Query().Get("seriesId"); got != "4" {
				t.Errorf("seriesId = %q", got)
			}
			_, _ = fmt.Fprint(w, `[{"id":31,"seriesId":4,"seasonNumber":2,"path":"/shows/file.mkv","size":1000}]`)
		case "/api/v3/episode":
			_, _ = fmt.Fprint(w, `[{"id":12,"episodeFileId":31}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	files, err := s.LibraryFilesForMedia(t.Context(), "arr", []int{4, 4, 0, -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ArrFileID != 31 {
		t.Fatalf("files = %#v", files)
	}
	if slices.Contains(paths, "/api/v3/series") {
		t.Fatalf("listed the whole library: %v", paths)
	}
	if len(paths) != 2 {
		t.Fatalf("requests = %v, want one per endpoint for a single series", paths)
	}
}

func TestLibraryFilesForMediaReadsOneMovie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/8":
			_, _ = fmt.Fprint(w, `{"id":8,"movieFile":{"id":42,"movieId":8,"path":"/movies/file.mkv","size":2000}}`)
		case "/api/v3/movie/9":
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Radarr})
	files, err := s.LibraryFilesForMedia(t.Context(), "arr", []int{8, 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ArrFileID != 42 || files[0].MovieID != 8 {
		t.Fatalf("files = %#v", files)
	}
}

func TestLibraryFilesForMediaWithoutIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %q", r.URL.Path)
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	files, err := s.LibraryFilesForMedia(t.Context(), "arr", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %#v", files)
	}
}
