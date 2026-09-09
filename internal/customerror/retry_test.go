package customerror_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/pkg/manager/link"
)

func TestRetryClassificationUsesWrappedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name             string
		err              error
		retry, permanent bool
	}{
		{"refetchable 404", link.NewRefetchableError(link.Err404, "404"), true, false},
		{"retryable forbidden", link.NewRetryableError(errors.New("forbidden"), "403"), true, false},
		{"permanent timeout", link.NewPermanentError(errors.New("i/o timeout"), ""), false, true},
		{"custom permanent", customerror.NewPermanentError(errors.New("broken pipe")), false, true},
		{"custom retryable", customerror.NewSilentError(errors.New("not found")).Retryable(), true, false},
		{"plain not found", errors.New("not found"), false, true},
		{"plain timeout", errors.New("i/o timeout"), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{tc.err, fmt.Errorf("fetch: %w", tc.err), errors.Join(errors.New("context"), tc.err)} {
				if got := customerror.IsRetriableError(err); got != tc.retry {
					t.Errorf("IsRetriableError(%v) = %v, want %v", err, got, tc.retry)
				}
				if got := customerror.IsPermanentError(err); got != tc.permanent {
					t.Errorf("IsPermanentError(%v) = %v, want %v", err, got, tc.permanent)
				}
			}
		})
	}
}
