package manager

import (
	"slices"

	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
)

// ArrRecovery is the stream-facing subset of the Arr service.
type ArrRecovery interface {
	Lookup(entryID, fileID string) (reacquire.Binding, bool)
	Reacquire(reacquire.Request) (*reacquire.Job, error)
}

type streamTarget struct {
	entryID string
	fileID  string
}

// SetArrRecovery installs the Arr recovery service used by streaming failures.
func (m *Manager) SetArrRecovery(recovery ArrRecovery) {
	m.arrRecoveryMu.Lock()
	m.arrRecovery = recovery
	m.arrRecoveryMu.Unlock()
}

func (m *Manager) recoveryService() ArrRecovery {
	m.arrRecoveryMu.RLock()
	recovery := m.arrRecovery
	m.arrRecoveryMu.RUnlock()
	return recovery
}

func (m *Manager) lookupArrBinding(entryID, fileID string) (reacquire.Binding, bool) {
	recovery := m.recoveryService()
	if recovery == nil || entryID == "" || fileID == "" {
		return reacquire.Binding{}, false
	}
	binding, ok := recovery.Lookup(entryID, fileID)
	if ok {
		binding.EpisodeIDs = slices.Clone(binding.EpisodeIDs)
	}
	return binding, ok
}

func (m *Manager) submitStreamReacquire(entryID, fileID string) {
	recovery := m.recoveryService()
	if recovery == nil || entryID == "" || fileID == "" {
		return
	}

	target := streamTarget{entryID: entryID, fileID: fileID}
	if _, loaded := m.reacquireNotifications.LoadOrStore(target, struct{}{}); loaded {
		return
	}

	go func() {
		defer m.reacquireNotifications.Delete(target)
		job, err := recovery.Reacquire(reacquire.Request{
			EntryID: entryID,
			FileID:  fileID,
			Cause:   reacquire.CauseStream,
		})
		if err != nil {
			m.logger.Error().Err(err).
				Str("entry_id", entryID).
				Str("file_id", fileID).
				Msg("Failed to queue Arr reacquisition")
			return
		}
		if job == nil {
			return
		}
		m.setStreamReacquireJob(entryID, fileID, job.ID)
	}()
}

func (m *Manager) setStreamReacquireJob(entryID, fileID, jobID string) {
	if jobID == "" || m.activeStreams == nil {
		return
	}
	m.activeStreams.Range(func(_ string, stream *ActiveStream) bool {
		if stream.EntryID == entryID && stream.FileID == fileID {
			stream.setReacquireJobID(jobID)
		}
		return true
	})
}
