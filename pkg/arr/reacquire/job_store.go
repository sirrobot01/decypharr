package reacquire

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sirrobot01/appendstore"
)

type JobRepository struct {
	mu    sync.RWMutex
	store *appendstore.Store
}

func OpenReacquireJobRepository(path string) (*JobRepository, error) {
	store, err := openArrStore(path, []string{jobAttributeArrName, jobAttributeStatus})
	if err != nil {
		return nil, fmt.Errorf("open reacquire job repository: %w", err)
	}
	return &JobRepository{store: store}, nil
}

func (r *JobRepository) LoadAll() ([]Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	jobs := make([]Job, 0, r.store.Len())
	err := r.store.ForEach(func(_ string, value []byte) error {
		var job Job
		if err := json.Unmarshal(value, &job); err != nil {
			return fmt.Errorf("decode reacquire job: %w", err)
		}
		if err := validateJob(job); err != nil {
			return fmt.Errorf("decode reacquire job: %w", err)
		}
		jobs = append(jobs, cloneJob(job))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *JobRepository) Get(id string) (Job, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data, err := r.store.Get(id)
	if errors.Is(err, appendstore.ErrKeyNotFound) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("load reacquire job: %w", err)
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, false, fmt.Errorf("decode reacquire job: %w", err)
	}
	if err := validateJob(job); err != nil {
		return Job{}, false, fmt.Errorf("decode reacquire job: %w", err)
	}
	return cloneJob(job), true, nil
}

func (r *JobRepository) Save(job Job) error {
	return r.save(job, false)
}

func (r *JobRepository) SaveDurable(job Job) error {
	return r.save(job, true)
}

func (r *JobRepository) save(job Job, durable bool) error {
	if err := validateJob(job); err != nil {
		return fmt.Errorf("save reacquire job: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode reacquire job: %w", err)
	}
	options := &appendstore.PutOptions{Attributes: map[string]string{
		jobAttributeArrName: job.ArrName,
		jobAttributeStatus:  string(job.Status),
	}}
	if err := r.store.Put(job.ID, data, options); err != nil {
		return fmt.Errorf("persist reacquire job: %w", err)
	}
	if durable {
		if err := r.store.Sync(); err != nil {
			return fmt.Errorf("sync reacquire job: %w", err)
		}
	}
	return nil
}

func (r *JobRepository) Delete(id string) error {
	if id == "" {
		return errors.New("job ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return deleteStoreKey(r.store, id)
}

func (r *JobRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.Close()
}

func validateJob(job Job) error {
	switch {
	case job.ID == "":
		return errors.New("job ID is required")
	case !job.Status.valid():
		return fmt.Errorf("invalid job status %q", job.Status)
	case !job.Cause.valid():
		return fmt.Errorf("invalid job cause %q", job.Cause)
	case !job.Strategy.valid():
		return fmt.Errorf("invalid job strategy %q", job.Strategy)
	case job.ArrName == "":
		return errors.New("arr name is required")
	case job.EntryID == "" || job.FileID == "":
		return errors.New("entry ID and file ID are required")
	case job.CreatedAt.IsZero() || job.UpdatedAt.IsZero():
		return errors.New("job timestamps are required")
	}
	if len(job.Bindings) == 0 {
		return errors.New("job bindings are required")
	}
	for _, binding := range job.Bindings {
		if err := binding.validate(); err != nil {
			return fmt.Errorf("invalid job binding: %w", err)
		}
	}
	mutationKeys := make(map[string]struct{}, len(job.Mutations))
	for _, mutation := range job.Mutations {
		if err := mutation.validate(); err != nil {
			return fmt.Errorf("invalid job mutation: %w", err)
		}
		if _, exists := mutationKeys[mutation.Key]; exists {
			return fmt.Errorf("duplicate job mutation key %q", mutation.Key)
		}
		mutationKeys[mutation.Key] = struct{}{}
	}
	return nil
}
