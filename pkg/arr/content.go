package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sync/errgroup"
	"net/http"
	"strconv"
	"strings"
)

type episode struct {
	Id            int `json:"id"`
	EpisodeFileID int `json:"episodeFileId"`
}

type sonarrSearch struct {
	Name         string `json:"name"`
	SeasonNumber int    `json:"seasonNumber"`
	SeriesId     int    `json:"seriesId"`
}

type radarrSearch struct {
	Name     string `json:"name"`
	MovieIds []int  `json:"movieIds"`
}

func (a *Arr) GetMedia(mediaId string) ([]Content, error) {
	// Get series
	if a.Type == Radarr {
		return GetMovies(a, mediaId)
	}
	// This is likely Sonarr
	resp, err := a.Request(http.MethodGet, fmt.Sprintf("api/v3/series?tvdbId=%s", mediaId), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// This is likely Radarr
		return GetMovies(a, mediaId)
	}
	a.Type = Sonarr

	type series struct {
		Title string `json:"title"`
		Id    int    `json:"id"`
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get series: %s", resp.Status)
	}
	var data []series
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode series: %v", err)
	}
	// Get series files
	contents := make([]Content, 0)
	for _, d := range data {
		resp, err = a.Request(http.MethodGet, fmt.Sprintf("api/v3/episodefile?seriesId=%d", d.Id), nil)
		if err != nil {
			continue
		}
		var ct Content
		var seriesFiles []seriesFile
		episodeFileIDMap := make(map[int]int)
		func() {
			defer resp.Body.Close()
			if err = json.NewDecoder(resp.Body).Decode(&seriesFiles); err != nil {
				return
			}
			ct = Content{
				Title: d.Title,
				Id:    d.Id,
			}
		}()
		resp, err = a.Request(http.MethodGet, fmt.Sprintf("api/v3/episode?seriesId=%d", d.Id), nil)
		if err != nil {
			continue
		}
		func() {
			defer resp.Body.Close()
			var episodes []episode
			if err = json.NewDecoder(resp.Body).Decode(&episodes); err != nil {
				return
			}

			for _, e := range episodes {
				episodeFileIDMap[e.EpisodeFileID] = e.Id
			}
		}()
		files := make([]ContentFile, 0)
		for _, file := range seriesFiles {
			eId, ok := episodeFileIDMap[file.Id]
			if !ok {
				eId = 0
			}
			if file.Id == 0 || file.Path == "" {
				// Skip files without path
				continue
			}
			files = append(files, ContentFile{
				FileId:       file.Id,
				Path:         file.Path,
				Id:           d.Id,
				EpisodeId:    eId,
				SeasonNumber: file.SeasonNumber,
				Size:         file.Size,
			})
		}
		if len(files) == 0 {
			// Skip series without files
			continue
		}
		ct.Files = files
		contents = append(contents, ct)
	}
	return contents, nil
}

func GetMovies(a *Arr, tvId string) ([]Content, error) {
	resp, err := a.Request(http.MethodGet, fmt.Sprintf("api/v3/movie?tmdbId=%s", tvId), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		// This is likely Lidarr or Readarr
		return nil, fmt.Errorf("failed to get movies: %s", resp.Status)
	}
	a.Type = Radarr
	defer resp.Body.Close()
	var movies []Movie
	if err = json.NewDecoder(resp.Body).Decode(&movies); err != nil {
		return nil, fmt.Errorf("failed to decode movies: %v", err)
	}
	contents := make([]Content, 0)
	for _, movie := range movies {
		if movie.MovieFile.Id == 0 || movie.MovieFile.Path == "" {
			// Skip movies without files
			continue
		}
		ct := Content{
			Title: movie.Title,
			Id:    movie.Id,
		}
		files := make([]ContentFile, 0)

		files = append(files, ContentFile{
			FileId: movie.MovieFile.Id,
			Id:     movie.Id,
			Path:   movie.MovieFile.Path,
			Size:   movie.MovieFile.Size,
		})
		ct.Files = files
		contents = append(contents, ct)
	}
	return contents, nil
}

// searchSonarr searches for missing files in the arr
// map ids are series id and season number
func (a *Arr) searchSonarr(files []ContentFile) error {
	ids := make(map[string]any)
	for _, f := range files {
		// Join series id and season number
		id := fmt.Sprintf("%d-%d", f.Id, f.SeasonNumber)
		ids[id] = nil
	}

	g, ctx := errgroup.WithContext(context.Background())

	// Limit concurrent goroutines
	g.SetLimit(10)
	for id := range ids {
		id := id
		g.Go(func() error {

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			parts := strings.Split(id, "-")
			if len(parts) != 2 {
				return fmt.Errorf("invalid id: %s", id)
			}
			seriesId, err := strconv.Atoi(parts[0])
			if err != nil {
				return err
			}
			seasonNumber, err := strconv.Atoi(parts[1])
			if err != nil {
				return err
			}
			payload := sonarrSearch{
				Name:         "SeasonSearch",
				SeasonNumber: seasonNumber,
				SeriesId:     seriesId,
			}
			resp, err := a.Request(http.MethodPost, "api/v3/command", payload)
			if err != nil {
				return fmt.Errorf("failed to automatic search: %v", err)
			}
			if resp.StatusCode >= 300 || resp.StatusCode < 200 {
				return fmt.Errorf("failed to automatic search. Status Code: %s", resp.Status)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return nil
}

func (a *Arr) searchRadarr(files []ContentFile) error {
	ids := make([]int, 0)
	for _, f := range files {
		ids = append(ids, f.Id)
	}
	payload := radarrSearch{
		Name:     "MoviesSearch",
		MovieIds: ids,
	}
	resp, err := a.Request(http.MethodPost, "api/v3/command", payload)
	if err != nil {
		return fmt.Errorf("failed to automatic search: %v", err)
	}
	if statusOk := strconv.Itoa(resp.StatusCode)[0] == '2'; !statusOk {
		return fmt.Errorf("failed to automatic search. Status Code: %s", resp.Status)
	}
	return nil
}

func (a *Arr) SearchMissing(files []ContentFile) error {
	switch a.Type {
	case Sonarr:
		return a.searchSonarr(files)
	case Radarr:
		return a.searchRadarr(files)
	default:
		return fmt.Errorf("unknown arr type: %s", a.Type)
	}
}

func (a *Arr) DeleteFiles(files []ContentFile) error {
	ids := make([]int, 0)
	for _, f := range files {
		ids = append(ids, f.FileId)
	}
	defer func() {
		// Delete files, or at least try
		for _, f := range files {
			f.Delete()
		}
	}()
	var payload interface{}
	switch a.Type {
	case Sonarr:
		payload = struct {
			EpisodeFileIds []int `json:"episodeFileIds"`
		}{
			EpisodeFileIds: ids,
		}
		_, err := a.Request(http.MethodDelete, "api/v3/episodefile/bulk", payload)
		if err != nil {
			return err
		}
	case Radarr:
		payload = struct {
			MovieFileIds []int `json:"movieFileIds"`
		}{
			MovieFileIds: ids,
		}
		_, err := a.Request(http.MethodDelete, "api/v3/moviefile/bulk", payload)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown arr type: %s", a.Type)
	}
	return nil
}
