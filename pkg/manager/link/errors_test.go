package link

import "testing"

func TestErrorCodeToLinkErrorRefetchesCDNRejections(t *testing.T) {
	for _, code := range []string{"400", "403", "410"} {
		if err := ErrorCodeToLinkError(code); !err.ShouldRefetch() {
			t.Errorf("code %s: category %v, want refetchable", code, err.Category)
		}
	}
}
