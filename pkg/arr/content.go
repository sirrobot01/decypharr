package arr

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
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

func (a *Arr) GetMedia(ctx context.Context, mediaId string) ([]Content, error) {
	// GetReader series
	type series struct {
		Title string `json:"title"`
		Id    int    `json:"id"`
	}
	var data []series
	if a.Type == Radarr {
		return a.GetMovies(ctx, mediaId)
	}
	// This is likely Sonarr
	resp, err := a.RequestCtx(ctx, http.MethodGet, fmt.Sprintf("api/v3/series?tvdbId=%s", mediaId), nil, &data)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		// This is likely Radarr
		return a.GetMovies(ctx, mediaId)
	}
	a.Type = Sonarr

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get series: %s", resp.Status)
	}
	// GetReader series files
	contents := make([]Content, 0)
	var seriesFiles []seriesFile
	for _, d := range data {
		if ctx != nil && ctx.Err() != nil {
			return contents, ctx.Err()
		}
		_, err = a.RequestCtx(ctx, http.MethodGet, fmt.Sprintf("api/v3/episodefile?seriesId=%d", d.Id), nil, &seriesFiles)
		if err != nil {
			continue
		}
		var ct Content

		episodeFileIDMap := make(map[int]int)
		ct = Content{
			Title: d.Title,
			Id:    d.Id,
		}
		var episodes []episode
		_, err = a.RequestCtx(ctx, http.MethodGet, fmt.Sprintf("api/v3/episode?seriesId=%d", d.Id), nil, &episodes)
		if err != nil {
			continue
		}
		for _, e := range episodes {
			episodeFileIDMap[e.EpisodeFileID] = e.Id
		}
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

func (a *Arr) GetMovies(ctx context.Context, tvId string) ([]Content, error) {
	var movies []Movie
	resp, err := a.RequestCtx(ctx, http.MethodGet, fmt.Sprintf("api/v3/movie?tmdbId=%s", tvId), nil, &movies)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		// This is likely Lidarr or Readarr
		return nil, fmt.Errorf("failed to get movies: %s", resp.Status)
	}
	a.Type = Radarr
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
func (a *Arr) searchSonarr(ctx context.Context, files []ContentFile) error {
	ids := make(map[string]any)
	for _, f := range files {
		// Join series id and season number
		id := fmt.Sprintf("%d-%d", f.Id, f.SeasonNumber)
		ids[id] = nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	g, gctx := errgroup.WithContext(ctx)

	// Limit concurrent goroutines
	g.SetLimit(10)
	for id := range ids {
		g.Go(func() error {
			select {
			case <-gctx.Done():
				return gctx.Err()
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
			resp, err := a.RequestCtx(gctx, http.MethodPost, "api/v3/command", payload, nil)
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

func (a *Arr) searchRadarr(ctx context.Context, files []ContentFile) error {
	ids := make([]int, 0)
	for _, f := range files {
		ids = append(ids, f.Id)
	}
	payload := radarrSearch{
		Name:     "MoviesSearch",
		MovieIds: ids,
	}
	resp, err := a.RequestCtx(ctx, http.MethodPost, "api/v3/command", payload, nil)
	if err != nil {
		return fmt.Errorf("failed to automatic search: %v", err)
	}
	if statusOk := strconv.Itoa(resp.StatusCode)[0] == '2'; !statusOk {
		return fmt.Errorf("failed to automatic search. Status Code: %s", resp.Status)
	}
	return nil
}

func (a *Arr) SearchMissing(ctx context.Context, files []ContentFile) error {
	if len(files) == 0 {
		return nil
	}
	return a.batchSearchMissing(ctx, files)
}

func (a *Arr) batchSearchMissing(ctx context.Context, files []ContentFile) error {
	if len(files) == 0 {
		return nil
	}
	BatchSize := 50
	if len(files) > BatchSize {
		for i := 0; i < len(files); i += BatchSize {
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			end := i + BatchSize
			if end > len(files) {
				end = len(files)
			}
			if err := a.searchMissing(ctx, files[i:end]); err != nil {
				continue
			}
		}
		return nil
	}
	return a.searchMissing(ctx, files)
}

func (a *Arr) searchMissing(ctx context.Context, files []ContentFile) error {
	switch a.Type {
	case Sonarr:
		return a.searchSonarr(ctx, files)
	case Radarr:
		return a.searchRadarr(ctx, files)
	default:
		return fmt.Errorf("unknown arr type: %s", a.Type)
	}
}

func (a *Arr) DeleteFiles(ctx context.Context, files []ContentFile) error {
	if len(files) == 0 {
		return nil
	}
	BatchSize := 50
	if len(files) > BatchSize {
		for i := 0; i < len(files); i += BatchSize {
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			end := i + BatchSize
			if end > len(files) {
				end = len(files)
			}
			if err := a.batchDeleteFiles(ctx, files[i:end]); err != nil {
				continue
			}
		}
		return nil
	}
	return a.batchDeleteFiles(ctx, files)
}

func (a *Arr) batchDeleteFiles(ctx context.Context, files []ContentFile) error {
	ids := make([]int, 0)
	for _, f := range files {
		ids = append(ids, f.FileId)
	}
	defer func() {
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
		_, err := a.RequestCtx(ctx, http.MethodDelete, "api/v3/episodefile/bulk", payload, nil)
		if err != nil {
			return err
		}
	case Radarr:
		payload = struct {
			MovieFileIds []int `json:"movieFileIds"`
		}{
			MovieFileIds: ids,
		}
		_, err := a.RequestCtx(ctx, http.MethodDelete, "api/v3/moviefile/bulk", payload, nil)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown arr type: %s", a.Type)
	}
	return nil
}
