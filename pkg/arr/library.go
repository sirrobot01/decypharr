package arr

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/sirrobot01/decypharr/internal/request"
	"golang.org/x/sync/errgroup"
)

const sonarrLibraryConcurrency = 4

// LibraryFile is the Arr-side identity of an imported media file.
type LibraryFile struct {
	ArrFileID    int    `json:"arr_file_id"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	SeriesID     int    `json:"series_id,omitempty"`
	SeasonNumber int    `json:"season_number,omitempty"`
	EpisodeIDs   []int  `json:"episode_ids,omitempty"`
	MovieID      int    `json:"movie_id,omitempty"`
}

type sonarrSeries struct {
	ID int `json:"id"`
}

type sonarrEpisodeFile struct {
	ID           int    `json:"id"`
	SeriesID     int    `json:"seriesId"`
	SeasonNumber int    `json:"seasonNumber"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
}

type sonarrEpisode struct {
	ID            int `json:"id"`
	EpisodeFileID int `json:"episodeFileId"`
}

type radarrMovie struct {
	ID        int `json:"id"`
	MovieFile struct {
		ID      int    `json:"id"`
		MovieID int    `json:"movieId"`
		Path    string `json:"path"`
		Size    int64  `json:"size"`
	} `json:"movieFile"`
}

func arrayResponseDecoder[T any](visit func(T) error) responseDecoder {
	return func(resp *http.Response) error {
		return request.DecodeJSONArray(resp, visit)
	}
}

// LibraryFiles enumerates every imported file an instance owns.
func (s *Service) LibraryFiles(ctx context.Context, name string) ([]LibraryFile, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}
	switch instance.Type {
	case Sonarr:
		return s.sonarrLibraryFiles(ctx, instance)
	case Radarr:
		return s.radarrLibraryFiles(ctx, instance)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, instance.Type)
	}
}

func (s *Service) sonarrLibraryFiles(ctx context.Context, instance Arr) ([]LibraryFile, error) {
	var series []sonarrSeries
	resp, err := s.getDecoded(ctx, instance, "api/v3/series", arrayResponseDecoder(func(item sonarrSeries) error {
		series = append(series, item)
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("list sonarr series: %w", err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list sonarr series: %w", err)
	}
	if len(series) == 0 {
		return nil, nil
	}

	bySeries := make([][]LibraryFile, len(series))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(sonarrLibraryConcurrency)
	for i, item := range series {
		group.Go(func() error {
			files, err := s.sonarrSeriesFiles(groupCtx, instance, item.ID)
			if err != nil {
				return err
			}
			bySeries[i] = files
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return slices.Concat(bySeries...), nil
}

func (s *Service) sonarrSeriesFiles(ctx context.Context, instance Arr, seriesID int) ([]LibraryFile, error) {
	var episodeFiles []sonarrEpisodeFile
	endpoint := fmt.Sprintf("api/v3/episodefile?seriesId=%d", seriesID)
	resp, err := s.getDecoded(ctx, instance, endpoint, arrayResponseDecoder(func(file sonarrEpisodeFile) error {
		episodeFiles = append(episodeFiles, file)
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("list episode files for series %d: %w", seriesID, err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list episode files for series %d: %w", seriesID, err)
	}

	episodesByFile, err := s.sonarrEpisodeIDs(ctx, instance, seriesID)
	if err != nil {
		return nil, err
	}

	files := make([]LibraryFile, 0, len(episodeFiles))
	for _, file := range episodeFiles {
		if file.ID <= 0 || file.Path == "" {
			continue
		}
		files = append(files, LibraryFile{
			ArrFileID:    file.ID,
			Path:         file.Path,
			Size:         file.Size,
			SeriesID:     cmp.Or(file.SeriesID, seriesID),
			SeasonNumber: file.SeasonNumber,
			EpisodeIDs:   episodesByFile[file.ID],
		})
	}
	return files, nil
}

func (s *Service) sonarrEpisodeIDs(ctx context.Context, instance Arr, seriesID int) (map[int][]int, error) {
	byFile := make(map[int][]int)
	endpoint := fmt.Sprintf("api/v3/episode?seriesId=%d", seriesID)
	resp, err := s.getDecoded(ctx, instance, endpoint, arrayResponseDecoder(func(episode sonarrEpisode) error {
		if episode.ID > 0 && episode.EpisodeFileID > 0 {
			byFile[episode.EpisodeFileID] = append(byFile[episode.EpisodeFileID], episode.ID)
		}
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("list episodes for series %d: %w", seriesID, err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list episodes for series %d: %w", seriesID, err)
	}

	for fileID, ids := range byFile {
		slices.Sort(ids)
		byFile[fileID] = slices.Compact(ids)
	}
	return byFile, nil
}

func (s *Service) radarrLibraryFiles(ctx context.Context, instance Arr) ([]LibraryFile, error) {
	var files []LibraryFile
	resp, err := s.getDecoded(ctx, instance, "api/v3/movie", arrayResponseDecoder(func(movie radarrMovie) error {
		if file, ok := movieLibraryFile(movie); ok {
			files = append(files, file)
		}
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("list radarr movies: %w", err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list radarr movies: %w", err)
	}

	return files, nil
}

// movieLibraryFile reads the imported file off a Radarr movie. It reports false
// when the movie has none.
func movieLibraryFile(movie radarrMovie) (LibraryFile, bool) {
	if movie.MovieFile.ID <= 0 || movie.MovieFile.Path == "" {
		return LibraryFile{}, false
	}
	return LibraryFile{
		ArrFileID: movie.MovieFile.ID,
		Path:      movie.MovieFile.Path,
		Size:      movie.MovieFile.Size,
		MovieID:   cmp.Or(movie.MovieFile.MovieID, movie.ID),
	}, true
}

// radarrMovieFiles reads the imported file of one movie. A movie the Arr no
// longer has contributes nothing.
func (s *Service) radarrMovieFiles(ctx context.Context, instance Arr, movieID int) ([]LibraryFile, error) {
	var movie radarrMovie
	endpoint := fmt.Sprintf("api/v3/movie/%d", movieID)
	resp, err := s.get(ctx, instance, endpoint, &movie)
	if err != nil {
		return nil, fmt.Errorf("get radarr movie %d: %w", movieID, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get radarr movie %d: %w", movieID, err)
	}
	if file, ok := movieLibraryFile(movie); ok {
		return []LibraryFile{file}, nil
	}
	return nil, nil
}

// LibraryFilesForMedia enumerates the imported files of specific series or
// movies. A targeted index only ever matches against the media an entry was
// imported into, and a full Sonarr scan costs two requests per series in the
// library — repeated on every backoff attempt.
func (s *Service) LibraryFilesForMedia(ctx context.Context, name string, mediaIDs []int) ([]LibraryFile, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}
	ids := slices.Clone(mediaIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	ids = slices.DeleteFunc(ids, func(id int) bool { return id <= 0 })
	if len(ids) == 0 {
		return nil, nil
	}

	var read func(context.Context, int) ([]LibraryFile, error)
	switch instance.Type {
	case Sonarr:
		read = func(ctx context.Context, id int) ([]LibraryFile, error) {
			return s.sonarrSeriesFiles(ctx, instance, id)
		}
	case Radarr:
		read = func(ctx context.Context, id int) ([]LibraryFile, error) {
			return s.radarrMovieFiles(ctx, instance, id)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, instance.Type)
	}

	byMedia := make([][]LibraryFile, len(ids))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(sonarrLibraryConcurrency)
	for i, id := range ids {
		group.Go(func() error {
			files, err := read(groupCtx, id)
			if err != nil {
				return err
			}
			byMedia[i] = files
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return slices.Concat(byMedia...), nil
}
