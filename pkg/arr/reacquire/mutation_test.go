package reacquire

import (
	"errors"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

type failingDurableProgress struct {
	err error
}

func (progress failingDurableProgress) Update(Status, func(*Job)) error {
	return nil
}

func (progress failingDurableProgress) UpdateDurable(Status, func(*Job)) error {
	return progress.err
}

func TestConfirmMutationPersistenceFailureHasUnknownOutcome(t *testing.T) {
	now := time.Now().UTC()
	mutation := Mutation{
		Key:              "movie_search:9",
		Kind:             MutationMovieSearch,
		State:            MutationIntent,
		CommandName:      "MoviesSearch",
		MovieIDs:         []int{9},
		IntentAt:         now.Add(-time.Second),
		LastDispatchedAt: now,
		Attempts:         1,
	}
	err := confirmMutation(
		&Job{Mutations: []Mutation{mutation}},
		failingDurableProgress{err: errors.New("sync failed")},
		StatusSearching,
		mutation,
		42,
	)
	if !errors.Is(err, arr.ErrMutationOutcomeUnknown) {
		t.Fatalf("error = %v, want unknown mutation outcome", err)
	}
}

func TestFindCommandReceiptRequiresExactScopeAndTime(t *testing.T) {
	dispatchedAt := time.Now().UTC().Add(-time.Minute)
	mutation := Mutation{
		Kind:             MutationEpisodeSearch,
		CommandName:      "EpisodeSearch",
		EpisodeIDs:       []int{4, 9},
		LastDispatchedAt: dispatchedAt,
	}
	commands := []arr.Command{
		{ID: 1, Name: "EpisodeSearch", Queued: dispatchedAt.Add(-time.Minute), Body: arr.CommandBody{EpisodeIDs: []int{4, 9}}},
		{ID: 2, Name: "EpisodeSearch", Queued: dispatchedAt.Add(time.Second), Body: arr.CommandBody{EpisodeIDs: []int{4}}},
		{ID: 3, Name: "EpisodeSearch", Queued: dispatchedAt.Add(time.Second), Body: arr.CommandBody{EpisodeIDs: []int{9, 4}}},
	}
	command, found := findCommandReceipt(commands, mutation)
	if !found || command.ID != 3 {
		t.Fatalf("command = %#v, found = %v", command, found)
	}
}

func TestFindGrabReceiptRequiresGuidIndexerMediaAndTime(t *testing.T) {
	dispatchedAt := time.Now().UTC().Add(-time.Minute)
	mutation := Mutation{
		Kind:             MutationReleaseGrab,
		ReleaseGUID:      "release-guid",
		ReleaseIndexer:   "Indexer",
		SeriesID:         7,
		EpisodeIDs:       []int{101},
		LastDispatchedAt: dispatchedAt,
	}
	records := []arr.HistoryRecord{
		{ID: 1, EventType: arr.EventGrabbed, Date: dispatchedAt.Add(time.Second), SeriesID: 7, EpisodeID: 101, SourceTitle: "same title"},
		{ID: 2, EventType: arr.EventGrabbed, Date: dispatchedAt.Add(time.Second), SeriesID: 7, EpisodeID: 101, Data: map[string]string{"guid": "release-guid", "indexer": "Other"}},
		{ID: 3, EventType: arr.EventGrabbed, Date: dispatchedAt.Add(time.Second), SeriesID: 7, EpisodeID: 102, Data: map[string]string{"guid": "release-guid", "indexer": "Indexer"}},
		{ID: 4, EventType: arr.EventGrabbed, Date: dispatchedAt.Add(-time.Minute), SeriesID: 7, EpisodeID: 101, Data: map[string]string{"guid": "release-guid", "indexer": "Indexer"}},
		{ID: 5, EventType: arr.EventGrabbed, Date: dispatchedAt.Add(time.Second), SeriesID: 7, EpisodeID: 101, Data: map[string]string{"Guid": "release-guid", "Indexer": "Indexer"}},
	}
	record, found := findGrabReceipt(records, mutation)
	if !found || record.ID != 5 {
		t.Fatalf("record = %#v, found = %v", record, found)
	}
}

func TestMutationRedispatchWaitsAndStopsAtAttemptCap(t *testing.T) {
	mutation := Mutation{
		Key:              "movie_search:9",
		Attempts:         maxMutationAttempts,
		LastDispatchedAt: time.Now().UTC(),
	}
	if err := mutationRedispatchError(mutation); !errors.Is(err, arr.ErrMutationOutcomeUnknown) {
		t.Fatalf("visibility error = %v, want unknown outcome", err)
	}
	mutation.LastDispatchedAt = time.Now().UTC().Add(-mutationVisibilityDelay - time.Second)
	if err := mutationRedispatchError(mutation); err == nil || errors.Is(err, arr.ErrMutationOutcomeUnknown) {
		t.Fatalf("attempt cap error = %v", err)
	}
}
