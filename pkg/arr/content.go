package arr

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"

	"golang.org/x/sync/errgroup"
)

// contentBatchSize bounds one bulk delete or search, which Arr applies in a
// single database transaction.
const contentBatchSize = 50

type Content struct {
	Title string        `json:"title"`
	Id    int           `json:"id"`
	Files []ContentFile `json:"files"`
}

type ContentFile struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Id           int    `json:"id"`
	EpisodeId    int    `json:"showId"`
	FileId       int    `json:"fileId"`
	TargetPath   string `json:"targetPath"`
	EntryName    string `json:"entryName,omitempty"`
	IsSymlink    bool   `json:"isSymlink"`
	IsBroken     bool   `json:"isBroken"`
	SeasonNumber int    `json:"seasonNumber"`
	Processed    bool   `json:"processed"`
	Size         int64  `json:"size"`
}

// Delete removes the file from disk. Sonarr's bulk delete regularly fails to,
// which leaves a broken symlink behind.
func (f *ContentFile) Delete() {
	_ = os.Remove(f.Path)
}

type Movie struct {
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle"`
	Path          string `json:"path"`
	MovieFile     struct {
		MovieId      int    `json:"movieId"`
		RelativePath string `json:"relativePath"`
		Path         string `json:"path"`
		Id           int    `json:"id"`
		Size         int64  `json:"size"`
	} `json:"movieFile"`
	Id int `json:"id"`
}

// Media enumerates the library as repair sees it: one Content per series or
// movie, with the files it has imported. An empty mediaID returns everything.
func (s *Service) Media(ctx context.Context, name, mediaID string) ([]Content, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}
	if instance.Type == Radarr {
		return s.movies(ctx, instance, mediaID)
	}

	var series []struct {
		Title string `json:"title"`
		Id    int    `json:"id"`
	}
	resp, err := s.get(ctx, instance, fmt.Sprintf("api/v3/series?tvdbId=%s", mediaID), &series)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return s.movies(ctx, instance, mediaID)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}

	contents := make([]Content, 0, len(series))
	for _, item := range series {
		if err := ctx.Err(); err != nil {
			return contents, err
		}
		files, err := s.sonarrSeriesFiles(ctx, instance, item.Id)
		if err != nil {
			continue
		}
		content := Content{Title: item.Title, Id: item.Id, Files: make([]ContentFile, 0, len(files))}
		for _, file := range files {
			episodeID := 0
			if len(file.EpisodeIDs) > 0 {
				episodeID = file.EpisodeIDs[0]
			}
			content.Files = append(content.Files, ContentFile{
				FileId:       file.ArrFileID,
				Path:         file.Path,
				Id:           item.Id,
				EpisodeId:    episodeID,
				SeasonNumber: file.SeasonNumber,
				Size:         file.Size,
			})
		}
		if len(content.Files) == 0 {
			continue
		}
		contents = append(contents, content)
	}
	return contents, nil
}

func (s *Service) movies(ctx context.Context, instance Arr, mediaID string) ([]Content, error) {
	var movies []Movie
	resp, err := s.get(ctx, instance, fmt.Sprintf("api/v3/movie?tmdbId=%s", mediaID), &movies)
	if err != nil {
		return nil, err
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list movies: %w", err)
	}

	contents := make([]Content, 0, len(movies))
	for _, movie := range movies {
		if movie.MovieFile.Id == 0 || movie.MovieFile.Path == "" {
			continue
		}
		contents = append(contents, Content{
			Title: movie.Title,
			Id:    movie.Id,
			Files: []ContentFile{{
				FileId: movie.MovieFile.Id,
				Id:     movie.Id,
				Path:   movie.MovieFile.Path,
				Size:   movie.MovieFile.Size,
			}},
		})
	}
	return contents, nil
}

// SearchMissing asks the Arr to look for replacements for the given files.
func (s *Service) SearchMissing(ctx context.Context, name string, files []ContentFile) error {
	instance, err := s.instance(name)
	if err != nil || len(files) == 0 {
		return err
	}
	for batch := range slices.Chunk(files, contentBatchSize) {
		switch instance.Type {
		case Sonarr:
			err = s.searchSonarrSeasons(ctx, instance, batch)
		case Radarr:
			err = s.searchRadarrMovies(ctx, instance, batch)
		default:
			return fmt.Errorf("%w: %s", ErrUnsupportedType, instance.Type)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) searchSonarrSeasons(ctx context.Context, instance Arr, files []ContentFile) error {
	seasons := make(map[[2]int]struct{}, len(files))
	for _, file := range files {
		seasons[[2]int{file.Id, file.SeasonNumber}] = struct{}{}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	// Each command writes to Sonarr's command table, and its single SQLite
	// writer turns parallel posts into "database is locked" failures.
	group.SetLimit(1)
	for season := range seasons {
		group.Go(func() error {
			_, err := s.command(groupCtx, instance, struct {
				Name         string `json:"name"`
				SeriesId     int    `json:"seriesId"`
				SeasonNumber int    `json:"seasonNumber"`
			}{Name: "SeasonSearch", SeriesId: season[0], SeasonNumber: season[1]})
			return err
		})
	}
	return group.Wait()
}

func (s *Service) searchRadarrMovies(ctx context.Context, instance Arr, files []ContentFile) error {
	ids := make([]int, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.Id)
	}
	_, err := s.command(ctx, instance, struct {
		Name     string `json:"name"`
		MovieIds []int  `json:"movieIds"`
	}{Name: "MoviesSearch", MovieIds: ids})
	return err
}

// DeleteFiles removes imported files in bulk, then removes whatever the Arr
// left on disk.
func (s *Service) DeleteFiles(ctx context.Context, name string, files []ContentFile) error {
	instance, err := s.instance(name)
	if err != nil || len(files) == 0 {
		return err
	}
	resource, err := fileResource(instance.Type)
	if err != nil {
		return err
	}
	field := map[Type]string{Sonarr: "episodeFileIds", Radarr: "movieFileIds"}[instance.Type]

	for batch := range slices.Chunk(files, contentBatchSize) {
		ids := make([]int, 0, len(batch))
		for _, file := range batch {
			// Sonarr rejects the whole batch when an ID repeats.
			if file.FileId != 0 && !slices.Contains(ids, file.FileId) {
				ids = append(ids, file.FileId)
			}
		}
		if len(ids) == 0 {
			continue
		}
		resp, err := s.mutate(ctx, instance, http.MethodDelete, "api/v3/"+resource+"/bulk", map[string][]int{field: ids}, nil)
		if err != nil {
			return fmt.Errorf("delete %s bulk: %w", resource, err)
		}
		if err := expectSuccess(resp); err != nil {
			return fmt.Errorf("delete %s bulk: %w", resource, err)
		}
		for i := range batch {
			batch[i].Delete()
		}
	}
	return nil
}
