package arr

import (
	"encoding/json"
	"io"
	"net/http"
	gourl "net/url"
	"strconv"
	"strings"
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
	resp, err := a.Request(http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var data *HistorySchema

	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
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
		resp, err := a.Request(http.MethodGet, url, nil)
		if err != nil {
			break
		}

		func() {
			defer func(Body io.ReadCloser) {
				err := Body.Close()
				if err != nil {
					return
				}
			}(resp.Body)

			var data QueueResponseScheme
			if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
				return
			}

			results = append(results, data.Records...)

			if len(results) >= data.TotalRecords {
				// We've fetched all records
				err = io.EOF // Signal to exit the loop
				return
			}

			query.Set("page", strconv.Itoa(data.Page+1))
		}()

		if err != nil {
			break
		}
	}

	return results
}

func (a *Arr) CleanupQueue() error {
	queue := a.GetQueue()
	type messedUp struct {
		id        int
		episodeId int
		seasonNum int
	}
	cleanups := make(map[int][]messedUp)
	for _, q := range queue {
		isMessedUp := false
		if q.Protocol == "torrent" && q.Status == "completed" && q.TrackedDownloadStatus == "warning" && q.TrackedDownloadState == "importPending" {
			messages := q.StatusMessages
			if len(messages) > 0 {
				for _, m := range messages {
					if strings.Contains(strings.Join(m.Messages, " "), "No files found are eligible") {
						isMessedUp = true
						break
					}
					if strings.Contains(m.Title, "One or more episodes expected in this release were not imported or missing from the release") {
						isMessedUp = true
						break
					}
				}
			}
		}
		if isMessedUp {
			cleanups[q.SeriesId] = append(cleanups[q.SeriesId], messedUp{
				id:        q.Id,
				episodeId: q.EpisodeId,
				seasonNum: q.SeasonNumber,
			})
		}
	}

	if len(cleanups) == 0 {
		return nil
	}

	queueIds := make([]int, 0)

	for _, c := range cleanups {
		// Delete the messed up episodes from queue
		for _, m := range c {
			queueIds = append(queueIds, m.id)
		}
	}

	// Delete the messed up episodes from queue

	payload := struct {
		Ids []int `json:"ids"`
	}{
		Ids: queueIds,
	}

	// Blocklist that hash(it's typically not complete, then research the episode)

	query := gourl.Values{}
	query.Add("removeFromClient", "true")
	query.Add("blocklist", "true")
	query.Add("skipRedownload", "false")
	query.Add("changeCategory", "false")
	url := "api/v3/queue/bulk" + "?" + query.Encode()

	_, err := a.Request(http.MethodDelete, url, payload)
	if err != nil {
		return err
	}
	return nil
}
