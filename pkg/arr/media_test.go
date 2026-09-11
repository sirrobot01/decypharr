package arr

import (
	stdjson "encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDeleteManagedFile(t *testing.T) {
	for _, test := range []struct {
		name       string
		arrType    Type
		path       string
		statusCode int
		wantErr    bool
	}{
		{name: "sonarr", arrType: Sonarr, path: "/api/v3/episodefile/17", statusCode: http.StatusNoContent},
		{name: "radarr missing is idempotent", arrType: Radarr, path: "/api/v3/moviefile/17", statusCode: http.StatusNotFound},
		{name: "failure", arrType: Radarr, path: "/api/v3/moviefile/17", statusCode: http.StatusBadRequest, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != test.path {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(test.statusCode)
			}))
			defer server.Close()

			s := testService(Arr{Host: server.URL, Token: "secret", Type: test.arrType})
			err := s.DeleteLibraryFile(t.Context(), "arr", 17)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestGetDownloadClientConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/config/downloadclient" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"id":1,"enableCompletedDownloadHandling":true,"autoRedownloadFailed":false,"autoRedownloadFailedFromInteractiveSearch":true}`)
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	config, err := s.DownloadClientConfig(t.Context(), "arr")
	if err != nil {
		t.Fatal(err)
	}
	if config.AutoRedownloadFailed || !config.AutoRedownloadFailedFromInteractiveSearch || !config.EnableCompletedDownloadHandling {
		t.Fatalf("config = %#v", config)
	}
}

func TestExplicitSearchCommands(t *testing.T) {
	for _, test := range []struct {
		name     string
		arrType  Type
		wantBody map[string]any
		run      func(service *Service) (Command, error)
	}{
		{
			name:     "episodes",
			arrType:  Sonarr,
			wantBody: map[string]any{"name": "EpisodeSearch", "episodeIds": []any{float64(4), float64(9)}},
			run: func(s *Service) (Command, error) {
				return s.SearchEpisodes(t.Context(), "arr", []int{9, 4, 9})
			},
		},
		{
			name:     "season",
			arrType:  Sonarr,
			wantBody: map[string]any{"name": "SeasonSearch", "seriesId": float64(8), "seasonNumber": float64(0)},
			run: func(s *Service) (Command, error) {
				return s.SearchSeason(t.Context(), "arr", 8, 0)
			},
		},
		{
			name:     "movies",
			arrType:  Radarr,
			wantBody: map[string]any{"name": "MoviesSearch", "movieIds": []any{float64(3), float64(7)}},
			run: func(s *Service) (Command, error) {
				return s.SearchMovies(t.Context(), "arr", []int{7, 3})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v3/command" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				if !reflect.DeepEqual(body, test.wantBody) {
					t.Errorf("body = %#v, want %#v", body, test.wantBody)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprint(w, `{"id":31,"status":"queued"}`)
			}))
			defer server.Close()

			s := testService(Arr{Host: server.URL, Token: "secret", Type: test.arrType})
			command, err := test.run(s)
			if err != nil {
				t.Fatal(err)
			}
			if command.ID != 31 {
				t.Fatalf("command = %#v", command)
			}
		})
	}
}

func TestMutationRequestClassifiesOnlyPossiblyDispatchedErrorsAsUnknown(t *testing.T) {
	unconfigured := testService(Arr{Type: Radarr})
	_, err := unconfigured.SearchMovies(t.Context(), "arr", []int{7})
	if err == nil || errors.Is(err, ErrMutationOutcomeUnknown) {
		t.Fatalf("preflight error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host := server.URL
	server.Close()
	unreachable := testService(Arr{Host: host, Token: "secret", Type: Radarr})
	_, err = unreachable.SearchMovies(t.Context(), "arr", []int{7})
	if !errors.Is(err, ErrMutationOutcomeUnknown) {
		t.Fatalf("transport error = %v, want unknown outcome", err)
	}
}
