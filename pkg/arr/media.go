package arr

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"
)

type DownloadClientConfig struct {
	ID                                        int  `json:"id"`
	EnableCompletedDownloadHandling           bool `json:"enableCompletedDownloadHandling"`
	AutoRedownloadFailed                      bool `json:"autoRedownloadFailed"`
	AutoRedownloadFailedFromInteractiveSearch bool `json:"autoRedownloadFailedFromInteractiveSearch"`
}

type Command struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	CommandName string      `json:"commandName"`
	Status      string      `json:"status"`
	Queued      time.Time   `json:"queued,omitzero"`
	Body        CommandBody `json:"body"`
}

type CommandBody struct {
	Name         string `json:"name"`
	EpisodeIDs   []int  `json:"episodeIds,omitempty"`
	SeriesID     int    `json:"seriesId,omitzero"`
	SeasonNumber int    `json:"seasonNumber,omitzero"`
	MovieIDs     []int  `json:"movieIds,omitempty"`
}

type mediaFile struct {
	ID           int    `json:"id"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	SeriesID     int    `json:"seriesId"`
	SeasonNumber int    `json:"seasonNumber"`
	MovieID      int    `json:"movieId"`
}

func (s *Service) DownloadClientConfig(ctx context.Context, name string) (DownloadClientConfig, error) {
	instance, err := s.instance(name)
	if err != nil {
		return DownloadClientConfig{}, err
	}

	var clientConfig DownloadClientConfig
	resp, err := s.get(ctx, instance, "api/v3/config/downloadclient", &clientConfig)
	if err != nil {
		return DownloadClientConfig{}, fmt.Errorf("get download client config: %w", err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return DownloadClientConfig{}, fmt.Errorf("get download client config: %w", err)
	}
	return clientConfig, nil
}

// LibraryFile reads one imported file by its Arr file ID.
func (s *Service) LibraryFile(ctx context.Context, name string, fileID int) (LibraryFile, bool, error) {
	instance, err := s.instance(name)
	if err != nil {
		return LibraryFile{}, false, err
	}
	if fileID <= 0 {
		return LibraryFile{}, false, fmt.Errorf("get arr file: invalid file ID %d", fileID)
	}
	resource, err := fileResource(instance.Type)
	if err != nil {
		return LibraryFile{}, false, err
	}

	var file mediaFile
	resp, err := s.get(ctx, instance, fmt.Sprintf("api/v3/%s/%d", resource, fileID), &file)
	if err != nil {
		return LibraryFile{}, false, fmt.Errorf("get %s %d: %w", resource, fileID, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return LibraryFile{}, false, nil
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return LibraryFile{}, false, fmt.Errorf("get %s %d: %w", resource, fileID, err)
	}

	libraryFile := LibraryFile{
		ArrFileID:    file.ID,
		Path:         file.Path,
		Size:         file.Size,
		SeriesID:     file.SeriesID,
		SeasonNumber: file.SeasonNumber,
		MovieID:      file.MovieID,
	}
	if instance.Type == Sonarr {
		episodesByFile, err := s.sonarrEpisodeIDs(ctx, instance, file.SeriesID)
		if err != nil {
			return LibraryFile{}, false, err
		}
		libraryFile.EpisodeIDs = episodesByFile[file.ID]
	}
	return libraryFile, true, nil
}

// DeleteLibraryFile removes one imported file. A file the Arr no longer has is
// treated as deleted.
func (s *Service) DeleteLibraryFile(ctx context.Context, name string, fileID int) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}
	if fileID <= 0 {
		return fmt.Errorf("delete arr file: invalid file ID %d", fileID)
	}
	resource, err := fileResource(instance.Type)
	if err != nil {
		return err
	}

	resp, err := s.mutate(ctx, instance, http.MethodDelete, fmt.Sprintf("api/v3/%s/%d", resource, fileID), nil, nil)
	if err != nil {
		return fmt.Errorf("delete %s %d: %w", resource, fileID, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if err := expectSuccess(resp); err != nil {
		return fmt.Errorf("delete %s %d: %w", resource, fileID, err)
	}
	return nil
}

func (s *Service) SearchEpisodes(ctx context.Context, name string, episodeIDs []int) (Command, error) {
	instance, err := s.instanceOfType(name, Sonarr)
	if err != nil {
		return Command{}, err
	}
	ids, err := uniqueIDs(episodeIDs)
	if err != nil {
		return Command{}, fmt.Errorf("episode search: %w", err)
	}
	return s.command(ctx, instance, struct {
		Name       string `json:"name"`
		EpisodeIDs []int  `json:"episodeIds"`
	}{Name: "EpisodeSearch", EpisodeIDs: ids})
}

func (s *Service) SearchSeason(ctx context.Context, name string, seriesID, seasonNumber int) (Command, error) {
	instance, err := s.instanceOfType(name, Sonarr)
	if err != nil {
		return Command{}, err
	}
	if seriesID <= 0 || seasonNumber < 0 {
		return Command{}, fmt.Errorf("season search: invalid series %d season %d", seriesID, seasonNumber)
	}
	return s.command(ctx, instance, struct {
		Name         string `json:"name"`
		SeriesID     int    `json:"seriesId"`
		SeasonNumber int    `json:"seasonNumber"`
	}{Name: "SeasonSearch", SeriesID: seriesID, SeasonNumber: seasonNumber})
}

func (s *Service) SearchMovies(ctx context.Context, name string, movieIDs []int) (Command, error) {
	instance, err := s.instanceOfType(name, Radarr)
	if err != nil {
		return Command{}, err
	}
	ids, err := uniqueIDs(movieIDs)
	if err != nil {
		return Command{}, fmt.Errorf("movie search: %w", err)
	}
	return s.command(ctx, instance, struct {
		Name     string `json:"name"`
		MovieIDs []int  `json:"movieIds"`
	}{Name: "MoviesSearch", MovieIDs: ids})
}

// RefreshMonitoredDownloads asks the Arr to re-read its download client.
func (s *Service) RefreshMonitoredDownloads(ctx context.Context, name string) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}
	_, err = s.command(ctx, instance, struct {
		Name string `json:"name"`
	}{Name: "RefreshMonitoredDownloads"})
	return err
}

func (s *Service) Commands(ctx context.Context, name string) ([]Command, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}

	var commands []Command
	resp, err := s.get(ctx, instance, "api/v3/command", &commands)
	if err != nil {
		return nil, fmt.Errorf("list arr commands: %w", err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list arr commands: %w", err)
	}
	return commands, nil
}

func (s *Service) command(ctx context.Context, instance Arr, payload any) (Command, error) {
	var command Command
	resp, err := s.mutate(ctx, instance, http.MethodPost, "api/v3/command", payload, &command)
	if err != nil {
		if dispatched(resp, err) {
			return Command{}, UnknownMutationOutcome(fmt.Errorf("submit arr command: %w", err), 0)
		}
		return Command{}, fmt.Errorf("submit arr command: %w", err)
	}
	if err := expectSuccess(resp); err != nil {
		return Command{}, fmt.Errorf("submit arr command: %w", err)
	}
	return command, nil
}

func fileResource(kind Type) (string, error) {
	switch kind {
	case Sonarr:
		return "episodefile", nil
	case Radarr:
		return "moviefile", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedType, kind)
	}
}

func uniqueIDs(ids []int) ([]int, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one ID is required")
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid ID %d", id)
		}
	}
	unique := slices.Clone(ids)
	slices.Sort(unique)
	return slices.Compact(unique), nil
}
