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
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	SortKey       string `json:"sortKey"`
	SortDirection string `json:"sortDirection"`
	TotalRecords  int    `json:"totalRecords"`
	Records       []struct {
		ID         int    `json:"id"`
		DownloadID string `json:"downloadId"`
	} `json:"records"`
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

func queueFilter(q QueueSchema, autoRedownloadFailed bool) QueueAction {
	// Temporary testing override.
	// autoRedownloadFailed = true

	// Check for failed downloads(for both usenet and torrent)
	// if q.Status == "failed" && autoRedownloadFailed {
	if q.Status == "failed" {
		return QueueActionBlocklist
	}

	// Check for completed downloads with warning status and import pending state
	// if q.Status == "completed" && q.TrackedDownloadStatus == "warning" && autoRedownloadFailed {
	if q.Status == "completed" && q.TrackedDownloadStatus == "warning" {
		fmt.Printf("[queueFilter] completed warning item: %+v\n", q)
		// Check status messages for specific errors
		messages := q.StatusMessages
		if len(messages) > 0 {
			// "Unexpected error processing file" is often transient (race/mount visibility). However "one or more episodes expected.." will catch this and blocklist it.
			// This will quickly iterate and see if there is a processing error first and leave it for manual adjustment. Most of the time sonarr will be able to import these files on the next pass.
			// If we use fregapples CanSeePath fuction in arr.go to check if the file is visible to sonarr, we can avoid a lot of these false positives and only blocklist the ones that are truly not visible to sonarr.
			// Meaning, only a small fraction of the "unexpected error processing file" will be truly unexpected and not just a race condition or mount visibility issue. But this check will allow the user to decide if
			// it is truly unexpected and blocklist it, or if it is just a transient issue and let sonarr handle it on the next pass.
			for _, m := range messages {
				title := strings.ToLower(m.Title)
				joinedMessages := strings.ToLower(strings.Join(m.Messages, " "))
				if strings.Contains(title, "unexpected error processing file") || strings.Contains(joinedMessages, "unexpected error processing file") {
					return QueueActionNone
				}
			}

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
	autoRedownloadFailed := a.GetAutoRedownloadFailed()
	blacklists := make(map[int]bool)
	manualImports := make(map[string]bool)
	for _, q := range queue {
		switch queueFilter(q, autoRedownloadFailed) {
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
