package manager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/parser"
)

// JobType represents the type of processing job
type JobType string

const (
	JobTypeTorrent JobType = "torrent"
	JobTypeNZB     JobType = "nzb"
)

// Job represents a unified processing job for both torrents and NZBs
type Job struct {
	ID        string
	Type      JobType
	Request   *ImportRequest               // The original import request
	NZBMeta   *storage.NZB                 // NZB metadata (set after parse, before worker processes)
	NZBGroups map[string]*parser.FileGroup // NZB file groups (set after parse)
	Entry     *storage.Entry               // Entry created during processing
	CreatedAt time.Time
}

// NewJob creates a new job
func NewJob(jobType JobType, req *ImportRequest) *Job {
	id := ""
	if req != nil {
		id = req.Id
	}
	return &Job{
		ID:        id,
		Type:      jobType,
		Request:   req,
		CreatedAt: time.Now(),
	}
}

// JobQueue is a unified, unbounded, thread-safe job queue with a fixed worker pool.
// It replaces the separate ImportRequest queue, nzbJobQueue, and unbounded goroutine
// fan-out with a single queue that processes both torrent and NZB jobs.
type JobQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	jobs   []*Job
	closed bool

	maxWorkers int
	logger     zerolog.Logger
	wg         sync.WaitGroup
	active     atomic.Int64

	// processFunc is called by workers to process a job
	processFunc func(ctx context.Context, job *Job)
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewJobQueue creates a new unified job queue with the given number of workers
func NewJobQueue(ctx context.Context, maxWorkers int, processFunc func(ctx context.Context, job *Job)) *JobQueue {
	if maxWorkers <= 0 {
		maxWorkers = 5
	}

	ctx, cancel := context.WithCancel(ctx)
	q := &JobQueue{
		jobs:        make([]*Job, 0, 64),
		maxWorkers:  maxWorkers,
		logger:      logger.New("jobqueue"),
		processFunc: processFunc,
		ctx:         ctx,
		cancel:      cancel,
	}
	q.cond = sync.NewCond(&q.mu)

	// Start worker goroutines
	for i := 0; i < maxWorkers; i++ {
		q.wg.Add(1)
		go q.worker(i)
	}

	q.logger.Info().Int("workers", maxWorkers).Msg("Job queue started")
	return q
}

// Submit adds a job to the queue (never blocks)
func (q *JobQueue) Submit(job *Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return fmt.Errorf("job queue is closed")
	}

	q.jobs = append(q.jobs, job)
	q.cond.Signal() // Wake one waiting worker
	q.logger.Debug().
		Str("id", job.ID).
		Str("type", string(job.Type)).
		Int("queued", len(q.jobs)).
		Msg("Job submitted")
	return nil
}

// Len returns the current number of pending jobs
func (q *JobQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// ActiveCount returns the number of jobs currently holding an active-download slot.
func (q *JobQueue) ActiveCount() int {
	return int(q.active.Load())
}

// Retry submits a job again after a delay without holding an active slot.
func (q *JobQueue) Retry(job *Job, delay time.Duration) {
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-q.ctx.Done():
			return
		case <-timer.C:
			if err := q.Submit(job); err != nil {
				q.logger.Debug().Err(err).Str("job_id", job.ID).Msg("Failed to retry job")
			}
		}
	}()
}

// Close signals all workers to stop and waits for them to finish
func (q *JobQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	q.cond.Broadcast() // Wake all waiting workers
	q.wg.Wait()
	q.logger.Info().Msg("Job queue stopped")
}

// worker is the main loop for a single worker goroutine
func (q *JobQueue) worker(id int) {
	defer q.wg.Done()

	for {
		job := q.pop()
		if job == nil {
			q.logger.Debug().Int("worker_id", id).Msg("Worker exiting")
			return
		}

		q.logger.Debug().
			Int("worker_id", id).
			Str("job_id", job.ID).
			Str("type", string(job.Type)).
			Int("queued", q.Len()).
			Msg("Processing job")

		q.active.Add(1)
		q.processFunc(q.ctx, job)
		q.active.Add(-1)
	}
}

// pop removes and returns the next job, blocking if queue is empty.
// Returns nil if the queue is closed and empty.
func (q *JobQueue) pop() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.jobs) == 0 && !q.closed {
		q.cond.Wait()
	}

	if q.closed {
		return nil
	}

	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job
}

// DeleteJob removes a pending job by ID (before it's picked up by a worker).
// Returns true if the job was found and removed.
func (q *JobQueue) DeleteJob(jobID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, job := range q.jobs {
		if job.ID == jobID {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return true
		}
	}
	return false
}

// FindJob returns a pending job by ID without removing it
func (q *JobQueue) FindJob(jobID string) *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, job := range q.jobs {
		if job.ID == jobID {
			return job
		}
	}
	return nil
}

// PendingCount returns the count of pending jobs, optionally filtered by type
func (q *JobQueue) PendingCount(jobType JobType) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	if jobType == "" {
		return len(q.jobs)
	}

	count := 0
	for _, job := range q.jobs {
		if job.Type == jobType {
			count++
		}
	}
	return count
}
