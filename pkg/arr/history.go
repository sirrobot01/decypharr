package arr

import (
	"fmt"
	"net/http"
	gourl "net/url"
	"strconv"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
)

type QueueAction string

const (
	QueueActionNone              QueueAction = ""                   // leave the item in the queue
	QueueActionImport            QueueAction = "import"             // force a manual import
	QueueActionBlocklist         QueueAction = "blacklist"          // blocklist + remove, do NOT re-search
	QueueActionBlocklistResearch QueueAction = "blacklist_research" // blocklist + remove + re-search
)

// actionFromConfig maps a config rule action string to a QueueAction. Unknown
// or empty strings resolve to QueueActionNone (ignore).
func actionFromConfig(s string) QueueAction {
	switch s {
	case string(QueueActionImport):
		return QueueActionImport
	case string(QueueActionBlocklist):
		return QueueActionBlocklist
	case string(QueueActionBlocklistResearch):
		return QueueActionBlocklistResearch
	default:
		return QueueActionNone
	}
}

// catalogMatchers holds the hardcoded predicates for built-in catalog rules,
// keyed by config.QueueCleanupRule.ID. text is the lowercased join of every
// statusMessages title + message for the queue item.
var catalogMatchers = map[string]func(q QueueSchema, text string) bool{
	"failed_download": func(q QueueSchema, _ string) bool {
		return strings.EqualFold(q.Status, "failed")
	},
	"title_mismatch": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "title mismatch")
	},
	"matched_by_id": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "matched to") && strings.Contains(text, "by id")
	},
	"unable_to_parse": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "unable to parse download")
	},
	"no_eligible_files": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "no files found are eligible")
	},
	"episodes_missing": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "not imported or missing from the release")
	},
	"file_empty": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "file is empty")
	},
	"invalid_local_path": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "is not a valid local path")
	},
	"not_grabbed": func(_ QueueSchema, text string) bool {
		return strings.Contains(text, "not in a category")
	},
}

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

// queueItemText returns the lowercased join of every statusMessages title and
// message for a queue item — the haystack all message-based rules match against.
func queueItemText(q QueueSchema) string {
	var b strings.Builder
	for _, m := range q.StatusMessages {
		b.WriteString(m.Title)
		b.WriteByte(' ')
		b.WriteString(strings.Join(m.Messages, " "))
		b.WriteByte(' ')
	}
	return strings.ToLower(b.String())
}

// resolveAction decides what to do with a single queue item given the ordered
// cleanup rule set. Only failed downloads and items flagged warning/error are
// considered; everything else is left alone. Rules are evaluated in order and
// the first match wins. No match resolves to QueueActionNone (ignore).
func resolveAction(q QueueSchema, rules []config.QueueCleanupRule) QueueAction {
	status := strings.ToLower(q.TrackedDownloadStatus)
	if !strings.EqualFold(q.Status, "failed") && status != "warning" && status != "error" {
		return QueueActionNone
	}

	text := queueItemText(q)
	for _, r := range rules {
		matched := false
		if r.ID != "" {
			if m, ok := catalogMatchers[r.ID]; ok {
				matched = m(q, text)
			}
		} else if s := strings.ToLower(strings.TrimSpace(r.Match)); s != "" {
			matched = strings.Contains(text, s)
		}
		if matched {
			return actionFromConfig(r.Action)
		}
	}
	return QueueActionNone
}

func (a *Arr) CleanupQueue() error {
	if a == nil {
		return fmt.Errorf("arr not configured")
	}
	l := logger.New("arr")
	rules := config.Get().QueueCleanup.Rules

	queue := a.GetQueue()
	blacklists := make(map[int]bool)        // blocklist + remove, no re-search
	blacklistResearch := make(map[int]bool) // blocklist + remove + re-search
	manualImports := make(map[string]bool)  // force manual import
	for _, q := range queue {
		switch resolveAction(q, rules) {
		case QueueActionBlocklist:
			blacklists[q.Id] = true
		case QueueActionBlocklistResearch:
			blacklistResearch[q.Id] = true
		case QueueActionImport:
			manualImports[q.DownloadId] = true
		}
	}

	if len(blacklistResearch) > 0 {
		if err := a.removeQueueItems(blacklistResearch, true, false); err != nil {
			l.Error().Err(err).Str("arr", a.Name).Msg("queue cleanup: blacklist + research failed")
		}
	}
	if len(blacklists) > 0 {
		if err := a.removeQueueItems(blacklists, true, true); err != nil {
			l.Error().Err(err).Str("arr", a.Name).Msg("queue cleanup: blacklist failed")
		}
	}
	if len(manualImports) > 0 {
		go func() {
			if err := a.ManualImportItems(manualImports); err != nil {
				l.Error().Err(err).Str("arr", a.Name).Msg("queue cleanup: manual import failed")
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

// removeQueueItems bulk-removes queue items from the arr. blocklist controls
// whether the releases are added to the blocklist; skipRedownload controls
// whether a re-search is triggered (false = re-search, the "research" action).
func (a *Arr) removeQueueItems(items map[int]bool, blocklist, skipRedownload bool) error {
	queueIDs := make([]int, 0, len(items))
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
	query.Add("blocklist", strconv.FormatBool(blocklist))
	query.Add("skipRedownload", strconv.FormatBool(skipRedownload))
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
