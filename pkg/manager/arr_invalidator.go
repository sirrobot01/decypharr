package manager

import (
	"context"
	"fmt"

	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
)

func (m *Manager) InvalidateReacquire(ctx context.Context, job reacquire.Job) error {
	entryIDs := make(map[string]struct{}, len(job.Bindings))
	for _, binding := range job.Bindings {
		if binding.EntryID != "" {
			entryIDs[binding.EntryID] = struct{}{}
		}
	}
	if len(entryIDs) == 0 && job.EntryID != "" {
		entryIDs[job.EntryID] = struct{}{}
	}

	for entryID := range entryIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		exists, err := m.storage.Exists(entryID)
		if err != nil {
			return fmt.Errorf("check managed entry %q: %w", entryID, err)
		}
		if !exists {
			continue
		}
		if err := m.DeleteEntry(entryID, true); err != nil {
			return fmt.Errorf("invalidate managed entry %q: %w", entryID, err)
		}
	}
	if m.arrService != nil {
		for _, binding := range job.Bindings {
			if err := m.arrService.DeleteBinding(binding.EntryID, binding.EntryFileID); err != nil {
				return fmt.Errorf("remove Arr binding %q/%q: %w", binding.EntryID, binding.EntryFileID, err)
			}
		}
	}
	return nil
}
