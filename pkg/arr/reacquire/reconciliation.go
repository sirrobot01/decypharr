package reacquire

import (
	"fmt"
	"time"
)

// reconciliationDeadline uses the first intent time. Retries do not extend it.
func (job Job) reconciliationDeadline() time.Time {
	var first time.Time
	for _, mutation := range job.Mutations {
		if mutation.State == MutationIntent && (first.IsZero() || mutation.IntentAt.Before(first)) {
			first = mutation.IntentAt
		}
	}
	if first.IsZero() && len(job.Mutations) == 0 {
		first = job.StartedAt
	}
	if first.IsZero() {
		return time.Time{}
	}
	return first.Add(reconciliationTimeout)
}

func (s *Service) stopReconciliation(id string, cause error) bool {
	_, err := s.updateJobDurable(id, StatusNeedsAttention, func(job *Job) {
		job.LastError = fmt.Sprintf("%v; check the action in Arr, then acknowledge this job before starting another", cause)
		job.RetryAt = time.Time{}
	})
	return err == nil
}

// AcknowledgeJob releases a stopped job after an operator checks its remote actions.
// It retains the mutation record. It does not send another action to Arr.
func (s *Service) AcknowledgeJob(id string) (Job, error) {
	release, err := s.beginOperation()
	if err != nil {
		return Job{}, err
	}
	defer release()
	job, ok := s.Job(id)
	if !ok || job.Status != StatusNeedsAttention {
		return Job{}, ErrJobNotBlocked
	}
	return s.updateJobDurable(id, StatusCancelled, nil)
}
