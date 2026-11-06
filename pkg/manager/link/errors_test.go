package link

import (
	"fmt"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/request"
)

func TestErrorCodeToLinkErrorRefetchesExpiredLinks(t *testing.T) {
	for _, code := range []string{"400", "403", "410", "link_expired"} {
		if err := ErrorCodeToLinkError(code); !err.ShouldRefetch() {
			t.Errorf("code %s: category = %v, want refetchable", code, err.Category)
		}
	}
	if err := ErrorCodeToLinkError("link_not_found"); !err.IsPermanent() {
		t.Errorf("link_not_found: category = %v, want permanent", err.Category)
	}
}

func TestThrottledErrorsKeepTheirWait(t *testing.T) {
	err := fmt.Errorf("get link: %w", &request.ThrottledError{RetryAfter: 5 * time.Minute})
	got := ClassifyTransportError(err)
	if !got.ShouldBackoff() || got.RetryAfter != 5*time.Minute {
		t.Fatalf("classified as %v with wait %s", got.Category, got.RetryAfter)
	}
	if code := ErrorCodeToLinkError("429"); !code.ShouldBackoff() {
		t.Fatalf("429 category = %v, want throttled", code.Category)
	}
}
