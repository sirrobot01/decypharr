package arr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	EventGrabbed        = "grabbed"
	EventDownloadFailed = "downloadFailed"

	eventTypeGrabbed = 1
	historyPageSize  = 100
)

type HistorySchema struct {
	Page          int             `json:"page"`
	PageSize      int             `json:"pageSize"`
	SortKey       string          `json:"sortKey"`
	SortDirection string          `json:"sortDirection"`
	TotalRecords  int             `json:"totalRecords"`
	Records       []HistoryRecord `json:"records"`
}

type HistoryRecord struct {
	ID          int               `json:"id"`
	DownloadID  string            `json:"downloadId"`
	EventType   string            `json:"eventType"`
	EpisodeID   int               `json:"episodeId,omitempty"`
	SeriesID    int               `json:"seriesId,omitempty"`
	MovieID     int               `json:"movieId,omitempty"`
	SourceTitle string            `json:"sourceTitle,omitempty"`
	Data        map[string]string `json:"data,omitempty"`
	Date        time.Time         `json:"date,omitzero"`
}

// DataValue reads one field of a record's data map. Arr casing is not stable
// across versions, so the lookup ignores case.
func (r HistoryRecord) DataValue(key string) (string, bool) {
	for current, value := range r.Data {
		if strings.EqualFold(current, key) {
			return value, true
		}
	}
	return "", false
}

// DownloadHistory returns every record for one download, newest first. An empty
// eventType returns all of them.
func (s *Service) DownloadHistory(ctx context.Context, name, downloadID, eventType string) ([]HistoryRecord, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}
	downloadID = strings.TrimSpace(downloadID)
	if downloadID == "" {
		return nil, fmt.Errorf("history lookup: download ID is required")
	}

	records := make([]HistoryRecord, 0)
	fetched := 0
	for page := 1; ; page++ {
		query := url.Values{
			"page":          {strconv.Itoa(page)},
			"pageSize":      {strconv.Itoa(historyPageSize)},
			"sortKey":       {"date"},
			"sortDirection": {"descending"},
			"downloadId":    {downloadID},
		}
		history, err := s.history(ctx, instance, query)
		if err != nil {
			return nil, fmt.Errorf("history for download %q: %w", downloadID, err)
		}

		fetched += len(history.Records)
		for _, record := range history.Records {
			if record.DownloadID != downloadID {
				continue
			}
			if eventType == "" || strings.EqualFold(record.EventType, eventType) {
				records = append(records, record)
			}
		}
		if len(history.Records) == 0 || fetched >= history.TotalRecords {
			break
		}
	}
	return records, nil
}

// GrabHistory returns the record of the grab that produced a download.
func (s *Service) GrabHistory(ctx context.Context, name, downloadID string) (HistoryRecord, bool, error) {
	return s.firstHistory(ctx, name, downloadID, EventGrabbed)
}

// FailedHistory returns the download-failed record for a download, which is
// how a blocklist already applied is recognised.
func (s *Service) FailedHistory(ctx context.Context, name, downloadID string) (HistoryRecord, bool, error) {
	return s.firstHistory(ctx, name, downloadID, EventDownloadFailed)
}

func (s *Service) firstHistory(ctx context.Context, name, downloadID, eventType string) (HistoryRecord, bool, error) {
	records, err := s.DownloadHistory(ctx, name, downloadID, eventType)
	if err != nil || len(records) == 0 {
		return HistoryRecord{}, false, err
	}
	return records[0], true, nil
}

// LatestGrabID returns the most recent grab record for one episode or movie.
// It returns zero when history no longer holds the grab.
func (s *Service) LatestGrabID(ctx context.Context, name string, mediaID int) (int, string, error) {
	instance, err := s.instance(name)
	if err != nil {
		return 0, "", err
	}
	if mediaID <= 0 {
		return 0, "", nil
	}

	query := url.Values{
		"page":          {"1"},
		"pageSize":      {"50"},
		"sortKey":       {"date"},
		"sortDirection": {"descending"},
		"eventType":     {strconv.Itoa(eventTypeGrabbed)},
	}
	switch instance.Type {
	case Sonarr:
		query.Set("episodeId", strconv.Itoa(mediaID))
	case Radarr:
		query.Set("movieIds", strconv.Itoa(mediaID))
	default:
		return 0, "", nil
	}

	history, err := s.history(ctx, instance, query)
	if err != nil {
		return 0, "", err
	}
	if len(history.Records) == 0 {
		return 0, "", nil
	}
	return history.Records[0].ID, history.Records[0].DownloadID, nil
}

// GrabHistorySince returns the grabs an instance recorded since a point in
// time, which is how a release grab is reconciled after an unclear response.
func (s *Service) GrabHistorySince(ctx context.Context, name string, since time.Time) ([]HistoryRecord, error) {
	instance, err := s.instance(name)
	if err != nil {
		return nil, err
	}

	records := make([]HistoryRecord, 0)
	fetched := 0
	for page := 1; ; page++ {
		query := url.Values{
			"page":          {strconv.Itoa(page)},
			"pageSize":      {strconv.Itoa(historyPageSize)},
			"sortKey":       {"date"},
			"sortDirection": {"descending"},
			"eventType":     {strconv.Itoa(eventTypeGrabbed)},
		}
		history, err := s.history(ctx, instance, query)
		if err != nil {
			return nil, fmt.Errorf("grab history: %w", err)
		}

		fetched += len(history.Records)
		reachedOlder := false
		for _, record := range history.Records {
			if !strings.EqualFold(record.EventType, EventGrabbed) {
				continue
			}
			if !record.Date.IsZero() && record.Date.Before(since) {
				reachedOlder = true
				continue
			}
			records = append(records, record)
		}
		if reachedOlder || len(history.Records) == 0 || fetched >= history.TotalRecords {
			return records, nil
		}
	}
}

// FailHistory marks a grab as failed. The Arr blocklists the release and, when
// its own redownload is enabled, searches for a replacement.
func (s *Service) FailHistory(ctx context.Context, name string, historyID int) error {
	instance, err := s.instance(name)
	if err != nil {
		return err
	}
	if historyID <= 0 {
		return fmt.Errorf("fail history: invalid history ID %d", historyID)
	}

	endpoint := fmt.Sprintf("api/v3/history/failed/%d", historyID)
	resp, err := s.mutate(ctx, instance, http.MethodPost, endpoint, nil, nil)
	if err != nil {
		if dispatched(resp, err) {
			return UnknownMutationOutcome(fmt.Errorf("fail history %d: %w", historyID, err), 0)
		}
		return fmt.Errorf("fail history %d: %w", historyID, err)
	}
	if err := expectSuccess(resp); err != nil {
		return fmt.Errorf("fail history %d: %w", historyID, err)
	}
	return nil
}

func (s *Service) history(ctx context.Context, instance Arr, query url.Values) (HistorySchema, error) {
	var history HistorySchema
	resp, err := s.get(ctx, instance, "api/v3/history?"+query.Encode(), &history)
	if err != nil {
		return HistorySchema{}, err
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return HistorySchema{}, err
	}
	return history, nil
}
