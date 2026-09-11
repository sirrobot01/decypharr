package reacquire

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

const (
	mutationVisibilityDelay = 5 * time.Second
	mutationClockSkew       = 2 * time.Second
	maxMutationAttempts     = 3
)

type MutationKind string

const (
	MutationHistoryFailed MutationKind = "history_failed"
	MutationEpisodeSearch MutationKind = "episode_search"
	MutationSeasonSearch  MutationKind = "season_search"
	MutationMovieSearch   MutationKind = "movie_search"
	MutationReleaseGrab   MutationKind = "release_grab"
)

func (kind MutationKind) valid() bool {
	switch kind {
	case MutationHistoryFailed,
		MutationEpisodeSearch,
		MutationSeasonSearch,
		MutationMovieSearch,
		MutationReleaseGrab:
		return true
	default:
		return false
	}
}

type MutationState string

const (
	MutationIntent    MutationState = "intent"
	MutationConfirmed MutationState = "confirmed"
)

func (state MutationState) valid() bool {
	return state == MutationIntent || state == MutationConfirmed
}

type Mutation struct {
	Key              string        `json:"key"`
	Kind             MutationKind  `json:"kind"`
	State            MutationState `json:"state"`
	CommandName      string        `json:"commandName,omitempty"`
	DownloadID       string        `json:"downloadId,omitempty"`
	HistoryID        int           `json:"historyId,omitzero"`
	EpisodeIDs       []int         `json:"episodeIds,omitempty"`
	SeriesID         int           `json:"seriesId,omitzero"`
	SeasonNumber     int           `json:"seasonNumber,omitzero"`
	MovieIDs         []int         `json:"movieIds,omitempty"`
	ReleaseGUID      string        `json:"releaseGuid,omitempty"`
	ReleaseIndexer   string        `json:"releaseIndexer,omitempty"`
	ReleaseIndexerID int           `json:"releaseIndexerId,omitzero"`
	IntentAt         time.Time     `json:"intentAt"`
	LastDispatchedAt time.Time     `json:"lastDispatchedAt,omitzero"`
	Attempts         int           `json:"attempts,omitzero"`
	ReceiptID        int           `json:"receiptId,omitzero"`
	ConfirmedAt      time.Time     `json:"confirmedAt,omitzero"`
}

func (m Mutation) validate() error {
	switch {
	case m.Key == "":
		return errors.New("mutation key is required")
	case !m.Kind.valid():
		return fmt.Errorf("invalid mutation kind %q", m.Kind)
	case !m.State.valid():
		return fmt.Errorf("invalid mutation state %q", m.State)
	case m.IntentAt.IsZero():
		return errors.New("mutation intent timestamp is required")
	case m.Attempts < 0:
		return errors.New("mutation attempts cannot be negative")
	case m.Attempts > 0 && m.LastDispatchedAt.IsZero():
		return errors.New("attempted mutation requires a dispatch timestamp")
	case m.Attempts == 0 && !m.LastDispatchedAt.IsZero():
		return errors.New("unattempted mutation cannot have a dispatch timestamp")
	case m.State == MutationConfirmed && m.ConfirmedAt.IsZero():
		return errors.New("confirmed mutation timestamp is required")
	}
	switch m.Kind {
	case MutationHistoryFailed:
		if m.DownloadID == "" {
			return errors.New("failed-history mutation requires a download ID")
		}
	case MutationEpisodeSearch:
		if m.CommandName == "" || !validMutationIDs(m.EpisodeIDs) {
			return errors.New("episode-search mutation requires a command and episode IDs")
		}
	case MutationSeasonSearch:
		if m.CommandName == "" || m.SeriesID <= 0 || m.SeasonNumber < 0 {
			return errors.New("season-search mutation requires an exact season scope")
		}
	case MutationMovieSearch:
		if m.CommandName == "" || !validMutationIDs(m.MovieIDs) {
			return errors.New("movie-search mutation requires a command and movie IDs")
		}
	case MutationReleaseGrab:
		if m.ReleaseGUID == "" || m.ReleaseIndexer == "" || m.ReleaseIndexerID <= 0 {
			return errors.New("release-grab mutation requires a GUID and indexer identity")
		}
		movieScope := validMutationIDs(m.MovieIDs) && len(m.EpisodeIDs) == 0
		episodeScope := m.SeriesID > 0 && validMutationIDs(m.EpisodeIDs) && len(m.MovieIDs) == 0
		if !movieScope && !episodeScope {
			return errors.New("release-grab mutation requires one exact media scope")
		}
	}
	return nil
}

func validMutationIDs(ids []int) bool {
	if len(ids) == 0 {
		return false
	}
	for i, id := range ids {
		if id <= 0 || slices.Contains(ids[:i], id) {
			return false
		}
	}
	return true
}

func cloneMutation(mutation Mutation) Mutation {
	mutation.EpisodeIDs = slices.Clone(mutation.EpisodeIDs)
	mutation.MovieIDs = slices.Clone(mutation.MovieIDs)
	return mutation
}

func mutationForKey(job Job, key string) (Mutation, bool) {
	for _, mutation := range job.Mutations {
		if mutation.Key == key {
			return cloneMutation(mutation), true
		}
	}
	return Mutation{}, false
}

func mutationOfKind(job Job, kind MutationKind) (Mutation, bool) {
	for _, mutation := range job.Mutations {
		if mutation.Kind == kind {
			return cloneMutation(mutation), true
		}
	}
	return Mutation{}, false
}

func setMutation(job *Job, mutation Mutation) {
	for i := range job.Mutations {
		if job.Mutations[i].Key == mutation.Key {
			job.Mutations[i] = cloneMutation(mutation)
			return
		}
	}
	job.Mutations = append(job.Mutations, cloneMutation(mutation))
}

func persistMutation(job *Job, progress JobProgress, status Status, mutation Mutation) error {
	if err := progress.UpdateDurable(status, func(current *Job) {
		setMutation(current, mutation)
	}); err != nil {
		return err
	}
	setMutation(job, mutation)
	return nil
}

func ensureMutationIntent(job *Job, progress JobProgress, status Status, mutation Mutation) (Mutation, error) {
	if existing, ok := mutationForKey(*job, mutation.Key); ok {
		return existing, nil
	}
	mutation.State = MutationIntent
	mutation.IntentAt = time.Now().UTC()
	if err := persistMutation(job, progress, status, mutation); err != nil {
		return Mutation{}, err
	}
	return mutation, nil
}

func recordMutationAttempt(job *Job, progress JobProgress, status Status, mutation Mutation) (Mutation, error) {
	mutation.State = MutationIntent
	mutation.Attempts++
	mutation.LastDispatchedAt = time.Now().UTC()
	if err := persistMutation(job, progress, status, mutation); err != nil {
		return Mutation{}, err
	}
	return mutation, nil
}

func confirmMutation(job *Job, progress JobProgress, status Status, mutation Mutation, receiptID int) error {
	mutation.State = MutationConfirmed
	mutation.ReceiptID = receiptID
	mutation.ConfirmedAt = time.Now().UTC()
	if err := persistMutation(job, progress, status, mutation); err != nil {
		return arr.UnknownMutationOutcome(fmt.Errorf("persist confirmed Arr mutation: %w", err), 0)
	}
	return nil
}

func mutationVisibilityRemaining(mutation Mutation) time.Duration {
	if mutation.LastDispatchedAt.IsZero() {
		return 0
	}
	return max(0, time.Until(mutation.LastDispatchedAt.Add(mutationVisibilityDelay)))
}

func mutationRedispatchError(mutation Mutation) error {
	if remaining := mutationVisibilityRemaining(mutation); remaining > 0 {
		return arr.UnknownMutationOutcome(nil, remaining)
	}
	if mutation.Attempts >= maxMutationAttempts {
		return fmt.Errorf(
			"arr mutation %q was not observed after %d dispatch attempts; refusing another automatic dispatch",
			mutation.Key,
			mutation.Attempts,
		)
	}
	return nil
}

func unresolvedMutation(mutation Mutation, dispatchErr, reconcileErr error) error {
	cause := dispatchErr
	if reconcileErr != nil {
		cause = errors.Join(dispatchErr, fmt.Errorf("reconcile Arr mutation: %w", reconcileErr))
	}
	return arr.UnknownMutationOutcome(cause, mutationVisibilityRemaining(mutation))
}

func unavailableMutationReconciliation(mutation Mutation, err error) error {
	if mutationVisibilityRemaining(mutation) == 0 && mutation.Attempts >= maxMutationAttempts {
		return fmt.Errorf(
			"arr mutation %q reached %d dispatch attempts and reconciliation is unavailable; refusing another automatic dispatch: %w",
			mutation.Key,
			mutation.Attempts,
			err,
		)
	}
	return arr.UnknownMutationOutcome(err, mutationVisibilityRemaining(mutation))
}

func mutationKey(kind MutationKind, values ...string) string {
	return string(kind) + ":" + strings.Join(values, ":")
}

func findCommandReceipt(commands []arr.Command, mutation Mutation) (arr.Command, bool) {
	for _, command := range commands {
		if (!strings.EqualFold(command.Name, mutation.CommandName) &&
			!strings.EqualFold(command.Body.Name, mutation.CommandName)) ||
			command.Queued.IsZero() ||
			command.Queued.Before(mutation.LastDispatchedAt.Add(-mutationClockSkew)) {
			continue
		}
		if commandScopeMatches(command.Body, mutation) {
			return command, true
		}
	}
	return arr.Command{}, false
}

func commandScopeMatches(body arr.CommandBody, mutation Mutation) bool {
	switch mutation.Kind {
	case MutationEpisodeSearch:
		return equalNormalizedIDs(body.EpisodeIDs, mutation.EpisodeIDs)
	case MutationSeasonSearch:
		return body.SeriesID == mutation.SeriesID && body.SeasonNumber == mutation.SeasonNumber
	case MutationMovieSearch:
		return equalNormalizedIDs(body.MovieIDs, mutation.MovieIDs)
	default:
		return false
	}
}

func equalNormalizedIDs(left, right []int) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func findGrabReceipt(records []arr.HistoryRecord, mutation Mutation) (arr.HistoryRecord, bool) {
	for _, record := range records {
		if !strings.EqualFold(record.EventType, arr.EventGrabbed) ||
			record.Date.IsZero() ||
			record.Date.Before(mutation.LastDispatchedAt.Add(-mutationClockSkew)) {
			continue
		}
		guid, guidFound := record.DataValue("guid")
		indexer, indexerFound := record.DataValue("indexer")
		if !guidFound || !indexerFound || guid != mutation.ReleaseGUID || indexer != mutation.ReleaseIndexer {
			continue
		}
		if grabScopeMatches(record, mutation) {
			return record, true
		}
	}
	return arr.HistoryRecord{}, false
}

func grabScopeMatches(record arr.HistoryRecord, mutation Mutation) bool {
	if len(mutation.MovieIDs) > 0 {
		return slices.Contains(mutation.MovieIDs, record.MovieID)
	}
	if len(mutation.EpisodeIDs) > 0 {
		return record.SeriesID == mutation.SeriesID && slices.Contains(mutation.EpisodeIDs, record.EpisodeID)
	}
	return false
}
