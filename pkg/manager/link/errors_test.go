package link

import "testing"

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
