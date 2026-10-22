package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type ImportResponseSchema struct {
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	FolderName   string `json:"folderName"`
	Name         string `json:"name"`
	Size         int    `json:"size"`
	Series       struct {
		Id int `json:"id"`
	} `json:"series"`
	Movie struct {
		Id int `json:"id"`
	} `json:"movie"`
	SeasonNumber int `json:"seasonNumber"`
	Episodes     []struct {
		Id int `json:"id"`
	} `json:"episodes"`
	ReleaseGroup string          `json:"releaseGroup"`
	Quality      json.RawMessage `json:"quality"`
	Languages    []struct {
		Id   int    `json:"id"`
		Name string `json:"name"`
	} `json:"languages"`
	CustomFormats     []any  `json:"customFormats"`
	CustomFormatScore int    `json:"customFormatScore"`
	IndexerFlags      int    `json:"indexerFlags"`
	ReleaseType       string `json:"releaseType"`
	Rejections        []struct {
		Reason string `json:"reason"`
		Type   string `json:"type"`
	} `json:"rejections"`
	Id    int       `json:"id"`
	Added time.Time `json:"added,omitzero"`
}

type ManualImportFile struct {
	DownloadId   string          `json:"downloadId"`
	FolderName   string          `json:"folderName"`
	Path         string          `json:"path"`
	MovieId      int             `json:"movieId,omitzero"`
	SeriesId     int             `json:"seriesId,omitzero"`
	SeasonNumber int             `json:"seasonNumber,omitzero"`
	EpisodeIds   []int           `json:"episodeIds,omitempty"`
	Quality      json.RawMessage `json:"quality"`
	Languages    []struct {
		Id   int    `json:"id"`
		Name string `json:"name"`
	} `json:"languages"`
	ReleaseGroup      string `json:"releaseGroup"`
	CustomFormats     []any  `json:"customFormats"`
	CustomFormatScore int    `json:"customFormatScore"`
	IndexerFlags      int    `json:"indexerFlags"`
	ReleaseType       string `json:"releaseType,omitempty"`
	Rejections        []struct {
		Reason string `json:"reason"`
		Type   string `json:"type"`
	} `json:"rejections"`
}

// ManualImport asks the Arr to import a finished download it did not pick up.
func (s *Service) ManualImport(ctx context.Context, name, downloadID string) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}

	var candidates []ImportResponseSchema
	query := url.Values{"downloadId": {downloadID}}
	// The lookup makes the Arr probe every file in the download. On a network
	// mount that can outlast the client's response timeout, so it is sent once.
	resp, err := s.getOnce(ctx, instance, "api/v3/manualimport?"+query.Encode(), &candidates)
	if err != nil {
		return fmt.Errorf("manual import lookup: %w", err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return fmt.Errorf("manual import lookup: %w", err)
	}
	if len(candidates) == 0 {
		return fmt.Errorf("manual import: no files found for download %q", downloadID)
	}

	files := make([]ManualImportFile, 0, len(candidates))
	for _, candidate := range candidates {
		if instance.Type == Radarr && candidate.Movie.Id <= 0 {
			return fmt.Errorf("manual import: no movie matched for %q", candidate.Path)
		}
		if len(candidate.Quality) == 0 || string(candidate.Quality) == "null" {
			return fmt.Errorf("manual import: no quality returned for %q", candidate.Path)
		}
		episodeIDs := make([]int, 0, len(candidate.Episodes))
		for _, episode := range candidate.Episodes {
			episodeIDs = append(episodeIDs, episode.Id)
		}
		files = append(files, ManualImportFile{
			DownloadId:        downloadID,
			Path:              candidate.Path,
			FolderName:        candidate.FolderName,
			MovieId:           candidate.Movie.Id,
			SeriesId:          candidate.Series.Id,
			SeasonNumber:      candidate.SeasonNumber,
			EpisodeIds:        episodeIDs,
			Quality:           candidate.Quality,
			Languages:         candidate.Languages,
			ReleaseGroup:      candidate.ReleaseGroup,
			CustomFormats:     candidate.CustomFormats,
			CustomFormatScore: candidate.CustomFormatScore,
			IndexerFlags:      candidate.IndexerFlags,
			ReleaseType:       candidate.ReleaseType,
			Rejections:        candidate.Rejections,
		})
	}

	_, err = s.command(ctx, instance, struct {
		Name       string             `json:"name"`
		Files      []ManualImportFile `json:"files"`
		ImportMode string             `json:"importMode"`
	}{Name: "ManualImport", Files: files, ImportMode: "copy"})
	if err != nil {
		return fmt.Errorf("manual import: %w", err)
	}
	return nil
}
