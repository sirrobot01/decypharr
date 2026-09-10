package reacquire

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

type reconciliationHandler func(context.Context, Job, JobProgress) error

func (handler reconciliationHandler) Reacquire(ctx context.Context, job Job, progress JobProgress) error {
	return handler(ctx, job, progress)
}

func TestReconciliationStopsAndRetainsDuplicateProtection(t *testing.T) {
	for _, scenario := range []string{"expired before dispatch", "expired during reconciliation", "attempts exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			service, err := NewService(ServiceOptions{Directory: directory})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close() })
			base := time.Now().UTC()
			var elapsed atomic.Int64
			service.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
			if err := service.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			binding := Binding{
				ArrName: "radarr", ArrType: arr.Radarr, ArrInstanceFingerprint: testArrInstanceFingerprint,
				EntryID: "entry", EntryFileID: "file", DownloadID: "download", ArrFileID: 7,
				LibraryPath: "/library/movie.mkv", MovieID: 7, Confidence: ConfidenceExactPath,
			}
			if err := service.UpsertBinding(binding); err != nil {
				t.Fatal(err)
			}
			request := Request{EntryID: binding.EntryID, FileID: binding.EntryFileID, Cause: CauseStream}
			created, err := service.Reacquire(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.AcknowledgeJob(created.ID); !errors.Is(err, ErrJobNotBlocked) {
				t.Fatalf("acknowledge queued job: %v", err)
			}
			job, err := service.updateJobDurable(created.ID, StatusQueued, func(job *Job) {
				job.Mutations = []Mutation{{Key: "movie_search:7", Kind: MutationMovieSearch, State: MutationIntent,
					CommandName: "MoviesSearch", MovieIDs: []int{7}, IntentAt: base,
					LastDispatchedAt: base, Attempts: 1}}
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "expired before dispatch" {
				elapsed.Store(int64(reconciliationTimeout))
			}
			calls := 0
			handler := reconciliationHandler(func(context.Context, Job, JobProgress) error {
				calls++
				if scenario == "attempts exhausted" {
					return errors.New("dispatch budget exhausted")
				}
				elapsed.Store(int64(reconciliationTimeout))
				return arr.UnknownMutationOutcome(errors.New("receipt endpoint unavailable"), time.Second)
			})
			if !service.runJob(t.Context(), handler, job) {
				t.Fatal("could not save stopped job")
			}
			if scenario == "expired before dispatch" && calls != 0 {
				t.Fatal("expired job invoked remote handler")
			}
			if scenario != "expired before dispatch" && calls != 1 {
				t.Fatalf("handler calls = %d", calls)
			}
			stopped, _ := service.Job(job.ID)
			if stopped.Status != StatusNeedsAttention || !stopped.RetryAt.IsZero() || !stopped.CompletedAt.IsZero() {
				t.Fatalf("stopped job = %#v", stopped)
			}
			if stopped.Mutations[0].State != MutationIntent || !stopped.Mutations[0].IntentAt.Equal(base) {
				t.Fatalf("mutation evidence changed: %#v", stopped.Mutations)
			}
			elapsed.Store(int64(2 * failureRetention))
			service.maintainJobs()
			if _, ok := service.nextJob(); ok {
				t.Fatal("stopped job remains dispatchable")
			}
			duplicate, err := service.Reacquire(request)
			if err != nil || duplicate.ID != job.ID {
				t.Fatalf("duplicate = %v, %v", duplicate, err)
			}
			if _, err := service.DeleteJobs([]string{job.ID}); !errors.Is(err, ErrJobNotTerminal) {
				t.Fatalf("delete stopped job: %v", err)
			}
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewService(ServiceOptions{Directory: directory})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			if err := reopened.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			duplicate, err = reopened.Reacquire(request)
			if err != nil || duplicate.ID != job.ID || duplicate.Status != StatusNeedsAttention {
				t.Fatalf("restored duplicate = %v, %v", duplicate, err)
			}
			if _, ok := reopened.nextJob(); ok {
				t.Fatal("restart resumes stopped job")
			}
			acknowledged, err := reopened.AcknowledgeJob(job.ID)
			if err != nil || acknowledged.Status != StatusCancelled || len(acknowledged.Mutations) != 1 {
				t.Fatalf("acknowledge = %#v, %v", acknowledged, err)
			}
			fresh, err := reopened.Reacquire(request)
			if err != nil || fresh.ID == job.ID {
				t.Fatalf("new request = %v, %v", fresh, err)
			}
		})
	}
}
