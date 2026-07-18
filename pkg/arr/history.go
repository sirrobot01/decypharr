package arr

import (
	"errors"
	"fmt"
	"net/http"
	gourl "net/url"
	"strconv"
	"strings"
	"time"

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

type queueDecision struct {
	Action           QueueAction
	RuleKey          string
	RemoveFromClient bool
}

type confirmedQueueDecision struct {
	QueueSchema
	queueDecision
	Observation cleanupAttempt
}

type cleanupAttempt struct {
	Condition string
	FirstSeen time.Time
}

type cleanupObservation struct {
	Condition string
	FirstSeen time.Time
	Sweeps    int
	Acted     bool
}

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

func (a *Arr) GetQueue() ([]QueueSchema, error) {
	query := gourl.Values{}
	query.Add("page", "1")
	query.Add("pageSize", "200")
	results := make([]QueueSchema, 0)
	requestedPage := 1

	for {
		url := "api/v3/queue" + "?" + query.Encode()
		var data QueueResponseScheme
		resp, err := a.Request(http.MethodGet, url, nil, &data)
		if err != nil {
			return nil, fmt.Errorf("fetch queue page %d: %w", requestedPage, err)
		}
		if resp == nil {
			return nil, fmt.Errorf("fetch queue page %d: no response", requestedPage)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, fmt.Errorf("fetch queue page %d: %s", requestedPage, resp.Status)
		}

		results = append(results, data.Records...)

		if len(results) >= data.TotalRecords {
			return results, nil
		}

		if len(data.Records) == 0 {
			return nil, fmt.Errorf("fetch queue page %d: incomplete response (%d of %d records)", requestedPage, len(results), data.TotalRecords)
		}
		nextPage := data.Page + 1
		if nextPage <= requestedPage {
			return nil, fmt.Errorf("fetch queue page %d: non-advancing page response %d", requestedPage, data.Page)
		}
		requestedPage = nextPage
		query.Set("page", strconv.Itoa(requestedPage))
	}
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

// resolveDecision decides what to do with a single queue item and records the
// matching rule identity so a changed condition resets its confirmation clock.
func resolveDecision(q QueueSchema, rules []config.QueueCleanupRule) queueDecision {
	status := strings.ToLower(q.TrackedDownloadStatus)
	if !strings.EqualFold(q.Status, "failed") && status != "warning" && status != "error" {
		return queueDecision{}
	}

	text := queueItemText(q)
	for i, r := range rules {
		matched := false
		ruleKey := r.ID
		if r.ID != "" {
			if m, ok := catalogMatchers[r.ID]; ok {
				matched = m(q, text)
			}
		} else if s := strings.ToLower(strings.TrimSpace(r.Match)); s != "" {
			matched = strings.Contains(text, s)
			ruleKey = fmt.Sprintf("custom:%d:%s", i, s)
		}
		if matched {
			return queueDecision{
				Action:           actionFromConfig(r.Action),
				RuleKey:          ruleKey,
				RemoveFromClient: ruleKey != "no_eligible_files",
			}
		}
	}
	return queueDecision{}
}

func resolveAction(q QueueSchema, rules []config.QueueCleanupRule) QueueAction {
	return resolveDecision(q, rules).Action
}

func cleanupConfirmationPolicy(policy config.QueueCleanup) (int, time.Duration) {
	sweeps := policy.ConfirmationSweeps
	if sweeps <= 0 {
		sweeps = 3
	}
	delay, err := time.ParseDuration(policy.ConfirmationDelay)
	if err != nil || delay <= 0 {
		delay = 5 * time.Minute
	}
	return sweeps, delay
}

// confirmedDecisions returns only conditions that stayed unchanged for both
// the configured number of observations and minimum delay. An acted condition
// is suppressed until it disappears from the queue or changes.
func (a *Arr) confirmedDecisions(queue []QueueSchema, policy config.QueueCleanup, now time.Time) []confirmedQueueDecision {
	a.cleanupMu.Lock()
	defer a.cleanupMu.Unlock()

	if a.cleanupObservations == nil {
		a.cleanupObservations = make(map[int]cleanupObservation)
	}

	requiredSweeps, requiredDelay := cleanupConfirmationPolicy(policy)
	actionable := make(map[int]bool, len(queue))
	confirmed := make([]confirmedQueueDecision, 0)

	for _, q := range queue {
		decision := resolveDecision(q, policy.Rules)
		if decision.Action == QueueActionNone {
			delete(a.cleanupObservations, q.Id)
			continue
		}

		actionable[q.Id] = true
		condition := strings.Join([]string{
			decision.RuleKey,
			string(decision.Action),
			strings.ToLower(q.Status),
			strings.ToLower(q.TrackedDownloadStatus),
		}, "|")

		observation, ok := a.cleanupObservations[q.Id]
		if !ok || observation.Condition != condition {
			observation = cleanupObservation{Condition: condition, FirstSeen: now, Sweeps: 1}
		} else {
			observation.Sweeps++
		}

		if !observation.Acted && observation.Sweeps >= requiredSweeps && now.Sub(observation.FirstSeen) >= requiredDelay {
			observation.Acted = true
			confirmed = append(confirmed, confirmedQueueDecision{
				QueueSchema:   q,
				queueDecision: decision,
				Observation: cleanupAttempt{
					Condition: observation.Condition,
					FirstSeen: observation.FirstSeen,
				},
			})
		}
		a.cleanupObservations[q.Id] = observation
	}

	for id := range a.cleanupObservations {
		if !actionable[id] {
			delete(a.cleanupObservations, id)
		}
	}
	return confirmed
}

func (a *Arr) retryCleanupDecisions(items map[int]cleanupAttempt) {
	a.cleanupMu.Lock()
	defer a.cleanupMu.Unlock()
	for id, attempt := range items {
		observation, ok := a.cleanupObservations[id]
		if !ok || observation.Condition != attempt.Condition || !observation.FirstSeen.Equal(attempt.FirstSeen) {
			continue
		}
		observation.Acted = false
		a.cleanupObservations[id] = observation
	}
}

func addCleanupAttempt(grouped map[bool]map[int]cleanupAttempt, removeFromClient bool, decision confirmedQueueDecision) {
	if grouped[removeFromClient] == nil {
		grouped[removeFromClient] = make(map[int]cleanupAttempt)
	}
	grouped[removeFromClient][decision.Id] = decision.Observation
}

func (a *Arr) CleanupQueue() error {
	if a == nil {
		return fmt.Errorf("arr not configured")
	}
	if !a.Cleanup {
		return nil
	}
	l := logger.New("arr")
	policy := config.Get().SnapshotQueueCleanup()

	queue, err := a.GetQueue()
	if err != nil {
		return fmt.Errorf("queue cleanup poll failed: %w", err)
	}
	blacklists := make(map[bool]map[int]cleanupAttempt)        // removeFromClient -> attempts
	blacklistResearch := make(map[bool]map[int]cleanupAttempt) // removeFromClient -> attempts
	manualImports := make(map[string]map[int]cleanupAttempt)   // download ID -> queue attempts
	for _, decision := range a.confirmedDecisions(queue, policy, time.Now()) {
		switch decision.Action {
		case QueueActionBlocklist:
			addCleanupAttempt(blacklists, decision.RemoveFromClient, decision)
		case QueueActionBlocklistResearch:
			addCleanupAttempt(blacklistResearch, decision.RemoveFromClient, decision)
		case QueueActionImport:
			if manualImports[decision.DownloadId] == nil {
				manualImports[decision.DownloadId] = make(map[int]cleanupAttempt)
			}
			manualImports[decision.DownloadId][decision.Id] = decision.Observation
		}
	}

	for removeFromClient, items := range blacklistResearch {
		if err := a.removeQueueItems(items, removeFromClient, true, false); err != nil {
			a.retryCleanupDecisions(items)
			l.Error().Err(err).Str("arr", a.Name).Msg("queue cleanup: blacklist + research failed")
		}
	}
	for removeFromClient, items := range blacklists {
		if err := a.removeQueueItems(items, removeFromClient, true, true); err != nil {
			a.retryCleanupDecisions(items)
			l.Error().Err(err).Str("arr", a.Name).Msg("queue cleanup: blacklist failed")
		}
	}
	if len(manualImports) > 0 {
		go func() {
			for downloadID, attempts := range manualImports {
				if err := a.manualImportItem(downloadID); err != nil {
					a.retryCleanupDecisions(attempts)
					l.Error().Err(err).Str("arr", a.Name).Str("download_id", downloadID).Msg("queue cleanup: manual import failed")
				}
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

// removeQueueItems bulk-removes queue items from the arr. removeFromClient
// controls whether the download client's data is deleted; blocklist controls
// whether releases are blocklisted; skipRedownload=false triggers a re-search.
func (a *Arr) removeQueueItems(items map[int]cleanupAttempt, removeFromClient, blocklist, skipRedownload bool) error {
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
	query.Add("removeFromClient", strconv.FormatBool(removeFromClient))
	query.Add("blocklist", strconv.FormatBool(blocklist))
	query.Add("skipRedownload", strconv.FormatBool(skipRedownload))
	query.Add("changeCategory", "false")
	url := "api/v3/queue/bulk" + "?" + query.Encode()

	resp, err := a.Request(http.MethodDelete, url, payload, nil)
	if err != nil {
		return err
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("queue bulk delete failed: %s", resp.Status)
	}
	return nil
}

func (a *Arr) manualImportItem(downloadID string) error {
	body, err := a.Import(downloadID)
	if err != nil {
		return err
	}
	if body != nil {
		if err := body.Close(); err != nil {
			return fmt.Errorf("close response: %w", err)
		}
	}
	return nil
}

func (a *Arr) ManualImportItems(items map[string]bool) error {
	var errs []error
	for downloadId := range items {
		if err := a.manualImportItem(downloadId); err != nil {
			errs = append(errs, fmt.Errorf("import %s: %w", downloadId, err))
		}
	}
	return errors.Join(errs...)
}
