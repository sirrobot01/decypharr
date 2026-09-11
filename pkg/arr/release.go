package arr

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	json "github.com/bytedance/sonic"
)

type Release struct {
	GUID                string             `json:"guid"`
	Title               string             `json:"title"`
	Indexer             string             `json:"indexer"`
	IndexerID           int                `json:"indexerId"`
	Approved            bool               `json:"approved"`
	Rejected            bool               `json:"rejected"`
	TemporarilyRejected bool               `json:"temporarilyRejected"`
	DownloadAllowed     bool               `json:"downloadAllowed"`
	Rejections          []string           `json:"rejections"`
	Payload             stdjson.RawMessage `json:"-"`
}

func (s *Service) EpisodeReleases(ctx context.Context, name string, episodeID int) ([]Release, error) {
	instance, err := s.instanceOfType(name, Sonarr)
	if err != nil {
		return nil, err
	}
	if episodeID <= 0 {
		return nil, fmt.Errorf("release search: invalid episode ID %d", episodeID)
	}
	return s.releases(ctx, instance, url.Values{"episodeId": {strconv.Itoa(episodeID)}})
}

func (s *Service) SeasonReleases(ctx context.Context, name string, seriesID, seasonNumber int) ([]Release, error) {
	instance, err := s.instanceOfType(name, Sonarr)
	if err != nil {
		return nil, err
	}
	if seriesID <= 0 || seasonNumber < 0 {
		return nil, fmt.Errorf("release search: invalid series %d season %d", seriesID, seasonNumber)
	}
	return s.releases(ctx, instance, url.Values{
		"seriesId":     {strconv.Itoa(seriesID)},
		"seasonNumber": {strconv.Itoa(seasonNumber)},
	})
}

func (s *Service) MovieReleases(ctx context.Context, name string, movieID int) ([]Release, error) {
	instance, err := s.instanceOfType(name, Radarr)
	if err != nil {
		return nil, err
	}
	if movieID <= 0 {
		return nil, fmt.Errorf("release search: invalid movie ID %d", movieID)
	}
	return s.releases(ctx, instance, url.Values{"movieId": {strconv.Itoa(movieID)}})
}

// GrabRelease sends a release back to the Arr, which downloads it through its
// own configured download client.
func (s *Service) GrabRelease(ctx context.Context, name string, release Release) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}
	if len(release.Payload) == 0 {
		return fmt.Errorf("grab release: release payload is required")
	}
	if release.GUID == "" || release.IndexerID <= 0 {
		return fmt.Errorf("grab release: release identity is incomplete")
	}

	resp, err := s.mutate(ctx, instance, http.MethodPost, "api/v3/release", release.Payload, nil)
	if err != nil {
		if dispatched(resp, err) {
			return UnknownMutationOutcome(fmt.Errorf("grab release %q: %w", release.Title, err), 0)
		}
		return fmt.Errorf("grab release %q: %w", release.Title, err)
	}
	if err := expectSuccess(resp); err != nil {
		return fmt.Errorf("grab release %q: %w", release.Title, err)
	}
	return nil
}

func (s *Service) releases(ctx context.Context, instance Arr, query url.Values) ([]Release, error) {
	var payloads []stdjson.RawMessage
	resp, err := s.get(ctx, instance, "api/v3/release?"+query.Encode(), &payloads)
	if err != nil {
		return nil, fmt.Errorf("search releases: %w", err)
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("search releases: %w", err)
	}

	releases := make([]Release, 0, len(payloads))
	for _, payload := range payloads {
		var release Release
		if err := json.Unmarshal(payload, &release); err != nil {
			return nil, fmt.Errorf("decode release: %w", err)
		}
		// The Arr only accepts a release it produced, verbatim.
		release.Payload = bytes.Clone(payload)
		releases = append(releases, release)
	}
	return releases, nil
}
