package arr

import (
	"fmt"
	"net/http"
	gourl "net/url"
	"strconv"
	"strings"
)

type QueueAction string

const (
	QueueActionNone      QueueAction = ""
	QueueActionBlocklist QueueAction = "blocklist"
	QueueActionImport    QueueAction = "import"
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
	ID         int    `json:"id"`
	DownloadID string `json:"downloadId"`
	EventType  string `json:"eventType"`
	EpisodeID  int    `json:"episodeId,omitempty"`
	SeriesID   int    `json:"seriesId,omitempty"`
	MovieID    int    `json:"movieId,omitempty"`
}

type QueueResponseScheme struct {
	Page          int           `json:"page"`
	PageSize      int           `json:"pageSize"`
	SortKey       string        `json:"sortKey"`
	SortDirection string        `json:"sortDirection"`
	TotalRecords  int           `json:"totalRecords"`
	Records       []QueueSchema `json:"records"`
}

type QueueSchema struct {
	SeriesId              int    `json:"seriesId"`
	EpisodeId             int    `json:"episodeId"`
	SeasonNumber          int    `json:"seasonNumber"`
	Title                 string `json:"title"`
	Status                string `json:"status"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus"`
	TrackedDownloadState  string `json:"trackedDownloadState"`
	StatusMessages        []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	} `json:"statusMessages"`
	DownloadId                          string `json:"downloadId"`
	Protocol                            string `json:"protocol"`
	DownloadClient                      string `json:"downloadClient"`
	DownloadClientHasPostImportCategory bool   `json:"downloadClientHasPostImportCategory"`
	Indexer                             string `json:"indexer"`
	OutputPath                          string `json:"outputPath"`
	EpisodeHasFile                      bool   `json:"episodeHasFile"`
	Id                                  int    `json:"id"`
}

func (a *Arr) GetHistory(downloadId, eventType string) *HistorySchema {
	query := gourl.Values{}
	if downloadId != "" {
		query.Add("downloadId", downloadId)
	}
	query.Add("eventType", eventType)
	query.Add("pageSize", "100")
	url := "api/v3/history" + "?" + query.Encode()
	var data *HistorySchema
	resp, err := a.Request(http.MethodGet, url, nil, &data)
	if err != nil {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	return data
}

func (a *Arr) GetQueue() []QueueSchema {
	query := gourl.Values{}
	query.Add("page", "1")
	query.Add("pageSize", "200")
	results := make([]QueueSchema, 0)

	for {
		url := "api/v3/queue" + "?" + query.Encode()
		var data QueueResponseScheme
		resp, err := a.Request(http.MethodGet, url, nil, &data)
		if err != nil {
			break
		}
		if resp.StatusCode != http.StatusOK {
			break
		}

		results = append(results, data.Records...)

		if len(results) >= data.TotalRecords {
			break
		}

		query.Set("page", strconv.Itoa(data.Page+1))
	}

	return results
}

func queueFilter(q QueueSchema) QueueAction {
	// Check for failed downloads(for both usenet and torrent)
	if q.Status == "failed" {
		return QueueActionBlocklist
	}

	// Check for completed downloads with warning status and import pending state
	if q.Status == "completed" && q.TrackedDownloadStatus == "warning" {
		// Check status messages for specific errors
		messages := q.StatusMessages
		if len(messages) > 0 {
			for _, m := range messages {
				if strings.Contains(strings.ToLower(strings.Join(m.Messages, " ")), "no files found are eligible") {
					return QueueActionBlocklist
				}
				if strings.Contains(strings.ToLower(m.Title), "one or more episodes expected in this release were not imported or missing from the release") {
					return QueueActionBlocklist
				}
				if strings.Contains(strings.ToLower(strings.Join(m.Messages, " ")), "downloaded file is empty") {
					return QueueActionBlocklist
				}
				if strings.Contains(strings.ToLower(strings.Join(m.Messages, " ")), "found matching series via grab history, but release was matched to series by id") {
					return QueueActionImport
				}
			}
		}
	}
	return QueueActionNone
}

func (a *Arr) CleanupQueue() error {
	if a == nil {
		return fmt.Errorf("arr not configured")
	}
	queue := a.GetQueue()
	blacklists := make(map[int]bool)
	manualImports := make(map[string]bool)
	for _, q := range queue {
		switch queueFilter(q) {
		case QueueActionBlocklist:
			blacklists[q.Id] = true
		case QueueActionImport:
			manualImports[q.DownloadId] = true
		}
	}

	if len(blacklists) > 0 {
		if err := a.BlackListAndResearchItems(blacklists); err != nil {
			// log error
			fmt.Println("Error during blacklist and research:", err)
		}
	}
	if len(manualImports) > 0 {
		go func() {
			if err := a.ManualImportItems(manualImports); err != nil {
				// log error
				fmt.Println("Error during manual import:", err)
			}
		}()
	}

	return nil
}

// FindGrabHistoryID returns the ID and downloadId of the most recent "grabbed"
// history record for the given episode (Sonarr) or movie (Radarr). Returns
// (0, "", nil) when no grab record is found (e.g. history trimmed, manual import).
func (a *Arr) FindGrabHistoryID(mediaDBID int) (int, string, error) {
	if a == nil {
		return 0, "", fmt.Errorf("arr not configured")
	}
	if mediaDBID <= 0 {
		return 0, "", nil
	}

	query := gourl.Values{}
	query.Add("page", "1")
	query.Add("pageSize", "50")
	query.Add("sortKey", "date")
	query.Add("sortDirection", "descending")
	query.Add("eventType", "1") // 1 = grabbed

	switch a.Type {
	case Sonarr:
		query.Add("episodeId", strconv.Itoa(mediaDBID))
	case Radarr:
		query.Add("movieIds", strconv.Itoa(mediaDBID))
	default:
		return 0, "", nil
	}

	var data HistorySchema
	url := "api/v3/history?" + query.Encode()
	resp, err := a.Request(http.MethodGet, url, nil, &data)
	if err != nil {
		return 0, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("history lookup failed: %s", resp.Status)
	}
	if len(data.Records) == 0 {
		return 0, "", nil
	}
	r := data.Records[0]
	return r.ID, r.DownloadID, nil
}

// MarkHistoryFailed marks a grab history record as failed. This blocklists
// the release in the arr and, if redownload is enabled, triggers a re-search
// for whatever is currently missing from that grab's scope.
func (a *Arr) MarkHistoryFailed(historyID int) error {
	if a == nil {
		return fmt.Errorf("arr not configured")
	}
	if historyID <= 0 {
		return nil
	}
	url := fmt.Sprintf("api/v3/history/failed/%d", historyID)
	resp, err := a.Request(http.MethodPost, url, nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("history/failed %d: %s", historyID, resp.Status)
	}
	return nil
}

func (a *Arr) BlackListAndResearchItems(items map[int]bool) error {
	queueIDs := make([]int, 0)
	for id := range items {
		queueIDs = append(queueIDs, id)
	}
	payload := struct {
		Ids []int `json:"ids"`
	}{
		Ids: queueIDs,
	}
	query := gourl.Values{}
	query.Add("removeFromClient", "true")
	query.Add("blocklist", "true")
	query.Add("skipRedownload", "false")
	query.Add("changeCategory", "false")
	url := "api/v3/queue/bulk" + "?" + query.Encode()

	_, err := a.Request(http.MethodDelete, url, payload, nil)
	if err != nil {
		return err
	}
	return nil
}

func (a *Arr) ManualImportItems(items map[string]bool) error {
	for downloadId := range items {
		_, err := a.Import(downloadId)
		if err != nil {
			// log error
			fmt.Println(err)
		}
	}
	return nil
}
