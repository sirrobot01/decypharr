package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// ErrNothingToImport means every file the Arr offered for a download carries
// a permanent rejection, such as "Episode file already imported". Retrying
// will not change that until something else changes in the Arr.
var ErrNothingToImport = errors.New("nothing left to import")

// ErrIncompleteCandidate means the Arr could not identify a file (no movie,
// no quality), so the import needs a human to map it.
var ErrIncompleteCandidate = errors.New("incomplete import candidate")

// rejectedPermanently reports whether the Arr will not import this file as
// offered. The Arr's own Manual Import screen leaves these files unticked.
func rejectedPermanently(candidate ImportResponseSchema) bool {
	for _, rejection := range candidate.Rejections {
		if strings.EqualFold(rejection.Type, "permanent") {
			return true
		}
	}
	return false
}

// ManualImport asks the Arr to import a finished download it did not pick up.
// Files the Arr rejects permanently are left out: submitting them anyway makes
// Sonarr treat an already imported episode as an upgrade of itself, deleting
// and recreating the library file on every cleanup pass.
func (s *Service) ManualImport(ctx context.Context, name, downloadID string) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}

	var candidates []ImportResponseSchema
	query := url.Values{"downloadId": {downloadID}}
	resp, err := s.get(ctx, instance, "api/v3/manualimport?"+query.Encode(), &candidates)
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
		if rejectedPermanently(candidate) {
			continue
		}
		if instance.Type == Radarr && candidate.Movie.Id <= 0 {
			return fmt.Errorf("manual import: no movie matched for %q: %w", candidate.Path, ErrIncompleteCandidate)
		}
		if len(candidate.Quality) == 0 || string(candidate.Quality) == "null" {
			return fmt.Errorf("manual import: no quality returned for %q: %w", candidate.Path, ErrIncompleteCandidate)
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

	if len(files) == 0 {
		return fmt.Errorf("manual import: download %q: %w", downloadID, ErrNothingToImport)
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
